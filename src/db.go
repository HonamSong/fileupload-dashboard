package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// File is an uploaded file's metadata row.
type File struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Folder        string     `json:"folder"`
	Size          int64      `json:"size"`
	Checksum      string     `json:"checksum"`
	ContentType   string     `json:"content_type"`
	StoredPath    string     `json:"-"`
	UploadedAt    time.Time  `json:"uploaded_at"`
	DownloadCount int        `json:"download_count"`
	DeletedAt     *time.Time `json:"deleted_at,omitempty"`
	DeletedBy     string     `json:"deleted_by,omitempty"`
	UploadedBy    string     `json:"uploaded_by,omitempty"`
	// CurrentVersionID points at the file_versions row that is currently active
	// (empty for non-versioned files). Restoring moves this pointer instead of
	// creating a new revision.
	CurrentVersionID string `json:"current_version_id,omitempty"`
}

// FileVersion is a previous content revision of a file, kept when a same-named
// file is re-uploaded to the same folder. The current content lives in files;
// older revisions are archived here (each with its own blob).
type FileVersion struct {
	ID          string    `json:"id"`      // blob id (basename under versionsDir)
	FileID      string    `json:"file_id"` // the file this revision belongs to
	VersionNo   int       `json:"version_no"`
	Name        string    `json:"name"`
	Folder      string    `json:"folder"`
	Size        int64     `json:"size"`
	Checksum    string    `json:"checksum"`
	ContentType string    `json:"content_type"`
	UploadedBy  string    `json:"uploaded_by"`
	CreatedAt   time.Time `json:"created_at"`
}

// User is a dashboard login account. Role is one of admin | user | view.
type User struct {
	ID           string     `json:"id"`
	Username     string     `json:"username"`
	PasswordHash string     `json:"-"`
	Role         string     `json:"role"`
	CreatedAt    time.Time  `json:"created_at"`
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`
}

// APIKey is a credential used to authenticate downloads.
type APIKey struct {
	ID         string     `json:"id"`
	Key        string     `json:"key"`
	Label      string     `json:"label"`
	Scope      string     `json:"scope"` // download | upload | all
	IsService  bool       `json:"is_service"`
	UserID     string     `json:"user_id"`
	Owner      string     `json:"owner"` // username, filled for admin listing
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	Disabled   bool       `json:"disabled"`
	Revoked    bool       `json:"revoked"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	UseCount   int        `json:"use_count"`
}

// AccessLog records a single upload or authenticated download.
type AccessLog struct {
	ID         int64     `json:"id"`
	Action     string    `json:"action"` // download | upload
	Status     string    `json:"status"` // ok | denied
	Detail     string    `json:"detail"` // reason (for denied)
	Actor      string    `json:"actor"`  // username or key owner ("service")
	APIKeyID   string    `json:"api_key_id"`
	KeyLabel   string    `json:"key_label"`
	FileID     string    `json:"file_id"`
	FileName   string    `json:"file_name"`
	IP         string    `json:"ip"`
	UserAgent  string    `json:"user_agent"`
	AccessedAt time.Time `json:"accessed_at"`
}

// AuditLog records a user-initiated management action (login, file/folder/key CRUD).
type AuditLog struct {
	ID        int64     `json:"id"`
	Actor     string    `json:"actor"`  // username (or "-" for anonymous)
	Action    string    `json:"action"` // e.g. login, file_delete, folder_create, key_create
	Target    string    `json:"target"` // affected resource (path, name, username, ...)
	Detail    string    `json:"detail"` // extra context
	IP        string    `json:"ip"`
	CreatedAt time.Time `json:"created_at"`
}

// ErrorLog records a system/server error (5xx response or panic).
type ErrorLog struct {
	ID        int64     `json:"id"`
	Source    string    `json:"source"`  // "METHOD /path"
	Message   string    `json:"message"` // error text / panic message
	IP        string    `json:"ip"`
	CreatedAt time.Time `json:"created_at"`
}

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

func openStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, err
	}
	// modernc/sqlite is not safe for concurrent writers on one connection.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS files (
    id             TEXT PRIMARY KEY,
    name           TEXT NOT NULL,
    size           INTEGER NOT NULL,
    content_type   TEXT NOT NULL,
    stored_path    TEXT NOT NULL,
    uploaded_at    TIMESTAMP NOT NULL,
    download_count INTEGER NOT NULL DEFAULT 0,
    deleted_at     TIMESTAMP
);
CREATE TABLE IF NOT EXISTS api_keys (
    id          TEXT PRIMARY KEY,
    key         TEXT NOT NULL UNIQUE,
    label       TEXT NOT NULL,
    created_at  TIMESTAMP NOT NULL,
    last_used_at TIMESTAMP,
    revoked     INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS access_logs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    api_key_id  TEXT,
    file_id     TEXT,
    ip          TEXT NOT NULL,
    user_agent  TEXT,
    accessed_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_logs_key ON access_logs(api_key_id);
CREATE INDEX IF NOT EXISTS idx_logs_time ON access_logs(accessed_at);
CREATE TABLE IF NOT EXISTS audit_logs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    actor      TEXT NOT NULL DEFAULT '',
    action     TEXT NOT NULL,
    target     TEXT NOT NULL DEFAULT '',
    detail     TEXT NOT NULL DEFAULT '',
    ip         TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_time ON audit_logs(created_at);
CREATE TABLE IF NOT EXISTS error_logs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    source     TEXT NOT NULL DEFAULT '',
    message    TEXT NOT NULL DEFAULT '',
    ip         TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_error_time ON error_logs(created_at);
CREATE TABLE IF NOT EXISTS file_versions (
    id           TEXT PRIMARY KEY,
    file_id      TEXT NOT NULL,
    version_no   INTEGER NOT NULL,
    name         TEXT NOT NULL,
    folder       TEXT NOT NULL,
    size         INTEGER NOT NULL,
    checksum     TEXT NOT NULL DEFAULT '',
    content_type TEXT NOT NULL DEFAULT '',
    uploaded_by  TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_fv_file ON file_versions(file_id, version_no);
CREATE TABLE IF NOT EXISTS folders (
    path       TEXT PRIMARY KEY,
    created_at TIMESTAMP NOT NULL
);
CREATE TABLE IF NOT EXISTS folder_permissions (
    folder  TEXT NOT NULL,
    user_id TEXT NOT NULL,
    level   TEXT NOT NULL,          -- 'read' | 'write'
    PRIMARY KEY (folder, user_id)
);
CREATE TABLE IF NOT EXISTS blocked_ips (
    ip         TEXT PRIMARY KEY,
    blocked_at TIMESTAMP NOT NULL,
    reason     TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS shares (
    token          TEXT PRIMARY KEY,
    file_id        TEXT NOT NULL,
    password_hash  TEXT NOT NULL DEFAULT '',
    expires_at     TIMESTAMP NOT NULL,
    created_by     TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMP NOT NULL,
    download_count INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_shares_file ON shares(file_id);
CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
    token      TEXT PRIMARY KEY,
    expires_at TIMESTAMP NOT NULL
);
CREATE TABLE IF NOT EXISTS users (
    id            TEXT PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    is_admin      INTEGER NOT NULL DEFAULT 0,
    created_at    TIMESTAMP NOT NULL
);
`
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	// Add the folder column to pre-existing databases.
	if err := s.ensureColumn("files", "folder", `ALTER TABLE files ADD COLUMN folder TEXT NOT NULL DEFAULT '/'`); err != nil {
		return err
	}
	if err := s.ensureColumn("files", "checksum", `ALTER TABLE files ADD COLUMN checksum TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureColumn("files", "deleted_by", `ALTER TABLE files ADD COLUMN deleted_by TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureColumn("files", "uploaded_by", `ALTER TABLE files ADD COLUMN uploaded_by TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureColumn("files", "current_version_id", `ALTER TABLE files ADD COLUMN current_version_id TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	// API key state columns (disable + revoke countdown) for pre-existing databases.
	if err := s.ensureColumn("api_keys", "disabled", `ALTER TABLE api_keys ADD COLUMN disabled INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := s.ensureColumn("api_keys", "revoked_at", `ALTER TABLE api_keys ADD COLUMN revoked_at TIMESTAMP`); err != nil {
		return err
	}
	// Associate sessions with a user (pre-existing sessions get NULL and are invalidated).
	if err := s.ensureColumn("sessions", "user_id", `ALTER TABLE sessions ADD COLUMN user_id TEXT`); err != nil {
		return err
	}
	// Role column for users; migrate legacy is_admin once (role='' means unmigrated).
	if err := s.ensureColumn("users", "role", `ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE users SET role = CASE WHEN is_admin = 1 THEN 'admin' ELSE 'user' END WHERE role = ''`); err != nil {
		return err
	}
	// Owner column for API keys.
	if err := s.ensureColumn("api_keys", "user_id", `ALTER TABLE api_keys ADD COLUMN user_id TEXT`); err != nil {
		return err
	}
	// Scope column (download | upload | all). Existing keys keep both abilities.
	if err := s.ensureColumn("api_keys", "scope", `ALTER TABLE api_keys ADD COLUMN scope TEXT NOT NULL DEFAULT 'all'`); err != nil {
		return err
	}
	// Service keys (org-level, not tied to a person's limit).
	if err := s.ensureColumn("api_keys", "is_service", `ALTER TABLE api_keys ADD COLUMN is_service INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	// access_logs now records uploads too (action) and who did it (actor).
	if err := s.ensureColumn("access_logs", "action", `ALTER TABLE access_logs ADD COLUMN action TEXT NOT NULL DEFAULT 'download'`); err != nil {
		return err
	}
	if err := s.ensureColumn("access_logs", "actor", `ALTER TABLE access_logs ADD COLUMN actor TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureColumn("users", "last_login_at", `ALTER TABLE users ADD COLUMN last_login_at TIMESTAMP`); err != nil {
		return err
	}
	// blocked_ips records who blocked (username or "system").
	if err := s.ensureColumn("blocked_ips", "blocked_by", `ALTER TABLE blocked_ips ADD COLUMN blocked_by TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	// shares can hold multiple files (comma-separated ids) for zip downloads.
	if err := s.ensureColumn("shares", "file_ids", `ALTER TABLE shares ADD COLUMN file_ids TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	// shares can cap total downloads (0 = unlimited; legacy rows = unlimited).
	if err := s.ensureColumn("shares", "max_downloads", `ALTER TABLE shares ADD COLUMN max_downloads INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	// access_logs now records denied attempts too (status + reason).
	if err := s.ensureColumn("access_logs", "status", `ALTER TABLE access_logs ADD COLUMN status TEXT NOT NULL DEFAULT 'ok'`); err != nil {
		return err
	}
	if err := s.ensureColumn("access_logs", "detail", `ALTER TABLE access_logs ADD COLUMN detail TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	// One-time: promote pre-existing admins to owner when no owner exists yet
	// (introducing the owner tier). Runs once; later admins keep their role.
	var owners int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role = 'owner'`).Scan(&owners); err == nil && owners == 0 {
		_, _ = s.db.Exec(`UPDATE users SET role = 'owner' WHERE role = 'admin'`)
	}
	// Root folder always exists.
	_, err := s.db.Exec(`INSERT OR IGNORE INTO folders (path, created_at) VALUES ('/', ?)`, time.Now().UTC())
	return err
}

// ensureColumn adds a column via the given ALTER statement if it is missing.
func (s *Store) ensureColumn(table, col, alter string) error {
	rows, err := s.db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == col {
			return nil // already present
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = s.db.Exec(alter)
	return err
}

func newID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failure is not recoverable
	}
	return hex.EncodeToString(b)
}

// ---- files ----

func (s *Store) insertFile(f *File) error {
	_, err := s.db.Exec(
		`INSERT INTO files (id, name, folder, size, checksum, content_type, stored_path, uploaded_at, download_count, deleted_at, uploaded_by, current_version_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, NULL, ?, ?)`,
		f.ID, f.Name, f.Folder, f.Size, f.Checksum, f.ContentType, f.StoredPath, f.UploadedAt, f.UploadedBy, f.CurrentVersionID,
	)
	return err
}

// setCurrentVersion moves the current-version pointer (restore) or sets it after
// a new upload. Empty vid means the file is not versioned.
func (s *Store) setCurrentVersion(fileID, vid string) error {
	_, err := s.db.Exec(`UPDATE files SET current_version_id = ? WHERE id = ?`, vid, fileID)
	return err
}

// fileColumns lists file columns in the order scanFile expects them.
const fileColumns = "id, name, folder, size, checksum, content_type, stored_path, uploaded_at, download_count, deleted_at, COALESCE(deleted_by,''), COALESCE(uploaded_by,''), COALESCE(current_version_id,'')"

func scanFile(row interface{ Scan(...any) error }) (*File, error) {
	var f File
	var deleted sql.NullTime
	if err := row.Scan(&f.ID, &f.Name, &f.Folder, &f.Size, &f.Checksum, &f.ContentType, &f.StoredPath,
		&f.UploadedAt, &f.DownloadCount, &deleted, &f.DeletedBy, &f.UploadedBy, &f.CurrentVersionID); err != nil {
		return nil, err
	}
	if deleted.Valid {
		f.DeletedAt = &deleted.Time
	}
	return &f, nil
}

func (s *Store) getFile(id string) (*File, error) {
	row := s.db.QueryRow(`SELECT `+fileColumns+` FROM files WHERE id = ?`, id)
	return scanFile(row)
}

// getActiveFileByNameInFolder finds a non-trashed file with the given name in a folder.
func (s *Store) getActiveFileByNameInFolder(name, folder string) (*File, error) {
	row := s.db.QueryRow(
		`SELECT `+fileColumns+` FROM files WHERE name = ? AND folder = ? AND deleted_at IS NULL LIMIT 1`,
		name, folder)
	return scanFile(row)
}

// updateFileContent overwrites the metadata of an existing file (same id/link).
func (s *Store) updateFileContent(id string, size int64, contentType, checksum string, uploadedAt time.Time, uploadedBy string) error {
	_, err := s.db.Exec(
		`UPDATE files SET size = ?, content_type = ?, checksum = ?, uploaded_at = ?, uploaded_by = ? WHERE id = ?`,
		size, contentType, checksum, uploadedAt, uploadedBy, id)
	return err
}

// ---- file versions ----

func (s *Store) nextVersionNo(fileID string) int {
	var n sql.NullInt64
	_ = s.db.QueryRow(`SELECT MAX(version_no) FROM file_versions WHERE file_id = ?`, fileID).Scan(&n)
	if n.Valid {
		return int(n.Int64) + 1
	}
	return 1
}

func (s *Store) addFileVersion(v *FileVersion) error {
	_, err := s.db.Exec(
		`INSERT INTO file_versions (id, file_id, version_no, name, folder, size, checksum, content_type, uploaded_by, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		v.ID, v.FileID, v.VersionNo, v.Name, v.Folder, v.Size, v.Checksum, v.ContentType, v.UploadedBy, v.CreatedAt)
	return err
}

func scanFileVersion(row interface{ Scan(...any) error }) (*FileVersion, error) {
	var v FileVersion
	if err := row.Scan(&v.ID, &v.FileID, &v.VersionNo, &v.Name, &v.Folder, &v.Size, &v.Checksum,
		&v.ContentType, &v.UploadedBy, &v.CreatedAt); err != nil {
		return nil, err
	}
	return &v, nil
}

const versionColumns = "id, file_id, version_no, name, folder, size, checksum, content_type, COALESCE(uploaded_by,''), created_at"

// listFileVersions returns a file's revisions, newest first.
func (s *Store) listFileVersions(fileID string) ([]*FileVersion, error) {
	rows, err := s.db.Query(`SELECT `+versionColumns+` FROM file_versions WHERE file_id = ? ORDER BY version_no DESC`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*FileVersion
	for rows.Next() {
		v, err := scanFileVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) getFileVersion(fileID, versionID string) (*FileVersion, error) {
	row := s.db.QueryRow(`SELECT `+versionColumns+` FROM file_versions WHERE file_id = ? AND id = ?`, fileID, versionID)
	return scanFileVersion(row)
}

func (s *Store) countFileVersions(fileID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM file_versions WHERE file_id = ?`, fileID).Scan(&n)
	return n, err
}

func (s *Store) deleteFileVersionRow(id string) error {
	_, err := s.db.Exec(`DELETE FROM file_versions WHERE id = ?`, id)
	return err
}

// filesMissingChecksum returns files whose checksum has not been computed yet.
func (s *Store) filesMissingChecksum() ([]*File, error) {
	return s.queryFiles(`SELECT ` + fileColumns + ` FROM files WHERE checksum = ''`)
}

func (s *Store) setChecksum(id, checksum string) error {
	_, err := s.db.Exec(`UPDATE files SET checksum = ? WHERE id = ?`, checksum, id)
	return err
}

// listFiles returns active files (trashed=false) or trashed files (trashed=true).
func (s *Store) listFiles(trashed bool) ([]*File, error) {
	cond := "deleted_at IS NULL"
	if trashed {
		cond = "deleted_at IS NOT NULL"
	}
	return s.queryFiles(`SELECT ` + fileColumns + ` FROM files WHERE ` + cond + ` ORDER BY uploaded_at DESC`)
}

// listFilesInFolder returns active files that live directly in the given folder.
func (s *Store) listFilesInFolder(folder string) ([]*File, error) {
	return s.queryFiles(
		`SELECT `+fileColumns+` FROM files WHERE deleted_at IS NULL AND folder = ? ORDER BY uploaded_at DESC`,
		folder)
}

func (s *Store) queryFiles(query string, args ...any) ([]*File, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*File
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) softDeleteFile(id string, when time.Time, by string) error {
	res, err := s.db.Exec(`UPDATE files SET deleted_at = ?, deleted_by = ? WHERE id = ? AND deleted_at IS NULL`, when, by, id)
	if err != nil {
		return err
	}
	return affectedOne(res)
}

func (s *Store) restoreFile(id string) error {
	res, err := s.db.Exec(`UPDATE files SET deleted_at = NULL, deleted_by = '' WHERE id = ? AND deleted_at IS NOT NULL`, id)
	if err != nil {
		return err
	}
	return affectedOne(res)
}

func (s *Store) deleteFileRow(id string) error {
	_, err := s.db.Exec(`DELETE FROM files WHERE id = ?`, id)
	return err
}

// moveFile changes an active file's folder.
func (s *Store) moveFile(id, folder string) error {
	res, err := s.db.Exec(`UPDATE files SET folder = ? WHERE id = ? AND deleted_at IS NULL`, folder, id)
	if err != nil {
		return err
	}
	return affectedOne(res)
}

func (s *Store) incrementDownload(id string) error {
	_, err := s.db.Exec(`UPDATE files SET download_count = download_count + 1 WHERE id = ?`, id)
	return err
}

// expiredTrash returns files whose deleted_at is older than the cutoff.
func (s *Store) expiredTrash(cutoff time.Time) ([]*File, error) {
	rows, err := s.db.Query(
		`SELECT `+fileColumns+` FROM files WHERE deleted_at IS NOT NULL AND deleted_at < ?`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*File
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ---- folders ----

func (s *Store) listFolders() ([]string, error) {
	rows, err := s.db.Query(`SELECT path FROM folders ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// folderFileCounts returns a map of folder path -> active file count.
func (s *Store) folderFileCounts() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT folder, COUNT(*) FROM files WHERE deleted_at IS NULL GROUP BY folder`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var folder string
		var n int
		if err := rows.Scan(&folder, &n); err != nil {
			return nil, err
		}
		out[folder] = n
	}
	return out, rows.Err()
}

func (s *Store) createFolder(path string) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO folders (path, created_at) VALUES (?, ?)`, path, time.Now().UTC())
	return err
}

func (s *Store) folderExists(path string) bool {
	var p string
	err := s.db.QueryRow(`SELECT path FROM folders WHERE path = ?`, path).Scan(&p)
	return err == nil
}

func (s *Store) deleteFolder(path string) error {
	res, err := s.db.Exec(`DELETE FROM folders WHERE path = ?`, path)
	if err != nil {
		return err
	}
	_, _ = s.db.Exec(`DELETE FROM folder_permissions WHERE folder = ?`, path)
	return affectedOne(res)
}

// trashFilesUnderFolder soft-deletes (moves to trash) every ACTIVE file that
// lives in the folder or any subfolder, recording the deleter. Blobs stay on
// disk so the files remain recoverable from the trash. Returns how many moved.
func (s *Store) trashFilesUnderFolder(path string, when time.Time, by string) (int, error) {
	prefix := strings.TrimRight(path, "/") + "/"
	res, err := s.db.Exec(
		`UPDATE files SET deleted_at = ?, deleted_by = ? WHERE deleted_at IS NULL AND (folder = ? OR folder LIKE ?)`,
		when, by, path, prefix+"%")
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// moveFolderTree relocates a folder and everything under it: the folder row and
// all subfolders, every file's folder reference, and permission grants all have
// their oldPath prefix rewritten to newPath — in one transaction. LENGTH/SUBSTR
// are character-based in SQLite, so this is safe for multibyte (e.g. Korean) names.
func (s *Store) moveFolderTree(oldPath, newPath string) error {
	like := oldPath + "/%"
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`UPDATE folders SET path = ? || SUBSTR(path, LENGTH(?)+1) WHERE path = ? OR path LIKE ?`,
		newPath, oldPath, oldPath, like); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE files SET folder = ? || SUBSTR(folder, LENGTH(?)+1) WHERE folder = ? OR folder LIKE ?`,
		newPath, oldPath, oldPath, like); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE folder_permissions SET folder = ? || SUBSTR(folder, LENGTH(?)+1) WHERE folder = ? OR folder LIKE ?`,
		newPath, oldPath, oldPath, like); err != nil {
		return err
	}
	return tx.Commit()
}

// deleteFolderTree removes the folder, all of its subfolders, and the matching
// permission grants in one transaction. File rows are left untouched (they are
// moved to trash separately by trashFilesUnderFolder).
func (s *Store) deleteFolderTree(path string) error {
	prefix := strings.TrimRight(path, "/") + "/"
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM folders WHERE path = ? OR path LIKE ?`, path, prefix+"%"); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM folder_permissions WHERE folder = ? OR folder LIKE ?`, path, prefix+"%"); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- folder permissions (per-user ACL) ----

type FolderGrant struct {
	Folder   string `json:"folder"`
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	Level    string `json:"level"` // read | write
}

// setFolderPermission upserts a grant; level "" removes it.
func (s *Store) setFolderPermission(folder, userID, level string) error {
	if level == "" {
		_, err := s.db.Exec(`DELETE FROM folder_permissions WHERE folder = ? AND user_id = ?`, folder, userID)
		return err
	}
	_, err := s.db.Exec(
		`INSERT INTO folder_permissions (folder, user_id, level) VALUES (?, ?, ?)
		 ON CONFLICT(folder, user_id) DO UPDATE SET level = excluded.level`,
		folder, userID, level)
	return err
}

// listFolderPermissions returns the direct grants on a folder (with usernames).
func (s *Store) listFolderPermissions(folder string) ([]*FolderGrant, error) {
	rows, err := s.db.Query(`
		SELECT p.folder, p.user_id, COALESCE(u.username, ''), COALESCE(u.role, ''), p.level
		FROM folder_permissions p LEFT JOIN users u ON u.id = p.user_id
		WHERE p.folder = ?`, folder)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*FolderGrant
	for rows.Next() {
		var g FolderGrant
		if err := rows.Scan(&g.Folder, &g.UserID, &g.Username, &g.Role, &g.Level); err != nil {
			return nil, err
		}
		out = append(out, &g)
	}
	return out, rows.Err()
}

// permissionsForUser returns folder->level for all of a user's grants.
func (s *Store) permissionsForUser(userID string) (map[string]string, error) {
	rows, err := s.db.Query(`SELECT folder, level FROM folder_permissions WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var f, l string
		if err := rows.Scan(&f, &l); err != nil {
			return nil, err
		}
		m[f] = l
	}
	return m, rows.Err()
}

func (s *Store) deletePermissionsForUser(userID string) error {
	_, err := s.db.Exec(`DELETE FROM folder_permissions WHERE user_id = ?`, userID)
	return err
}

// ---- auto-blocked IPs (brute-force protection) ----

type BlockedIP struct {
	IP        string    `json:"ip"`
	BlockedAt time.Time `json:"blocked_at"`
	Reason    string    `json:"reason"`
	BlockedBy string    `json:"blocked_by"` // 차단 실행자 (username 또는 "system")
}

func (s *Store) addBlockedIP(ip, reason, by string) error {
	_, err := s.db.Exec(
		`INSERT INTO blocked_ips (ip, blocked_at, reason, blocked_by) VALUES (?, ?, ?, ?)
		 ON CONFLICT(ip) DO UPDATE SET blocked_at = excluded.blocked_at, reason = excluded.reason, blocked_by = excluded.blocked_by`,
		ip, time.Now().UTC(), reason, by)
	return err
}

func (s *Store) removeBlockedIP(ip string) error {
	_, err := s.db.Exec(`DELETE FROM blocked_ips WHERE ip = ?`, ip)
	return err
}

func (s *Store) isBlockedIP(ip string) bool {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM blocked_ips WHERE ip = ?`, ip).Scan(&n)
	return n > 0
}

// ---- public share links ----

type Share struct {
	Token         string    `json:"token"`
	FileID        string    `json:"file_id"`  // first file (back-compat / NOT NULL)
	FileIDs       []string  `json:"file_ids"` // all files in this share (1+)
	HasPassword   bool      `json:"has_password"`
	PasswordHash  string    `json:"-"`
	ExpiresAt     time.Time `json:"expires_at"`
	CreatedBy     string    `json:"created_by"`
	CreatedAt     time.Time `json:"created_at"`
	DownloadCount int       `json:"download_count"`
	MaxDownloads  int       `json:"max_downloads"` // 0 = unlimited
}

const shareCols = "token, file_id, COALESCE(file_ids,''), password_hash, expires_at, created_by, created_at, download_count, COALESCE(max_downloads,0)"

func scanShare(row interface{ Scan(...any) error }) (*Share, error) {
	var sh Share
	var ids string
	if err := row.Scan(&sh.Token, &sh.FileID, &ids, &sh.PasswordHash, &sh.ExpiresAt, &sh.CreatedBy, &sh.CreatedAt, &sh.DownloadCount, &sh.MaxDownloads); err != nil {
		return nil, err
	}
	if ids != "" {
		sh.FileIDs = strings.Split(ids, ",")
	} else if sh.FileID != "" {
		sh.FileIDs = []string{sh.FileID} // legacy single-file share
	}
	sh.HasPassword = sh.PasswordHash != ""
	return &sh, nil
}

func (s *Store) createShare(sh *Share) error {
	first := ""
	if len(sh.FileIDs) > 0 {
		first = sh.FileIDs[0]
	}
	_, err := s.db.Exec(
		`INSERT INTO shares (token, file_id, file_ids, password_hash, expires_at, created_by, created_at, download_count, max_downloads)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?)`,
		sh.Token, first, strings.Join(sh.FileIDs, ","), sh.PasswordHash, sh.ExpiresAt, sh.CreatedBy, sh.CreatedAt, sh.MaxDownloads)
	return err
}

// consumeShareDownload atomically reserves one download slot. Returns allowed
// (a slot was available) and exhausted (this use hit the max). The conditional
// UPDATE makes the check-and-increment race-free under concurrent requests.
func (s *Store) consumeShareDownload(token string) (allowed, exhausted bool, err error) {
	res, err := s.db.Exec(
		`UPDATE shares SET download_count = download_count + 1
		 WHERE token = ? AND (max_downloads = 0 OR download_count < max_downloads)`, token)
	if err != nil {
		return false, false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return false, false, nil // no slot left (limit reached) or token gone
	}
	var cnt, max int
	if err := s.db.QueryRow(`SELECT download_count, max_downloads FROM shares WHERE token = ?`, token).Scan(&cnt, &max); err != nil {
		return true, false, nil
	}
	return true, max > 0 && cnt >= max, nil
}

func (s *Store) getShare(token string) (*Share, error) {
	return scanShare(s.db.QueryRow(`SELECT `+shareCols+` FROM shares WHERE token = ?`, token))
}

func (s *Store) listAllShares() ([]*Share, error) {
	rows, err := s.db.Query(`SELECT ` + shareCols + ` FROM shares ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Share
	for rows.Next() {
		sh, err := scanShare(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sh)
	}
	return out, rows.Err()
}

func (s *Store) deleteShare(token string) error {
	_, err := s.db.Exec(`DELETE FROM shares WHERE token = ?`, token)
	return err
}

func (s *Store) deleteSharesForFile(fileID string) error {
	_, err := s.db.Exec(`DELETE FROM shares WHERE file_id = ?`, fileID)
	return err
}

func (s *Store) deleteExpiredShares(now time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM shares WHERE expires_at < ?`, now)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (s *Store) listBlockedIPs() ([]*BlockedIP, error) {
	rows, err := s.db.Query(`SELECT ip, blocked_at, reason, COALESCE(blocked_by, '') FROM blocked_ips ORDER BY blocked_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*BlockedIP
	for rows.Next() {
		var b BlockedIP
		if err := rows.Scan(&b.IP, &b.BlockedAt, &b.Reason, &b.BlockedBy); err != nil {
			return nil, err
		}
		out = append(out, &b)
	}
	return out, rows.Err()
}

// folderContentCount counts files (active or trashed) and subfolders under path.
func (s *Store) folderContentCount(path string) (int, error) {
	var files, subs int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM files WHERE folder = ?`, path).Scan(&files); err != nil {
		return 0, err
	}
	prefix := strings.TrimRight(path, "/") + "/"
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM folders WHERE path LIKE ?`, prefix+"%").Scan(&subs); err != nil {
		return 0, err
	}
	return files + subs, nil
}

// ---- api keys ----

func (s *Store) createKey(label, userID, scope string, isService bool, key string) (*APIKey, error) {
	k := &APIKey{
		ID:        newID(8),
		Key:       key,
		Label:     label,
		Scope:     scope,
		IsService: isService,
		UserID:    userID,
		CreatedAt: time.Now().UTC(),
	}
	svc := 0
	if isService {
		svc = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO api_keys (id, key, label, scope, is_service, user_id, created_at, revoked) VALUES (?, ?, ?, ?, ?, ?, ?, 0)`,
		k.ID, k.Key, k.Label, k.Scope, svc, k.UserID, k.CreatedAt)
	if err != nil {
		return nil, err
	}
	return k, nil
}

// scanKeyRow scans a full key row incl. owner username and use_count.
func scanKeyRow(row interface{ Scan(...any) error }) (*APIKey, error) {
	var k APIKey
	var last, revokedAt sql.NullTime
	var disabled, revoked, service int
	if err := row.Scan(&k.ID, &k.Key, &k.Label, &k.Scope, &service, &k.UserID, &k.Owner, &k.CreatedAt,
		&last, &disabled, &revoked, &revokedAt, &k.UseCount); err != nil {
		return nil, err
	}
	if last.Valid {
		k.LastUsedAt = &last.Time
	}
	if revokedAt.Valid {
		k.RevokedAt = &revokedAt.Time
	}
	k.IsService = service != 0
	k.Disabled = disabled != 0
	k.Revoked = revoked != 0
	return &k, nil
}

const keySelect = `
	SELECT k.id, k.key, k.label, k.scope, k.is_service, COALESCE(k.user_id,''), COALESCE(u.username,''), k.created_at,
	       k.last_used_at, k.disabled, k.revoked, k.revoked_at,
	       (SELECT COUNT(*) FROM access_logs l WHERE l.api_key_id = k.id) AS use_count
	FROM api_keys k LEFT JOIN users u ON u.id = k.user_id`

// keyLabelExists reports whether a user already has a key with the given label.
func (s *Store) keyLabelExists(label, userID string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM api_keys WHERE label = ? AND user_id = ?`, label, userID).Scan(&n)
	return n > 0, err
}

// countActiveKeys counts a user's non-revoked personal keys (against the per-user limit).
func (s *Store) countActiveKeys(userID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM api_keys WHERE user_id = ? AND revoked = 0 AND is_service = 0`, userID).Scan(&n)
	return n, err
}

func (s *Store) queryKeys(query string, args ...any) ([]*APIKey, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*APIKey
	for rows.Next() {
		k, err := scanKeyRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// listAllKeys returns every key (admin view).
func (s *Store) listAllKeys() ([]*APIKey, error) {
	return s.queryKeys(keySelect + ` ORDER BY k.created_at DESC`)
}

// listKeysForUser returns a single user's keys.
func (s *Store) listKeysForUser(userID string) ([]*APIKey, error) {
	return s.queryKeys(keySelect+` WHERE k.user_id = ? ORDER BY k.created_at DESC`, userID)
}

// lookupKey returns a key by its secret value (caller checks Disabled/Revoked).
func (s *Store) lookupKey(secret string) (*APIKey, error) {
	var k APIKey
	var last, revokedAt sql.NullTime
	var disabled, revoked int
	var service int
	err := s.db.QueryRow(
		`SELECT id, key, label, scope, is_service, COALESCE(user_id,''), created_at, last_used_at, disabled, revoked, revoked_at
		 FROM api_keys WHERE key = ?`, secret).Scan(
		&k.ID, &k.Key, &k.Label, &k.Scope, &service, &k.UserID, &k.CreatedAt, &last, &disabled, &revoked, &revokedAt)
	if err != nil {
		return nil, err
	}
	k.IsService = service != 0
	k.Disabled = disabled != 0
	k.Revoked = revoked != 0
	return &k, nil
}

func (s *Store) setKeyDisabled(id string, disabled bool) error {
	d := 0
	if disabled {
		d = 1
	}
	res, err := s.db.Exec(`UPDATE api_keys SET disabled = ? WHERE id = ? AND revoked = 0`, d, id)
	if err != nil {
		return err
	}
	return affectedOne(res)
}

// revokeKey marks a disabled key as revoked and stamps the time (starts the purge countdown).
func (s *Store) revokeKey(id string, when time.Time) error {
	res, err := s.db.Exec(`UPDATE api_keys SET revoked = 1, revoked_at = ? WHERE id = ? AND revoked = 0`, when, id)
	if err != nil {
		return err
	}
	return affectedOne(res)
}

func (s *Store) getKey(id string) (*APIKey, error) {
	var k APIKey
	var last, revokedAt sql.NullTime
	var disabled, revoked int
	var service int
	err := s.db.QueryRow(
		`SELECT id, key, label, scope, is_service, COALESCE(user_id,''), created_at, last_used_at, disabled, revoked, revoked_at
		 FROM api_keys WHERE id = ?`, id).Scan(
		&k.ID, &k.Key, &k.Label, &k.Scope, &service, &k.UserID, &k.CreatedAt, &last, &disabled, &revoked, &revokedAt)
	if err != nil {
		return nil, err
	}
	k.IsService = service != 0
	k.Disabled = disabled != 0
	k.Revoked = revoked != 0
	return &k, nil
}

func (s *Store) deleteKey(id string) error {
	res, err := s.db.Exec(`DELETE FROM api_keys WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return affectedOne(res)
}

// expiredRevokedKeys returns revoked keys whose revoked_at is older than the cutoff.
func (s *Store) expiredRevokedKeys(cutoff time.Time) ([]string, error) {
	rows, err := s.db.Query(`SELECT id FROM api_keys WHERE revoked = 1 AND revoked_at IS NOT NULL AND revoked_at < ?`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) touchKey(id string, when time.Time) error {
	_, err := s.db.Exec(`UPDATE api_keys SET last_used_at = ? WHERE id = ?`, when, id)
	return err
}

// ---- settings & sessions (auth) ----

func (s *Store) getSetting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (s *Store) setSetting(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (s *Store) createSession(token, userID string, expiresAt time.Time) error {
	_, err := s.db.Exec(`INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, ?)`, token, userID, expiresAt)
	return err
}

// sessionUser returns the user for a valid (non-expired) session token.
func (s *Store) sessionUser(token string, now time.Time) (*User, error) {
	row := s.db.QueryRow(`
		SELECT u.id, u.username, u.password_hash, u.role, u.created_at, u.last_login_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token = ? AND s.expires_at > ?`, token, now)
	return scanUser(row)
}

func (s *Store) deleteSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token = ?`, token)
	return err
}

func (s *Store) deleteSessionsForUser(userID string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

func (s *Store) deleteExpiredSessions(now time.Time) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, now)
	return err
}

// ---- users ----

const userColumns = "id, username, password_hash, role, created_at, last_login_at"

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	var last sql.NullTime
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.CreatedAt, &last); err != nil {
		return nil, err
	}
	if last.Valid {
		t := last.Time
		u.LastLoginAt = &t
	}
	return &u, nil
}

func (s *Store) updateLastLogin(id string, when time.Time) error {
	_, err := s.db.Exec(`UPDATE users SET last_login_at = ? WHERE id = ?`, when, id)
	return err
}

func (s *Store) createUser(u *User) error {
	admin := 0
	if isManager(u.Role) {
		admin = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO users (id, username, password_hash, role, is_admin, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		u.ID, u.Username, u.PasswordHash, u.Role, admin, u.CreatedAt)
	return err
}

func (s *Store) listUsers() ([]*User, error) {
	rows, err := s.db.Query(`SELECT ` + userColumns + ` FROM users ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) getUserByUsername(username string) (*User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userColumns+` FROM users WHERE username = ?`, username))
}

func (s *Store) getUser(id string) (*User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

func (s *Store) countAdminsExcept(excludeID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role = 'admin' AND id != ?`, excludeID).Scan(&n)
	return n, err
}

func (s *Store) countOwnersExcept(excludeID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role = 'owner' AND id != ?`, excludeID).Scan(&n)
	return n, err
}

func (s *Store) updateUserRole(id, role string) error {
	admin := 0
	if isManager(role) {
		admin = 1
	}
	_, err := s.db.Exec(`UPDATE users SET role = ?, is_admin = ? WHERE id = ?`, role, admin, id)
	return err
}

func (s *Store) usernameExists(username string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE username = ?`, username).Scan(&n)
	return n > 0, err
}

func (s *Store) deleteUser(id string) error {
	res, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return affectedOne(res)
}

func (s *Store) countAdmins() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE is_admin = 1`).Scan(&n)
	return n, err
}

func (s *Store) updateUserPassword(id, hash string) error {
	_, err := s.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, hash, id)
	return err
}

// ---- access logs ----

func (s *Store) addLog(l *AccessLog) error {
	action := l.Action
	if action == "" {
		action = "download"
	}
	status := l.Status
	if status == "" {
		status = "ok"
	}
	_, err := s.db.Exec(
		`INSERT INTO access_logs (action, status, detail, actor, api_key_id, file_id, ip, user_agent, accessed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		action, status, l.Detail, l.Actor, l.APIKeyID, l.FileID, l.IP, l.UserAgent, l.AccessedAt)
	return err
}

func (s *Store) listLogs(limit int) ([]*AccessLog, error) {
	rows, err := s.db.Query(`
		SELECT l.id, COALESCE(l.action, 'download'), COALESCE(l.status, 'ok'), COALESCE(l.detail, ''),
		       COALESCE(l.actor, ''), l.api_key_id, COALESCE(k.label, ''), l.file_id, COALESCE(f.name, ''),
		       l.ip, COALESCE(l.user_agent, ''), l.accessed_at
		FROM access_logs l
		LEFT JOIN api_keys k ON k.id = l.api_key_id
		LEFT JOIN files f ON f.id = l.file_id
		ORDER BY l.accessed_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AccessLog
	for rows.Next() {
		var l AccessLog
		if err := rows.Scan(&l.ID, &l.Action, &l.Status, &l.Detail, &l.Actor, &l.APIKeyID, &l.KeyLabel, &l.FileID, &l.FileName,
			&l.IP, &l.UserAgent, &l.AccessedAt); err != nil {
			return nil, err
		}
		out = append(out, &l)
	}
	return out, rows.Err()
}

func (s *Store) addAudit(l *AuditLog) error {
	_, err := s.db.Exec(
		`INSERT INTO audit_logs (actor, action, target, detail, ip, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		l.Actor, l.Action, l.Target, l.Detail, l.IP, l.CreatedAt)
	return err
}

func (s *Store) listAudit(limit int) ([]*AuditLog, error) {
	rows, err := s.db.Query(
		`SELECT id, actor, action, target, detail, ip, created_at FROM audit_logs ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AuditLog
	for rows.Next() {
		var l AuditLog
		if err := rows.Scan(&l.ID, &l.Actor, &l.Action, &l.Target, &l.Detail, &l.IP, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &l)
	}
	return out, rows.Err()
}

func (s *Store) addError(l *ErrorLog) error {
	_, err := s.db.Exec(
		`INSERT INTO error_logs (source, message, ip, created_at) VALUES (?, ?, ?, ?)`,
		l.Source, l.Message, l.IP, l.CreatedAt)
	return err
}

func (s *Store) listErrors(limit int) ([]*ErrorLog, error) {
	rows, err := s.db.Query(
		`SELECT id, source, message, ip, created_at FROM error_logs ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ErrorLog
	for rows.Next() {
		var l ErrorLog
		if err := rows.Scan(&l.ID, &l.Source, &l.Message, &l.IP, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &l)
	}
	return out, rows.Err()
}

func affectedOne(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("not found")
	}
	return nil
}
