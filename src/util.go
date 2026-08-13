package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ---- helpers ----

// normalizeFolder cleans a user-supplied path into a canonical form like "/" or "/a/b".
func normalizeFolder(p string) string {
	p = strings.TrimSpace(p)
	var parts []string
	for _, seg := range strings.Split(p, "/") {
		seg = strings.TrimSpace(seg)
		switch seg {
		case "", ".":
			// skip
		case "..":
			if len(parts) > 0 {
				parts = parts[:len(parts)-1]
			}
		default:
			parts = append(parts, seg)
		}
	}
	if len(parts) == 0 {
		return "/"
	}
	return "/" + strings.Join(parts, "/")
}

// ancestorFolders returns every folder from the first level down to path itself.
func ancestorFolders(path string) []string {
	path = normalizeFolder(path)
	if path == "/" {
		return nil
	}
	segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
	var out []string
	cur := ""
	for _, seg := range segs {
		cur += "/" + seg
		out = append(out, cur)
	}
	return out
}

// textExts are treated as text preview even when the content type is generic
// (e.g. application/octet-stream from a curl upload).
var textExts = map[string]bool{
	".txt": true, ".md": true, ".markdown": true, ".sh": true, ".bash": true, ".zsh": true,
	".json": true, ".xml": true, ".yaml": true, ".yml": true, ".csv": true, ".tsv": true,
	".log": true, ".conf": true, ".cfg": true, ".ini": true, ".env": true, ".toml": true,
	".properties": true, ".js": true, ".mjs": true, ".ts": true, ".jsx": true, ".tsx": true,
	".py": true, ".go": true, ".rb": true, ".php": true, ".pl": true, ".lua": true, ".r": true,
	".java": true, ".kt": true, ".c": true, ".h": true, ".cpp": true, ".cc": true, ".hpp": true,
	".cs": true, ".rs": true, ".swift": true, ".scala": true, ".sql": true, ".html": true,
	".htm": true, ".css": true, ".scss": true, ".less": true, ".vue": true, ".svelte": true,
	".tf": true, ".hcl": true, ".gradle": true, ".groovy": true, ".mk": true, ".dockerfile": true,
	".gitignore": true, ".editorconfig": true,
}

var imageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".bmp": true, ".ico": true, ".svg": true,
}

func previewKind(ct, name string) string {
	ct = strings.ToLower(ct)
	switch {
	case strings.HasPrefix(ct, "text/"),
		strings.Contains(ct, "json"),
		strings.Contains(ct, "xml"),
		strings.Contains(ct, "javascript"),
		strings.Contains(ct, "yaml"),
		strings.Contains(ct, "csv"):
		return "text"
	case strings.HasPrefix(ct, "image/"):
		return "image"
	}
	// Content type is generic/unknown — fall back to the file extension.
	ext := strings.ToLower(filepath.Ext(name))
	if textExts[ext] {
		return "text"
	}
	if imageExts[ext] {
		return "image"
	}
	return "none"
}

// clientIP prefers the reverse proxy's forwarded address.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	if xr := r.Header.Get("X-Real-IP"); xr != "" {
		return strings.TrimSpace(xr)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func sanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\"", "")
	name = strings.ReplaceAll(name, "\n", "")
	name = strings.ReplaceAll(name, "\r", "")
	return name
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, status int, format string, args ...any) {
	writeJSON(w, status, map[string]string{"error": fmt.Sprintf(format, args...)})
}
