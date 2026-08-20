package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func logLimit(r *http.Request, def int) int {
	limit := def
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 2000 {
			limit = n
		}
	}
	return limit
}

// ---- access log (uploads/downloads + denied attempts) ----

func (s *Server) handleListLogs(w http.ResponseWriter, r *http.Request) {
	logs, err := s.store.listLogs(logLimit(r, 200))
	if err != nil {
		httpError(w, http.StatusInternalServerError, "db error: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, logs)
}

// ---- audit log (user-initiated management actions) ----

func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	logs, err := s.store.listAudit(logLimit(r, 500))
	if err != nil {
		httpError(w, http.StatusInternalServerError, "db error: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, logs)
}

// audit records a management action performed by the current session user.
func (s *Server) audit(r *http.Request, action, target, detail string) {
	actor := "-"
	if u := s.currentUser(r); u != nil && u.Username != "" {
		actor = u.Username
	}
	s.auditAs(actor, r, action, target, detail)
}

// auditAs records a management action for an explicit actor (used at login,
// where the session isn't attached to the request yet).
func (s *Server) auditAs(actor string, r *http.Request, action, target, detail string) {
	if actor == "" {
		actor = "-"
	}
	_ = s.store.addAudit(&AuditLog{
		Actor: actor, Action: action, Target: target, Detail: detail,
		IP: clientIP(r), CreatedAt: time.Now().UTC(),
	})
}

// ---- error log (system/server errors) ----

func (s *Server) handleListErrors(w http.ResponseWriter, r *http.Request) {
	logs, err := s.store.listErrors(logLimit(r, 500))
	if err != nil {
		httpError(w, http.StatusInternalServerError, "db error: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, logs)
}

// capWriter captures the response status (and a small slice of the body for 5xx)
// so the errorLogger middleware can record server errors.
type capWriter struct {
	http.ResponseWriter
	status int
	buf    []byte
}

func (c *capWriter) WriteHeader(code int) {
	c.status = code
	c.ResponseWriter.WriteHeader(code)
}

func (c *capWriter) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	if c.status >= 500 && len(c.buf) < 1024 {
		c.buf = append(c.buf, b...)
	}
	return c.ResponseWriter.Write(b)
}

// Flush forwards to the underlying writer so streaming responses (downloads,
// zip streaming) keep working through this wrapper.
func (c *capWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// errorLogger records any 5xx response or panic into the error log, then lets
// the response proceed as usual.
func (s *Server) errorLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cw := &capWriter{ResponseWriter: w}
		defer func() {
			if rec := recover(); rec != nil {
				_ = s.store.addError(&ErrorLog{
					Source:  r.Method + " " + r.URL.Path,
					Message: fmt.Sprintf("panic: %v", rec),
					IP:      clientIP(r), CreatedAt: time.Now().UTC(),
				})
				if cw.status == 0 {
					http.Error(cw, "internal server error", http.StatusInternalServerError)
				}
				return
			}
			if cw.status >= 500 {
				msg := strings.TrimSpace(string(cw.buf))
				if msg == "" {
					msg = http.StatusText(cw.status)
				}
				_ = s.store.addError(&ErrorLog{
					Source:  r.Method + " " + r.URL.Path,
					Message: msg,
					IP:      clientIP(r), CreatedAt: time.Now().UTC(),
				})
			}
		}()
		next.ServeHTTP(cw, r)
	})
}
