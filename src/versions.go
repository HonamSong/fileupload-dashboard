package main

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// GET /api/files/{id}/versions — current file + its archived previous revisions.
func (s *Server) handleListVersions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	f, err := s.store.getFile(id)
	if err != nil {
		httpError(w, http.StatusNotFound, "file not found")
		return
	}
	if !s.allowRead(w, r, f.Folder) {
		return
	}
	vers, err := s.store.listFileVersions(id)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "db error: %v", err)
		return
	}
	if vers == nil {
		vers = []*FileVersion{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"current":  s.fileView(f),
		"versions": vers,
		"limit":    s.versionLimit(),
	})
}

// versionFilename inserts ".v<no>" before the extension so a downloaded revision
// doesn't clash with the current file (e.g. report.v2.pdf).
func versionFilename(name string, no int) string {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	return base + ".v" + strconv.Itoa(no) + ext
}

// GET /api/files/{id}/versions/{vid}/download — download one archived revision.
func (s *Server) handleDownloadVersion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	f, err := s.store.getFile(id)
	if err != nil {
		httpError(w, http.StatusNotFound, "file not found")
		return
	}
	if !s.allowRead(w, r, f.Folder) {
		return
	}
	v, err := s.store.getFileVersion(id, r.PathValue("vid"))
	if err != nil {
		httpError(w, http.StatusNotFound, "version not found")
		return
	}
	file, err := os.Open(filepath.Join(s.versionsDir, v.ID))
	if err != nil {
		httpError(w, http.StatusInternalServerError, "cannot read version")
		return
	}
	defer file.Close()
	s.logSession(r, "download", f.ID)
	w.Header().Set("Content-Type", v.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(v.Size, 10))
	w.Header().Set("Content-Disposition", "attachment; filename=\""+sanitizeFilename(versionFilename(v.Name, v.VersionNo))+"\"")
	io.Copy(w, file)
}

// POST /api/files/{id}/versions/{vid}/restore — promote a revision to current.
// The current content is archived as a new revision so nothing is lost.
func (s *Server) handleRestoreVersion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	f, err := s.store.getFile(id)
	if err != nil || f.DeletedAt != nil {
		httpError(w, http.StatusNotFound, "file not found")
		return
	}
	if !s.allowWrite(w, r, f.Folder) {
		return
	}
	v, err := s.store.getFileVersion(id, r.PathValue("vid"))
	if err != nil {
		httpError(w, http.StatusNotFound, "version not found")
		return
	}
	// Restore just moves the current pointer to an existing revision — it never
	// creates a new version. If it's already current, there's nothing to do.
	if v.ID == f.CurrentVersionID {
		writeJSON(w, http.StatusOK, map[string]any{"status": "unchanged", "version_no": v.VersionNo})
		return
	}
	// Copy the chosen revision's content into the live blob and repoint current.
	if err := copyFile(filepath.Join(s.versionsDir, v.ID), f.StoredPath); err != nil {
		httpError(w, http.StatusInternalServerError, "version blob missing")
		return
	}
	actor := ""
	if u := s.currentUser(r); u != nil {
		actor = u.Username
	}
	_ = s.store.updateFileContent(id, v.Size, v.ContentType, v.Checksum, time.Now().UTC(), actor)
	_ = s.store.setCurrentVersion(id, v.ID)
	s.audit(r, "file_version_restore", strings.TrimRight(f.Folder, "/")+"/"+f.Name, "v"+strconv.Itoa(v.VersionNo)+" (으)로 전환")
	writeJSON(w, http.StatusOK, map[string]any{"status": "restored", "version_no": v.VersionNo})
}

// DELETE /api/files/{id}/versions/{vid} — permanently remove one revision.
// The current version cannot be deleted (switch to another version first).
func (s *Server) handleDeleteVersion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	f, err := s.store.getFile(id)
	if err != nil {
		httpError(w, http.StatusNotFound, "file not found")
		return
	}
	if !s.allowWrite(w, r, f.Folder) {
		return
	}
	v, err := s.store.getFileVersion(id, r.PathValue("vid"))
	if err != nil {
		httpError(w, http.StatusNotFound, "version not found")
		return
	}
	if v.ID == f.CurrentVersionID {
		httpError(w, http.StatusConflict, "현재 버전은 삭제할 수 없습니다. 먼저 다른 버전으로 전환하세요")
		return
	}
	os.Remove(filepath.Join(s.versionsDir, v.ID))
	_ = s.store.deleteFileVersionRow(v.ID)
	s.audit(r, "file_version_delete", strings.TrimRight(f.Folder, "/")+"/"+f.Name, "v"+strconv.Itoa(v.VersionNo)+" 삭제")
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted", "version_no": v.VersionNo})
}

// purgeFileVersions removes all archived revisions (blobs + rows) of a file.
// Called when a file is permanently deleted.
func (s *Server) purgeFileVersions(fileID string) {
	vers, err := s.store.listFileVersions(fileID)
	if err != nil {
		return
	}
	for _, v := range vers {
		os.Remove(filepath.Join(s.versionsDir, v.ID))
		_ = s.store.deleteFileVersionRow(v.ID)
	}
}
