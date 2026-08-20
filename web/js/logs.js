// ---- 로그 하위 탭 (Access / Error / Audit) ----
function selectLogTab(name) {
  if (!["access", "error", "audit"].includes(name)) name = "access";
  localStorage.setItem("logTab", name);
  $("#logTabAccess").classList.toggle("active", name === "access");
  $("#logTabError").classList.toggle("active", name === "error");
  $("#logTabAudit").classList.toggle("active", name === "audit");
  $("#log-access").classList.toggle("hidden", name !== "access");
  $("#log-error").classList.toggle("hidden", name !== "error");
  $("#log-audit").classList.toggle("hidden", name !== "audit");
  if (name === "access") loadLogs();
  else if (name === "error") loadErrorLogs();
  else loadAuditLogs();
}

// 공통 페이지당 개수 (Access 로그와 동일 키 공유)
function logsPageSize() { return +(localStorage.getItem("logPageSize") || 20); }
function setLogsPageSizeShared(v) { localStorage.setItem("logPageSize", +v); }

// 공통 페이저 렌더 (id 접두어로 구분)
function renderPager(elId, page, pages, total, start, shown, sizeFn, gotoFn) {
  const el = $("#" + elId); if (!el) return;
  const from = total ? start + 1 : 0, to = start + shown;
  el.innerHTML = `
    <div class="pg-left">
      <span class="muted">페이지당</span>
      <select class="txt" onchange="${sizeFn}(this.value)">
        ${[20, 50, 100].map(n => `<option value="${n}" ${n === logsPageSize() ? "selected" : ""}>${n}</option>`).join("")}
      </select>
    </div>
    <div class="pg-center">
      <button class="btn ghost small" ${page <= 1 ? "disabled" : ""} onclick="${gotoFn}(${page - 1})">‹ 이전</button>
      <span class="muted">${page} / ${pages}</span>
      <button class="btn ghost small" ${page >= pages ? "disabled" : ""} onclick="${gotoFn}(${page + 1})">다음 ›</button>
    </div>
    <div class="pg-right"><span class="muted">${from}–${to} / ${total}건</span></div>`;
}

// ==================== Error Log ====================
let errCache = [], errPage = 1;
async function loadErrorLogs() {
  try { errCache = (await api("GET", "/api/error-logs")) || []; }
  catch (e) { errCache = []; }
  errPage = 1;
  renderErr();
}
function filterErr() { errPage = 1; renderErr(); }
function setErrPageSize(v) { setLogsPageSizeShared(v); errPage = 1; renderErr(); }
function gotoErrPage(p) { errPage = p; renderErr(); }
function renderErr() {
  const b = $("#errBody"); if (!b) return;
  const q = ($("#errSearch").value || "").trim().toLowerCase();
  let rows = errCache;
  if (q) rows = rows.filter(l => [l.source, l.message, l.ip].some(v => (v || "").toString().toLowerCase().includes(q)));
  const total = rows.length, size = logsPageSize();
  const pages = Math.max(1, Math.ceil(total / size));
  if (errPage > pages) errPage = pages;
  const start = (errPage - 1) * size;
  const pageRows = rows.slice(start, start + size);
  b.innerHTML = pageRows.map(l => `
    <tr>
      <td class="muted" style="white-space:nowrap">${fmtTime(l.created_at)}</td>
      <td class="key" style="white-space:nowrap">${esc(l.source || "-")}</td>
      <td style="word-break:break-word;color:var(--danger)">${esc(l.message || "-")}</td>
      <td class="key" style="white-space:nowrap">${esc(l.ip || "-")}</td>
    </tr>`).join("") || `<tr><td colspan="4" class="muted">에러 로그가 없습니다.</td></tr>`;
  $("#errCount").textContent = total + "건";
  renderPager("errPager", errPage, pages, total, start, pageRows.length, "setErrPageSize", "gotoErrPage");
}

// ==================== Audit Log ====================
let auditCache = [], auditPage = 1;

// action → {label, group, color}
const AUDIT_ACTIONS = {
  login:          { label: "로그인",          group: "auth",   color: "ok" },
  logout:         { label: "로그아웃",        group: "auth",   color: "muted" },
  login_failed:   { label: "로그인 실패",     group: "auth",   color: "denied" },
  file_upload:    { label: "파일 업로드",     group: "file",   color: "up" },
  file_delete:    { label: "파일 삭제",       group: "file",   color: "warnp" },
  file_purge:     { label: "파일 완전삭제",   group: "file",   color: "denied" },
  file_restore:   { label: "파일 복구",       group: "file",   color: "ok" },
  file_move:      { label: "파일 이동",       group: "file",   color: "down" },
  file_version_restore: { label: "버전 되돌리기", group: "file", color: "down" },
  file_version_delete:  { label: "버전 삭제",     group: "file", color: "denied" },
  folder_create:  { label: "폴더 생성",       group: "folder", color: "ok" },
  folder_delete:  { label: "폴더 삭제",       group: "folder", color: "denied" },
  folder_move:    { label: "폴더 이동",       group: "folder", color: "down" },
  folder_perm:    { label: "폴더 권한 변경",   group: "folder", color: "down" },
  key_create:     { label: "키 생성",         group: "key",    color: "ok" },
  key_delete:     { label: "키 삭제",         group: "key",    color: "denied" },
  key_revoke:     { label: "키 폐기",         group: "key",    color: "warnp" },
  key_disable:    { label: "키 비활성화",     group: "key",    color: "warnp" },
  key_enable:     { label: "키 활성화",       group: "key",    color: "ok" },
  user_create:    { label: "사용자 생성",     group: "user",   color: "ok" },
  user_delete:    { label: "사용자 삭제",     group: "user",   color: "denied" },
  user_role:      { label: "역할 변경",       group: "user",   color: "down" },
  user_password:  { label: "비밀번호 재설정", group: "user",   color: "warnp" },
  password_change:{ label: "비밀번호 변경",   group: "user",   color: "warnp" },
};
function auditInfo(a) { return AUDIT_ACTIONS[a] || { label: a || "-", group: "", color: "muted" }; }

async function loadAuditLogs() {
  try { auditCache = (await api("GET", "/api/audit-logs")) || []; }
  catch (e) { auditCache = []; }
  auditPage = 1;
  renderAudit();
}
function filterAudit() { auditPage = 1; renderAudit(); }
function setAuditPageSize(v) { setLogsPageSizeShared(v); auditPage = 1; renderAudit(); }
function gotoAuditPage(p) { auditPage = p; renderAudit(); }
function renderAudit() {
  const b = $("#auditBody"); if (!b) return;
  const q = ($("#auditSearch").value || "").trim().toLowerCase();
  const grp = $("#auditFilter").value;
  let rows = auditCache;
  if (grp) rows = rows.filter(l => auditInfo(l.action).group === grp);
  if (q) rows = rows.filter(l => {
    const info = auditInfo(l.action);
    return [info.label, l.action, l.target, l.detail, l.actor, l.ip]
      .some(v => (v || "").toString().toLowerCase().includes(q));
  });
  const total = rows.length, size = logsPageSize();
  const pages = Math.max(1, Math.ceil(total / size));
  if (auditPage > pages) auditPage = pages;
  const start = (auditPage - 1) * size;
  const pageRows = rows.slice(start, start + size);
  b.innerHTML = pageRows.map(l => {
    const info = auditInfo(l.action);
    return `<tr>
      <td class="muted" style="white-space:nowrap">${fmtTime(l.created_at)}</td>
      <td><span class="pill ${info.color}">${esc(info.label)}</span></td>
      <td style="word-break:break-all">${esc(l.target || "-")}</td>
      <td class="muted" style="word-break:break-word">${esc(l.detail || "")}</td>
      <td>${esc(l.actor || "-")}</td>
      <td class="key" style="white-space:nowrap">${esc(l.ip || "-")}</td>
    </tr>`;
  }).join("") || `<tr><td colspan="6" class="muted">감사 로그가 없습니다.</td></tr>`;
  $("#auditCount").textContent = total + "건";
  renderPager("auditPager", auditPage, pages, total, start, pageRows.length, "setAuditPageSize", "gotoAuditPage");
}
