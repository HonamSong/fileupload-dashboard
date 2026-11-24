package main

import (
	"crypto/sha256"
	"encoding/hex"
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

// sha256Hex returns the hex SHA-256 of s (used to store session tokens / API keys
// hashed at rest, so a DB leak can't be replayed).
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// maxJSONBody caps the size of a JSON request body. API payloads are tiny, so this
// keeps an unauthenticated caller from exhausting memory with a huge body.
const maxJSONBody = 64 << 10 // 64 KiB

// decodeJSONBody caps the request body and decodes it as JSON into dst. Callers that
// ignore the returned error still get the memory cap (reads stop past the limit).
func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	return json.NewDecoder(r.Body).Decode(dst)
}

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
// shortUA extracts a compact "Browser · OS" label from a User-Agent string.
func shortUA(ua string) string {
	ua = strings.TrimSpace(ua)
	if ua == "" {
		return ""
	}
	browser := ""
	switch {
	case strings.Contains(ua, "Edg/"):
		browser = "Edge"
	case strings.Contains(ua, "OPR/"), strings.Contains(ua, "Opera"):
		browser = "Opera"
	case strings.Contains(ua, "Firefox/"):
		browser = "Firefox"
	case strings.Contains(ua, "Chrome/"):
		browser = "Chrome"
	case strings.Contains(ua, "Version/") && strings.Contains(ua, "Safari/"):
		browser = "Safari"
	case strings.Contains(ua, "curl/"):
		browser = "curl"
	case strings.Contains(ua, "python-requests"):
		browser = "python-requests"
	case strings.Contains(ua, "Wget"):
		browser = "Wget"
	}
	os := ""
	switch {
	case strings.Contains(ua, "Windows"):
		os = "Windows"
	case strings.Contains(ua, "Mac OS X"), strings.Contains(ua, "Macintosh"):
		os = "macOS"
	case strings.Contains(ua, "Android"):
		os = "Android"
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"):
		os = "iOS"
	case strings.Contains(ua, "Linux"):
		os = "Linux"
	}
	parts := make([]string, 0, 2)
	if browser != "" {
		parts = append(parts, browser)
	}
	if os != "" {
		parts = append(parts, os)
	}
	if len(parts) == 0 {
		if len(ua) > 40 {
			return ua[:40] + "…"
		}
		return ua
	}
	return strings.Join(parts, " · ")
}

// trustedProxies is the set of proxy networks whose X-Forwarded-For / X-Real-IP
// headers we trust. Empty = trust forwarded headers from anyone (legacy behavior,
// for setups that always sit behind a proxy) but still validate the value as an IP.
var trustedProxies []*net.IPNet

// setTrustedProxies parses a comma-separated list of IPs/CIDRs (TRUSTED_PROXIES).
func setTrustedProxies(list string) {
	trustedProxies = nil
	for _, part := range strings.Split(list, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		if _, n, err := net.ParseCIDR(p); err == nil {
			trustedProxies = append(trustedProxies, n)
			continue
		}
		if ip := net.ParseIP(p); ip != nil {
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			trustedProxies = append(trustedProxies, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
		}
	}
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// isTrustedProxy reports whether ip is within a configured trusted-proxy network.
func isTrustedProxy(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, n := range trustedProxies {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// clientIP returns the real client IP. Forwarded headers (X-Forwarded-For /
// X-Real-IP) are honored ONLY when the direct peer is a configured trusted proxy
// (TRUSTED_PROXIES); otherwise the direct connection IP is used. The XFF chain is
// read right-to-left, skipping trusted proxies, so a spoofed left-most value can't
// forge the logged / access-controlled IP. With no TRUSTED_PROXIES set, headers are
// never trusted (set it when running behind a reverse proxy).
func clientIP(r *http.Request) string {
	remote := remoteIP(r)
	if !isTrustedProxy(net.ParseIP(remote)) {
		return remote // 직접 접속이거나 신뢰 프록시가 아님 → 헤더 무시
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			ipStr := strings.TrimSpace(parts[i])
			ip := net.ParseIP(ipStr)
			if ip == nil {
				break // 비정상 값 → 더 왼쪽은 신뢰 불가
			}
			if isTrustedProxy(ip) {
				continue // 신뢰 프록시 체인은 건너뜀
			}
			return ipStr // 신뢰 체인 바로 왼쪽의 실제 클라이언트
		}
	}
	if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
		if net.ParseIP(xr) != nil {
			return xr
		}
	}
	return remote
}

func sanitizeFilename(name string) string {
	// Strip characters that could break the Content-Disposition header.
	name = strings.ReplaceAll(name, "\"", "")
	name = strings.ReplaceAll(name, "\\", "")
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
