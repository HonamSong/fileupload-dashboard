// ---- trash ----
let selectedTrash = new Set();

async function loadTrash() {
  const files = await api("GET", "/api/trash");
  const b = $("#trashBody"); b.innerHTML = "";
  const ids = new Set((files || []).map(f => f.id));
  // drop selections for files that are no longer in the trash
  selectedTrash.forEach(id => { if (!ids.has(id)) selectedTrash.delete(id); });
  (files || []).forEach(f => {
    const tr = document.createElement("tr");
    const checked = selectedTrash.has(f.id) ? "checked" : "";
    tr.innerHTML = `
      <td><input type="checkbox" class="trash-cb" data-id="${f.id}" ${checked} onchange="toggleTrash('${f.id}', this.checked)"></td>
      <td class="name">${esc(f.name)}<br><span class="muted key">${esc(f.folder || "/")}</span></td>
      <td>${fmtBytes(f.size)}</td>
      <td class="muted">${esc(f.deleted_by || "-")}</td>
      <td class="muted">${fmtTime(f.deleted_at)}</td>
      <td class="muted">${fmtTime(f.purge_at)}</td>
      <td><div class="row-actions">
        <button class="btn ghost small" onclick="restore('${f.id}')">복구</button>
        <button class="btn danger small" onclick="forceDelete('${f.id}')">완전삭제</button>
      </div></td>`;
    b.appendChild(tr);
  });
  if (!files || !files.length) b.innerHTML = `<tr><td colspan="7" class="muted">휴지통이 비어 있습니다.</td></tr>`;
  setTrashBadge((files || []).length);
  updateTrashBulkUI();
}
function setTrashBadge(n) { $("#trashTab").textContent = n ? `휴지통 (${n})` : "휴지통"; }
async function updateTrashCount() {
  try { const files = await api("GET", "/api/trash"); setTrashBadge((files || []).length); } catch {}
}

function toggleTrash(id, on) {
  if (on) selectedTrash.add(id); else selectedTrash.delete(id);
  updateTrashBulkUI();
}
function toggleAllTrash(on) {
  document.querySelectorAll(".trash-cb").forEach(cb => {
    cb.checked = on;
    if (on) selectedTrash.add(cb.dataset.id); else selectedTrash.delete(cb.dataset.id);
  });
  updateTrashBulkUI();
}
function updateTrashBulkUI() {
  const n = selectedTrash.size;
  const rb = $("#trashRestoreBtn"), pb = $("#trashPurgeBtn");
  if (rb) { rb.textContent = `선택 복구 (${n})`; rb.disabled = n === 0; }
  if (pb) { pb.textContent = `선택 완전삭제 (${n})`; pb.disabled = n === 0; }
  const all = $("#trashSelAll");
  const boxes = document.querySelectorAll(".trash-cb");
  if (all) all.checked = boxes.length > 0 && n === boxes.length;
}

async function restore(id) {
  try { await api("POST", `/api/files/${id}/restore`); toast("복구됨"); loadTrash(); loadFiles(); }
  catch (e) { toast("복구 실패: " + e.message); }
}

async function restoreSelectedTrash() {
  const ids = [...selectedTrash];
  if (!ids.length) return;
  let ok = 0, fail = 0;
  for (const id of ids) {
    try { await api("POST", `/api/files/${id}/restore`); ok++; }
    catch { fail++; }
  }
  selectedTrash.clear();
  toast(fail ? `${ok}개 복구, ${fail}개 실패` : `${ok}개 복구됨`);
  loadTrash(); loadFiles();
}

async function purgeSelectedTrash() {
  const ids = [...selectedTrash];
  if (!ids.length) return;
  if (!await confirmDialog(`선택한 ${ids.length}개 파일을 완전 삭제하시겠습니까? 복구할 수 없습니다.`, { title: "완전 삭제", okLabel: "완전 삭제" })) return;
  let ok = 0, fail = 0;
  for (const id of ids) {
    try { await api("DELETE", `/api/files/${id}?force=true`); ok++; }
    catch { fail++; }
  }
  selectedTrash.clear();
  toast(fail ? `${ok}개 완전삭제, ${fail}개 실패` : `${ok}개 완전삭제됨`);
  loadTrash(); loadFiles();
}
