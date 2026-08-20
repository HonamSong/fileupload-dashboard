package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// ---- user management (owner / admin) ----

// managerBlocked enforces that a plain admin (not owner) may only manage
// user/view accounts and may only assign user/view roles. Returns (status,msg)
// to reject, or (0,"") to allow. Owner is unrestricted.
func managerBlocked(actor *User, targetRole, newRole string) (int, string) {
	if actor == nil || actor.Role == roleOwner {
		return 0, ""
	}
	if isManager(targetRole) {
		return http.StatusForbidden, "admin은 owner/admin 계정을 관리할 수 없습니다"
	}
	if newRole != "" && isManager(newRole) {
		return http.StatusForbidden, "admin은 owner/admin 역할을 지정할 수 없습니다"
	}
	return 0, ""
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.listUsers()
	if err != nil {
		httpError(w, http.StatusInternalServerError, "db error: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, users)
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	username := strings.TrimSpace(body.Username)
	if username == "" {
		httpError(w, http.StatusBadRequest, "아이디를 입력하세요")
		return
	}
	if len(body.Password) < 4 {
		httpError(w, http.StatusBadRequest, "비밀번호는 4자 이상이어야 합니다")
		return
	}
	if !validRole(body.Role) {
		httpError(w, http.StatusBadRequest, "역할을 선택하세요 (owner/admin/user/view)")
		return
	}
	if st, msg := managerBlocked(s.currentUser(r), "", body.Role); st != 0 {
		httpError(w, st, msg)
		return
	}
	if exists, err := s.store.usernameExists(username); err != nil {
		httpError(w, http.StatusInternalServerError, "db error: %v", err)
		return
	} else if exists {
		httpError(w, http.StatusConflict, "이미 존재하는 아이디입니다")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "hash error")
		return
	}
	u := &User{ID: newID(8), Username: username, PasswordHash: string(hash), Role: body.Role, CreatedAt: time.Now().UTC()}
	if err := s.store.createUser(u); err != nil {
		httpError(w, http.StatusInternalServerError, "db error: %v", err)
		return
	}
	s.audit(r, "user_create", u.Username, "역할="+u.Role)
	writeJSON(w, http.StatusCreated, u)
}

// handleSetUserRole lets an admin change a user's role.
func (s *Server) handleSetUserRole(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	me := s.currentUser(r)
	if me != nil && me.ID == id {
		httpError(w, http.StatusConflict, "자기 자신의 역할은 변경할 수 없습니다")
		return
	}
	var body struct {
		Role string `json:"role"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if !validRole(body.Role) {
		httpError(w, http.StatusBadRequest, "역할을 선택하세요 (owner/admin/user/view)")
		return
	}
	target, err := s.store.getUser(id)
	if err != nil {
		httpError(w, http.StatusNotFound, "사용자를 찾을 수 없습니다")
		return
	}
	if st, msg := managerBlocked(me, target.Role, body.Role); st != 0 {
		httpError(w, st, msg)
		return
	}
	// Do not allow demoting the last remaining owner.
	if target.Role == roleOwner && body.Role != roleOwner {
		if n, err := s.store.countOwnersExcept(id); err == nil && n == 0 {
			httpError(w, http.StatusConflict, "마지막 owner의 역할은 변경할 수 없습니다")
			return
		}
	}
	if err := s.store.updateUserRole(id, body.Role); err != nil {
		httpError(w, http.StatusInternalServerError, "db error: %v", err)
		return
	}
	s.audit(r, "user_role", target.Username, target.Role+" → "+body.Role)
	writeJSON(w, http.StatusOK, map[string]string{"status": "changed"})
}

// handleSetUserPassword lets an admin reset another user's password.
func (s *Server) handleSetUserPassword(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	target, err := s.store.getUser(id)
	if err != nil {
		httpError(w, http.StatusNotFound, "사용자를 찾을 수 없습니다")
		return
	}
	if st, msg := managerBlocked(s.currentUser(r), target.Role, ""); st != 0 {
		httpError(w, st, msg)
		return
	}
	var body struct {
		New string `json:"new"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if len(body.New) < 4 {
		httpError(w, http.StatusBadRequest, "비밀번호는 4자 이상이어야 합니다")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.New), bcrypt.DefaultCost)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "hash error")
		return
	}
	if err := s.store.updateUserPassword(id, string(hash)); err != nil {
		httpError(w, http.StatusInternalServerError, "db error: %v", err)
		return
	}
	_ = s.store.deleteSessionsForUser(id) // force the user to log in again
	s.audit(r, "user_password", target.Username, "관리자에 의한 비밀번호 재설정")
	writeJSON(w, http.StatusOK, map[string]string{"status": "changed"})
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	me := s.currentUser(r)
	if me != nil && me.ID == id {
		httpError(w, http.StatusConflict, "자기 자신은 삭제할 수 없습니다")
		return
	}
	target, err := s.store.getUser(id)
	if err != nil {
		httpError(w, http.StatusNotFound, "사용자를 찾을 수 없습니다")
		return
	}
	if st, msg := managerBlocked(me, target.Role, ""); st != 0 {
		httpError(w, st, msg)
		return
	}
	if target.Role == roleOwner {
		if n, err := s.store.countOwnersExcept(id); err == nil && n == 0 {
			httpError(w, http.StatusConflict, "마지막 owner는 삭제할 수 없습니다")
			return
		}
	}
	if err := s.store.deleteUser(id); err != nil {
		httpError(w, http.StatusInternalServerError, "db error: %v", err)
		return
	}
	_ = s.store.deleteSessionsForUser(id)    // log the deleted user out everywhere
	_ = s.store.deletePermissionsForUser(id) // drop their folder grants
	s.audit(r, "user_delete", target.Username, "역할="+target.Role)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
