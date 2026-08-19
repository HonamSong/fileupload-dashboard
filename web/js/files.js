// ---- upload (modal: pick files, review list, then upload) ----
let pendingUploads = [];

function openUploadModal() {
  pendingUploads = [];
  $("#uploadPath").textContent = currentFolder;
  $("#uploadCurl").textContent =
    `curl -H "X-API-Key: <YOUR_API_KEY>" -F "file=@./로컬파일" -F "folder=${currentFolder}" ${location.origin}/u`;
  renderUploadList();
  const modal = $("#uploadModal"), ok = $("#uploadOk"), cancel = $("#uploadCancel");
  const drop = $("#uploadDrop"), input = $("#uploadInput");
  modal.classList.remove("hidden");
  drop.onclick = () => input.click();
  drop.ondragover = e => { e.preventDefault(); drop.classList.add("hover"); };
  drop.ondragleave = () => drop.classList.remove("hover");
  drop.ondrop = e => { e.preventDefault(); drop.classList.remove("hover"); addPending(e.dataTransfer.files); };
  input.onchange = () => { addPending(input.files); input.value = ""; };
  const close = () => { modal.classList.add("hidden"); ok.onclick = cancel.onclick = modal.onclick = drop.onclick = input.onchange = null; };
  ok.onclick = async () => {
    if (!pendingUploads.length) { toast("업로드할 파일을 선택하세요"); return; }
    const lim = maxUploadBytes();
    const over = lim ? pendingUploads.filter(f => f.size > lim) : [];
    if (over.length) {
      toast(`파일당 최대 ${fmtBytes(lim)} 초과: ${over.map(f => f.name).join(", ")}`);
      return;
    }
    const files = pendingUploads.slice();
    close();
    await doUpload(files);
  };
  cancel.onclick = () => close();
  modal.onclick = e => { if (e.target === modal) close(); };
}

function addPending(fileList) {
  for (const f of fileList) pendingUploads.push(f);
  renderUploadList();
}

function removePending(idx) {
  pendingUploads.splice(idx, 1);
  renderUploadList();
}

function maxUploadBytes() { return (me && me.max_upload) || 0; }

function renderUploadList() {
  const box = $("#uploadList");
  const lim = maxUploadBytes();
  if (!pendingUploads.length) {
    box.innerHTML = `<div class="muted" style="padding:8px 2px">선택된 파일이 없습니다.</div>`;
  } else {
    box.innerHTML = pendingUploads.map((f, i) => {
      const over = lim && f.size > lim;
      return `<div class="uploaditem${over ? " over" : ""}"><span style="word-break:break-all">${esc(f.name)}
        <span class="muted">(${fmtBytes(f.size)})</span>${over ? ' <span class="overtag">한도 초과</span>' : ""}</span>
        <span class="del" title="제거" onclick="removePending(${i})">✕</span></div>`;
    }).join("");
  }
  // Summary: count + total size (+ per-file limit if configured).
  const summary = $("#uploadSummary");
  summary.classList.toggle("hidden", pendingUploads.length === 0);
  const total = pendingUploads.reduce((s, f) => s + f.size, 0);
  $("#uploadCount").textContent = pendingUploads.length;
  $("#uploadTotal").textContent = fmtBytes(total);
  $("#uploadLimit").textContent = lim ? `파일당 최대 ${fmtBytes(lim)}` : "용량 제한 없음";
}

async function doUpload(files) {
  const folder = currentFolder;
  let ok = 0, fail = 0;
  for (const f of files) {
    const fd = new FormData(); fd.append("file", f); fd.append("folder", folder);
    try { await api("POST", "/api/files", fd, true); ok++; }
    catch { fail++; }
  }
  toast(`${ok}개 업로드${fail ? `, ${fail}개 실패` : ""}`);
  loadFiles();
}


// ---- command key selection ----
let keysCache = [];
async function refreshKeysCache() {
  try { const res = await api("GET", "/api/keys"); keysCache = res.keys || []; } catch { keysCache = []; }
}
// Keys the current user may use for download/upload (own keys + service keys).
function usableKeys(need) {
  return keysCache.filter(k => !k.revoked && !k.disabled
    && (k.owner === me.username || k.is_service)
    && (need === "upload" ? (k.scope === "upload" || k.scope === "all")
                          : (k.scope === "download" || k.scope === "all")));
}
function fillKeySelect(id, need) {
  const sel = $("#" + id); if (!sel) return;
  const prev = sel.value;
  const keys = usableKeys(need);
  sel.innerHTML = keys.length
    ? keys.map(k => `<option value="${k.id}">${esc(k.label)} · ${maskKey(k.key)}</option>`).join("")
    : `<option value="">사용 가능한 키 없음</option>`;
  if (prev && keys.some(k => k.id === prev)) sel.value = prev;
}
function realKeyOf(id) { const k = keysCache.find(x => x.id === $("#" + id).value); return k ? k.key : "<YOUR_API_KEY>"; }
function maskedKeyOf(id) { const k = keysCache.find(x => x.id === $("#" + id).value); return k ? maskKey(k.key) : "<YOUR_API_KEY>"; }

// Base URL for commands: admin-configured (설정>Server) or the current origin.
function serverBase() {
  return (me.base_url && me.base_url.trim()) || location.origin;
}
// Build /d/<id> and /f/<folder>/<name> URLs client-side so they honor serverBase().
function dlUrl(f) { return serverBase() + "/d/" + f.id; }
function pathUrl(f) {
  const segs = (f.folder || "/").split("/").filter(Boolean).map(encodeURIComponent);
  segs.push(encodeURIComponent(f.name));
  return serverBase() + "/f/" + segs.join("/");
}
function uploadCmdStr(keyStr) {
  return `curl -H "X-API-Key: ${keyStr}" -F "file=@./로컬파일" -F "folder=${currentFolder}" ${serverBase()}/u`;
}

// ---- files ----
function updateUploadCmd() {
  $("#cmdUploadPath").textContent = currentFolder;
  fillKeySelect("uploadKeySel", "upload");
  $("#cmdUpload").textContent = uploadCmdStr(maskedKeyOf("uploadKeySel"));
}
function copyUploadCmd() { copyText(uploadCmdStr(realKeyOf("uploadKeySel"))); }
// Disable write actions / hide upload command when the user can't write here.
function updateFolderActions() {
  const canWrite = (typeof folderWritable === "function") ? folderWritable(currentFolder) : true;
  const upBtn = $("#uploadBtn"); if (upBtn) upBtn.disabled = !canWrite;
  const uw = $("#cmdUploadWrap"); if (uw) uw.style.display = canWrite ? "" : "none";
}
async function loadFiles() {
  updateUploadCmd();
  updateFolderActions();
  let files;
  try {
    files = await api("GET", "/api/files?folder=" + encodeURIComponent(currentFolder));
  } catch (e) {
    // No read access to this folder (container-only or ungranted).
    filesById = {}; selectedFiles.clear();
    $("#filesBody").innerHTML = `<tr><td colspan="7" class="muted">이 폴더에 접근 권한이 없습니다.</td></tr>`;
    selectedId = null; clearPreviewPane(); showCommands(null); updateBulkUI();
    $("#filesUpdated").textContent = "";
    return;
  }
  filesById = {};
  selectedFiles.clear();
  allFiles = files || [];
  allFiles.forEach(f => { filesById[f.id] = f; });
  filePage = 1;
  renderFilePage();
  // Hide the select-all checkbox for view role.
  $("#selectAll").style.visibility = canEdit() ? "visible" : "hidden";
  $("#filesUpdated").textContent = "Updated " + new Date().toLocaleTimeString("en-US");
}

// ---- file list pagination (client-side) ----
let allFiles = [];
let filePage = 1;
let filePageSize = +(localStorage.getItem("filePageSize") || 10);

function renderFilePage() {
  const b = $("#filesBody"); if (!b) return;
  const total = allFiles.length;
  const pages = Math.max(1, Math.ceil(total / filePageSize));
  if (filePage > pages) filePage = pages;
  const start = (filePage - 1) * filePageSize;
  const pageFiles = allFiles.slice(start, start + filePageSize);
  b.innerHTML = "";
  pageFiles.forEach(f => {
    const tr = document.createElement("tr");
    tr.dataset.id = f.id;
    if (f.id === selectedId) tr.classList.add("sel");
    tr.onclick = () => selectFile(f.id);
    const checkCell = canEdit()
      ? `<td><input type="checkbox" class="rowcheck" data-id="${f.id}" ${selectedFiles.has(f.id) ? "checked" : ""} onclick="event.stopPropagation(); toggleSelect('${f.id}', this.checked)"></td>`
      : `<td></td>`;
    const previewBtn = canEdit()
      ? `<button class="btn ghost small" onclick="event.stopPropagation(); showPreview('${f.id}','${escAttr(f.name)}')">미리보기</button>`
      : "";
    tr.innerHTML = `
      ${checkCell}
      <td class="name">${esc(f.name)}<br><span class="muted key">${f.id}</span></td>
      <td>${fmtBytes(f.size)}</td>
      <td class="key">${f.checksum
        ? `<span class="muted" title="${f.checksum}">${f.checksum.slice(0, 12)}…</span>
           <button class="btn ghost small" style="margin-left:6px" onclick="event.stopPropagation(); copyText('${f.checksum}')">복사</button>`
        : '<span class="muted">-</span>'}</td>
      <td class="muted">${fmtTime(f.uploaded_at)}</td>
      <td><span class="pill">${f.download_count}회</span></td>
      <td><div class="row-actions">${previewBtn}</div></td>`;
    b.appendChild(tr);
  });
  if (!total) b.innerHTML = `<tr><td colspan="7" class="muted">파일이 없습니다.</td></tr>`;
  renderFilePager(total, pages, start, pageFiles.length);
  updateBulkUI();
}

function renderFilePager(total, pages, start, shown) {
  const el = $("#filePager"); if (!el) return;
  const from = total ? start + 1 : 0, to = start + shown;
  el.innerHTML = `
    <div class="pg-left">
      <span class="muted">페이지당</span>
      <select class="txt" onchange="setFilePageSize(this.value)">
        ${[10, 20, 30, 50, 100].map(n => `<option value="${n}" ${n === filePageSize ? "selected" : ""}>${n}</option>`).join("")}
      </select>
    </div>
    <div class="pg-center">
      <button class="btn ghost small" ${filePage <= 1 ? "disabled" : ""} onclick="gotoFilePage(${filePage - 1})">‹ 이전</button>
      <span class="muted">${filePage} / ${pages}</span>
      <button class="btn ghost small" ${filePage >= pages ? "disabled" : ""} onclick="gotoFilePage(${filePage + 1})">다음 ›</button>
    </div>
    <div class="pg-right">
      <span class="muted">${from}–${to} / ${total}개</span>
    </div>`;
}
function setFilePageSize(v) { filePageSize = +v; filePage = 1; localStorage.setItem("filePageSize", filePageSize); renderFilePage(); }
function gotoFilePage(p) { filePage = p; renderFilePage(); }

// ---- multi-select ----
function toggleSelect(id, checked) {
  if (checked) selectedFiles.add(id); else selectedFiles.delete(id);
  updateBulkUI();
}

function toggleSelectAll() {
  const on = $("#selectAll").checked;
  selectedFiles.clear();
  document.querySelectorAll("#filesBody .rowcheck").forEach(cb => {
    cb.checked = on;
    if (on) selectedFiles.add(cb.dataset.id);
  });
  updateBulkUI();
}

function updateBulkUI() {
  const n = selectedFiles.size;
  $("#selCount").textContent = n;
  $("#selCountMove").textContent = n;
  $("#bulkDeleteBtn").disabled = n === 0;
  $("#bulkMoveBtn").disabled = n === 0;
  const boxes = [...document.querySelectorAll("#filesBody .rowcheck")];
  const all = $("#selectAll");
  if (all) all.checked = boxes.length > 0 && boxes.every(cb => cb.checked);
  updateDownloadBtn();
}

// Download button enables when a row is selected OR checkboxes are ticked.
function updateDownloadBtn() {
  const btn = $("#downloadBtn");
  if (btn) btn.disabled = selectedFiles.size === 0 && !selectedId;
}

async function bulkDelete() {
  const ids = [...selectedFiles];
  if (!ids.length) return;
  const r = await confirmDialog(`선택한 ${ids.length}개 파일을 휴지통으로 이동합니다. '강제 삭제'는 즉시 완전 삭제되어 복구할 수 없습니다.`,
    { title: "선택 삭제", okLabel: "삭제", danger: false, extraLabel: "강제 삭제", extraValue: "force" });
  if (!r) return;
  const force = r === "force";
  let ok = 0, fail = 0;
  for (const id of ids) {
    try { await api("DELETE", `/api/files/${id}${force ? "?force=true" : ""}`); ok++; }
    catch { fail++; }
  }
  toast(`${ok}개 ${force ? "완전 삭제" : "휴지통 이동"}${fail ? `, ${fail}개 실패` : ""}`);
  if (selectedId && ids.includes(selectedId)) { selectedId = null; clearPreviewPane(); showCommands(null); }
  loadFiles(); loadTrash();
}

function clearPreviewPane() {
  $("#previewName").textContent = "";
  $("#previewPane").innerHTML = `<span class="muted">파일의 "미리보기"를 누르면 내용이 표시됩니다.</span>`;
}

let cmdNameMode = "uuid"; // "uuid" (default) or "filename" — how the download URL is shown
let cmdFile = null;       // file currently shown in the 명령어 panel

// Build download/exec commands. uuid mode uses /d/<id> with -O -J; filename mode
// uses /f/<folder>/<name> so the URL is readable and curl -O saves the name.
function buildCommands(f, keyStr) {
  const isSh = (f.name || "").toLowerCase().endsWith(".sh");
  if (cmdNameMode === "filename") {
    const url = pathUrl(f); // http://host/f/<folder>/<name>
    return {
      dl: `curl -H "X-API-Key: ${keyStr}" -O ${url}`,
      exec: isSh ? `curl -sH "X-API-Key: ${keyStr}" ${url} | bash` : null,
    };
  }
  const base = dlUrl(f); // http://host/d/<id>
  return {
    dl: `curl -H "X-API-Key: ${keyStr}" -O -J ${base}`,
    exec: isSh ? `curl -sH "X-API-Key: ${keyStr}" ${base} | bash` : null,
  };
}

// Trigger a browser download from a URL (optionally with an object-URL blob).
function triggerDownload(href, filename, revoke) {
  const a = document.createElement("a");
  a.href = href; a.download = filename || "";
  document.body.appendChild(a); a.click(); a.remove();
  if (revoke) setTimeout(() => URL.revokeObjectURL(href), 1000);
}

// Download selected file(s) to the local PC (cookie-authed, no API key needed).
// Checked files win; otherwise the row-selected file.
// 1 file → original file; 2+ files → a single .zip archive.
async function downloadSelected() {
  const ids = selectedFiles.size ? [...selectedFiles] : (selectedId ? [selectedId] : []);
  if (!ids.length) return;
  if (ids.length === 1) {
    triggerDownload("/api/files/" + ids[0] + "/download");
    return;
  }
  try {
    toast(ids.length + "개 파일 압축 중…");
    const res = await fetch("/api/files/download-zip", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ids }),
    });
    if (!res.ok) throw new Error(res.status === 404 ? "파일을 찾을 수 없음" : "HTTP " + res.status);
    const blob = await res.blob();
    const cd = res.headers.get("Content-Disposition") || "";
    const m = cd.match(/filename="?([^"]+)"?/);
    triggerDownload(URL.createObjectURL(blob), m ? m[1] : "files.zip", true);
    toast(ids.length + "개 파일 다운로드 완료");
  } catch (e) { toast("압축 다운로드 실패: " + e.message); }
}

// Show download/exec commands for a file (null = show empty state).
function showCommands(f) {
  cmdFile = f;
  $("#cmdName").textContent = f ? f.name : "";
  updateDownloadBtn();
  if (!f) {
    $("#cmdEmpty").classList.remove("hidden");
    $("#curlWrap").classList.add("hidden");
    $("#execWrap").classList.add("hidden");
    return;
  }
  $("#cmdEmpty").classList.add("hidden");
  fillKeySelect("dlKeySel", "download");
  const { dl, exec } = buildCommands(f, maskedKeyOf("dlKeySel"));
  $("#curlText").textContent = dl; $("#curlWrap").classList.remove("hidden");
  if (exec) { $("#execText").textContent = exec; $("#execWrap").classList.remove("hidden"); }
  else { $("#execWrap").classList.add("hidden"); }
}

function setCmdMode(mode) {
  cmdNameMode = mode;
  if (cmdFile) showCommands(cmdFile);
}

// Selecting a file: show its commands immediately, blank the preview.
function selectFile(id) {
  selectedId = id;
  document.querySelectorAll("#filesBody tr").forEach(r => r.classList.toggle("sel", r.dataset.id === id));
  clearPreviewPane();
  showCommands(filesById[id]);
}

// Preview button: select, then load and render the file content.
async function showPreview(id, name) {
  selectFile(id);
  $("#previewName").textContent = name;
  const pane = $("#previewPane");
  pane.innerHTML = `<span class="muted">불러오는 중…</span>`;
  try {
    const res = await fetch(`/api/files/${id}/preview`);
    const kind = res.headers.get("X-Preview-Kind");
    if (kind === "image") {
      const blob = await res.blob();
      pane.innerHTML = ""; const img = document.createElement("img");
      img.src = URL.createObjectURL(blob); pane.appendChild(img);
    } else {
      const text = await res.text();
      pane.innerHTML = `<pre></pre>`;
      const pre = pane.querySelector("pre");
      const hl = (typeof highlightCode === "function") ? highlightCode(text, name) : null;
      if (hl !== null) pre.innerHTML = hl; else pre.textContent = text; // fallback: plain
    }
  } catch (e) { pane.innerHTML = `<span class="muted">미리보기 실패: ${esc(e.message)}</span>`; }
}

function copyCurl() {
  if (!cmdFile) return;
  copyText(buildCommands(cmdFile, realKeyOf("dlKeySel")).dl);
}
function copyExec() {
  if (!cmdFile) return;
  const { exec } = buildCommands(cmdFile, realKeyOf("dlKeySel"));
  if (exec) copyText(exec);
}

// Files list: 삭제 → 휴지통(기본) 또는 강제 삭제(즉시 완전삭제) 선택.
async function del(id) {
  const r = await confirmDialog("휴지통으로 이동합니다. '강제 삭제'는 즉시 완전 삭제되어 복구할 수 없습니다.",
    { title: "삭제", okLabel: "삭제", danger: false, extraLabel: "강제 삭제", extraValue: "force" });
  if (!r) return;
  await doDelete(id, r === "force");
}

// Trash: 완전삭제(강제) 확인.
async function forceDelete(id) {
  if (!await confirmDialog("완전 삭제하시겠습니까? 복구할 수 없습니다.", { title: "완전 삭제", okLabel: "완전 삭제" })) return;
  await doDelete(id, true);
}

async function doDelete(id, force) {
  try {
    await api("DELETE", `/api/files/${id}${force ? "?force=true" : ""}`);
    toast(force ? "완전 삭제됨" : "휴지통으로 이동");
    if (selectedId === id) { selectedId = null; clearPreviewPane(); showCommands(null); }
    loadFiles(); loadTrash();
  } catch (e) { toast("삭제 실패: " + e.message); }
}

