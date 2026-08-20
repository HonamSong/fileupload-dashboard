package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ---- api keys ----

type KeyView struct {
	*APIKey
	PurgeAt *time.Time `json:"purge_at,omitempty"`
}

const maxKeysPerUser = 3

func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	var keys []*APIKey
	var err error
	if isManager(u.Role) {
		keys, err = s.store.listAllKeys() // owner/admin see everyone's keys
	} else {
		keys, err = s.store.listKeysForUser(u.ID)
	}
	if err != nil {
		httpError(w, http.StatusInternalServerError, "db error: %v", err)
		return
	}
	views := make([]KeyView, 0, len(keys))
	for _, k := range keys {
		v := KeyView{APIKey: k}
		if k.Revoked && k.RevokedAt != nil {
			p := k.RevokedAt.Add(s.cfg.TrashTTL)
			v.PurgeAt = &p
		}
		views = append(views, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": views, "is_admin": isManager(u.Role), "limit": maxKeysPerUser})
}

func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	var body struct {
		Label     string `json:"label"`
		Scope     string `json:"scope"`
		IsService bool   `json:"is_service"`
		UserID    string `json:"user_id"` // 대상 사용자(관리자만 지정 가능). 비우면 본인.
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	// Resolve the key owner. Managers (admin+) may issue a key on behalf of any
	// user; everyone else can only create keys for themselves.
	owner := u
	if body.UserID != "" && body.UserID != u.ID {
		if !isManager(u.Role) {
			httpError(w, http.StatusForbidden, "다른 사용자 명의의 키는 관리자(admin) 이상만 발급할 수 있습니다")
			return
		}
		target, err := s.store.getUser(body.UserID)
		if err != nil {
			httpError(w, http.StatusNotFound, "대상 사용자를 찾을 수 없습니다")
			return
		}
		owner = target
	}

	label := strings.TrimSpace(body.Label)
	if !validKeyLabel(label) {
		httpError(w, http.StatusBadRequest, "라벨은 영문/숫자/_/- 만 사용할 수 있습니다")
		return
	}
	if body.Scope == "" {
		body.Scope = scopeDownload
	}
	if !validScope(body.Scope) {
		httpError(w, http.StatusBadRequest, "잘못된 키 종류입니다")
		return
	}
	if body.IsService && !isManager(u.Role) {
		httpError(w, http.StatusForbidden, "서비스 키는 관리자만 만들 수 있습니다")
		return
	}
	// Personal keys: a view-role owner can only hold download keys, and the
	// per-user limit applies to the OWNER. Service keys (admin-only) have neither
	// restriction.
	if !body.IsService {
		if !isEditor(owner.Role) && scopeAllowsUpload(body.Scope) {
			httpError(w, http.StatusForbidden, "view 사용자는 다운로드 키만 가질 수 있습니다")
			return
		}
		if n, err := s.store.countActiveKeys(owner.ID); err != nil {
			httpError(w, http.StatusInternalServerError, "db error: %v", err)
			return
		} else if n >= maxKeysPerUser {
			httpError(w, http.StatusConflict, fmt.Sprintf("개인 키는 사용자당 최대 %d개까지 만들 수 있습니다", maxKeysPerUser))
			return
		}
	}
	if exists, err := s.store.keyLabelExists(label, owner.ID); err != nil {
		httpError(w, http.StatusInternalServerError, "db error: %v", err)
		return
	} else if exists {
		httpError(w, http.StatusConflict, "해당 사용자에게 이미 같은 라벨의 키가 있습니다")
		return
	}
	key, err := s.store.createKey(label, owner.ID, body.Scope, body.IsService, s.newSignedKey())
	if err != nil {
		httpError(w, http.StatusInternalServerError, "db error: %v", err)
		return
	}
	writeJSON(w, http.StatusCreated, key)
}

// keyOwnedOrAdmin returns the key if the current user owns it or is admin, else writes an error.
func (s *Server) keyOwnedOrAdmin(w http.ResponseWriter, r *http.Request) *APIKey {
	u := s.currentUser(r)
	k, err := s.store.getKey(r.PathValue("id"))
	if err != nil {
		httpError(w, http.StatusNotFound, "key not found")
		return nil
	}
	if !isManager(u.Role) && k.UserID != u.ID {
		httpError(w, http.StatusForbidden, "본인 키만 관리할 수 있습니다")
		return nil
	}
	return k
}

func (s *Server) handleDisableKey(w http.ResponseWriter, r *http.Request) {
	k := s.keyOwnedOrAdmin(w, r)
	if k == nil {
		return
	}
	if err := s.store.setKeyDisabled(k.ID, true); err != nil {
		httpError(w, http.StatusNotFound, "key not found or already revoked")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "disabled"})
}

func (s *Server) handleEnableKey(w http.ResponseWriter, r *http.Request) {
	k := s.keyOwnedOrAdmin(w, r)
	if k == nil {
		return
	}
	if err := s.store.setKeyDisabled(k.ID, false); err != nil {
		httpError(w, http.StatusNotFound, "key not found or already revoked")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "enabled"})
}

func (s *Server) handleRevokeKey(w http.ResponseWriter, r *http.Request) {
	k := s.keyOwnedOrAdmin(w, r)
	if k == nil {
		return
	}
	if !k.Disabled { // must be disabled before it can be revoked
		httpError(w, http.StatusConflict, "먼저 비활성화한 뒤 폐기할 수 있습니다")
		return
	}
	if err := s.store.revokeKey(k.ID, time.Now().UTC()); err != nil {
		httpError(w, http.StatusNotFound, "key not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

func (s *Server) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	k := s.keyOwnedOrAdmin(w, r)
	if k == nil {
		return
	}
	if err := s.store.deleteKey(k.ID); err != nil {
		httpError(w, http.StatusNotFound, "key not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

const (
	scopeDownload = "download"
	scopeUpload   = "upload"
	scopeAll      = "all"
)

func validScope(s string) bool { return s == scopeDownload || s == scopeUpload || s == scopeAll }

// validKeyLabel allows only ASCII letters, digits, underscore and hyphen.
func validKeyLabel(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
func scopeAllowsDownload(s string) bool { return s == scopeDownload || s == scopeAll }
func scopeAllowsUpload(s string) bool   { return s == scopeUpload || s == scopeAll }
