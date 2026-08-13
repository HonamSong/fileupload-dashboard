package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ---- server settings: base URL + IP allow/block ----
//
// Allow and Block are two independent lists (settings keys ip_allow / ip_block),
// each holding one IP or CIDR per line. Enforcement on the public endpoints
// (/d, /f, /u): a Block match always denies; if the Allow list is non-empty,
// only matching IPs pass. Both lists empty = open.

func (s *Server) handleGetServer(w http.ResponseWriter, r *http.Request) {
	base, _ := s.store.getSetting("base_url")
	allow, _ := s.store.getSetting("ip_allow")
	block, _ := s.store.getSetting("ip_block")
	uiAllow, _ := s.store.getSetting("ui_allow")
	uiBlock, _ := s.store.getSetting("ui_block")
	autoBlock, _ := s.store.getSetting("auto_block")
	if autoBlock != "on" {
		autoBlock = "off"
	}
	blocked, _ := s.store.listBlockedIPs()
	writeJSON(w, http.StatusOK, map[string]any{
		"base_url": base, "ip_allow": allow, "ip_block": block,
		"ui_allow": uiAllow, "ui_block": uiBlock,
		"env_base_url":         strings.TrimRight(s.cfg.PublicBaseURL, "/"),
		"auto_block":           autoBlock,
		"auto_block_threshold": s.settingInt("auto_block_threshold", 10),
		"auto_block_window":    s.settingInt("auto_block_window", 10),
		"blocked_ips":          blocked,
		"guard_recovery":       s.guardOff(),     // true = all IP limits currently bypassed
		"guard_env":            s.cfg.IPGuardOff, // IP_GUARD_DISABLE is set on the container
	})
}

// handleRearmGuard re-enables IP restrictions at runtime (no restart), used to
// leave recovery mode after fixing the allow list.
func (s *Server) handleRearmGuard(w http.ResponseWriter, r *http.Request) {
	s.guardReArm.Store(true)
	log.Printf("IP guards re-armed at runtime (enforcement on)")
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "guard_recovery": s.guardOff()})
}

func (s *Server) handleSetServer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BaseURL            string `json:"base_url"`
		IPAllow            string `json:"ip_allow"`
		IPBlock            string `json:"ip_block"`
		UIAllow            string `json:"ui_allow"`
		UIBlock            string `json:"ui_block"`
		AutoBlock          string `json:"auto_block"`
		AutoBlockThreshold int    `json:"auto_block_threshold"`
		AutoBlockWindow    int    `json:"auto_block_window"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	for _, list := range []string{body.IPAllow, body.IPBlock, body.UIAllow, body.UIBlock} {
		if bad := invalidIPEntry(list); bad != "" {
			httpError(w, http.StatusBadRequest, "잘못된 IP/CIDR: %s", bad)
			return
		}
	}
	_ = s.store.setSetting("base_url", strings.TrimRight(strings.TrimSpace(body.BaseURL), "/"))
	_ = s.store.setSetting("ip_allow", strings.TrimSpace(body.IPAllow))
	_ = s.store.setSetting("ip_block", strings.TrimSpace(body.IPBlock))
	_ = s.store.setSetting("ui_allow", strings.TrimSpace(body.UIAllow))
	_ = s.store.setSetting("ui_block", strings.TrimSpace(body.UIBlock))
	autoBlock := "off"
	if body.AutoBlock == "on" {
		autoBlock = "on"
	}
	_ = s.store.setSetting("auto_block", autoBlock)
	_ = s.store.setSetting("auto_block_threshold", strconv.Itoa(clampInt(body.AutoBlockThreshold, 1, 1000, 10)))
	_ = s.store.setSetting("auto_block_window", strconv.Itoa(clampInt(body.AutoBlockWindow, 1, 1440, 10)))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleBlockIP manually blocks an IP (e.g. from the access log).
func (s *Server) handleBlockIP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IP     string `json:"ip"`
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	ip := strings.TrimSpace(body.IP)
	if net.ParseIP(ip) == nil {
		httpError(w, http.StatusBadRequest, "invalid ip")
		return
	}
	reason := strings.TrimSpace(body.Reason)
	if reason == "" {
		reason = "수동 차단"
	}
	if err := s.store.addBlockedIP(ip, reason); err != nil {
		httpError(w, http.StatusInternalServerError, "db error: %v", err)
		return
	}
	log.Printf("manually blocked ip=%s", ip)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleUnblockIP removes an auto-blocked IP.
func (s *Server) handleUnblockIP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IP string `json:"ip"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	ip := strings.TrimSpace(body.IP)
	if ip == "" {
		httpError(w, http.StatusBadRequest, "ip required")
		return
	}
	_ = s.store.removeBlockedIP(ip)
	s.failMu.Lock()
	delete(s.keyFails, ip) // reset its counter too
	s.failMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func clampInt(v, lo, hi, def int) int {
	if v <= 0 {
		return def
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (s *Server) settingInt(key string, def int) int {
	if v, _ := s.store.getSetting(key); strings.TrimSpace(v) != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// recordKeyFailure notes a bad-key attempt from ip and auto-blocks the IP once
// it exceeds the configured threshold within the window (when auto_block is on).
func (s *Server) recordKeyFailure(ip string) {
	if v, _ := s.store.getSetting("auto_block"); v != "on" {
		return
	}
	threshold := s.settingInt("auto_block_threshold", 10)
	window := time.Duration(s.settingInt("auto_block_window", 10)) * time.Minute
	now := time.Now()
	cutoff := now.Add(-window)
	s.failMu.Lock()
	defer s.failMu.Unlock()
	kept := s.keyFails[ip][:0]
	for _, t := range s.keyFails[ip] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	kept = append(kept, now)
	s.keyFails[ip] = kept
	if len(kept) >= threshold {
		_ = s.store.addBlockedIP(ip, fmt.Sprintf("잘못된 키 %d회 시도 (자동 차단)", len(kept)))
		delete(s.keyFails, ip)
		log.Printf("auto-blocked ip=%s after %d bad key attempts", ip, len(kept))
	}
}

// ipGate enforces the API-endpoint (/d, /f, /u) IP rules + auto-block.
func (s *Server) ipGate(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if !s.guardOff() && (s.store.isBlockedIP(ip) || !s.ipAllowed(ip)) {
			log.Printf("ip blocked (api): %s %s", ip, r.URL.Path)
			httpError(w, http.StatusForbidden, "forbidden")
			return
		}
		h(w, r)
	}
}

// uiGate restricts dashboard/UI access by IP. API-key endpoints (/d, /f, /u)
// are exempt — they keep their own ipGate. Set IP_GUARD_DISABLE=1 to bypass
// everything (lockout recovery).
func (s *Server) uiGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/d/") || strings.HasPrefix(p, "/f/") || p == "/u" {
			next.ServeHTTP(w, r) // API-key endpoints are not UI-restricted
			return
		}
		if !s.guardOff() {
			ip := clientIP(r)
			if s.store.isBlockedIP(ip) || !s.uiIPAllowed(ip) {
				log.Printf("ui blocked: %s %s", ip, p)
				httpError(w, http.StatusForbidden, "forbidden")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ipPass: block-list denies; a non-empty allow-list means allow-only.
func (s *Server) ipPass(ip, allowKey, blockKey string) bool {
	block, _ := s.store.getSetting(blockKey)
	if ipInList(ip, block) {
		return false
	}
	allow, _ := s.store.getSetting(allowKey)
	if strings.TrimSpace(allow) != "" {
		return ipInList(ip, allow)
	}
	return true
}
func (s *Server) ipAllowed(ip string) bool   { return s.ipPass(ip, "ip_allow", "ip_block") }
func (s *Server) uiIPAllowed(ip string) bool { return s.ipPass(ip, "ui_allow", "ui_block") }

// splitIPList tokenizes a list on commas/whitespace/newlines.
func splitIPList(list string) []string {
	return strings.FieldsFunc(list, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == ' ' || r == '\t'
	})
}

// invalidIPEntry returns the first entry that is neither an IP nor a CIDR ("" if all valid).
func invalidIPEntry(list string) string {
	for _, tok := range splitIPList(list) {
		if strings.Contains(tok, "/") {
			if _, _, err := net.ParseCIDR(tok); err != nil {
				return tok
			}
		} else if net.ParseIP(tok) == nil {
			return tok
		}
	}
	return ""
}

// ipInList reports whether ip matches any entry (single IP or CIDR) in list.
func ipInList(ip, list string) bool {
	target := net.ParseIP(ip)
	for _, tok := range splitIPList(list) {
		if strings.Contains(tok, "/") {
			if _, cidr, err := net.ParseCIDR(tok); err == nil && target != nil && cidr.Contains(target) {
				return true
			}
		} else if tok == ip {
			return true
		}
	}
	return false
}
