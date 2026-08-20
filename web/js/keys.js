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
  const b = $("#keysBody"); b.innerHTML = "";
  keys.forEach(k => {
    const tr = document.createElement("tr");
    let status, actions;
    if (k.revoked) {
      tr.className = "dimmed";
      status = k.purge_at
        ? `<span style="color:var(--danger)">폐기됨 - ${daysLeft(k.purge_at)}일 뒤 완전 삭제</span>`
        : `<span style="color:var(--danger)">폐기됨</span>`;
      actions = `<button class="btn danger small" onclick="forceDeleteKey('${k.id}')">강제 삭제</button>`;
    } else if (k.disabled) {
      status = `<span style="color:var(--warn)">비활성</span>`;
      actions = `<button class="btn ghost small" onclick="enableKey('${k.id}')">활성화</button>
                 <button class="btn danger small" onclick="revokeKey('${k.id}')">폐기</button>`;
    } else {
      status = `<span style="color:var(--ok)">활성</span>`;
      actions = `<button class="btn ghost small" onclick="disableKey('${k.id}')">비활성화</button>`;
    }
    const owner = k.is_service
      ? `<br><span class="muted key">🔧 서비스</span>`
      : (admin ? `<br><span class="muted key">👤 ${esc(k.owner || '-')}</span>` : '');
    tr.innerHTML = `
      <td class="name">${esc(k.label)}${owner}</td>
      <td>${scopeLabel(k.scope)}</td>
      <td class="key"><span class="muted">${maskKey(k.key)}</span>
        <button class="btn ghost small" style="margin-left:8px" onclick="copyText('${esc(k.key)}')">복사</button></td>
      <td class="muted">${fmtTime(k.created_at)}</td>
      <td class="muted">${fmtTime(k.last_used_at)}</td>
      <td><span class="pill">${k.use_count}</span></td>
      <td>${status}</td>
      <td><div class="row-actions">${actions}</div></td>`;
    b.appendChild(tr);
  });
  if (!keys.length) b.innerHTML = `<tr><td colspan="8" class="muted">발급된 키가 없습니다.</td></tr>`;
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
function scopeLabel(s) {
  if (s === "upload") return '<span style="color:var(--warn)">Upload</span>';
  if (s === "all") return '<span style="color:var(--accent)">Both</span>';
  return '<span class="muted">Download</span>';
}
function updateKeyBtn() {
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
  try {
    const k = await api("POST", "/api/keys", { label, scope, is_service, user_id: owner.id });
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

