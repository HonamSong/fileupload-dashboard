// ---- tabs ----
async function selectTab(name) {
  // Any tab switch leaves the Server settings view — guard unsaved changes.
  if (typeof guardLeaveServer === "function" && !(await guardLeaveServer(false))) return;
  document.querySelectorAll(".tabs button, .settingsbtn").forEach(x => x.classList.toggle("active", x.dataset.tab === name));
  ["files","trash","keys","settings"].forEach(t => $("#tab-"+t).classList.toggle("hidden", t !== name));
  if (name === "files") { loadFolders(); loadFiles(); }
  if (name === "trash") loadTrash();
  if (name === "keys") loadKeys();
  if (name === "settings") showSettings();
}
document.querySelectorAll(".tabs button").forEach(b => b.onclick = () => selectTab(b.dataset.tab));

// Warn on tab close / refresh / external navigation with unsaved server changes.
window.addEventListener("beforeunload", e => {
  if (typeof serverDirty !== "undefined" && serverDirty) { e.preventDefault(); e.returnValue = ""; }
});

// Draggable splitter between the files pane and the command/preview pane.
function initFilesGutter() {
  const g = $("#filesGutter"), main = $("#tab-files");
  if (!g || !main) return;
  // Width is stored as a percentage so the split scales with the window.
  let saved = localStorage.getItem("filesRightW");
  if (saved) {
    if (saved.endsWith("px")) {                      // migrate legacy fixed px -> %
      const w = main.getBoundingClientRect().width;
      saved = w ? ((parseFloat(saved) / w) * 100).toFixed(2) + "%" : null;
      if (saved) localStorage.setItem("filesRightW", saved);
    }
    if (saved) main.style.setProperty("--right-w", saved);
  }
  let dragging = false;
  g.addEventListener("mousedown", e => {
    dragging = true; g.classList.add("dragging");
    document.body.style.userSelect = "none"; document.body.style.cursor = "col-resize";
    e.preventDefault();
  });
  window.addEventListener("mousemove", e => {
    if (!dragging) return;
    const rect = main.getBoundingClientRect();
    let right = rect.right - e.clientX;               // right pane width from cursor to edge
    right = Math.max(320, Math.min(rect.width - 340, right));
    main.style.setProperty("--right-w", ((right / rect.width) * 100).toFixed(2) + "%");
  });
  window.addEventListener("mouseup", () => {
    if (!dragging) return;
    dragging = false; g.classList.remove("dragging");
    document.body.style.userSelect = ""; document.body.style.cursor = "";
    localStorage.setItem("filesRightW", main.style.getPropertyValue("--right-w").trim());
  });
  // Double-click resets to the default 50/50 split.
  g.addEventListener("dblclick", () => {
    main.style.removeProperty("--right-w");
    localStorage.removeItem("filesRightW");
  });
}
initFilesGutter();

// bootstrap
checkAuth();
