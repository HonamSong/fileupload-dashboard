package main

import (
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Share expiry bounds (minutes). 5분(테스트) ~ 30일, 기본 1일.
const (
	shareMinMinutes     = 5
	shareMaxMinutes     = 30 * 24 * 60
	shareDefaultMinutes = 24 * 60
)

// fmtSize renders a byte count as a short human string.
func fmtSize(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(u), 0
	for m := n / u; m >= u; m /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

// shareView adds the public URL, resolved file names, and hides the password hash.
func (s *Server) shareView(sh *Share) map[string]any {
	names := make([]string, 0, len(sh.FileIDs))
	var total int64
	for _, id := range sh.FileIDs {
		if f, err := s.store.getFile(id); err == nil {
			names = append(names, f.Name)
			total += f.Size
		}
	}
	remaining := -1 // unlimited
	if sh.MaxDownloads > 0 {
		remaining = sh.MaxDownloads - sh.DownloadCount
		if remaining < 0 {
			remaining = 0
		}
	}
	return map[string]any{
		"token": sh.Token, "url": s.baseURL() + "/s/" + sh.Token,
		"has_password": sh.HasPassword, "expires_at": sh.ExpiresAt,
		"created_by": sh.CreatedBy, "created_at": sh.CreatedAt,
		"download_count": sh.DownloadCount, "max_downloads": sh.MaxDownloads, "remaining": remaining,
		"files": names, "file_count": len(sh.FileIDs), "total_size": total,
	}
}

// POST /api/shares — create a public share link for one or more files.
func (s *Server) handleCreateShare(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	if !isEditor(u.Role) {
		httpError(w, http.StatusForbidden, "공유 링크는 편집 권한이 필요합니다")
		return
	}
	var body struct {
		FileIDs      []string `json:"file_ids"`
		Password     string   `json:"password"`
		Minutes      int      `json:"minutes"`
		Hours        int      `json:"hours"`         // legacy fallback
		MaxDownloads *int     `json:"max_downloads"` // nil→기본 1, 0→무제한, n→n회
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if len(body.FileIDs) == 0 {
		httpError(w, http.StatusBadRequest, "공유할 파일을 선택하세요")
		return
	}
	maxDl := 1 // default: single download
	if body.MaxDownloads != nil {
		maxDl = *body.MaxDownloads
	}
	if maxDl < 0 {
		maxDl = 1
	}
	if maxDl > 100000 {
		maxDl = 100000
	}
	// Every file must exist, be live, and be readable by the creator.
	ids := make([]string, 0, len(body.FileIDs))
	for _, id := range body.FileIDs {
		f, err := s.store.getFile(id)
		if err != nil || f.DeletedAt != nil {
			httpError(w, http.StatusNotFound, "파일을 찾을 수 없습니다")
			return
		}
		if !canRead(s.folderAccess(u, f.Folder)) {
			httpError(w, http.StatusForbidden, "일부 파일에 접근 권한이 없습니다")
			return
		}
		ids = append(ids, f.ID)
	}
	mins := body.Minutes
	if mins <= 0 && body.Hours > 0 {
		mins = body.Hours * 60
	}
	if mins <= 0 {
		mins = shareDefaultMinutes
	}
	if mins < shareMinMinutes {
		mins = shareMinMinutes
	}
	if mins > shareMaxMinutes {
		mins = shareMaxMinutes
	}
	pwHash := ""
	if pw := strings.TrimSpace(body.Password); pw != "" {
		h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
		if err != nil {
			httpError(w, http.StatusInternalServerError, "hash error")
			return
		}
		pwHash = string(h)
	}
	now := time.Now().UTC()
	sh := &Share{
		Token: newID(16), FileIDs: ids, PasswordHash: pwHash,
		ExpiresAt: now.Add(time.Duration(mins) * time.Minute),
		CreatedBy: u.Username, CreatedAt: now, MaxDownloads: maxDl,
	}
	sh.HasPassword = pwHash != ""
	if err := s.store.createShare(sh); err != nil {
		httpError(w, http.StatusInternalServerError, "db error: %v", err)
		return
	}
	log.Printf("share created: token=%s files=%d max=%d by=%s expires=%s", sh.Token, len(ids), maxDl, u.Username, sh.ExpiresAt.Format(time.RFC3339))
	writeJSON(w, http.StatusCreated, s.shareView(sh))
}

// GET /api/shares — list shares (own; managers see all).
func (s *Server) handleListShares(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	all, err := s.store.listAllShares()
	if err != nil {
		httpError(w, http.StatusInternalServerError, "db error: %v", err)
		return
	}
	out := make([]map[string]any, 0, len(all))
	for _, sh := range all {
		if !isManager(u.Role) && sh.CreatedBy != u.Username {
			continue
		}
		out = append(out, s.shareView(sh))
	}
	writeJSON(w, http.StatusOK, map[string]any{"shares": out})
}

// DELETE /api/shares/{token} — revoke a share (creator or manager).
func (s *Server) handleDeleteShare(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	sh, err := s.store.getShare(token)
	if err != nil {
		httpError(w, http.StatusNotFound, "not found")
		return
	}
	u := s.currentUser(r)
	if !isManager(u.Role) && sh.CreatedBy != u.Username {
		httpError(w, http.StatusForbidden, "본인이 만든 공유만 삭제할 수 있습니다")
		return
	}
	_ = s.store.deleteShare(token)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// Brute-force guard for share passwords: max 10 wrong tries per token / 10 min.
const sharePwMaxTries = 10
const sharePwWindow = 10 * time.Minute

func (s *Server) sharePwBlocked(token string) bool {
	cutoff := time.Now().Add(-sharePwWindow)
	s.failMu.Lock()
	defer s.failMu.Unlock()
	kept := s.sharePwFail[token][:0]
	for _, t := range s.sharePwFail[token] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	s.sharePwFail[token] = kept
	return len(kept) >= sharePwMaxTries
}

func (s *Server) recordSharePwFail(token string) {
	s.failMu.Lock()
	s.sharePwFail[token] = append(s.sharePwFail[token], time.Now())
	s.failMu.Unlock()
}

// ---- public (no auth) ----

// resolveShare returns the share + its live files if the link is valid.
func (s *Server) resolveShare(token string) (*Share, []*File, bool) {
	sh, err := s.store.getShare(token)
	if err != nil {
		return nil, nil, false
	}
	if time.Now().After(sh.ExpiresAt) {
		return nil, nil, false
	}
	var files []*File
	for _, id := range sh.FileIDs {
		if f, err := s.store.getFile(id); err == nil && f.DeletedAt == nil {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		return nil, nil, false
	}
	return sh, files, true
}

// GET /s/{token} — public landing page.
func (s *Server) handleSharePage(w http.ResponseWriter, r *http.Request) {
	sh, files, ok := s.resolveShare(r.PathValue("token"))
	if !ok {
		s.renderShareGone(w)
		return
	}
	s.renderSharePage(w, sh, files, "")
}

// POST /s/{token} — verify password (if any) and stream file(s). Multiple → zip.
func (s *Server) handleShareDownload(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	sh, files, ok := s.resolveShare(token)
	if !ok {
		s.renderShareGone(w)
		return
	}
	if sh.HasPassword {
		if s.sharePwBlocked(token) {
			s.renderSharePage(w, sh, files, "비밀번호 시도가 너무 많습니다. 잠시 후 다시 시도하세요.")
			return
		}
		pw := r.FormValue("password")
		if bcrypt.CompareHashAndPassword([]byte(sh.PasswordHash), []byte(pw)) != nil {
			s.recordSharePwFail(token)
			s.renderSharePage(w, sh, files, "비밀번호가 올바르지 않습니다")
			return
		}
	}
	// Atomically reserve a download slot (race-free). If none left, the link is
	// used up → show the gone/warning page.
	allowed, exhausted, err := s.store.consumeShareDownload(token)
	if err != nil || !allowed {
		s.renderShareGone(w)
		return
	}
	logOne := func(f *File) {
		_ = s.store.addLog(&AccessLog{
			Action: "download", Actor: "공유(" + sh.CreatedBy + ")", Detail: "share " + token,
			FileID: f.ID, IP: clientIP(r), UserAgent: r.UserAgent(), AccessedAt: time.Now().UTC(),
		})
	}
	if len(files) == 1 {
		logOne(files[0])
		s.streamFile(w, files[0])
	} else {
		zipName := fmt.Sprintf("share-%s.zip", time.Now().Format("20060102-150405"))
		s.streamZip(w, files, zipName, logOne)
	}
	// Download limit reached with this use → delete the link (expiry may remain).
	if exhausted {
		_ = s.store.deleteShare(token)
		log.Printf("share exhausted, deleted: token=%s", token)
	}
}

// ---- public page rendering (self-contained) ----

func (s *Server) renderShareGone(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotFound)
	fmt.Fprint(w, shareShell(`<div class="lock">⛔</div><h1>링크를 사용할 수 없습니다</h1>
	  <p>이 공유 링크는 만료되었거나 존재하지 않습니다.</p>`))
}

func (s *Server) renderSharePage(w http.ResponseWriter, sh *Share, files []*File, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	status := http.StatusOK
	if errMsg != "" {
		status = http.StatusUnauthorized
	}
	w.WriteHeader(status)

	var total int64
	for _, f := range files {
		total += f.Size
	}
	var b strings.Builder
	b.WriteString(`<div class="lock">📄</div><h1>파일 다운로드</h1>`)
	if len(files) == 1 {
		fmt.Fprintf(&b, `<div class="fname">%s</div>`, html.EscapeString(files[0].Name))
	} else {
		fmt.Fprintf(&b, `<div class="fname">%d개 파일 (zip)</div>`, len(files))
		b.WriteString(`<ul class="flist">`)
		for _, f := range files {
			fmt.Fprintf(&b, `<li>%s <span class="fsz">%s</span></li>`, html.EscapeString(f.Name), html.EscapeString(fmtSize(f.Size)))
		}
		b.WriteString(`</ul>`)
	}
	dl := "다운로드 무제한"
	if sh.MaxDownloads > 0 {
		rem := sh.MaxDownloads - sh.DownloadCount
		if rem < 0 {
			rem = 0
		}
		dl = fmt.Sprintf("남은 다운로드 %d회", rem)
	}
	fmt.Fprintf(&b, `<div class="meta">%s · 만료 %s · %s</div>`,
		html.EscapeString(fmtSize(total)), html.EscapeString(sh.ExpiresAt.Local().Format("2006-01-02 15:04")), html.EscapeString(dl))
	if errMsg != "" {
		fmt.Fprintf(&b, `<div class="err">%s</div>`, html.EscapeString(errMsg))
	}
	b.WriteString(`<form method="post" action="">`)
	if sh.HasPassword {
		b.WriteString(`<input type="password" name="password" placeholder="비밀번호" autofocus required>`)
	}
	b.WriteString(`<button type="submit">` + map[bool]string{true: "zip 다운로드", false: "다운로드"}[len(files) > 1] + `</button>`)
	b.WriteString(`</form>`)
	fmt.Fprint(w, shareShell(b.String()))
}

// shareShell wraps content in a minimal, self-contained dark page.
func shareShell(inner string) string {
	return `<!doctype html><html lang="ko"><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>파일 다운로드</title>
<style>
  :root { color-scheme: dark; }
  * { box-sizing: border-box; }
  body { margin:0; min-height:100vh; display:flex; align-items:center; justify-content:center;
    background:#0f1220; color:#e7e9f3; font:15px/1.6 system-ui,-apple-system,"Segoe UI",sans-serif; padding:24px; }
  .box { background:#171a2b; border:1px solid #2a2f4a; border-radius:16px; padding:36px 32px;
    max-width:420px; width:100%; text-align:center; box-shadow:0 20px 60px rgba(0,0,0,.5); }
  .lock { font-size:42px; margin-bottom:8px; }
  h1 { margin:0 0 14px; font-size:20px; }
  .fname { font-size:16px; font-weight:600; word-break:break-all; margin-bottom:4px; }
  .flist { list-style:none; padding:0; margin:8px 0 4px; text-align:left; max-height:200px; overflow:auto; }
  .flist li { font-size:13px; padding:6px 10px; background:#0b0e1a; border:1px solid #2a2f4a;
    border-radius:8px; margin-bottom:6px; display:flex; justify-content:space-between; gap:10px; word-break:break-all; }
  .flist .fsz { color:#9aa0bd; flex-shrink:0; }
  .meta { color:#9aa0bd; font-size:13px; margin-bottom:18px; }
  .err { color:#ff6b6b; font-size:13px; margin-bottom:12px; }
  form { display:flex; flex-direction:column; gap:10px; }
  input { background:#0b0e1a; border:1px solid #2a2f4a; color:#e7e9f3; border-radius:8px;
    padding:10px 12px; font-size:14px; }
  input:focus { outline:none; border-color:#6c8cff; }
  button { background:#6c8cff; color:#fff; border:none; border-radius:8px; padding:11px 12px;
    font-size:15px; cursor:pointer; }
  button:hover { filter:brightness(1.08); }
</style></head><body><div class="box">` + inner + `</div></body></html>`
}
