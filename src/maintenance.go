package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"os"
	"time"
)

func (s *Server) backfillChecksums() {
	files, err := s.store.filesMissingChecksum()
	if err != nil {
		log.Printf("checksum backfill: query error: %v", err)
		return
	}
	for _, f := range files {
		sum, err := fileChecksum(f.StoredPath)
		if err != nil {
			log.Printf("checksum backfill: %s (%s): %v", f.ID, f.Name, err)
			continue
		}
		if err := s.store.setChecksum(f.ID, sum); err != nil {
			log.Printf("checksum backfill: db update %s: %v", f.ID, err)
			continue
		}
		log.Printf("checksum backfill: %s (%s) = %s", f.ID, f.Name, sum)
	}
}

// fileChecksum returns the SHA-256 hex digest of the file at path.
func fileChecksum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ---- background trash purge ----

func (s *Server) purgeLoop() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	s.purgeOnce() // run immediately at startup
	for range ticker.C {
		s.purgeOnce()
	}
}

func (s *Server) purgeOnce() {
	cutoff := time.Now().UTC().Add(-s.cfg.TrashTTL)
	expired, err := s.store.expiredTrash(cutoff)
	if err != nil {
		log.Printf("purge: query error: %v", err)
		return
	}
	for _, f := range expired {
		if err := os.Remove(f.StoredPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("purge: remove %s: %v", f.StoredPath, err)
			continue
		}
		_ = s.store.deleteSharesForFile(f.ID) // drop its public share links
		if err := s.store.deleteFileRow(f.ID); err != nil {
			log.Printf("purge: db delete %s: %v", f.ID, err)
			continue
		}
		log.Printf("purge: permanently deleted %s (%s)", f.ID, f.Name)
	}

	// Drop expired login sessions and expired share links.
	_ = s.store.deleteExpiredSessions(time.Now().UTC())
	if n, err := s.store.deleteExpiredShares(time.Now().UTC()); err == nil && n > 0 {
		log.Printf("purge: removed %d expired share link(s)", n)
	}

	// Also purge revoked API keys past their TTL.
	keyIDs, err := s.store.expiredRevokedKeys(cutoff)
	if err != nil {
		log.Printf("purge: key query error: %v", err)
		return
	}
	for _, id := range keyIDs {
		if err := s.store.deleteKey(id); err != nil {
			log.Printf("purge: key delete %s: %v", id, err)
			continue
		}
		log.Printf("purge: permanently deleted revoked key %s", id)
	}
}
