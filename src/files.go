package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ---- upload / list ----

// storeUpload saves an uploaded file part into folder, overwriting a same-named
// active file if present. Returns the file and whether it was newly created.
func (s *Server) storeUpload(part multipart.File, header *multipart.FileHeader, folderArg string) (*File, bool, error) {
	name := filepath.Base(header.Filename)
	folder := normalizeFolder(folderArg)
	if !s.store.folderExists(folder) {
		_ = s.store.createFolder(folder)
	}

	// Overwrite a same-named active file in this folder (keep its id/link).
	existing, _ := s.store.getActiveFileByNameInFolder(name, folder)
	id := newID(16)
	if existing != nil {
		id = existing.ID
	}
	stored := filepath.Join(s.filesDir, id)
	dst, err := os.Create(stored) // truncates when overwriting
	if err != nil {
		return nil, false, err
	}
	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(dst, hasher), part)
	dst.Close()
	if err != nil {
		if existing == nil {
			os.Remove(stored)
		}
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

	if existing != nil {
		if err := s.store.updateFileContent(id, size, ct, checksum, now); err != nil {
			return nil, false, err
		}
		f, err := s.store.getFile(id)
		return f, false, err
	}
	f := &File{
		ID: id, Name: name, Folder: folder, Size: size, Checksum: checksum,
		ContentType: ct, StoredPath: stored, UploadedAt: now,
	}
	if err := s.store.insertFile(f); err != nil {
		os.Remove(stored)
		return nil, false, err
	}
	return f, true, nil
}

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
	f, created, err := s.storeUpload(part, header, r.FormValue("folder"))
	if err != nil {
		httpError(w, http.StatusInternalServerError, "upload failed: %v", err)
		return
	}
	actor := ""
	if u := s.currentUser(r); u != nil {
		actor = u.Username
	}
	log.Printf("upload: %q -> %s (user=%s, ip=%s)", f.Name, f.Folder, actor, clientIP(r))
	_ = s.store.addLog(&AccessLog{
		Action: "upload", Actor: actor, FileID: f.ID,
		IP: clientIP(r), UserAgent: r.UserAgent(), AccessedAt: time.Now().UTC(),
	})
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
	f, created, err := s.storeUpload(part, header, folder)
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
	n, err := s.store.folderContentCount(path)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "db error: %v", err)
		return
	}
	if n > 0 {
		httpError(w, http.StatusConflict, "folder is not empty")
		return
	}
	if err := s.store.deleteFolder(path); err != nil {
		httpError(w, http.StatusNotFound, "folder not found")
		return
	}
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
		if err := s.store.deleteFileRow(id); err != nil {
			httpError(w, http.StatusInternalServerError, "db error: %v", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "purged"})
		return
	}

	if f.DeletedAt != nil {
		httpError(w, http.StatusConflict, "file is already in trash")
		return
	}
	// Soft delete: the blob stays in place; trash is a logical state (deleted_at).
	now := time.Now().UTC()
	if err := s.store.softDeleteFile(id, now); err != nil {
		httpError(w, http.StatusInternalServerError, "db error: %v", err)
		return
	}
	purgeAt := now.Add(s.cfg.TrashTTL)
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
	if err := s.store.restoreFile(id); err != nil {
		httpError(w, http.StatusNotFound, "file not in trash")
		return
	}
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
	writeJSON(w, http.StatusOK, map[string]any{"status": "moved", "folder": folder})
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
