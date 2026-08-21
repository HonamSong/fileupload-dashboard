package main

import (
	"net/http"
	"time"
)

// handleHealth is a health check that responds only to a valid, active API key.
// Any working download/upload/service key is accepted (no scope requirement).
// Missing, unknown, disabled, or revoked keys get a stealth 404 — the same
// response as the download endpoints — so the check reveals nothing to callers
// without a correct key. Successful checks are intentionally not written to the
// access log to avoid noise from frequent monitoring polls.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	secret := r.Header.Get("X-API-Key")
	if secret == "" {
		s.notFoundDownload(w, r, "health: missing X-API-Key header")
		return
	}
	key, err := s.store.lookupKey(secret)
	if err != nil {
		s.recordKeyFailure(clientIP(r)) // feed auto-block on unknown-key scanning
		s.notFoundDownload(w, r, "health: unknown API key")
		return
	}
	if key.Disabled || key.Revoked || key.expired(time.Now().UTC()) {
		s.notFoundDownload(w, r, "health: inactive or expired API key")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"version":    Version,
		"uptime_sec": int(time.Since(startedAt).Seconds()),
		"time":       time.Now().UTC(),
	})
}
