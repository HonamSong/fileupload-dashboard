// ---- folders ----
let collapsed = new Set(); // folder paths currently collapsed
let folderPerm = {};       // path -> effective level ("write"|"read"|"none")

function folderWritable(path) { return isAdmin() || folderPerm[path] === "write"; }
function folderReadable(path) { return isAdmin() || folderPerm[path] === "write" || folderPerm[path] === "read"; }

async function loadFolders() {
  const [paths, counts, access] = await Promise.all([
    api("GET", "/api/folders"),
    api("GET", "/api/folder-counts").catch(() => ({})),
    api("GET", "/api/folder-access").catch(() => ({})),
  ]);
  folderPerm = access || {};
  const all = (paths && paths.length ? paths : ["/"]);
  // Build parent -> children map.
  const children = {};
  all.forEach(p => {
    if (p === "/") return;
    const idx = p.lastIndexOf("/");
    const parent = idx === 0 ? "/" : p.substring(0, idx);
    (children[parent] = children[parent] || []).push(p);
  });
  Object.values(children).forEach(a => a.sort());

  const box = $("#folderTree"); box.innerHTML = "";
  const render = (path, depth) => {
    const kids = children[path] || [];
    const hasKids = kids.length > 0;
    const isCollapsed = collapsed.has(path);
    const label = path === "/" ? "/ (루트)" : path.split("/").pop();
    const locked = !folderReadable(path); // container-only ancestor (no access)
    const node = document.createElement("div");
    node.className = "foldernode" + (path === currentFolder ? " active" : "") + (locked ? " locked" : "");
    node.dataset.path = path;
    node.style.paddingLeft = (8 + depth * 16) + "px";
    node.onclick = () => setFolder(path);
    const toggle = hasKids
      ? `<span class="ftoggle" onclick="event.stopPropagation(); toggleFolder('${escAttr(path)}')">${isCollapsed ? "▶" : "▼"}</span>`
      : `<span class="ftoggle empty"></span>`;
    // admin manages per-user grants; a writable non-admin can delete a folder.
    const permBtn = isAdmin()
      ? `<span class="fperm" title="폴더 권한" onclick="event.stopPropagation(); openFolderPerms('${escAttr(path)}')">🔒</span>`
      : "";
    const del = (path !== "/" && folderWritable(path))
      ? `<span class="del" title="폴더 삭제" onclick="event.stopPropagation(); deleteFolder('${escAttr(path)}')">🗑</span>`
      : "";
    const cnt = counts[path] || 0;
    const badge = cnt ? ` <span class="muted" style="font-size:12px">(${cnt})</span>` : "";
    const lock = locked ? ' <span class="muted" title="접근 권한 없음">🔒</span>' : "";
    node.innerHTML = `${toggle}<span>📁 ${esc(label)}${badge}${lock}</span><span style="margin-left:auto;display:flex;gap:4px">${permBtn}${del}</span>`;
    box.appendChild(node);
    if (hasKids && !isCollapsed) kids.forEach(k => render(k, depth + 1));
  };
  render("/", 0);
  if (typeof updateFolderActions === "function") updateFolderActions();
}

function toggleFolder(path) {
  if (collapsed.has(path)) collapsed.delete(path); else collapsed.add(path);
  loadFolders();
}

function setFolder(path) {
  currentFolder = path;
  $("#curPathLabel").textContent = path;
  $("#uploadPath").textContent = path;
  document.querySelectorAll(".foldernode").forEach(n => n.classList.toggle("active", n.dataset.path === path));
  selectedId = null; clearPreviewPane(); showCommands(null);
  loadFiles();
}

// Fill a <select> with folder paths (optionally excluding one).
// writableOnly limits options to folders the user can write into.
function populateFolderSelect(sel, folders, exclude, writableOnly) {
  sel.innerHTML = "";
  (folders && folders.length ? folders : ["/"]).forEach(p => {
    if (exclude && p === exclude) return;
    if (writableOnly && !folderWritable(p)) return;
    const o = document.createElement("option");
    o.value = p; o.textContent = p === "/" ? "/ (루트)" : p;
    sel.appendChild(o);
  });
}

// ---- folder permissions (admin) ----
let fpFolder = null;
async function openFolderPerms(path) {
  fpFolder = path;
  $("#fpPath").textContent = path;
  const modal = $("#folderPermModal");
  modal.classList.remove("hidden");
  modal.onclick = e => { if (e.target === modal) closeFolderPerms(); };
  const box = $("#fpList"); box.innerHTML = '<div class="muted">불러오는 중…</div>';
  try {
    const res = await api("GET", "/api/folders/permissions?folder=" + encodeURIComponent(path));
    const lvl = {}; (res.grants || []).forEach(g => lvl[g.user_id] = g.level);
    const users = res.users || [];
    if (!users.length) {
      box.innerHTML = '<div class="muted">지정할 수 있는 사용자가 없습니다. (owner/admin 외 사용자를 먼저 추가하세요)</div>';
      return;
    }
    const opt = (v, label, sel) => `<option value="${v}" ${sel ? "selected" : ""}>${label}</option>`;
    box.innerHTML = users.map(u => {
      // Normalize the stored grant to this role's option set ("" = role default).
      let cur = lvl[u.id] || "";
      let opts;
      if (u.role === "view") { // default 차단; can grant 읽기
        if (cur === "none" || cur === "write") cur = cur === "write" ? "read" : "";
        opts = [opt("", "기본 (차단)", !cur), opt("read", "읽기", cur === "read")];
      } else { // user — default 읽기; can raise to 쓰기 or block
        if (cur === "read") cur = "";
        opts = [opt("", "기본 (읽기)", !cur), opt("write", "읽기+쓰기", cur === "write"), opt("none", "차단", cur === "none")];
      }
      return `<div class="fp-row">
        <span>${esc(u.username)} <span class="muted">(${u.role})</span></span>
        <select class="txt scope-select" onchange="setFolderPerm('${u.id}', this.value)">${opts.join("")}</select>
      </div>`;
    }).join("");
  } catch (e) { box.innerHTML = `<div class="muted">불러오기 실패: ${esc(e.message)}</div>`; }
}
async function setFolderPerm(userId, level) {
  try {
    await api("POST", "/api/folders/permissions", { folder: fpFolder, user_id: userId, level });
    toast("권한 저장됨");
    loadFolders(); // refresh counts/badges
  } catch (e) { toast("저장 실패: " + e.message); }
}
function closeFolderPerms() {
  const modal = $("#folderPermModal");
  modal.classList.add("hidden"); modal.onclick = null; fpFolder = null;
}

// Folder-create modal. defaultParent preselects the parent; onCreated(newPath) is
// called after success (defaults to navigating into the new folder).
async function openFolderModal(defaultParent, onCreated) {
  const paths = await api("GET", "/api/folders");
  const existing = new Set(paths && paths.length ? paths : ["/"]);
  const sel = $("#folderParent"), nameInput = $("#folderName"), preview = $("#folderPreview");
  const modal = $("#folderModal"), ok = $("#folderOk"), cancel = $("#folderCancel");
  populateFolderSelect(sel, paths, null, true); // parent must be writable
  sel.value = [...sel.options].some(o => o.value === defaultParent) ? defaultParent : (sel.options[0] ? sel.options[0].value : "/");
  nameInput.value = "";
  const joined = () => {
    const base = sel.value === "/" ? "" : sel.value;
    const n = nameInput.value.trim();
    return n ? base + "/" + n : "";
  };
  // Enable 생성 only for a non-empty, non-duplicate path.
  const validate = () => {
    const p = joined();
    const dup = !!p && existing.has(p);
    ok.disabled = !p || dup;
    preview.textContent = !p ? "" : dup ? "이미 존재하는 폴더입니다: " + p : "생성될 경로: " + p;
    preview.classList.toggle("dup", dup);
  };
  sel.onchange = validate; nameInput.oninput = validate; validate();

  modal.classList.remove("hidden");
  const close = () => { modal.classList.add("hidden"); ok.disabled = false; preview.classList.remove("dup"); ok.onclick = cancel.onclick = modal.onclick = nameInput.onkeydown = null; };
  const submit = async () => {
    const name = nameInput.value.trim();
    if (!name) { toast("폴더 이름을 입력하세요"); return; }
    const path = joined();
    if (existing.has(path)) { toast("이미 존재하는 폴더입니다"); return; }
    close();
    try {
      const r = await api("POST", "/api/folders", { path });
      toast("폴더 생성: " + r.path);
      await loadFolders();
      if (onCreated) onCreated(r.path); else setFolder(r.path);
    } catch (e) { toast("생성 실패: " + e.message); }
  };
  ok.onclick = submit;
  cancel.onclick = () => close();
  modal.onclick = e => { if (e.target === modal) close(); };
  nameInput.onkeydown = e => { if (e.key === "Enter") submit(); else if (e.key === "Escape") close(); };
  nameInput.focus();
}

async function deleteFolder(path) {
  if (!await confirmDialog(`폴더 "${path}" 를 삭제하시겠습니까?`, { title: "폴더 삭제", okLabel: "삭제" })) return;
  try {
    await api("DELETE", `/api/folders?path=${encodeURIComponent(path)}`);
    afterFolderDeleted(path, "폴더 삭제됨");
  } catch (e) {
    // 409 = 폴더가 비어 있지 않음. 관리자(admin+)면 강제 삭제 옵션을 제시한다.
    if (e.status === 409) {
      if (!isManager()) {
        toast(`"${path}" 폴더가 비어 있지 않습니다. 하위 파일을 먼저 삭제하거나 관리자에게 문의하세요.`);
        return;
      }
      const ok = await confirmDialog(
        `"${path}" 폴더 안에 파일 또는 하위 폴더가 있습니다.\n폴더와 하위 폴더는 삭제되고, 안의 파일들은 휴지통으로 이동되어 복구할 수 있습니다.\n계속하시겠습니까?`,
        { title: "폴더 삭제", okLabel: "삭제", danger: true });
      if (!ok) return;
      try {
        const r = await api("DELETE", `/api/folders?path=${encodeURIComponent(path)}&force=true`);
        afterFolderDeleted(path, `폴더 삭제됨 (파일 ${(r && r.trashed_files) || 0}개 휴지통으로 이동)`);
        if (typeof loadFiles === "function") loadFiles();
        if (typeof updateTrashCount === "function") updateTrashCount();
      } catch (e2) { toast("삭제 실패: " + e2.message); }
      return;
    }
    toast("삭제 실패: " + e.message);
  }
}

async function afterFolderDeleted(path, msg) {
  toast(msg);
  if (currentFolder === path || currentFolder.startsWith(path.replace(/\/?$/, "/"))) currentFolder = "/";
  await loadFolders();
  setFolder(currentFolder);
}

// Move the selected files to another folder (target chosen from a dropdown).
async function bulkMove() {
  const ids = [...selectedFiles];
  if (!ids.length) return;
  const sel = $("#moveTarget");
  const refill = async (selectPath) => {
    const paths = await api("GET", "/api/folders");
    populateFolderSelect(sel, paths, currentFolder, true); // writable targets only
    if (selectPath && [...sel.options].some(o => o.value === selectPath)) sel.value = selectPath;
  };
  await refill();
  $("#moveMsg").textContent = `선택한 ${ids.length}개 파일을 옮길 폴더를 선택하세요. (현재: ${currentFolder})`;
  const modal = $("#moveModal"), ok = $("#moveOk"), cancel = $("#moveCancel"), newBtn = $("#moveNewFolder");
  modal.classList.remove("hidden");
  const close = () => { modal.classList.add("hidden"); ok.onclick = cancel.onclick = modal.onclick = newBtn.onclick = null; };
  ok.onclick = async () => {
    if (!sel.value) { toast("대상 폴더를 선택하거나 새로 만드세요"); return; }
    const folder = sel.value; close();
    let okc = 0, fail = 0;
    for (const id of ids) {
      try { await api("POST", `/api/files/${id}/move`, { folder }); okc++; }
      catch { fail++; }
    }
    toast(`${okc}개 이동 → ${folder}${fail ? `, ${fail}개 실패(중복 등)` : ""}`);
    selectedId = null; clearPreviewPane(); showCommands(null);
    loadFiles(); loadFolders();
  };
  cancel.onclick = () => close();
  modal.onclick = e => { if (e.target === modal) close(); };
  // Create a new folder without leaving the move dialog; select it when done.
  newBtn.onclick = () => openFolderModal(sel.value || currentFolder, (newPath) => refill(newPath));
}

