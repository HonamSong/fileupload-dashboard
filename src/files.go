package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ---- upload / list ----

// storeUpload saves an uploaded file part into folder. If a same-named active
// file already exists, its current content is archived as a previous version
// (subject to the configured retention limit) and replaced with the new content,
// keeping the same file id/link. Returns the file and whether it was newly created.
func (s *Server) storeUpload(part multipart.File, header *multipart.FileHeader, folderArg, uploader string) (*File, bool, error) {
	name := filepath.Base(header.Filename)
	folder := normalizeFolder(folderArg)
	if !s.store.folderExists(folder) {
		_ = s.store.createFolder(folder)
	}
	existing, _ := s.store.getActiveFileByNameInFolder(name, folder)

	// Stream the upload to a temp file first (atomic + lets us hash before we
	// touch any existing blob).
	tmp := filepath.Join(s.filesDir, ".tmp-"+newID(8))
	dst, err := os.Create(tmp)
	if err != nil {
		return nil, false, err
	}
	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(dst, hasher), part)
	dst.Close()
	if err != nil {
		os.Remove(tmp)
		return nil, false, err
	}
	checksum := hex.EncodeToString(hasher.Sum(nil))

	ct := header.Header.Get("Content-Type")
	if ct == "" {
		ct = mime.TypeByExtension(filepath.Ext(name))
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	now := time.Now().UTC()
	limit := s.versionLimit()

	// Brand-new file.
	if existing == nil {
		id := newID(16)
		stored := filepath.Join(s.filesDir, id)
		f := &File{
			ID: id, Name: name, Folder: folder, Size: size, Checksum: checksum,
			ContentType: ct, StoredPath: stored, UploadedAt: now, UploadedBy: uploader,
		}
		if limit > 0 {
			// Versioning on: the content becomes version 1 (the current pointer).
			vid := newID(16)
			if err := os.Rename(tmp, filepath.Join(s.versionsDir, vid)); err != nil {
				os.Remove(tmp)
				return nil, false, err
			}
			if err := copyFile(filepath.Join(s.versionsDir, vid), stored); err != nil {
				return nil, false, err
			}
			f.CurrentVersionID = vid
			if err := s.store.insertFile(f); err != nil {
				return nil, false, err
			}
			_ = s.store.addFileVersion(&FileVersion{
				ID: vid, FileID: id, VersionNo: 1, Name: name, Folder: folder,
				Size: size, Checksum: checksum, ContentType: ct, UploadedBy: uploader, CreatedAt: now,
			})
			return f, true, nil
		}
		if err := os.Rename(tmp, stored); err != nil {
			os.Remove(tmp)
			return nil, false, err
		}
		if err := s.store.insertFile(f); err != nil {
			os.Remove(stored)
			return nil, false, err
		}
		return f, true, nil
	}

	// Same-named file exists. Identical content → just touch metadata, no version.
	stored := filepath.Join(s.filesDir, existing.ID)
	if checksum == existing.Checksum {
		os.Remove(tmp)
		_ = s.store.updateFileContent(existing.ID, size, ct, checksum, now, uploader)
		f, err := s.store.getFile(existing.ID)
		return f, false, err
	}

	// Versioning off → overwrite in place and drop any history.
	if limit <= 0 {
		if err := os.Rename(tmp, stored); err != nil {
			os.Remove(tmp)
			return nil, false, err
		}
		_ = s.store.updateFileContent(existing.ID, size, ct, checksum, now, uploader)
		s.purgeFileVersions(existing.ID)
		_ = s.store.setCurrentVersion(existing.ID, "")
		f, err := s.store.getFile(existing.ID)
		return f, false, err
	}

	// Versioning on. Legacy files whose current content isn't a version yet
	// (uploaded before versioning, or while it was off) get snapshotted first so
	// the existing content is preserved as a revision.
	if existing.CurrentVersionID == "" {
		lvid := newID(16)
		if err := copyFile(existing.StoredPath, filepath.Join(s.versionsDir, lvid)); err == nil {
			_ = s.store.addFileVersion(&FileVersion{
				ID: lvid, FileID: existing.ID, VersionNo: s.store.nextVersionNo(existing.ID),
				Name: existing.Name, Folder: existing.Folder, Size: existing.Size, Checksum: existing.Checksum,
				ContentType: existing.ContentType, UploadedBy: existing.UploadedBy, CreatedAt: existing.UploadedAt,
			})
		}
	}
	// Add the new content as a new version and point current at it.
	vid := newID(16)
	if err := os.Rename(tmp, filepath.Join(s.versionsDir, vid)); err != nil {
		os.Remove(tmp)
		return nil, false, err
	}
	if err := copyFile(filepath.Join(s.versionsDir, vid), stored); err != nil {
		return nil, false, err
	}
	_ = s.store.addFileVersion(&FileVersion{
		ID: vid, FileID: existing.ID, VersionNo: s.store.nextVersionNo(existing.ID),
		Name: name, Folder: existing.Folder, Size: size, Checksum: checksum,
		ContentType: ct, UploadedBy: uploader, CreatedAt: now,
	})
	_ = s.store.updateFileContent(existing.ID, size, ct, checksum, now, uploader)
	_ = s.store.setCurrentVersion(existing.ID, vid)
	s.pruneVersionsKeep(existing.ID, limit, vid)
	f, err := s.store.getFile(existing.ID)
	return f, false, err
}

// copyFile copies src to dst (dst is truncated/created).
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// pruneVersionsKeep keeps the newest `limit` revisions (always keeping the
// current one) and deletes the rest (blobs + rows).
func (s *Server) pruneVersionsKeep(fileID string, limit int, currentVID string) {
	vers, err := s.store.listFileVersions(fileID) // newest first
	if err != nil || len(vers) <= limit {
		return
	}
	keep := map[string]bool{currentVID: true}
	for _, v := range vers {
		if len(keep) >= limit {
			break
		}
		keep[v.ID] = true
	}
	for _, v := range vers {
		if keep[v.ID] {
			continue
		}
		os.Remove(filepath.Join(s.versionsDir, v.ID))
		_ = s.store.deleteFileVersionRow(v.ID)
	}
}

// versionLimit is the number of previous revisions kept per file (0 disables
// versioning → re-uploads overwrite in place). Configurable in 설정 > Server.
func (s *Server) versionLimit() int {
	v, _ := s.store.getSetting("version_limit")
	if v == "" {
		return defaultVersionLimit
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return defaultVersionLimit
	}
	if n > maxVersionLimit {
		n = maxVersionLimit
	}
	return n
}

const (
	defaultVersionLimit = 10
	maxVersionLimit     = 100
)

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUpload)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		httpError(w, http.StatusBadRequest, "invalid upload: %v", err)
		return
	}
	if !s.allowWrite(w, r, normalizeFolder(r.FormValue("folder"))) {
		return
	}
	part, header, err := r.FormFile("file")
	if err != nil {
		httpError(w, http.StatusBadRequest, "missing 'file' field: %v", err)
		return
	}
	defer part.Close()
	actor := ""
	if u := s.currentUser(r); u != nil {
		actor = u.Username
	}
	f, created, err := s.storeUpload(part, header, r.FormValue("folder"), actor)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "upload failed: %v", err)
		return
	}
	log.Printf("upload: %q -> %s (user=%s, ip=%s)", f.Name, f.Folder, actor, clientIP(r))
	_ = s.store.addLog(&AccessLog{
		Action: "upload", Actor: actor, FileID: f.ID,
		IP: clientIP(r), UserAgent: r.UserAgent(), AccessedAt: time.Now().UTC(),
	})
	detail := ""
	if !created {
		detail = "새 버전(덮어쓰기)"
	}
	s.audit(r, "file_upload", strings.TrimRight(f.Folder, "/")+"/"+f.Name, detail)
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, s.fileView(f))
}

// handleAPIUpload lets a user upload with curl using their X-API-Key.
func (s *Server) handleAPIUpload(w http.ResponseWriter, r *http.Request) {
	secret := r.Header.Get("X-API-Key")
	if secret == "" {
		s.logDenied(r, "upload", "API 키 없음", "", "", "")
		httpError(w, http.StatusUnauthorized, "missing X-API-Key header")
		return
	}
	key, err := s.store.lookupKey(secret)
	if err != nil {
		reason := "알 수 없는 키 (" + maskKeyGo(secret) + ")"
		switch s.keyOrigin(secret) {
		case "forged":
			reason = "위조된 키·서명 불일치 (" + maskKeyGo(secret) + ")"
		case "server":
			reason = "삭제된 키·서명은 유효 (" + maskKeyGo(secret) + ")"
		}
		s.logDenied(r, "upload", reason, "", "", "")
		s.recordKeyFailure(clientIP(r))
		httpError(w, http.StatusForbidden, "invalid, disabled, or revoked API key")
		return
	}
	if key.Revoked || key.Disabled {
		state := "폐기된 키"
		if key.Disabled {
			state = "비활성화된 키"
		}
		s.logDenied(r, "upload", state+" ("+maskKeyGo(secret)+")", s.keyOwnerName(key), key.ID, "")
		httpError(w, http.StatusForbidden, "invalid, disabled, or revoked API key")
		return
	}
	if !scopeAllowsUpload(key.Scope) {
		s.logDenied(r, "upload", "업로드 권한 없는 키 (scope="+key.Scope+")", s.keyOwnerName(key), key.ID, "")
		httpError(w, http.StatusForbidden, "이 키는 업로드용이 아닙니다 (업로드용 키를 사용하세요)")
		return
	}
	// Personal keys: the owner must have upload permission (view cannot upload).
	// Service keys are admin-created and always allowed.
	owner := "service"
	var ownerUser *User
	if !key.IsService {
		u, err := s.store.getUser(key.UserID)
		if err != nil || !isEditor(u.Role) {
			log.Printf("api upload denied: key=%s no upload permission (ip=%s)", key.ID, clientIP(r))
			s.logDenied(r, "upload", "업로드 권한 없는 사용자", s.keyOwnerName(key), key.ID, "")
			httpError(w, http.StatusForbidden, "이 키는 업로드 권한이 없습니다 (user/admin 키만 가능)")
			return
		}
		owner = u.Username
		ownerUser = u
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUpload)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		httpError(w, http.StatusBadRequest, "invalid upload: %v", err)
		return
	}
	part, header, err := r.FormFile("file")
	if err != nil {
		httpError(w, http.StatusBadRequest, "missing 'file' field (use -F \"file=@path\")")
		return
	}
	defer part.Close()
	folder := r.FormValue("folder")
	if folder == "" {
		folder = r.URL.Query().Get("folder")
	}
	// Enforce the key owner's folder write access (service keys bypass).
	if ownerUser != nil && !canWrite(s.folderAccess(ownerUser, normalizeFolder(folder))) {
		log.Printf("api upload denied: key=%s user=%s no write on %q (ip=%s)", key.ID, owner, normalizeFolder(folder), clientIP(r))
		s.logDenied(r, "upload", "폴더 쓰기 권한 없음 ("+normalizeFolder(folder)+")", owner, key.ID, "")
		httpError(w, http.StatusForbidden, "이 폴더에 대한 쓰기 권한이 없습니다")
		return
	}
	f, created, err := s.storeUpload(part, header, folder, owner)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "upload failed: %v", err)
		return
	}
	now := time.Now().UTC()
	_ = s.store.touchKey(key.ID, now)
	log.Printf("api upload: %q -> %s (key=%s, user=%s, ip=%s)", f.Name, f.Folder, key.ID, owner, clientIP(r))
	_ = s.store.addLog(&AccessLog{
		Action: "upload", Actor: owner, APIKeyID: key.ID, FileID: f.ID,
		IP: clientIP(r), UserAgent: r.UserAgent(), AccessedAt: now,
	})
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, s.fileView(f))
}

func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	folder := normalizeFolder(r.URL.Query().Get("folder"))
	if !s.allowRead(w, r, folder) {
		return
	}
	files, err := s.store.listFilesInFolder(folder)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "db error: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, s.fileViews(files))
}

// ---- folders ----

func (s *Server) handleFolderCounts(w http.ResponseWriter, r *http.Request) {
	counts, err := s.store.folderFileCounts()
	if err != nil {
		httpError(w, http.StatusInternalServerError, "db error: %v", err)
		return
	}
	// Only report counts for folders the user can read.
	u := s.currentUser(r)
	for folder := range counts {
		if !canRead(s.folderAccess(u, folder)) {
			delete(counts, folder)
		}
	}
	writeJSON(w, http.StatusOK, counts)
}

func (s *Server) handleListFolders(w http.ResponseWriter, r *http.Request) {
	folders, err := s.store.listFolders()
	if err != nil {
		httpError(w, http.StatusInternalServerError, "db error: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, s.visibleFolders(s.currentUser(r), folders))
}

func (s *Server) handleCreateFolder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	path := normalizeFolder(body.Path)
	if path == "/" {
		httpError(w, http.StatusBadRequest, "invalid folder name")
		return
	}
	// Need write access to the parent folder to create a subfolder.
	parent := "/"
	if i := strings.LastIndex(path, "/"); i > 0 {
		parent = path[:i]
	}
	if !s.allowWrite(w, r, parent) {
		return
	}
	// Create the folder and all of its ancestors.
	for _, p := range ancestorFolders(path) {
		if err := s.store.createFolder(p); err != nil {
			httpError(w, http.StatusInternalServerError, "db error: %v", err)
			return
		}
	}
	s.audit(r, "folder_create", path, "")
	writeJSON(w, http.StatusCreated, map[string]string{"path": path})
}

func (s *Server) handleDeleteFolder(w http.ResponseWriter, r *http.Request) {
	path := normalizeFolder(r.URL.Query().Get("path"))
	if path == "/" {
		httpError(w, http.StatusBadRequest, "root folder cannot be deleted")
		return
	}
	if !s.allowWrite(w, r, path) {
		return
	}
	force := r.URL.Query().Get("force") == "true"
	n, err := s.store.folderContentCount(path)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "db error: %v", err)
		return
	}
	if n > 0 {
		if !force {
			httpError(w, http.StatusConflict, "folder is not empty")
			return
		}
		// Force delete of a non-empty folder is restricted to managers (admin+).
		u := s.currentUser(r)
		if u == nil || !isManager(u.Role) {
			httpError(w, http.StatusForbidden, "관리자(admin) 이상만 비어있지 않은 폴더를 삭제할 수 있습니다")
			return
		}
		// Move all files under the folder (and subfolders) to the trash so they
		// remain recoverable, then remove the folder tree + permission grants.
		by := u.Username
		moved, err := s.store.trashFilesUnderFolder(path, time.Now().UTC(), by)
		if err != nil {
			httpError(w, http.StatusInternalServerError, "db error: %v", err)
			return
		}
		if err := s.store.deleteFolderTree(path); err != nil {
			httpError(w, http.StatusInternalServerError, "db error: %v", err)
			return
		}
		s.audit(r, "folder_delete", path, fmt.Sprintf("하위 파일 %d개 휴지통 이동", moved))
		writeJSON(w, http.StatusOK, map[string]any{"status": "folder-trashed", "trashed_files": moved})
		return
	}
	if err := s.store.deleteFolder(path); err != nil {
		httpError(w, http.StatusNotFound, "folder not found")
		return
	}
	s.audit(r, "folder_delete", path, "")
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleListTrash(w http.ResponseWriter, r *http.Request) {
	files, err := s.store.listFiles(true)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "db error: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, s.fileViews(files))
}

// ---- preview ----

func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	f, err := s.store.getFile(r.PathValue("id"))
	if err != nil {
		httpError(w, http.StatusNotFound, "file not found")
		return
	}
	if !s.allowRead(w, r, f.Folder) {
		return
	}
	file, err := os.Open(f.StoredPath)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "cannot read file")
		return
	}
	defer file.Close()

	kind := previewKind(f.ContentType, f.Name)
	w.Header().Set("X-Preview-Kind", kind)
	switch kind {
	case "text":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.Copy(w, io.LimitReader(file, s.cfg.PreviewLimit))
	case "image":
		w.Header().Set("Content-Type", f.ContentType)
		io.Copy(w, file)
	default:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "(미리보기를 지원하지 않는 형식입니다: "+f.ContentType+")")
	}
}

// ---- delete / restore ----

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	force := r.URL.Query().Get("force") == "true"
	f, err := s.store.getFile(id)
	if err != nil {
		httpError(w, http.StatusNotFound, "file not found")
		return
	}
	if !s.allowWrite(w, r, f.Folder) {
		return
	}

	if force {
		// Permanent deletion: remove the blob wherever it lives, then the row.
		os.Remove(f.StoredPath)
		s.purgeFileVersions(id)             // drop archived previous revisions
		_ = s.store.deleteSharesForFile(id) // drop its public share links
		if err := s.store.deleteFileRow(id); err != nil {
			httpError(w, http.StatusInternalServerError, "db error: %v", err)
			return
		}
		s.audit(r, "file_purge", strings.TrimRight(f.Folder, "/")+"/"+f.Name, "완전 삭제")
		writeJSON(w, http.StatusOK, map[string]string{"status": "purged"})
		return
	}

	if f.DeletedAt != nil {
		httpError(w, http.StatusConflict, "file is already in trash")
		return
	}
	// Soft delete: the blob stays in place; trash is a logical state (deleted_at).
	now := time.Now().UTC()
	by := ""
	if u := s.currentUser(r); u != nil {
		by = u.Username
	}
	if err := s.store.softDeleteFile(id, now, by); err != nil {
		httpError(w, http.StatusInternalServerError, "db error: %v", err)
		return
	}
	purgeAt := now.Add(s.cfg.TrashTTL)
	s.audit(r, "file_delete", strings.TrimRight(f.Folder, "/")+"/"+f.Name, "휴지통 이동")
	writeJSON(w, http.StatusOK, map[string]any{"status": "trashed", "purge_at": purgeAt})
}

func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	f, err := s.store.getFile(id)
	if err != nil {
		httpError(w, http.StatusNotFound, "file not in trash")
		return
	}
	if !s.allowWrite(w, r, f.Folder) {
		return
	}
	// The file's folder may have been removed (e.g. folder deletion moved it to
	// trash). Recreate the folder path so the restored file is navigable again.
	if f.Folder != "/" {
		for _, p := range ancestorFolders(f.Folder) {
			_ = s.store.createFolder(p)
		}
	}
	if err := s.store.restoreFile(id); err != nil {
		httpError(w, http.StatusNotFound, "file not in trash")
		return
	}
	s.audit(r, "file_restore", strings.TrimRight(f.Folder, "/")+"/"+f.Name, "휴지통에서 복구")
	writeJSON(w, http.StatusOK, map[string]string{"status": "restored"})
}

func (s *Server) handleMoveFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Folder string `json:"folder"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	folder := normalizeFolder(body.Folder)

	f, err := s.store.getFile(id)
	if err != nil || f.DeletedAt != nil {
		httpError(w, http.StatusNotFound, "file not found")
		return
	}
	// Need write on both the source (to remove) and destination (to place).
	if !s.allowWrite(w, r, f.Folder) || !s.allowWrite(w, r, folder) {
		return
	}
	if f.Folder == folder {
		writeJSON(w, http.StatusOK, map[string]string{"status": "unchanged"})
		return
	}
	// Refuse to overwrite an existing file with the same name in the target folder.
	if dup, _ := s.store.getActiveFileByNameInFolder(f.Name, folder); dup != nil {
		httpError(w, http.StatusConflict, "target folder already has a file named %q", f.Name)
		return
	}
	if !s.store.folderExists(folder) {
		for _, p := range ancestorFolders(folder) {
			_ = s.store.createFolder(p)
		}
	}
	if err := s.store.moveFile(id, folder); err != nil {
		httpError(w, http.StatusInternalServerError, "db error: %v", err)
		return
	}
	s.audit(r, "file_move", f.Name, strings.TrimRight(f.Folder, "/")+"/ → "+strings.TrimRight(folder, "/")+"/")
	writeJSON(w, http.StatusOK, map[string]any{"status": "moved", "folder": folder})
}

// handleMoveFolder relocates a folder (and its whole subtree) under a new parent.
func (s *Server) handleMoveFolder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path   string `json:"path"`   // 이동할 폴더
		Parent string `json:"parent"` // 옮겨 갈 상위 폴더
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	src := normalizeFolder(body.Path)
	parent := normalizeFolder(body.Parent)
	if src == "/" {
		httpError(w, http.StatusBadRequest, "루트 폴더는 이동할 수 없습니다")
		return
	}
	name := src[strings.LastIndex(src, "/")+1:]
	dst := normalizeFolder(strings.TrimRight(parent, "/") + "/" + name)
	if dst == src {
		writeJSON(w, http.StatusOK, map[string]any{"status": "unchanged", "path": src})
		return
	}
	// A folder cannot be moved into itself or one of its own descendants.
	if dst == src || strings.HasPrefix(parent+"/", src+"/") {
		httpError(w, http.StatusBadRequest, "폴더를 자기 자신 또는 하위 폴더로 이동할 수 없습니다")
		return
	}
	// Need write on the source (to remove it) and on the destination parent (to place it).
	if !s.allowWrite(w, r, src) || !s.allowWrite(w, r, parent) {
		return
	}
	if !s.store.folderExists(src) {
		httpError(w, http.StatusNotFound, "이동할 폴더를 찾을 수 없습니다")
		return
	}
	if s.store.folderExists(dst) {
		httpError(w, http.StatusConflict, "이동 위치에 같은 이름의 폴더가 이미 있습니다")
		return
	}
	// Ensure the destination parent chain exists (it normally does).
	if parent != "/" {
		for _, p := range ancestorFolders(parent) {
			_ = s.store.createFolder(p)
		}
	}
	if err := s.store.moveFolderTree(src, dst); err != nil {
		httpError(w, http.StatusInternalServerError, "db error: %v", err)
		return
	}
	s.audit(r, "folder_move", name, src+" → "+dst)
	writeJSON(w, http.StatusOK, map[string]any{"status": "moved", "path": dst})
}

// ---- view rendering ----

type FileView struct {
	*File
	DownloadURL string     `json:"download_url"`
	PathURL     string     `json:"path_url"`
	CurlCommand string     `json:"curl_command"`
	ExecCommand string     `json:"exec_command,omitempty"`
	PurgeAt     *time.Time `json:"purge_at,omitempty"`
}

func (s *Server) fileView(f *File) FileView {
	url := s.cfg.PublicBaseURL + "/d/" + f.ID
	v := FileView{
		File:        f,
		DownloadURL: url,
		PathURL:     s.cfg.PublicBaseURL + "/f/" + folderNamePath(f.Folder, f.Name),
		CurlCommand: "curl -H \"X-API-Key: <YOUR_API_KEY>\" -O -J " + url,
	}
	// For shell scripts, offer a download-and-run one-liner.
	if strings.HasSuffix(strings.ToLower(f.Name), ".sh") {
		v.ExecCommand = "curl -sH \"X-API-Key: <YOUR_API_KEY>\" " + url + " | bash"
	}
	if f.DeletedAt != nil {
		p := f.DeletedAt.Add(s.cfg.TrashTTL)
		v.PurgeAt = &p
	}
	return v
}

// folderNamePath builds a URL-escaped "folder/segments/name" path (no leading slash).
func folderNamePath(folder, name string) string {
	var segs []string
	for _, s := range strings.Split(strings.Trim(folder, "/"), "/") {
		if s != "" {
			segs = append(segs, url.PathEscape(s))
		}
	}
	segs = append(segs, url.PathEscape(name))
	return strings.Join(segs, "/")
}

func (s *Server) fileViews(files []*File) []FileView {
	out := make([]FileView, 0, len(files))
	for _, f := range files {
		out = append(out, s.fileView(f))
	}
	return out
}
