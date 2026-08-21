// ---- keys ----
function daysLeft(purgeAt) {
  if (!purgeAt) return 0;
  return Math.max(0, Math.ceil((new Date(purgeAt) - new Date()) / 86400000));
}

let keyLimit = 3, myActiveKeys = 0;
let usersById = {}; // id -> {username, role} (managers only, for on-behalf issuance)
async function loadKeys() {
  const res = await api("GET", "/api/keys");
  const keys = res.keys || [];
  keysCache = keys; // keep command key-dropdowns in sync
  const admin = !!res.is_admin;
  keyLimit = res.limit || 3;
  // Managers can issue keys on behalf of another user — populate the target list.
  if (isManager()) {
    try {
      const users = await api("GET", "/api/users");
      usersById = {};
      const sel = $("#keyOwner");
      const cur = sel.value;
      sel.innerHTML = `<option value="">본인 (${esc(me.username)})</option>`;
      (users || []).forEach(u => {
        usersById[u.id] = { username: u.username, role: u.role };
        if (u.username === me.username) return; // 본인은 위 옵션으로
        const o = document.createElement("option");
        o.value = u.id; o.textContent = `${u.username} (${u.role})`;
        sel.appendChild(o);
      });
      sel.value = [...sel.options].some(o => o.value === cur) ? cur : "";
      sel.classList.remove("hidden");
    } catch { $("#keyOwner").classList.add("hidden"); }
  } else {
    $("#keyOwner").classList.add("hidden");
    $("#keyOwner").value = "";
  }
  // Service keys are admin-only.
  $("#keyKind").classList.toggle("hidden", !isAdmin());
  if (!isAdmin()) $("#keyKind").value = "personal";
  applyOwnerConstraints(); // scope options + dup/limit for the selected owner
  syncKeyOwnerVisibility();
  const b = $("#keysBody"); b.innerHTML = "";
  keys.forEach(k => {
    const tr = document.createElement("tr");
    let status, actions;
    const isExpired = k.expires_at && new Date(k.expires_at) <= new Date();
    if (k.revoked) {
      tr.className = "dimmed";
      status = k.purge_at
        ? `<span style="color:var(--danger)">폐기됨 - ${daysLeft(k.purge_at)}일 뒤 완전 삭제</span>`
        : `<span style="color:var(--danger)">폐기됨</span>`;
      actions = `<button class="btn danger small" onclick="forceDeleteKey('${k.id}')">강제 삭제</button>`;
    } else if (isExpired) {
      tr.className = "dimmed";
      status = `<span style="color:var(--danger)">만료됨</span>`;
      actions = `<button class="btn danger small" onclick="forceDeleteKey('${k.id}')">삭제</button>`;
    } else if (k.disabled) {
      status = `<span style="color:var(--warn)">비활성</span>`;
      actions = `<button class="btn ghost small" onclick="enableKey('${k.id}')">활성화</button>
                 <button class="btn danger small" onclick="revokeKey('${k.id}')">폐기</button>`;
    } else {
      const exp = k.expires_at ? `<br><span class="muted key">⏱ 만료 ${fmtTime(k.expires_at)}</span>` : "";
      status = `<span style="color:var(--ok)">활성</span>${exp}`;
      actions = `<button class="btn ghost small" onclick="disableKey('${k.id}')">비활성화</button>`;
    }
    // 서비스 키는 owner/admin만 관리(및 키 값 확인) 가능.
    const canManage = isManager() || !k.is_service;
    if (!canManage) actions = `<span class="muted" style="font-size:12px">관리자 전용</span>`;
    const owner = k.is_service
      ? `<br><span class="muted key">🔧 서비스</span>`
      : (admin ? `<br><span class="muted key">👤 ${esc(k.owner || '-')}</span>` : '');
    const keyCell = canManage
      ? `<span class="muted">${maskKey(k.key)}</span>
         <button class="btn ghost small" style="margin-left:8px" onclick="copyText('${esc(k.key)}')">복사</button>`
      : `<span class="muted">••••••••</span>`;
    tr.innerHTML = `
      <td class="name">${esc(k.label)}${owner}</td>
      <td>${scopeLabel(k.scope)}</td>
      <td class="key">${keyCell}</td>
      <td class="muted">${fmtTime(k.created_at)}</td>
      <td class="muted">${fmtTime(k.last_used_at)}</td>
      <td><span class="pill">${k.use_count}</span></td>
      <td>${status}</td>
      <td><div class="row-actions">${actions}</div></td>`;
    b.appendChild(tr);
  });
  if (!keys.length) b.innerHTML = `<tr><td colspan="8" class="muted">발급된 키가 없습니다.</td></tr>`;
  populateHcKeys();
}

// ---- 헬스체크 (UI에서 /healthz 를 선택한 키로 호출) ----
function populateHcKeys() {
  const sel = $("#hcKeySel"); if (!sel) return;
  const cur = sel.value;
  const active = (keysCache || []).filter(k => !k.revoked && !k.disabled && !(k.expires_at && new Date(k.expires_at) <= new Date()));
  sel.innerHTML = active.length
    ? active.map(k => `<option value="${escAttr(k.key)}">${esc(k.label)}${k.is_service ? " (서비스)" : ""} · ${maskKey(k.key)}</option>`).join("")
    : `<option value="">사용 가능한 키 없음</option>`;
  if (cur && active.some(k => k.key === cur)) sel.value = cur;
}
async function runHealthCheck() {
  const key = $("#hcKeySel").value;
  const el = $("#hcResult");
  if (!key) { el.textContent = "사용 가능한 키가 없습니다"; el.style.color = "var(--muted)"; return; }
  el.textContent = "확인 중…"; el.style.color = "var(--muted)";
  try {
    const res = await fetch("/healthz", { headers: { "X-API-Key": key } });
    if (res.ok) {
      const d = await res.json().catch(() => ({}));
      el.textContent = `✔ 정상 (200) · v${d.version || "-"} · uptime ${d.uptime_sec != null ? d.uptime_sec + "s" : "-"}`;
      el.style.color = "#4ade80";
    } else {
      el.textContent = `✕ 실패 (${res.status})`;
      el.style.color = "var(--danger)";
    }
  } catch (e) { el.textContent = "✕ 요청 실패: " + e.message; el.style.color = "var(--danger)"; }
}
function copyHealthCurl() {
  const key = $("#hcKeySel").value || "<YOUR_API_KEY>";
  const base = (me.base_url || location.origin).replace(/\/$/, "");
  copyText(`curl -H "X-API-Key: ${key}" ${base}/healthz`);
}
// The currently-selected key owner: {id, name, role}. Empty id = self.
function selectedOwner() {
  const sel = $("#keyOwner");
  const id = (sel && !sel.classList.contains("hidden")) ? sel.value : "";
  if (id && usersById[id]) return { id, name: usersById[id].username, role: usersById[id].role };
  return { id: "", name: me.username, role: me.role };
}
// Recompute label-dup / per-user-limit state and scope options for the target owner.
function applyOwnerConstraints() {
  const owner = selectedOwner();
  const ownerIsEditor = owner.role === "owner" || owner.role === "admin" || owner.role === "user";
  const ownerKeys = keysCache.filter(k => k.owner === owner.name && !k.is_service);
  keyLabels = new Set(keysCache.filter(k => k.owner === owner.name && !k.revoked).map(k => k.label));
  myActiveKeys = ownerKeys.filter(k => !k.revoked).length;
  // A view-role owner can only hold download keys — hide upload/both options.
  $("#keyScope").querySelectorAll("option").forEach(o => {
    if (o.value !== "download") o.hidden = !ownerIsEditor;
  });
  if (!ownerIsEditor) $("#keyScope").value = "download";
  updateKeyBtn();
}
function onKeyOwnerChange() { applyOwnerConstraints(); }
// 서비스 키는 "사용자 명의"가 필요 없으므로 드롭다운을 숨긴다(개인 키는 관리자에게만 노출).
function syncKeyOwnerVisibility() {
  const sel = $("#keyOwner"); if (!sel) return;
  const isService = $("#keyKind") && $("#keyKind").value === "service";
  sel.classList.toggle("hidden", isService || !isManager());
}
function scopeLabel(s) {
  if (s === "upload") return '<span style="color:var(--warn)">Upload</span>';
  if (s === "all") return '<span style="color:var(--accent)">Both</span>';
  return '<span class="muted">Download</span>';
}
function updateKeyBtn() {
  syncKeyOwnerVisibility(); // 종류(개인/서비스)에 따라 사용자 명의 표시 토글
  const label = $("#keyLabel").value.trim();
  const isService = $("#keyKind").value === "service";
  const badChar = label && !/^[A-Za-z0-9_-]+$/.test(label);
  const dup = label && keyLabels.has(label);
  const full = !isService && myActiveKeys >= keyLimit; // 서비스 키는 개인 한도 무관
  let msg = "";
  if (badChar) msg = "영문/숫자/_/- 만 사용 가능";
  else if (full) msg = `개인 키는 최대 ${keyLimit}개`;
  else if (dup) msg = "중복되었습니다";
  $("#keyDupMsg").textContent = msg;
  $("#keyDupMsg").classList.toggle("hidden", !msg);
  $("#createKeyBtn").disabled = !label || dup || full || badChar;
}
async function createKey() {
  const label = $("#keyLabel").value.trim();
  if (!label) { toast("라벨을 입력하세요"); return; }
  const scope = $("#keyScope").value;
  const is_service = $("#keyKind").value === "service";
  const owner = selectedOwner();
  const expires_in_minutes = +($("#keyExpiry") ? $("#keyExpiry").value : 0) || 0;
  try {
    const k = await api("POST", "/api/keys", { label, scope, is_service, user_id: owner.id, expires_in_minutes });
    $("#keyLabel").value = "";
    toast(owner.id ? `키 생성됨 (${owner.name} 명의, 키: ${k.key})` : "키 생성됨 (키: " + k.key + ")");
    loadKeys();
  } catch (e) { toast("생성 실패: " + e.message); }
}
function copyText(t) { navigator.clipboard.writeText(t).then(() => toast("복사됨")); }
// Show only the first 4 and last 3 characters of a key.
function maskKey(k) {
  if (!k) return "";
  if (k.length <= 7) return k;
  return esc(k.slice(0, 4)) + "••••••••" + esc(k.slice(-3));
}
async function disableKey(id) {
  try { await api("POST", `/api/keys/${id}/disable`); toast("비활성화됨"); loadKeys(); }
  catch (e) { toast("실패: " + e.message); }
}
async function enableKey(id) {
  try { await api("POST", `/api/keys/${id}/enable`); toast("활성화됨"); loadKeys(); }
  catch (e) { toast("실패: " + e.message); }
}
async function revokeKey(id) {
  if (!await confirmDialog("이 키를 폐기하시겠습니까? 폐기 후 10일 뒤 완전 삭제됩니다.", { title: "API 키 폐기", okLabel: "폐기" })) return;
  try { await api("POST", `/api/keys/${id}/revoke`); toast("폐기됨"); loadKeys(); }
  catch (e) { toast("폐기 실패: " + e.message); }
}
async function forceDeleteKey(id) {
  if (!await confirmDialog("이 키를 즉시 완전 삭제하시겠습니까? 목록에서 제거됩니다.", { title: "키 강제 삭제", okLabel: "강제 삭제" })) return;
  try { await api("DELETE", `/api/keys/${id}`); toast("삭제됨"); loadKeys(); }
  catch (e) { toast("삭제 실패: " + e.message); }
}

