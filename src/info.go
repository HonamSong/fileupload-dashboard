package main

import (
	"net/http"
	"runtime"
	"time"
)

// handleInfo returns build/runtime info for the 설정 > Info tab.
func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version":    Version,
		"build_time": BuildTime,
		"go_version": runtime.Version(),
		"started_at": startedAt.UTC(),
		"uptime_sec": int(time.Since(startedAt).Seconds()),
	})
}
