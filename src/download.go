package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ---- download (public, API-key protected) ----

// notFoundDownload returns a generic 404 to the client (no details) and logs the
// real reason server-side, so we never reveal whether a key or file exists.
func (s *Server) notFoundDownload(w http.ResponseWriter, r *http.Request, reason string) {
	log.Printf("download denied: %s (ip=%s, path=%s, ua=%q)", reason, clientIP(r), r.URL.Path, r.UserAgent())
	httpError(w, http.StatusNotFound, "not found")
}

// logDenied records a rejected upload/download attempt in the access log.
func (s *Server) logDenied(r *http.Request, action, reason, actor, keyID, fileID string) {
	_ = s.store.addLog(&AccessLog{
		Action: action, Status: "denied", Detail: reason, Actor: actor,
		APIKeyID: keyID, FileID: fileID, IP: clientIP(r),
		UserAgent: r.UserAgent(), AccessedAt: time.Now().UTC(),
	})
}

// authDownload validates the X-API-Key header. On failure it writes a 404,
// records a denied entry in the access log, and returns nil.
func (s *Server) authDownload(w http.ResponseWriter, r *http.Request) *APIKey {
	secret := r.Header.Get("X-API-Key")
	if secret == "" {
		s.logDenied(r, "download", "API 키 없음", "", "", "")
		s.notFoundDownload(w, r, "missing X-API-Key header")
		return nil
	}
	key, err := s.store.lookupKey(secret)
	if err != nil {
		// Not in the DB — classify with the HMAC signature for a precise reason.
		reason := "알 수 없는 키 (" + maskKeyGo(secret) + ")"
		switch s.keyOrigin(secret) {
		case "forged":
			reason = "위조된 키·서명 불일치 (" + maskKeyGo(secret) + ")"
		case "server":
			reason = "삭제된 키·서명은 유효 (" + maskKeyGo(secret) + ")"
		}
		s.logDenied(r, "download", reason, "", "", "")
		s.recordKeyFailure(clientIP(r))
		s.notFoundDownload(w, r, "unknown API key")
		return nil
	}
	owner := s.keyOwnerName(key)
	if key.Disabled {
		s.logDenied(r, "download", "비활성화된 키 ("+maskKeyGo(secret)+")", owner, key.ID, "")
		s.notFoundDownload(w, r, fmt.Sprintf("disabled key id=%s label=%q", key.ID, key.Label))
		return nil
	}
	if key.Revoked {
		s.logDenied(r, "download", "폐기된 키 ("+maskKeyGo(secret)+")", owner, key.ID, "")
		s.notFoundDownload(w, r, fmt.Sprintf("revoked key id=%s label=%q", key.ID, key.Label))
		return nil
	}
	if !scopeAllowsDownload(key.Scope) {
		s.logDenied(r, "download", "다운로드 권한 없는 키 (scope="+key.Scope+")", owner, key.ID, "")
		s.notFoundDownload(w, r, fmt.Sprintf("key not a download key id=%s scope=%s", key.ID, key.Scope))
		return nil
	}
	return key
}

// handleDownload serves a file by its id (/d/{id} or /d/{id}/{name}).
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	key := s.authDownload(w, r)
	if key == nil {
		return
	}
	f, err := s.store.getFile(r.PathValue("id"))
	if err != nil || f.DeletedAt != nil {
		s.notFoundDownload(w, r, "file not found id="+r.PathValue("id"))
		return
	}
	if !s.keyCanRead(key, f.Folder) {
		s.logDenied(r, "download", "폴더 접근 권한 없음 ("+f.Folder+")", s.keyOwnerName(key), key.ID, f.ID)
		s.notFoundDownload(w, r, fmt.Sprintf("key=%s no read on %q", key.ID, f.Folder))
		return
	}
	s.serveDownload(w, r, f, key)
}

// keyCanRead reports whether an API key may read files in folder. Service keys
// bypass; personal keys are bound to their owner's folder grants.
func (s *Server) keyCanRead(key *APIKey, folder string) bool {
	if key.IsService {
		return true
	}
	u, err := s.store.getUser(key.UserID)
	if err != nil {
		return false
	}
	return canRead(s.folderAccess(u, folder))
}

// handleDownloadByPath serves a file by its folder path + name (/f/<folder>/<name>).
func (s *Server) handleDownloadByPath(w http.ResponseWriter, r *http.Request) {
	key := s.authDownload(w, r)
	if key == nil {
		return
	}
	p := strings.Trim(r.PathValue("path"), "/")
	if p == "" {
		s.notFoundDownload(w, r, "empty path")
		return
	}
	folder, name := "/", p
	if i := strings.LastIndex(p, "/"); i >= 0 {
		folder = normalizeFolder(p[:i])
		name = p[i+1:]
	}
	f, err := s.store.getActiveFileByNameInFolder(name, folder)
	if err != nil {
		s.notFoundDownload(w, r, fmt.Sprintf("file not found folder=%q name=%q", folder, name))
		return
	}
	if !s.keyCanRead(key, f.Folder) {
		s.logDenied(r, "download", "폴더 접근 권한 없음 ("+f.Folder+")", s.keyOwnerName(key), key.ID, f.ID)
		s.notFoundDownload(w, r, fmt.Sprintf("key=%s no read on %q", key.ID, f.Folder))
		return
	}
	s.serveDownload(w, r, f, key)
}

// keyOwnerName returns a human label for who a key belongs to.
func (s *Server) keyOwnerName(key *APIKey) string {
	if key.IsService {
		return "service"
	}
	if u, err := s.store.getUser(key.UserID); err == nil {
		return u.Username
	}
	return key.Owner
}

// serveDownload logs the access and streams the file (caller has authenticated).
func (s *Server) serveDownload(w http.ResponseWriter, r *http.Request, f *File, key *APIKey) {
	now := time.Now().UTC()
	_ = s.store.addLog(&AccessLog{
		Action: "download", Actor: s.keyOwnerName(key),
		APIKeyID: key.ID, FileID: f.ID, IP: clientIP(r),
		UserAgent: r.UserAgent(), AccessedAt: now,
	})
	_ = s.store.touchKey(key.ID, now)
	s.streamFile(w, f)
}

// streamFile opens the stored blob and streams it as an attachment.
func (s *Server) streamFile(w http.ResponseWriter, f *File) {
	file, err := os.Open(f.StoredPath)
	if err != nil {
		log.Printf("download error: cannot open %s: %v", f.StoredPath, err)
		httpError(w, http.StatusInternalServerError, "cannot read file")
		return
	}
	defer file.Close()

	_ = s.store.incrementDownload(f.ID)

	w.Header().Set("Content-Type", f.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(f.Size, 10))
	w.Header().Set("Content-Disposition", "attachment; filename=\""+sanitizeFilename(f.Name)+"\"")
	io.Copy(w, file)
}

// handleSessionDownload lets a logged-in dashboard user download a file to their
// PC without an API key (cookie-authenticated).
func (s *Server) handleSessionDownload(w http.ResponseWriter, r *http.Request) {
	f, err := s.store.getFile(r.PathValue("id"))
	if err != nil || f.DeletedAt != nil {
		httpError(w, http.StatusNotFound, "not found")
		return
	}
	if !s.allowRead(w, r, f.Folder) {
		return
	}
	s.logSession(r, "download", f.ID)
	s.streamFile(w, f)
}

// logSession records a dashboard (cookie-authed) action in the access log.
func (s *Server) logSession(r *http.Request, action, fileID string) {
	actor := ""
	if u := s.currentUser(r); u != nil {
		actor = u.Username
	}
	_ = s.store.addLog(&AccessLog{
		Action: action, Actor: actor, FileID: fileID,
		IP: clientIP(r), UserAgent: r.UserAgent(), AccessedAt: time.Now().UTC(),
	})
}

// handleDownloadZip streams the given file ids as a single .zip (cookie-authed).
// Used for multi-select downloads from the dashboard.
func (s *Server) handleDownloadZip(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if len(body.IDs) == 0 {
		httpError(w, http.StatusBadRequest, "no files selected")
		return
	}
	// Resolve valid, readable files up front so we can fail cleanly before the body.
	u := s.currentUser(r)
	var files []*File
	for _, id := range body.IDs {
		f, err := s.store.getFile(id)
		if err != nil || f.DeletedAt != nil {
			continue
		}
		if !canRead(s.folderAccess(u, f.Folder)) {
			continue
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		httpError(w, http.StatusNotFound, "not found")
		return
	}

	zipName := fmt.Sprintf("files-%s.zip", time.Now().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+zipName+"\"")

	zw := zip.NewWriter(w)
	defer zw.Close()
	used := map[string]int{}
	for _, f := range files {
		entry := zipEntryName(f.Name, used)
		hdr := &zip.FileHeader{Name: entry, Method: zip.Deflate, Modified: f.UploadedAt}
		fw, err := zw.CreateHeader(hdr)
		if err != nil {
			log.Printf("zip: create entry %q: %v", entry, err)
			return
		}
		src, err := os.Open(f.StoredPath)
		if err != nil {
			log.Printf("zip: open %s: %v", f.StoredPath, err)
			continue
		}
		_, err = io.Copy(fw, src)
		src.Close()
		if err != nil {
			log.Printf("zip: copy %s: %v", f.Name, err)
			return
		}
		_ = s.store.incrementDownload(f.ID)
		s.logSession(r, "download", f.ID)
	}
}

// zipEntryName returns a unique entry name, appending " (n)" on duplicates.
func zipEntryName(name string, used map[string]int) string {
	out := name
	if n := used[name]; n > 0 {
		ext := filepath.Ext(name)
		out = fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), n, ext)
	}
	used[name]++
	return out
}
