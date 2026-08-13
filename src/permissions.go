package main

import (
	"encoding/json"
	"net/http"
)

// ---- per-user folder permissions (whitelist / default-deny) ----
//
// admin always has full (write) access. For user/view accounts, access is
// granted per folder and inherited by subfolders; the deepest matching grant
// wins. No matching grant = no access.

func canRead(level string) bool  { return level == "read" || level == "write" }
func canWrite(level string) bool { return level == "write" }

// pathChain returns folder and its ancestors, deepest first, ending at "/".
func pathChain(folder string) []string {
	folder = normalizeFolder(folder)
	if folder == "/" {
		return []string{"/"}
	}
	anc := ancestorFolders(folder) // [/a, /a/b, /a/b/c] shallow->deep (includes self)
	chain := make([]string, 0, len(anc)+1)
	for i := len(anc) - 1; i >= 0; i-- {
		chain = append(chain, anc[i])
	}
	return append(chain, "/")
}

// roleDefaultLevel is the folder access a role has when no explicit grant
// applies: user reads everything by default; view has no access.
func roleDefaultLevel(role string) string {
	if role == roleUser {
		return "read"
	}
	return "none" // view
}

// folderAccess returns the effective level ("none"|"read"|"write") for a user.
// The deepest explicit grant (including an explicit "none" block) wins;
// otherwise the role default applies.
func (s *Server) folderAccess(u *User, folder string) string {
	if u == nil {
		return "none"
	}
	if isManager(u.Role) { // owner/admin: full access
		return "write"
	}
	grants, err := s.store.permissionsForUser(u.ID)
	if err == nil {
		for _, p := range pathChain(folder) {
			if lvl, ok := grants[p]; ok {
				return lvl
			}
		}
	}
	return roleDefaultLevel(u.Role)
}

// allowRead / allowWrite write a 403 and return false when the current user
// lacks the required access to folder.
func (s *Server) allowRead(w http.ResponseWriter, r *http.Request, folder string) bool {
	if canRead(s.folderAccess(s.currentUser(r), folder)) {
		return true
	}
	httpError(w, http.StatusForbidden, "이 폴더에 대한 접근 권한이 없습니다")
	return false
}
func (s *Server) allowWrite(w http.ResponseWriter, r *http.Request, folder string) bool {
	if canWrite(s.folderAccess(s.currentUser(r), folder)) {
		return true
	}
	httpError(w, http.StatusForbidden, "이 폴더에 대한 쓰기 권한이 없습니다")
	return false
}

// visibleFolders filters folders to those the user can read plus their
// ancestor containers (so the tree renders). Admin sees all.
func (s *Server) visibleFolders(u *User, all []string) []string {
	if u != nil && isManager(u.Role) {
		return all
	}
	visible := map[string]bool{"/": true}
	for _, f := range all {
		if canRead(s.folderAccess(u, f)) {
			visible[f] = true
			for _, a := range ancestorFolders(f) { // ancestors + self
				visible[a] = true
			}
		}
	}
	out := make([]string, 0, len(all))
	for _, f := range all {
		if visible[f] {
			out = append(out, f)
		}
	}
	return out
}

// handleFolderAccess returns the effective level for each visible folder,
// so the UI can grey out container-only nodes and restrict write actions.
func (s *Server) handleFolderAccess(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	all, err := s.store.listFolders()
	if err != nil {
		httpError(w, http.StatusInternalServerError, "db error: %v", err)
		return
	}
	out := map[string]string{}
	for _, f := range s.visibleFolders(u, all) {
		out[f] = s.folderAccess(u, f) // "none" for container-only ancestors
	}
	writeJSON(w, http.StatusOK, out)
}

// handleListFolderPerms (admin) returns the direct grants on a folder plus the
// list of candidate (non-admin) users to populate the management modal.
func (s *Server) handleListFolderPerms(w http.ResponseWriter, r *http.Request) {
	folder := normalizeFolder(r.URL.Query().Get("folder"))
	grants, err := s.store.listFolderPermissions(folder)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "db error: %v", err)
		return
	}
	users, err := s.store.listUsers()
	if err != nil {
		httpError(w, http.StatusInternalServerError, "db error: %v", err)
		return
	}
	type candidate struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Role     string `json:"role"`
	}
	var cands []candidate
	for _, u := range users {
		if isManager(u.Role) {
			continue // owner/admin already have full access
		}
		cands = append(cands, candidate{u.ID, u.Username, u.Role})
	}
	writeJSON(w, http.StatusOK, map[string]any{"folder": folder, "grants": grants, "users": cands})
}

// handleSetFolderPerm (admin) upserts or removes a grant. level: ""|read|write.
func (s *Server) handleSetFolderPerm(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Folder string `json:"folder"`
		UserID string `json:"user_id"`
		Level  string `json:"level"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	// "" removes the grant (revert to role default); none/read/write are stored.
	switch body.Level {
	case "", "none", "read", "write":
	default:
		httpError(w, http.StatusBadRequest, "invalid level")
		return
	}
	folder := normalizeFolder(body.Folder)
	if !s.store.folderExists(folder) {
		httpError(w, http.StatusNotFound, "폴더를 찾을 수 없습니다")
		return
	}
	u, err := s.store.getUser(body.UserID)
	if err != nil {
		httpError(w, http.StatusNotFound, "사용자를 찾을 수 없습니다")
		return
	}
	if isManager(u.Role) {
		httpError(w, http.StatusBadRequest, "owner/admin은 항상 전체 접근이라 지정할 수 없습니다")
		return
	}
	if err := s.store.setFolderPermission(folder, body.UserID, body.Level); err != nil {
		httpError(w, http.StatusInternalServerError, "db error: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
