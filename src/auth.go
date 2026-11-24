package main

import (
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// ---- auth ----

const sessionCookie = "session"
const sessionTTL = 7 * 24 * time.Hour // 쿠키 수명(브라우저 보관용). 실제 유휴 만료는 서버 세션 expires_at으로 관리.

// dummyBcrypt is a valid hash used for constant-work comparison against unknown
// usernames (timing-attack mitigation).
var dummyBcrypt, _ = bcrypt.GenerateFromPassword([]byte("x-timing-mitigation-dummy"), bcrypt.DefaultCost)

const minPasswordLen = 8

const (
	loginMaxFails = 10               // IP 또는 사용자명당 허용 실패 횟수
	loginWindow   = 10 * time.Minute // 집계/잠금 시간창
)

// loginThrottled reports whether the IP or username has too many recent failures.
func (s *Server) loginThrottled(ip, username string) bool {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	cutoff := time.Now().Add(-loginWindow)
	count := func(key string) int {
		n := 0
		for _, t := range s.loginFails[key] {
			if t.After(cutoff) {
				n++
			}
		}
		return n
	}
	return count("ip:"+ip) >= loginMaxFails || count("u:"+strings.ToLower(username)) >= loginMaxFails
}

// loginFailsMaxKeys bounds the failure map: once it grows past this, stale keys
// (all entries aged out) are swept so attempts with many distinct usernames/IPs
// can't leak memory.
const loginFailsMaxKeys = 4096

// recordLoginFail records a failed login against both the IP and the username.
func (s *Server) recordLoginFail(ip, username string) {
	now := time.Now()
	cutoff := now.Add(-loginWindow)
	s.failMu.Lock()
	for _, key := range []string{"ip:" + ip, "u:" + strings.ToLower(username)} {
		kept := s.loginFails[key][:0]
		for _, t := range s.loginFails[key] {
			if t.After(cutoff) {
				kept = append(kept, t)
			}
		}
		kept = append(kept, now)
		s.loginFails[key] = kept
	}
	if len(s.loginFails) > loginFailsMaxKeys {
		for k, times := range s.loginFails {
			live := false
			for _, t := range times {
				if t.After(cutoff) {
					live = true
					break
				}
			}
			if !live {
				delete(s.loginFails, k)
			}
		}
	}
	s.failMu.Unlock()
	s.recordKeyFailure(ip) // 자동 차단(켜져 있으면)과 연동
}

// clearLoginFails resets counters after a successful login.
func (s *Server) clearLoginFails(ip, username string) {
	s.failMu.Lock()
	delete(s.loginFails, "ip:"+ip)
	delete(s.loginFails, "u:"+strings.ToLower(username))
	s.failMu.Unlock()
}

const (
	defaultSessionTimeout = 600   // 초 (10분)
	minSessionTimeout     = 60    // 1분
	maxSessionTimeout     = 86400 // 24시간
)

// sessionTimeout is the idle timeout in seconds (설정 > Server에서 변경). 활동이
// 있으면 세션 만료가 이 값만큼 앞으로 밀리고, 유휴 시 만료됩니다.
func (s *Server) sessionTimeout() int {
	v, _ := s.store.getSetting("session_timeout")
	if v == "" {
		return defaultSessionTimeout
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return defaultSessionTimeout
	}
	if n < minSessionTimeout {
		n = minSessionTimeout
	}
	if n > maxSessionTimeout {
		n = maxSessionTimeout
	}
	return n
}

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
	generated := ""
	if hash == "" {
		pw := env("ADMIN_PASSWORD", "")
		if pw == "" {
			// 기본 admin/admin 대신 강한 랜덤 비밀번호를 생성해 로그에 1회만 출력.
			pw = newID(12)
			generated = pw
		}
		h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
		if err != nil {
			log.Fatalf("hash password: %v", err)
		}
		hash = string(h)
	}
	u := &User{ID: newID(8), Username: username, PasswordHash: hash, Role: roleOwner, CreatedAt: time.Now().UTC()}
	if err := s.store.createUser(u); err != nil {
		log.Fatalf("create owner user: %v", err)
	}
	if generated != "" {
		log.Printf("owner user %q initialized with a RANDOM password (ADMIN_PASSWORD not set): %s — 로그인 후 즉시 변경하세요", username, generated)
	} else {
		log.Printf("owner user %q initialized (ADMIN_PASSWORD 사용) — 로그인 후 비밀번호를 변경하세요", username)
	}
}

// currentUser returns the logged-in user for the request, or nil.
func (s *Server) currentUser(r *http.Request) *User {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	now := time.Now().UTC()
	u, err := s.store.sessionUser(c.Value, now)
	if err != nil {
		return nil
	}
	// Sliding idle timeout: any authenticated activity pushes expiry forward.
	_ = s.store.touchSession(c.Value, now.Add(time.Duration(s.sessionTimeout())*time.Second))
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
	_ = decodeJSONBody(w, r, &body)
	ip := clientIP(r)
	username := strings.TrimSpace(body.Username)
	// 무차별 대입 제한: IP 또는 사용자명 기준 실패가 임계값을 넘으면 일정 시간 차단.
	if s.loginThrottled(ip, username) {
		s.auditAs(username, r, "login_failed", "", "로그인 시도 제한(무차별 대입 차단)")
		httpError(w, http.StatusTooManyRequests, "로그인 시도가 너무 많습니다. 잠시 후 다시 시도하세요")
		return
	}
	u, err := s.store.getUserByUsername(username)
	if err != nil {
		// 존재하지 않는 사용자에도 더미 비교를 수행해 응답 시간으로 사용자명을
		// 알아내는 타이밍 오라클을 방지한다.
		_ = bcrypt.CompareHashAndPassword(dummyBcrypt, []byte(body.Password))
	}
	if err != nil || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(body.Password)) != nil {
		log.Printf("login failed user=%q (ip=%s)", username, ip)
		s.recordLoginFail(ip, username)
		s.auditAs(username, r, "login_failed", "", "아이디 또는 비밀번호 오류")
		httpError(w, http.StatusUnauthorized, "아이디 또는 비밀번호가 올바르지 않습니다")
		return
	}
	s.clearLoginFails(ip, username)
	now := time.Now().UTC()
	token := newID(24)
	// DB session uses the idle-timeout window (slides on activity); the cookie is
	// kept long-lived so the browser keeps sending it between activity.
	if err := s.store.createSession(token, u.ID, now.Add(time.Duration(s.sessionTimeout())*time.Second)); err != nil {
		s.serverError(w, r, "db", err)
		return
	}
	_ = s.store.updateLastLogin(u.ID, now)
	s.auditAs(u.Username, r, "login", "", shortUA(r.UserAgent()))
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r), Expires: now.Add(sessionTTL),
	})
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "username": u.Username, "role": u.Role, "base_url": s.baseURL(), "max_upload": s.cfg.MaxUpload, "session_timeout": s.sessionTimeout()})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reason string `json:"reason"`
	}
	_ = decodeJSONBody(w, r, &body)
	detail := "사용자 로그아웃"
	if body.Reason == "timeout" {
		detail = "세션 타임아웃(자동 로그아웃)"
	}
	s.audit(r, "logout", "", detail) // resolve actor before the session is dropped
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
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "username": u.Username, "role": u.Role, "base_url": s.baseURL(), "max_upload": s.cfg.MaxUpload, "session_timeout": s.sessionTimeout()})
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
	_ = decodeJSONBody(w, r, &body)
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
	if len(body.New) < minPasswordLen {
		httpError(w, http.StatusBadRequest, "새 비밀번호는 8자 이상이어야 합니다")
		return
	}
	nh, err := bcrypt.GenerateFromPassword([]byte(body.New), bcrypt.DefaultCost)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "hash error")
		return
	}
	if err := s.store.updateUserPassword(u.ID, string(nh)); err != nil {
		s.serverError(w, r, "db", err)
		return
	}
	// 비밀번호 변경 시 다른 모든 세션을 무효화하고, 현재 세션만 새 토큰으로 유지.
	_ = s.store.deleteSessionsForUser(u.ID)
	now := time.Now().UTC()
	token := newID(24)
	if err := s.store.createSession(token, u.ID, now.Add(time.Duration(s.sessionTimeout())*time.Second)); err == nil {
		http.SetCookie(w, &http.Cookie{
			Name: sessionCookie, Value: token, Path: "/",
			HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r), Expires: now.Add(sessionTTL),
		})
	}
	s.audit(r, "password_change", u.Username, "본인 비밀번호 변경(다른 세션 로그아웃)")
	writeJSON(w, http.StatusOK, map[string]string{"status": "changed"})
}

// isHTTPS reports whether the request reached us over TLS (directly or via a
// trusted proxy's X-Forwarded-Proto) — used to set the Secure cookie flag.
func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	// X-Forwarded-Proto는 신뢰 프록시에서 온 경우에만 신뢰.
	if isTrustedProxy(net.ParseIP(remoteIP(r))) {
		return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	}
	return false
}
