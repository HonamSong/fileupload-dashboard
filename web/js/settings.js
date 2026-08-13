// Populate the settings page (called when the 설정 tab opens).
function showSettings() {
  $("#curPw").value = ""; $("#newPw").value = "";
  $("#settingsUser").textContent = me.username + " (" + me.role + ")";
  $("#subUsers").classList.toggle("hidden", !isManager()); // 사용자·접근로그는 owner/admin
  $("#subLogs").classList.toggle("hidden", !isManager());
  $("#subServer").classList.toggle("hidden", !isOwner());  // 서버 설정은 owner만
  selectSettingsSub("profile");                            // 기본은 프로필
}
async function selectSettingsSub(name) {
  if ((name === "users" || name === "logs") && !isManager()) return;
  if (name === "server" && !isOwner()) return;
  if (!(await guardLeaveServer(name === "server"))) return; // block leaving unsaved server changes
  $("#subProfile").classList.toggle("active", name === "profile");
  $("#subUsers").classList.toggle("active", name === "users");
  $("#subLogs").classList.toggle("active", name === "logs");
  $("#subServer").classList.toggle("active", name === "server");
  $("#settings-profile").classList.toggle("hidden", name !== "profile");
  $("#settings-users").classList.toggle("hidden", name !== "users");
  $("#settings-logs").classList.toggle("hidden", name !== "logs");
  $("#settings-server").classList.toggle("hidden", name !== "server");
  if (name === "users") loadUsers();
  if (name === "logs") loadLogs();
  if (name === "server") loadServer();
}
// ---- server settings: validation ----
function isIPv4(s) {
  const p = s.split(".");
  return p.length === 4 && p.every(o => /^\d{1,3}$/.test(o) && +o >= 0 && +o <= 255);
}
function isIPv6ish(s) { // permissive; server (net.ParseIP) is the final authority
  let ip = s, pfx = null;
  if (s.includes("/")) { const a = s.split("/"); if (a.length !== 2) return false; ip = a[0]; pfx = a[1]; }
  if (pfx !== null && (!/^\d{1,3}$/.test(pfx) || +pfx > 128)) return false;
  return ip.includes(":") && /^[0-9a-fA-F:.]+$/.test(ip);
}
// One list entry: single IP or CIDR. IPv4 prefix must be 0–32.
function ipEntryValid(tok) {
  tok = tok.trim();
  if (!tok) return true;                 // blank lines are ignored
  if (tok.includes(":")) return isIPv6ish(tok);
  if (tok.includes("/")) {
    const parts = tok.split("/");
    if (parts.length !== 2) return false;
    const [ip, pfx] = parts;
    if (!/^\d{1,2}$/.test(pfx) || +pfx > 32) return false; // rejects /35 and /255.255.255.0
    return isIPv4(ip);
  }
  return isIPv4(tok);
}
function baseUrlValid(s) {
  s = s.trim();
  if (!s) return true;                    // empty = use env default
  try { const u = new URL(s); return (u.protocol === "http:" || u.protocol === "https:") && !!u.host; }
  catch { return false; }
}

function syncIpScroll(ta) {
  const hl = ta.previousElementSibling;
  if (hl) { hl.scrollTop = ta.scrollTop; hl.scrollLeft = ta.scrollLeft; }
}

// Paint invalid lines red in the highlight layer; return the invalid-line count.
function renderIpHighlight(taId, hlId, errId) {
  const ta = $("#" + taId), hl = $("#" + hlId), err = $("#" + errId);
  const lines = ta.value.split("\n");
  let bad = 0;
  hl.innerHTML = lines.map(line => {
    const ok = ipEntryValid(line);
    if (!ok) bad++;
    const safe = esc(line) || " ";
    return ok ? `<span>${safe}</span>` : `<span class="bad">${safe}</span>`;
  }).join("\n");
  syncIpScroll(ta);
  ta.classList.toggle("invalid", bad > 0);
  if (err) err.textContent = bad ? `잘못된 IP/CIDR ${bad}줄` : "";
  return bad;
}

// Validate the whole Server form: paint highlights, toggle save button.
// Returns true when everything is valid.
function validateServerForm() {
  const baseOk = baseUrlValid($("#srvBaseUrl").value);
  $("#srvBaseUrl").classList.toggle("invalid", !baseOk);
  $("#srvBaseErr").textContent = baseOk ? "" : "http:// 또는 https:// 형식의 URL이어야 합니다";
  const badAllow = renderIpHighlight("srvIpAllow", "srvIpAllowHL", "srvIpAllowErr");
  const badBlock = renderIpHighlight("srvIpBlock", "srvIpBlockHL", "srvIpBlockErr");
  const valid = baseOk && badAllow === 0 && badBlock === 0;
  const btn = $("#srvSaveBtn");
  if (btn) btn.disabled = !valid;
  return valid;
}

// Called on every input in the Server form.
function onServerInput() {
  validateServerForm();
  recomputeServerDirty();
}

// ---- server settings: unsaved-change tracking ----
let serverDirty = false;
let serverBaseline = null; // snapshot of last loaded/saved values

function currentServerVals() {
  return {
    base_url: $("#srvBaseUrl").value.trim(),
    ip_allow: $("#srvIpAllow").value,
    ip_block: $("#srvIpBlock").value,
    auto_block: $("#srvAutoBlock").checked ? "on" : "off",
    ab_threshold: $("#srvAbThreshold").value,
    ab_window: $("#srvAbWindow").value,
  };
}
function recomputeServerDirty() {
  if (!serverBaseline) { serverDirty = false; return; }
  const c = currentServerVals();
  serverDirty = Object.keys(c).some(k => c[k] !== serverBaseline[k]);
  $("#srvSaved").textContent = serverDirty ? "저장되지 않은 변경사항" : "";
}
// Returns true if navigation may proceed. Prompts when leaving unsaved changes.
async function guardLeaveServer(stayingOnServer) {
  if (stayingOnServer || !serverDirty) return true;
  const ok = await confirmDialog("저장하지 않은 변경사항이 있습니다. 저장하지 않고 이동할까요?",
    { title: "변경사항 저장 안 됨", okLabel: "저장 안 하고 이동", danger: true });
  if (ok) serverDirty = false; // discard
  return ok;
}

async function loadServer() {
  $("#srvSaved").textContent = "";
  try {
    const s = await api("GET", "/api/server");
    $("#srvBaseUrl").value = s.base_url || "";
    $("#srvEnvBase").textContent = s.env_base_url || "";
    $("#srvIpAllow").value = s.ip_allow || "";
    $("#srvIpBlock").value = s.ip_block || "";
    $("#srvAutoBlock").checked = s.auto_block === "on";
    $("#srvAbThreshold").value = s.auto_block_threshold || 10;
    $("#srvAbWindow").value = s.auto_block_window || 10;
    renderBlockedIPs(s.blocked_ips || []);
    serverBaseline = currentServerVals();
    serverDirty = false;
    validateServerForm();
  } catch (e) { $("#srvSaved").textContent = e.message; }
}
function renderBlockedIPs(list) {
  const el = $("#srvBlockedWrap"); if (!el) return;
  if (!list.length) { el.innerHTML = '<div class="muted" style="font-size:12px">현재 자동 차단된 IP가 없습니다.</div>'; return; }
  el.innerHTML = `<div class="muted" style="font-size:12px;margin-bottom:6px">자동 차단된 IP (${list.length})</div>` +
    list.map(b => `
      <div class="fp-row">
        <span><span class="key">${esc(b.ip)}</span> <span class="muted">${esc(b.reason || "")} · ${fmtTime(b.blocked_at)}</span></span>
        <button class="btn ghost small" onclick="unblockIP('${escAttr(b.ip)}')">차단 해제</button>
      </div>`).join("");
}
async function unblockIP(ip) {
  try {
    await api("POST", "/api/server/unblock", { ip });
    toast(ip + " 차단 해제됨");
    const s = await api("GET", "/api/server");
    renderBlockedIPs(s.blocked_ips || []);
  } catch (e) { toast("실패: " + e.message); }
}
async function saveServer() {
  if (!validateServerForm()) { toast("잘못된 IP/CIDR 또는 URL이 있습니다"); return; }
  try {
    await api("POST", "/api/server", {
      base_url: $("#srvBaseUrl").value.trim(),
      ip_allow: $("#srvIpAllow").value,
      ip_block: $("#srvIpBlock").value,
      auto_block: $("#srvAutoBlock").checked ? "on" : "off",
      auto_block_threshold: +$("#srvAbThreshold").value || 10,
      auto_block_window: +$("#srvAbWindow").value || 10,
    });
    me.base_url = $("#srvBaseUrl").value.trim();  // reflect in commands immediately
    serverBaseline = currentServerVals();
    serverDirty = false;
    $("#srvSaved").textContent = "저장됨";
    updateUploadCmd(); if (cmdFile) showCommands(cmdFile);
  } catch (e) { $("#srvSaved").textContent = e.message; }
}

// ---- users (owner / admin) ----
function roleIsManager(role) { return role === "owner" || role === "admin"; }
function roleLabel(role) {
  if (role === "owner") return '<span style="color:#ffcf6b">owner</span>';
  if (role === "admin") return '<span style="color:var(--accent)">admin</span>';
  if (role === "view") return '<span style="color:#8a90ad">view</span>';
  return '<span class="muted">user</span>';
}
// Fill a role <select>. Owner may assign any role; admin only user/view.
function populateRoleSelect(sel, current) {
  const roles = isOwner() ? ["user", "view", "admin", "owner"] : ["user", "view"];
  sel.innerHTML = roles.map(r => `<option value="${r}">${r}</option>`).join("");
  if (current && roles.includes(current)) sel.value = current;
}
async function loadUsers() {
  populateRoleSelect($("#newUserRole"), "user"); // add-user role options by actor
  const users = await api("GET", "/api/users");
  const b = $("#usersBody"); b.innerHTML = "";
  (users || []).forEach(u => {
    const isSelf = u.username === me.username;
    // admin can only manage user/view accounts; owner can manage anyone.
    const canManage = isOwner() || !roleIsManager(u.role);
    const editBtn = canManage
      ? `<button class="btn ghost small" onclick="openUserEdit('${u.id}','${escAttr(u.username)}','${u.role}',${isSelf})">변경</button>`
      : "";
    const delBtn = (!isSelf && canManage)
      ? `<button class="btn danger small" onclick="deleteUser('${u.id}','${escAttr(u.username)}')">삭제</button>`
      : "";
    const tr = document.createElement("tr");
    tr.innerHTML = `
      <td class="name">${esc(u.username)}${isSelf ? ' <span class="muted">(나)</span>' : ''}</td>
      <td>${roleLabel(u.role)}</td>
      <td class="muted">${fmtTime(u.created_at)}</td>
      <td class="muted">${u.last_login_at ? fmtTime(u.last_login_at) : '<span class="muted">-</span>'}</td>
      <td><div class="row-actions">${editBtn}${delBtn}</div></td>`;
    b.appendChild(tr);
  });
  if (!users || !users.length) b.innerHTML = `<tr><td colspan="5" class="muted">사용자가 없습니다.</td></tr>`;
}

// ---- user edit modal (password + role) ----
let ueUserId = null;
function openUserEdit(id, username, role, isSelf) {
  ueUserId = id;
  $("#ueName").textContent = username;
  $("#uePw").value = "";
  populateRoleSelect($("#ueRole"), role);
  // A user cannot change their own role (last-owner / self-lockout guard).
  $("#ueRoleSection").classList.toggle("hidden", isSelf);
  $("#ueRoleNote").textContent = "";
  const modal = $("#userEditModal");
  modal.classList.remove("hidden");
  modal.onclick = e => { if (e.target === modal) closeUserEdit(); };
  $("#uePw").onkeydown = e => { if (e.key === "Enter") ueApplyPassword(); else if (e.key === "Escape") closeUserEdit(); };
  setTimeout(() => $("#uePw").focus(), 0);
}
function closeUserEdit() {
  const modal = $("#userEditModal");
  modal.classList.add("hidden");
  modal.onclick = null; $("#uePw").onkeydown = null;
  ueUserId = null;
}
async function ueApplyPassword() {
  if (!ueUserId) return;
  const pw = $("#uePw").value;
  if (pw.length < 4) { toast("비밀번호는 4자 이상이어야 합니다"); $("#uePw").focus(); return; }
  try {
    await api("POST", `/api/users/${ueUserId}/password`, { new: pw });
    $("#uePw").value = "";
    toast(`${$("#ueName").textContent} 비밀번호가 변경되었습니다`);
  } catch (e) { toast("변경 실패: " + e.message); }
}
async function ueApplyRole() {
  if (!ueUserId) return;
  const role = $("#ueRole").value;
  try {
    await api("POST", `/api/users/${ueUserId}/role`, { role });
    toast(`${$("#ueName").textContent} 역할: ${role}`);
    loadUsers(); // reflect in the table
  } catch (e) { $("#ueRoleNote").textContent = e.message; toast("변경 실패: " + e.message); }
}
async function addUser() {
  const username = $("#newUser").value.trim(), password = $("#newUserPw").value, role = $("#newUserRole").value;
  if (!username) { toast("아이디를 입력하세요"); return; }
  if (password.length < 4) { toast("비밀번호는 4자 이상이어야 합니다"); return; }
  try {
    await api("POST", "/api/users", { username, password, role });
    $("#newUser").value = ""; $("#newUserPw").value = "";
    toast("사용자 추가됨: " + username);
    loadUsers();
  } catch (e) { toast("추가 실패: " + e.message); }
}
async function deleteUser(id, username) {
  if (!await confirmDialog(`사용자 "${username}" 를 삭제하시겠습니까?`, { title: "사용자 삭제", okLabel: "삭제" })) return;
  try { await api("DELETE", `/api/users/${id}`); toast("삭제됨"); loadUsers(); }
  catch (e) { toast("삭제 실패: " + e.message); }
}
async function changePassword() {
  const current = $("#curPw").value, nw = $("#newPw").value;
  if (!current) { toast("현재 비밀번호를 입력하세요"); $("#curPw").focus(); return; }
  if (!nw) { toast("새 비밀번호를 입력하세요"); $("#newPw").focus(); return; }
  try {
    await api("POST", "/api/password", { current, new: nw });
    toast("비밀번호가 변경되었습니다");
    $("#curPw").value = ""; $("#newPw").value = "";
  } catch (e) { toast("변경 실패: " + e.message); }
}


// Condense a User-Agent to "Browser vNN · OS" (full string kept in the cell title).
function shortUA(ua) {
  if (!ua) return "-";
  if (/^curl\//i.test(ua)) return ua.split(" ")[0]; // curl/8.7.1
  let m, browser = "";
  if ((m = ua.match(/Edg\/(\d+)/))) browser = "Edge " + m[1];
  else if ((m = ua.match(/OPR\/(\d+)/))) browser = "Opera " + m[1];
  else if ((m = ua.match(/Firefox\/(\d+)/))) browser = "Firefox " + m[1];
  else if ((m = ua.match(/Chrome\/(\d+)/))) browser = "Chrome " + m[1];
  else if ((m = ua.match(/Version\/(\d+)[^ ]*.*Safari/))) browser = "Safari " + m[1];
  else if (/Safari/.test(ua)) browser = "Safari";
  else if ((m = ua.match(/Wget\/(\d+[\d.]*)/))) browser = "Wget " + m[1];
  let os = "";
  if (/Windows/.test(ua)) os = "Windows";
  else if (/Mac OS X/.test(ua)) os = "macOS";
  else if (/Android/.test(ua)) os = "Android";
  else if (/iPhone|iPad|iOS/.test(ua)) os = "iOS";
  else if (/Linux/.test(ua)) os = "Linux";
  return [browser, os].filter(Boolean).join(" · ") || ua.slice(0, 32);
}

// ---- logs (upload + download, sortable + searchable) ----
let logsCache = [];
let logSort = { key: "accessed_at", dir: -1 }; // -1 desc, 1 asc

let logPage = 1;
let logPageSize = +(localStorage.getItem("logPageSize") || 20);

let logBlockedIPs = new Set(); // IPs currently blocked (owner-only, for the log actions)

async function loadLogs() {
  logsCache = (await api("GET", "/api/logs")) || [];
  // Owner: know which IPs are already blocked so we show 차단 vs 허용.
  if (isOwner()) {
    try { const s = await api("GET", "/api/server"); logBlockedIPs = new Set((s.blocked_ips || []).map(b => b.ip)); }
    catch { logBlockedIPs = new Set(); }
  } else { logBlockedIPs = new Set(); }
  $("#logsUpdated").textContent = "Updated " + new Date().toLocaleTimeString("en-US");
  logPage = 1;
  renderLogs();
}

// Hover action on a log row's IP (owner only): block or unblock that IP.
function ipActions(ip) {
  if (!isOwner() || !ip) return "";
  return logBlockedIPs.has(ip)
    ? `<button class="ipact allow" title="이 IP 차단 해제" onclick="event.stopPropagation(); allowLogIP('${escAttr(ip)}')">허용</button>`
    : `<button class="ipact block" title="이 IP 차단" onclick="event.stopPropagation(); blockLogIP('${escAttr(ip)}')">차단</button>`;
}
async function blockLogIP(ip) {
  try { await api("POST", "/api/server/block", { ip }); logBlockedIPs.add(ip); toast(ip + " 차단됨"); renderLogs(); }
  catch (e) { toast("차단 실패: " + e.message); }
}
async function allowLogIP(ip) {
  try { await api("POST", "/api/server/unblock", { ip }); logBlockedIPs.delete(ip); toast(ip + " 차단 해제됨"); renderLogs(); }
  catch (e) { toast("해제 실패: " + e.message); }
}

function sortLogs(key) {
  if (logSort.key === key) logSort.dir *= -1;
  else logSort = { key, dir: 1 };
  logPage = 1;
  renderLogs();
}
// Called by the filter/search controls to reset to the first page.
function filterLogs() { logPage = 1; renderLogs(); }
function setLogPageSize(v) { logPageSize = +v; logPage = 1; localStorage.setItem("logPageSize", logPageSize); renderLogs(); }
function gotoLogPage(p) { logPage = p; renderLogs(); }

function renderLogs() {
  const b = $("#logsBody"); if (!b) return;
  const q = ($("#logSearch").value || "").trim().toLowerCase();
  const act = $("#logFilter").value;
  let rows = logsCache.filter(l => {
    if (!act) return true;
    if (act === "denied") return l.status === "denied";
    return (l.action || "download") === act;
  });
  if (q) {
    rows = rows.filter(l => [l.file_name, l.file_id, l.actor, l.key_label, l.api_key_id, l.ip, l.user_agent, l.action, l.detail, l.status]
      .some(v => (v || "").toString().toLowerCase().includes(q)));
  }
  const k = logSort.key, dir = logSort.dir;
  rows.sort((a, b2) => {
    let x = a[k], y = b2[k];
    if (k === "accessed_at") { x = new Date(x).getTime(); y = new Date(y).getTime(); }
    else { x = (x || "").toString().toLowerCase(); y = (y || "").toString().toLowerCase(); }
    return x < y ? -dir : x > y ? dir : 0;
  });
  const total = rows.length;
  const pages = Math.max(1, Math.ceil(total / logPageSize));
  if (logPage > pages) logPage = pages;
  const start = (logPage - 1) * logPageSize;
  const pageRows = rows.slice(start, start + logPageSize);
  b.innerHTML = pageRows.map(l => {
    const denied = l.status === "denied";
    const isUp = (l.action || "download") === "upload";
    const badge = isUp
      ? '<span class="pill up">업로드</span>'
      : '<span class="pill down">다운로드</span>';
    const result = denied ? '<span class="pill denied">거부</span>' : '<span class="pill ok">성공</span>';
    const fileCell = denied
      ? `<span style="color:var(--danger)">${esc(l.detail || "거부됨")}</span>`
      : esc(l.file_name || l.file_id || "-");
    return `<tr class="${denied ? "log-denied" : ""}">
      <td class="muted" style="white-space:nowrap">${fmtTime(l.accessed_at)}</td>
      <td>${badge}</td>
      <td>${result}</td>
      <td style="word-break:break-all">${fileCell}</td>
      <td>${esc(l.actor || "-")}</td>
      <td>${esc(l.key_label || (l.api_key_id ? l.api_key_id : "-"))}</td>
      <td class="key" style="white-space:nowrap"><span>${esc(l.ip)}</span>${ipActions(l.ip)}</td>
      <td class="muted" style="white-space:nowrap" title="${esc(l.user_agent || "")}">${esc(shortUA(l.user_agent))}</td>
    </tr>`;
  }).join("") || `<tr><td colspan="8" class="muted">로그가 없습니다.</td></tr>`;
  $("#logsCount").textContent = total + "건";
  renderLogPager(total, pages, start, pageRows.length);
  // sort indicator on the active column header
  document.querySelectorAll("#settings-logs th[data-sort]").forEach(th => {
    th.classList.toggle("sorted", th.dataset.sort === k);
    th.dataset.dir = th.dataset.sort === k ? (dir === 1 ? "▲" : "▼") : "";
  });
}

function renderLogPager(total, pages, start, shown) {
  const el = $("#logPager"); if (!el) return;
  const from = total ? start + 1 : 0, to = start + shown;
  el.innerHTML = `
    <div class="pg-left">
      <span class="muted">페이지당</span>
      <select class="txt" onchange="setLogPageSize(this.value)">
        ${[20, 50, 100].map(n => `<option value="${n}" ${n === logPageSize ? "selected" : ""}>${n}</option>`).join("")}
      </select>
    </div>
    <div class="pg-center">
      <button class="btn ghost small" ${logPage <= 1 ? "disabled" : ""} onclick="gotoLogPage(${logPage - 1})">‹ 이전</button>
      <span class="muted">${logPage} / ${pages}</span>
      <button class="btn ghost small" ${logPage >= pages ? "disabled" : ""} onclick="gotoLogPage(${logPage + 1})">다음 ›</button>
    </div>
    <div class="pg-right">
      <span class="muted">${from}–${to} / ${total}건</span>
    </div>`;
}
