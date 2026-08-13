// ---- auth / settings ----
let me = { username: "", role: "view" };
const isOwner = () => me.role === "owner";
const isManager = () => me.role === "owner" || me.role === "admin"; // owner or admin
const isAdmin = isManager; // back-compat alias (manager-level gate)
const canEdit = () => me.role === "owner" || me.role === "admin" || me.role === "user";

async function checkAuth() {
  try {
    const m = await fetch("/api/me").then(r => r.json());
    if (m.authenticated) { me = m; showApp(); } else showLogin();
  } catch { showLogin(); }
}
function showLogin() {
  $("#loginOverlay").classList.remove("hidden");
  $("#loginErr").classList.add("hidden");
  setTimeout(() => $("#loginUser").focus(), 0);
}
function showApp() {
  $("#loginOverlay").classList.add("hidden");
  $("#loginPw").value = "";
  $("#sidebarUser").textContent = me.username + " (" + me.role + ")";
  // Editor-only controls (view role hides these).
  document.querySelectorAll(".editor-only").forEach(el => el.classList.toggle("hidden", !canEdit()));
  updateTrashCount();
  refreshKeysCache().then(() => selectTab("files"));
}
async function doLogin() {
  try {
    me = await api("POST", "/api/login", { username: $("#loginUser").value, password: $("#loginPw").value });
    showApp();
  } catch (e) {
    $("#loginErr").textContent = e.message; $("#loginErr").classList.remove("hidden");
  }
}
async function doLogout() {
  if (typeof guardLeaveServer === "function" && !(await guardLeaveServer(false))) return;
  try { await api("POST", "/api/logout"); } catch {}
  showLogin();
}
