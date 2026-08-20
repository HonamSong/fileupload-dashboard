package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// ---- auth ----

const sessionCookie = "session"
const sessionTTL = 7 * 24 * time.Hour

// Roles (most → least privileged).
const (
	roleOwner = "owner" // full access incl. server settings; manages everyone
	roleAdmin = "admin" // everything except server settings; manages user/view only
	roleUser  = "user"  // editor; default read on all folders, write where granted
	roleView  = "view"  // login only; access only to folders granted read
)

// isManager: owner or admin (may manage users, folders, keys, view logs).
func isManager(role string) bool { return role == roleOwner || role == roleAdmin }

// isEditor reports whether a role may modify files (upload/delete/move/preview).
func isEditor(role string) bool { return role == roleOwner || role == roleAdmin || role == roleUser }

func validRole(role string) bool {
	return role == roleOwner || role == roleAdmin || role == roleUser || role == roleView
}

// seedAdminUser creates the first admin account on first run, migrating the

func (s *Server) seedAdminUser() {
	users, err := s.store.listUsers()
	if err != nil {
		log.Fatalf("read users: %v", err)
	}
	if len(users) > 0 {
		return
	}
	username := env("ADMIN_USER", "admin")
	// Reuse the legacy password hash if it exists; otherwise hash ADMIN_PASSWORD.
	hash, _ := s.store.getSetting("password_hash")
	if hash == "" {
		h, err := bcrypt.GenerateFromPassword([]byte(env("ADMIN_PASSWORD", "admin")), bcrypt.DefaultCost)
		if err != nil {
			log.Fatalf("hash password: %v", err)
		}
		hash = string(h)
	}
	u := &User{ID: newID(8), Username: username, PasswordHash: hash, Role: roleOwner, CreatedAt: time.Now().UTC()}
	if err := s.store.createUser(u); err != nil {
		log.Fatalf("create owner user: %v", err)
	}
	log.Printf("owner user %q initialized (default password 'admin' unless ADMIN_PASSWORD set) — change it in 설정", username)
}

// currentUser returns the logged-in user for the request, or nil.
func (s *Server) currentUser(r *http.Request) *User {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	u, err := s.store.sessionUser(c.Value, time.Now().UTC())
	if err != nil {
		return nil
	}
	return u
}

// requireAuth wraps a handler so it only runs for an authenticated session.
func (s *Server) requireAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.currentUser(r) == nil {
			httpError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		h(w, r)
	}
}

// requireAdmin wraps a handler so it only runs for a manager (owner or admin).
func (s *Server) requireAdmin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := s.currentUser(r)
		if u == nil {
			httpError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if !isManager(u.Role) {
			httpError(w, http.StatusForbidden, "관리자만 사용할 수 있습니다")
			return
		}
		h(w, r)
	}
}

// requireOwner wraps a handler so it only runs for an owner session
// (server-level settings).
func (s *Server) requireOwner(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := s.currentUser(r)
		if u == nil {
			httpError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if u.Role != roleOwner {
			httpError(w, http.StatusForbidden, "owner만 변경할 수 있습니다")
			return
		}
		h(w, r)
	}
}

// requireEditor wraps a handler so it only runs for admin/user roles (not view).
func (s *Server) requireEditor(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := s.currentUser(r)
		if u == nil {
			httpError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if !isEditor(u.Role) {
			httpError(w, http.StatusForbidden, "권한이 없습니다")
			return
		}
		h(w, r)
	}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	u, err := s.store.getUserByUsername(strings.TrimSpace(body.Username))
	if err != nil || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(body.Password)) != nil {
		log.Printf("login failed user=%q (ip=%s)", body.Username, clientIP(r))
		s.auditAs(strings.TrimSpace(body.Username), r, "login_failed", "", "아이디 또는 비밀번호 오류")
		httpError(w, http.StatusUnauthorized, "아이디 또는 비밀번호가 올바르지 않습니다")
		return
	}
	now := time.Now().UTC()
	token := newID(24)
	exp := now.Add(sessionTTL)
	if err := s.store.createSession(token, u.ID, exp); err != nil {
		httpError(w, http.StatusInternalServerError, "db error: %v", err)
		return
	}
	_ = s.store.updateLastLogin(u.ID, now)
	s.auditAs(u.Username, r, "login", "", "")
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Expires: exp,
	})
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "username": u.Username, "role": u.Role, "base_url": s.baseURL(), "max_upload": s.cfg.MaxUpload})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.audit(r, "logout", "", "") // resolve actor before the session is dropped
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.store.deleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	if u == nil {
		writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "username": u.Username, "role": u.Role, "base_url": s.baseURL(), "max_upload": s.cfg.MaxUpload})
}

// baseURL returns the configured public base URL for commands, falling back to
// the PUBLIC_BASE_URL env config when unset.
func (s *Server) baseURL() string {
	if v, _ := s.store.getSetting("base_url"); strings.TrimSpace(v) != "" {
		return strings.TrimRight(strings.TrimSpace(v), "/")
	}
	return strings.TrimRight(s.cfg.PublicBaseURL, "/")
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	if u == nil {
		httpError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var body struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	// 400 (not 401): the session is valid — a 401 here would make the UI
	// treat it as an expired session and bounce to the login screen.
	if body.Current == "" {
		httpError(w, http.StatusBadRequest, "현재 비밀번호를 입력하세요")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(body.Current)) != nil {
		log.Printf("password change rejected: wrong current password user=%q (ip=%s)", u.Username, clientIP(r))
		httpError(w, http.StatusBadRequest, "현재 비밀번호가 올바르지 않습니다")
		return
	}
	if len(body.New) < 4 {
		httpError(w, http.StatusBadRequest, "새 비밀번호는 4자 이상이어야 합니다")
		return
	}
	nh, err := bcrypt.GenerateFromPassword([]byte(body.New), bcrypt.DefaultCost)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "hash error")
		return
	}
	if err := s.store.updateUserPassword(u.ID, string(nh)); err != nil {
		httpError(w, http.StatusInternalServerError, "db error: %v", err)
		return
	}
	s.audit(r, "password_change", u.Username, "본인 비밀번호 변경")
	writeJSON(w, http.StatusOK, map[string]string{"status": "changed"})
}
