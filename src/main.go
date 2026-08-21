package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Build metadata, injected at compile time via -ldflags "-X main.Version=... -X main.BuildTime=...".
var (
	Version   = "dev"
	BuildTime = "unknown"
	startedAt = time.Now()
)

type Config struct {
	ListenAddr    string        // e.g. ":8080"
	DataDir       string        // base data directory
	PublicBaseURL string        // used to render curl commands, e.g. https://files.example.com
	TrashTTL      time.Duration // how long trashed files live before purge
	MaxUpload     int64         // max upload size in bytes
	PreviewLimit  int64         // max bytes returned for text/image preview
	IPGuardOff    bool          // escape hatch: disable all IP restrictions (lockout recovery)
}

type Server struct {
	cfg         Config
	store       *Store
	filesDir    string
	versionsDir string // archived previous revisions of re-uploaded files
	keyHMAC     []byte // secret for signing/verifying API keys

	failMu      sync.Mutex             // guards keyFails + sharePwFail
	keyFails    map[string][]time.Time // per-IP recent bad-key attempts (auto-block)
	sharePwFail map[string][]time.Time // per-token wrong share-password attempts (brute-force guard)
	guardReArm  atomic.Bool            // runtime re-enable of IP guards despite IP_GUARD_DISABLE
}

// guardOff reports whether all IP restrictions are currently bypassed. The env
// escape hatch turns them off; the runtime re-arm (settings button) turns them
// back on without a restart.
func (s *Server) guardOff() bool {
	return s.cfg.IPGuardOff && !s.guardReArm.Load()
}

func main() {
	cfg := Config{
		ListenAddr:    env("LISTEN_ADDR", ":8180"),
		DataDir:       env("DATA_DIR", "./data"),
		PublicBaseURL: strings.TrimRight(env("PUBLIC_BASE_URL", "http://localhost:8180"), "/"),
		TrashTTL:      time.Duration(envInt("TRASH_TTL_DAYS", 10)) * 24 * time.Hour,
		MaxUpload:     int64(envInt("MAX_UPLOAD_MB", 1024)) * 1024 * 1024,
		PreviewLimit:  int64(envInt("PREVIEW_LIMIT_KB", 1024)) * 1024,
		IPGuardOff:    env("IP_GUARD_DISABLE", "") == "1" || env("IP_GUARD_DISABLE", "") == "true",
	}

	filesDir := filepath.Join(cfg.DataDir, "files")
	versionsDir := filepath.Join(cfg.DataDir, "versions")
	for _, d := range []string{cfg.DataDir, filesDir, versionsDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			log.Fatalf("mkdir %s: %v", d, err)
		}
	}

	store, err := openStore(filepath.Join(cfg.DataDir, "app.db"))
	if err != nil {
		log.Fatalf("open db: %v", err)
	}

	srv := &Server{cfg: cfg, store: store, filesDir: filesDir, versionsDir: versionsDir,
		keyFails: map[string][]time.Time{}, sharePwFail: map[string][]time.Time{}}
	srv.initKeyHMAC()
	srv.backfillChecksums()
	srv.seedAdminUser()
	go srv.purgeLoop()

	mux := http.NewServeMux()
	// Dashboard UI (JS gates the content behind the login screen).
	mux.HandleFunc("GET /", srv.handleIndex)
	mux.HandleFunc("GET /css/", srv.handleStatic)
	mux.HandleFunc("GET /js/", srv.handleStatic)
	// Auth endpoints (public).
	mux.HandleFunc("POST /api/login", srv.handleLogin)
	mux.HandleFunc("POST /api/logout", srv.handleLogout)
	mux.HandleFunc("GET /api/me", srv.handleMe)
	mux.HandleFunc("GET /api/info", srv.requireAuth(srv.handleInfo)) // 버전·빌드 시각
	// Management API — all require a valid session.
	auth := srv.requireAuth     // any logged-in user
	editor := srv.requireEditor // owner/admin/user (not view)
	admin := srv.requireAdmin   // owner or admin (managers)
	owner := srv.requireOwner   // owner only (server settings)
	mux.HandleFunc("POST /api/password", auth(srv.handleChangePassword))
	mux.HandleFunc("GET /api/users", admin(srv.handleListUsers))
	mux.HandleFunc("POST /api/users", admin(srv.handleCreateUser))
	mux.HandleFunc("POST /api/users/{id}/password", admin(srv.handleSetUserPassword))
	mux.HandleFunc("POST /api/users/{id}/role", admin(srv.handleSetUserRole))
	mux.HandleFunc("DELETE /api/users/{id}", admin(srv.handleDeleteUser))
	mux.HandleFunc("GET /api/files", auth(srv.handleListFiles))                     // view: list + commands
	mux.HandleFunc("POST /api/files", editor(srv.handleUpload))                     // 업로드
	mux.HandleFunc("GET /api/files/{id}/download", auth(srv.handleSessionDownload)) // 로컬 저장
	mux.HandleFunc("POST /api/files/download-zip", auth(srv.handleDownloadZip))     // 다중 선택 zip
	mux.HandleFunc("GET /api/files/{id}/preview", editor(srv.handlePreview))        // 미리보기: view 차단
	mux.HandleFunc("GET /api/files/{id}/versions", auth(srv.handleListVersions))    // 버전 목록
	mux.HandleFunc("GET /api/files/{id}/versions/{vid}/download", auth(srv.handleDownloadVersion))
	mux.HandleFunc("POST /api/files/{id}/versions/{vid}/restore", editor(srv.handleRestoreVersion))
	mux.HandleFunc("DELETE /api/files/{id}/versions/{vid}", editor(srv.handleDeleteVersion))
	mux.HandleFunc("DELETE /api/files/{id}", editor(srv.handleDelete))              // 삭제
	mux.HandleFunc("POST /api/files/{id}/restore", editor(srv.handleRestore))
	mux.HandleFunc("GET /api/shares", auth(srv.handleListShares))             // 공유 링크 목록(본인/관리자 전체)
	mux.HandleFunc("POST /api/shares", auth(srv.handleCreateShare))           // 공유 링크 생성(파일 1개 이상)
	mux.HandleFunc("DELETE /api/shares/{token}", auth(srv.handleDeleteShare)) // 공유 링크 삭제
	mux.HandleFunc("POST /api/files/{id}/move", editor(srv.handleMoveFile)) // 파일 이동
	mux.HandleFunc("POST /api/folders/move", editor(srv.handleMoveFolder))  // 폴더 이동
	mux.HandleFunc("GET /api/trash", auth(srv.handleListTrash))
	mux.HandleFunc("GET /api/folders", auth(srv.handleListFolders))
	mux.HandleFunc("GET /api/folder-counts", auth(srv.handleFolderCounts))
	mux.HandleFunc("GET /api/folder-access", auth(srv.handleFolderAccess)) // effective levels
	mux.HandleFunc("GET /api/folders/permissions", admin(srv.handleListFolderPerms))
	mux.HandleFunc("POST /api/folders/permissions", admin(srv.handleSetFolderPerm))
	mux.HandleFunc("POST /api/folders", editor(srv.handleCreateFolder))
	mux.HandleFunc("DELETE /api/folders", editor(srv.handleDeleteFolder))
	mux.HandleFunc("GET /api/keys", auth(srv.handleListKeys))     // own keys; admin sees all
	mux.HandleFunc("POST /api/keys", editor(srv.handleCreateKey)) // own key (max 3); user+ only
	mux.HandleFunc("POST /api/keys/{id}/disable", editor(srv.handleDisableKey))
	mux.HandleFunc("POST /api/keys/{id}/enable", editor(srv.handleEnableKey))
	mux.HandleFunc("POST /api/keys/{id}/revoke", editor(srv.handleRevokeKey))
	mux.HandleFunc("DELETE /api/keys/{id}", editor(srv.handleDeleteKey)) // user+ only
	mux.HandleFunc("GET /api/logs", admin(srv.handleListLogs))               // 접근 로그(업/다운로드)
	mux.HandleFunc("GET /api/audit-logs", admin(srv.handleListAudit))        // 감사 로그(로그인/CRUD)
	mux.HandleFunc("GET /api/error-logs", admin(srv.handleListErrors))       // 에러 로그(시스템 오류)
	mux.HandleFunc("GET /api/server", owner(srv.handleGetServer)) // base URL + IP rules (owner)
	mux.HandleFunc("POST /api/server", owner(srv.handleSetServer))
	mux.HandleFunc("POST /api/server/block", owner(srv.handleBlockIP))     // manually block an IP
	mux.HandleFunc("POST /api/server/unblock", owner(srv.handleUnblockIP)) // remove a blocked IP
	mux.HandleFunc("POST /api/server/rearm", owner(srv.handleRearmGuard))  // re-enable IP guards (no restart)
	// Public download endpoint (requires X-API-Key). The optional trailing
	// {name} segment is cosmetic (lets curl -O save with a readable filename).
	// gate = IP allow/block enforcement on the externally-exposed endpoints.
	gate := srv.ipGate
	// Health check — responds only to a valid, active API key (X-API-Key).
	mux.HandleFunc("GET /healthz", gate(srv.handleHealth))
	mux.HandleFunc("GET /d/{id}", gate(srv.handleDownload))
	mux.HandleFunc("GET /d/{id}/{name}", gate(srv.handleDownload))
	// Folder-path form: /f/<folder>/<name> (readable URL by folder + filename).
	mux.HandleFunc("GET /f/{path...}", gate(srv.handleDownloadByPath))
	// Public upload endpoint (X-API-Key; key owner must be user/admin).
	mux.HandleFunc("POST /u", gate(srv.handleAPIUpload))
	// Public share links (no account): landing page + password-verified download.
	mux.HandleFunc("GET /s/{token}", srv.handleSharePage)
	mux.HandleFunc("POST /s/{token}", srv.handleShareDownload)

	log.Printf("listening on %s (data=%s, trashTTL=%s)", cfg.ListenAddr, cfg.DataDir, cfg.TrashTTL)
	if err := http.ListenAndServe(cfg.ListenAddr, srv.securityHeaders(srv.uiGate(srv.errorLogger(mux)))); err != nil {
		log.Fatal(err)
	}
}
