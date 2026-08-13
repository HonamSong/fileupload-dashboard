package main

import "net/http"

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	// no-cache = the browser must revalidate before reuse, so a redeploy of the
	// UI is picked up on the next normal reload (no hard-refresh needed). ETag
	// still yields a cheap 304 when nothing changed.
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, "web/index.html")
}

// Static assets (css/js). Login gating happens client-side; the assets
// themselves contain no secrets.
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache")
	http.StripPrefix("/", http.FileServer(http.Dir("web"))).ServeHTTP(w, r)
}
