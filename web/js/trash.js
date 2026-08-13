// ---- trash ----
async function loadTrash() {
  const files = await api("GET", "/api/trash");
  const b = $("#trashBody"); b.innerHTML = "";
  (files || []).forEach(f => {
    const tr = document.createElement("tr");
    tr.innerHTML = `
      <td class="name">${esc(f.name)}<br><span class="muted key">${esc(f.folder || "/")}</span></td>
      <td>${fmtBytes(f.size)}</td>
      <td class="muted">${fmtTime(f.deleted_at)}</td>
      <td class="muted">${fmtTime(f.purge_at)}</td>
      <td><div class="row-actions">
        <button class="btn ghost small" onclick="restore('${f.id}')">복구</button>
        <button class="btn danger small" onclick="forceDelete('${f.id}')">완전삭제</button>
      </div></td>`;
    b.appendChild(tr);
  });
  if (!files || !files.length) b.innerHTML = `<tr><td colspan="5" class="muted">휴지통이 비어 있습니다.</td></tr>`;
  setTrashBadge((files || []).length);
}
function setTrashBadge(n) { $("#trashTab").textContent = n ? `휴지통 (${n})` : "휴지통"; }
async function updateTrashCount() {
  try { const files = await api("GET", "/api/trash"); setTrashBadge((files || []).length); } catch {}
}
async function restore(id) {
  try { await api("POST", `/api/files/${id}/restore`); toast("복구됨"); loadTrash(); loadFiles(); }
  catch (e) { toast("복구 실패: " + e.message); }
}

