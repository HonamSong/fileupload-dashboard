const $ = s => document.querySelector(s);
let selectedId = null;
let filesById = {};
let currentFolder = "/";
let selectedFiles = new Set();
let keyLabels = new Set();

function toast(msg) {
  const t = $("#toast"); t.textContent = msg; t.classList.add("show");
  setTimeout(() => t.classList.remove("show"), 2200);
}

// Custom confirm dialog (replaces the browser's native confirm popup).
// Returns true (ok), false (cancel/esc), or extraValue when the extra button is used.
function confirmDialog(msg, { title = "확인", okLabel = "확인", danger = true, extraLabel = null, extraValue = "extra" } = {}) {
  return new Promise(resolve => {
    const modal = $("#modal"), ok = $("#modalOk"), cancel = $("#modalCancel"), extra = $("#modalExtra");
    $("#modalTitle").textContent = title;
    $("#modalMsg").textContent = msg;
    ok.textContent = okLabel;
    ok.className = "btn " + (danger ? "danger" : "");
    if (extraLabel) { extra.textContent = extraLabel; extra.style.display = ""; }
    else { extra.style.display = "none"; }
    modal.classList.remove("hidden");
    const close = result => {
      modal.classList.add("hidden");
      ok.onclick = cancel.onclick = extra.onclick = modal.onclick = null;
      document.removeEventListener("keydown", onKey);
      resolve(result);
    };
    const onKey = e => { if (e.key === "Escape") close(false); if (e.key === "Enter") close(true); };
    ok.onclick = () => close(true);
    cancel.onclick = () => close(false);
    extra.onclick = () => close(extraValue);
    modal.onclick = e => { if (e.target === modal) close(false); };
    document.addEventListener("keydown", onKey);
    ok.focus();
  });
}

// Custom prompt dialog. Returns the entered string, or null if cancelled.
function promptDialog(title, msg, { password = false, placeholder = "" } = {}) {
  return new Promise(resolve => {
    const modal = $("#promptModal"), ok = $("#promptOk"), cancel = $("#promptCancel"), input = $("#promptInput");
    $("#promptTitle").textContent = title;
    $("#promptMsg").textContent = msg || "";
    input.type = password ? "password" : "text";
    input.placeholder = placeholder;
    input.value = "";
    modal.classList.remove("hidden");
    const close = result => {
      modal.classList.add("hidden");
      ok.onclick = cancel.onclick = modal.onclick = input.onkeydown = null;
      resolve(result);
    };
    ok.onclick = () => close(input.value);
    cancel.onclick = () => close(null);
    modal.onclick = e => { if (e.target === modal) close(null); };
    input.onkeydown = e => { if (e.key === "Enter") close(input.value); else if (e.key === "Escape") close(null); };
    setTimeout(() => input.focus(), 0);
  });
}

function fmtBytes(n) {
  if (n < 1024) return n + " B";
  const u = ["KB","MB","GB","TB"]; let i = -1;
  do { n /= 1024; i++; } while (n >= 1024 && i < u.length - 1);
  return n.toFixed(1) + " " + u[i];
}
function fmtTime(s) { return s ? new Date(s).toLocaleString("ko-KR") : "-"; }
async function api(method, url, body, isForm) {
  const opt = { method, headers: {} };
  if (body && !isForm) { opt.headers["Content-Type"] = "application/json"; opt.body = JSON.stringify(body); }
  if (isForm) opt.body = body;
  const res = await fetch(url, opt);
  if (res.status === 401 && url !== "/api/login") showLogin(); // session expired → gate
  const txt = await res.text();
  let data = null; try { data = txt ? JSON.parse(txt) : null; } catch { data = txt; }
  if (!res.ok) throw new Error((data && data.error) || res.statusText);
  return data;
}

// ---- utils ----
function esc(s) { return (s ?? "").toString().replace(/[&<>"']/g, c => ({ "&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;" }[c])); }
function escAttr(s) { return esc(s).replace(/'/g, "\\'"); }
