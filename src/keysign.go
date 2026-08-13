package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// API keys carry an HMAC signature so the server can verify it issued the key.
// New keys: "fk_" + <40 hex body> + <12 hex signature>. Legacy keys ("fk_" +
// 48 hex, pre-signature) still authenticate via the database.
const keyBodyLen = 40
const keySigLen = 12

// initKeyHMAC loads or creates the persistent secret used to sign API keys.
func (s *Server) initKeyHMAC() {
	secret, _ := s.store.getSetting("key_hmac_secret")
	if secret == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			panic(err) // crypto/rand failure is unrecoverable
		}
		secret = hex.EncodeToString(b)
		_ = s.store.setSetting("key_hmac_secret", secret)
	}
	s.keyHMAC = []byte(secret)
}

func (s *Server) signBody(body string) string {
	m := hmac.New(sha256.New, s.keyHMAC)
	m.Write([]byte(body))
	return hex.EncodeToString(m.Sum(nil))[:keySigLen]
}

// newSignedKey issues a fresh, signed API key.
func (s *Server) newSignedKey() string {
	body := newID(20) // 40 hex chars
	return "fk_" + body + s.signBody(body)
}

// keyOrigin classifies a presented key string:
//
//	"server"  — signed format with a valid signature (issued by this server)
//	"forged"  — signed-format length but the signature does not match (tampered)
//	"legacy"  — old pre-signature format; authenticity is decided by the DB
//	"unknown" — not shaped like one of our keys at all
func (s *Server) keyOrigin(key string) string {
	if !strings.HasPrefix(key, "fk_") {
		return "unknown"
	}
	rest := key[3:]
	if !isHexLower(rest) {
		return "unknown"
	}
	switch len(rest) {
	case keyBodyLen + keySigLen:
		body, sig := rest[:keyBodyLen], rest[keyBodyLen:]
		if hmac.Equal([]byte(s.signBody(body)), []byte(sig)) {
			return "server"
		}
		return "forged"
	case 48:
		return "legacy"
	default:
		return "unknown"
	}
}

func isHexLower(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// maskKeyGo hides the middle of a key for safe logging (first4…last3).
func maskKeyGo(k string) string {
	if len(k) <= 8 {
		return "…"
	}
	return k[:4] + "…" + k[len(k)-3:]
}
