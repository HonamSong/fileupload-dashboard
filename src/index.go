package main

import (
	"net/http"
	"os"
)

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

// noDirFS wraps a FileSystem to disable directory listings: opening a directory
// returns "not found", so http.FileServer responds 404 instead of listing the
// contents of /js/, /css/, etc.
type noDirFS struct{ fs http.FileSystem }

func (n noDirFS) Open(name string) (http.File, error) {
	f, err := n.fs.Open(name)
	if err != nil {
		return nil, err
	}
	if st, serr := f.Stat(); serr == nil && st.IsDir() {
		_ = f.Close()
		return nil, os.ErrNotExist
	}
	return f, nil
}

var staticFileServer = http.StripPrefix("/", http.FileServer(noDirFS{http.Dir("web")}))

// Static assets (css/js). Login gating happens client-side; the assets
// themselves contain no secrets. Directory listings are disabled (noDirFS).
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache")
	staticFileServer.ServeHTTP(w, r)
}
