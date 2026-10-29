// Tijori admin panel. Plain JavaScript, no build step. The page is served
// with a strict CSP, so there are no inline scripts, styles or handlers.
"use strict";

const BASE = document.currentScript.dataset.base;
const API = BASE + "/api";
const state = { csrf: "", user: "", role: "", buckets: [] };

// ---- i18n -------------------------------------------------------------------

const STRINGS = {
  en: {
    dashboard: "Dashboard", buckets: "Buckets", files: "Files", keys: "Keys", backup: "Backup", settings: "Settings",
    login: "Log in", logout: "Log out", username: "User name", password: "Password",
    totalUsed: "Storage used", objects: "Files", bucketsCount: "Buckets", keysCount: "Access keys",
    usageByBucket: "Usage by bucket", server: "Server", version: "Version", uptime: "Uptime", endpoint: "S3 endpoint",
    video: "Video processing", imageCache: "Image cache", lastBackup: "Last backup", never: "Never", freeDisk: "Free disk",
    newBucket: "New bucket", name: "Name", public: "Public", quota: "Quota", maxFile: "Max file size", created: "Created",
    browse: "Browse", edit: "Edit", delete: "Delete", save: "Save", cancel: "Cancel", create: "Create", close: "Close",
    publicHint: "Anyone with the link can download files. Uploads always need a key.",
    videoHint: "Make a thumbnail and a 720p copy of every uploaded video (needs ffmpeg).",
    managed: "Settings come from tijori.toml", noBuckets: "No buckets yet. Create one to start storing files.",
    upload: "Upload", newFolder: "New folder", search: "Search this folder", dropHere: "Drop files here to upload, or",
    chooseFiles: "choose files", emptyFolder: "This folder is empty.", download: "Download", copyUrl: "Copy public URL",
    shareLink: "Share link", expiresIn: "Link works for", copy: "Copy", copied: "Copied", generate: "Create link",
    confirmDelete: "Delete {name}? This cannot be undone.", confirmFolder: "Delete the folder {name} and everything in it?",
    newKey: "New key", permission: "Permission", allBuckets: "All buckets", revoke: "Revoke",
    confirmRevoke: "Revoke {name}? Apps using it will stop working.", secretOnce: "Copy the secret now. It is shown only once.",
    accessKey: "Access key", secretKey: "Secret key", runBackup: "Run backup now", running: "Running…",
    notConfigured: "No backup destination is set.", destination: "Destination", schedule: "Schedule",
    changePassword: "Change password", currentPassword: "Current password", newPassword: "New password",
    theme: "Theme", language: "Language", auto: "Auto", light: "Light", dark: "Dark", config: "Effective configuration",
    readonly: "read-only", available: "available", notFound: "not installed", ok: "OK", failed: "failed",
    perm_readwrite: "Read and write", perm_read: "Read only", perm_write: "Write only",
  },
  hi: {
    dashboard: "डैशबोर्ड", buckets: "बकेट", files: "फ़ाइलें", keys: "कुंजियाँ", backup: "बैकअप", settings: "सेटिंग्स",
    login: "लॉग इन", logout: "लॉग आउट", username: "यूज़र नाम", password: "पासवर्ड",
    totalUsed: "इस्तेमाल हुई जगह", objects: "फ़ाइलें", bucketsCount: "बकेट", keysCount: "एक्सेस कुंजियाँ",
    usageByBucket: "बकेट के अनुसार उपयोग", server: "सर्वर", version: "संस्करण", uptime: "चालू समय", endpoint: "S3 पता",
    video: "वीडियो प्रोसेसिंग", imageCache: "इमेज कैश", lastBackup: "पिछला बैकअप", never: "कभी नहीं", freeDisk: "खाली डिस्क",
    newBucket: "नया बकेट", name: "नाम", public: "सार्वजनिक", quota: "सीमा", maxFile: "अधिकतम फ़ाइल आकार", created: "बनाया गया",
    browse: "खोलें", edit: "बदलें", delete: "मिटाएँ", save: "सहेजें", cancel: "रद्द करें", create: "बनाएँ", close: "बंद करें",
    publicHint: "लिंक वाला कोई भी फ़ाइल डाउनलोड कर सकता है। अपलोड के लिए हमेशा कुंजी चाहिए।",
    videoHint: "हर वीडियो का थंबनेल और 720p कॉपी बनाएँ (ffmpeg ज़रूरी)।",
    managed: "सेटिंग्स tijori.toml से आती हैं", noBuckets: "अभी कोई बकेट नहीं है। फ़ाइलें रखने के लिए एक बनाएँ।",
    upload: "अपलोड", newFolder: "नया फ़ोल्डर", search: "इस फ़ोल्डर में खोजें", dropHere: "अपलोड के लिए फ़ाइलें यहाँ छोड़ें, या",
    chooseFiles: "फ़ाइलें चुनें", emptyFolder: "यह फ़ोल्डर खाली है।", download: "डाउनलोड", copyUrl: "सार्वजनिक लिंक कॉपी करें",
    shareLink: "शेयर लिंक", expiresIn: "लिंक कितने समय चलेगा", copy: "कॉपी", copied: "कॉपी हो गया", generate: "लिंक बनाएँ",
    confirmDelete: "{name} मिटाएँ? यह वापस नहीं होगा।", confirmFolder: "फ़ोल्डर {name} और उसकी सारी फ़ाइलें मिटाएँ?",
    newKey: "नई कुंजी", permission: "अनुमति", allBuckets: "सभी बकेट", revoke: "रद्द करें",
    confirmRevoke: "{name} रद्द करें? इसका उपयोग करने वाले ऐप बंद हो जाएँगे।", secretOnce: "सीक्रेट अभी कॉपी करें। यह सिर्फ़ एक बार दिखेगा।",
    accessKey: "एक्सेस कुंजी", secretKey: "सीक्रेट कुंजी", runBackup: "अभी बैकअप लें", running: "चल रहा है…",
    notConfigured: "बैकअप की जगह सेट नहीं है।", destination: "जगह", schedule: "समय-सारणी",
    changePassword: "पासवर्ड बदलें", currentPassword: "मौजूदा पासवर्ड", newPassword: "नया पासवर्ड",
    theme: "थीम", language: "भाषा", auto: "अपने-आप", light: "हल्का", dark: "गहरा", config: "लागू कॉन्फ़िगरेशन",
    readonly: "केवल पढ़ने के लिए", available: "उपलब्ध", notFound: "इंस्टॉल नहीं", ok: "ठीक", failed: "असफल",
    perm_readwrite: "पढ़ना और लिखना", perm_read: "केवल पढ़ना", perm_write: "केवल लिखना",
  },
};

function store(key, value) {
  try { if (value === undefined) return localStorage.getItem(key); localStorage.setItem(key, value); } catch (e) { return null; }
}
let lang = store("tijori.lang") || (navigator.language || "en").slice(0, 2);
if (!STRINGS[lang]) lang = "en";
function t(key, vars) {
  let s = (STRINGS[lang] && STRINGS[lang][key]) || STRINGS.en[key] || key;
  for (const k in vars || {}) s = s.replace("{" + k + "}", vars[k]);
  return s;
}
function applyTheme() {
  const th = store("tijori.theme") || "auto";
  if (th === "auto") document.documentElement.removeAttribute("data-theme");
  else document.documentElement.setAttribute("data-theme", th);
}
applyTheme();
document.documentElement.lang = lang;

// ---- helpers ----------------------------------------------------------------

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));
function esc(v) {
  return String(v ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}
function bytes(n) {
  if (n == null || n < 0) return "–";
  const u = ["B", "KB", "MB", "GB", "TB", "PB"];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (i === 0 ? n : n.toFixed(n < 10 ? 1 : 0)) + " " + u[i];
}
function duration(sec) {
  const d = Math.floor(sec / 86400), h = Math.floor((sec % 86400) / 3600), m = Math.floor((sec % 3600) / 60);
  return d ? `${d}d ${h}h` : h ? `${h}h ${m}m` : `${m}m`;
}
function when(iso) {
  if (!iso) return t("never");
  const d = new Date(iso);
  return isNaN(d) || d.getFullYear() < 2000 ? t("never") : d.toLocaleString();
}
function pct(used, max) { return max > 0 ? Math.min(100, (100 * used) / max) : 0; }
function setBars(root) {
  $$("[data-pct]", root).forEach((el) => {
    const p = parseFloat(el.dataset.pct);
    el.parentElement.classList.toggle("warn", p >= 80 && p < 95);
    el.parentElement.classList.toggle("full", p >= 95);
    requestAnimationFrame(() => (el.style.width = p + "%"));
  });
}
function objURL(bucket, key, extra = "") {
  return `${API}/file?bucket=${encodeURIComponent(bucket)}&key=${encodeURIComponent(key)}${extra}`;
}

const ICONS = {
  dashboard: '<path d="M3 13h8V3H3zm10 8h8V11h-8zM3 21h8v-6H3zm10-18v6h8V3z"/>',
  buckets: '<path d="M4 7h16l-1.5 13h-13zM3 4h18v3H3z"/>',
  files: '<path d="M3 6a2 2 0 0 1 2-2h5l2 2h7a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>',
  keys: '<path d="M14 3a7 7 0 0 0-6.7 9L2 17.3V22h4.7l1-1v-2h2v-2h2l1.3-1.3A7 7 0 1 0 14 3m2.5 3A1.5 1.5 0 1 1 15 7.5 1.5 1.5 0 0 1 16.5 6"/>',
  backup: '<path d="M12 3a9 9 0 1 0 9 9h-2a7 7 0 1 1-2.05-4.95L14 10h7V3l-2.64 2.64A8.96 8.96 0 0 0 12 3m-1 5v5l4.25 2.52.77-1.28-3.52-2.09V8z"/>',
  settings: '<path d="M19.4 13a7.6 7.6 0 0 0 0-2l2.1-1.6-2-3.5-2.5 1a7.4 7.4 0 0 0-1.7-1L15 3h-4l-.4 2.9a7.4 7.4 0 0 0-1.7 1l-2.5-1-2 3.5L6.6 11a7.6 7.6 0 0 0 0 2l-2.1 1.6 2 3.5 2.5-1a7.4 7.4 0 0 0 1.7 1L11 21h4l.4-2.9a7.4 7.4 0 0 0 1.7-1l2.5 1 2-3.5zM13 15.5a3.5 3.5 0 1 1 3.5-3.5 3.5 3.5 0 0 1-3.5 3.5"/>',
  folder: '<path d="M3 6a2 2 0 0 1 2-2h5l2 2h7a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>',
  file: '<path d="M6 2h8l6 6v12a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2m7 1.5V9h5.5z"/>',
  video: '<path d="M4 5h11a2 2 0 0 1 2 2v2.5l4-3v11l-4-3V17a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V7a2 2 0 0 1 2-2"/>',
  more: '<path d="M12 8a2 2 0 1 0-2-2 2 2 0 0 0 2 2m0 2a2 2 0 1 0 2 2 2 2 0 0 0-2-2m0 6a2 2 0 1 0 2 2 2 2 0 0 0-2-2"/>',
};
const icon = (n) => `<svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">${ICONS[n] || ICONS.file}</svg>`;

// ---- API --------------------------------------------------------------------

class ApiError extends Error { constructor(status, msg) { super(msg); this.status = status; } }

async function api(method, path, body) {
  const opts = { method, headers: {}, credentials: "same-origin" };
  if (method !== "GET") opts.headers["X-CSRF-Token"] = state.csrf;
  if (body !== undefined) { opts.headers["Content-Type"] = "application/json"; opts.body = JSON.stringify(body); }
  const res = await fetch(API + path, opts);
  let data = null;
  try { data = await res.json(); } catch (e) { /* empty body */ }
  if (res.status === 401 && path !== "/login") { showLogin(); throw new ApiError(401, "please log in"); }
  if (!res.ok) throw new ApiError(res.status, (data && data.error) || res.statusText);
  return data;
}

function toast(msg, isError) {
  const el = document.createElement("div");
  el.className = "toast" + (isError ? " error" : "");
  el.textContent = msg;
  $("#toasts").appendChild(el);
  setTimeout(() => el.remove(), isError ? 6000 : 3000);
}
const fail = (e) => { if (e.status !== 401) toast(e.message, true); };

async function copyText(text) {
  try { await navigator.clipboard.writeText(text); toast(t("copied")); }
  catch (e) { window.prompt(t("copy"), text); }
}

// ---- modal ------------------------------------------------------------------

function modal(html, { wide = false, onMount } = {}) {
  return new Promise((resolve) => {
    const bd = document.createElement("div");
    bd.className = "backdrop";
    bd.innerHTML = `<div class="modal${wide ? " wide" : ""}" role="dialog" aria-modal="true">${html}</div>`;
    const done = (v) => { bd.remove(); document.removeEventListener("keydown", onKey); resolve(v); };
    const onKey = (e) => { if (e.key === "Escape") done(null); };
    document.addEventListener("keydown", onKey);
    bd.addEventListener("click", (e) => {
      if (e.target === bd) return done(null);
      const b = e.target.closest("[data-close]");
      if (b) done(b.dataset.close === "ok" ? true : null);
    });
    document.body.appendChild(bd);
    const first = $("input, select, textarea, button.primary", bd);
    if (first) first.focus();
    if (onMount) onMount(bd.firstElementChild, done);
  });
}

function confirmBox(msg, danger = true) {
  return modal(`<p>${esc(msg)}</p><div class="foot">
    <button class="btn" data-close="no">${t("cancel")}</button>
    <button class="btn ${danger ? "danger solid" : "primary"}" data-close="ok">${danger ? t("delete") : t("ok")}</button></div>`);
}

// formModal shows a form and calls submit(values); errors stay in the dialog.
function formModal(title, fields, submit, submitLabel) {
  return modal(`<h2>${esc(title)}</h2><form novalidate><div class="error-box hidden"></div>${fields}
    <div class="foot"><button type="button" class="btn" data-close="no">${t("cancel")}</button>
    <button class="btn primary" type="submit">${esc(submitLabel || t("save"))}</button></div></form>`, {
    onMount(m, done) {
      const form = $("form", m), err = $(".error-box", m);
      form.addEventListener("submit", async (e) => {
        e.preventDefault();
        const btn = $("button[type=submit]", form);
        btn.disabled = true;
        try { done(await submit(form) ?? true); }
        catch (ex) { err.textContent = ex.message; err.classList.remove("hidden"); btn.disabled = false; }
      });
    },
  });
}

// ---- layout & routing -------------------------------------------------------

const NAV = ["dashboard", "buckets", "files", "keys", "backup", "settings"];

function layout() {
  const nav = NAV.map((n) => `<a href="#/${n === "dashboard" ? "" : n}" data-nav="${n}">${icon(n)}<span>${t(n)}</span></a>`).join("");
  $("#app").className = "";
  $("#app").removeAttribute("aria-busy");
  $("#app").innerHTML = `<div class="shell">
    <aside class="side"><a class="brand" href="#/"><img src="${BASE}/logo.svg" alt="">Tijori</a>
      <nav class="nav">${nav}</nav>
      <div class="side-foot"><span class="muted">${esc(state.user)}${state.role === "readonly" ? " · " + t("readonly") : ""}</span>
        <button class="btn small" id="logout">${t("logout")}</button></div></aside>
    <div><header class="topbar"><a class="brand" href="#/"><img src="${BASE}/logo.svg" alt="" width="28" height="28">Tijori</a>
      <button class="btn small" id="logout2">${t("logout")}</button></header>
      <main class="main" id="main"></main></div></div>`;
  const out = async () => { try { await api("POST", "/logout"); } catch (e) { /* ignore */ } showLogin(); };
  $("#logout").addEventListener("click", out);
  $("#logout2").addEventListener("click", out);
}

const VIEWS = {};
async function route() {
  if (!state.csrf) return;
  if (!$("#main")) layout();
  const parts = location.hash.replace(/^#\/?/, "").split("/");
  const name = parts[0] || "dashboard";
  $$("[data-nav]").forEach((a) => a.classList.toggle("active", a.dataset.nav === (name === "browse" ? "files" : name)));
  const main = $("#main");
  const view = VIEWS[name] || VIEWS.dashboard;
  try { await view(main, parts.slice(1).map(decodeURIComponent)); }
  catch (e) { fail(e); }
}
window.addEventListener("hashchange", route);

function showLogin() {
  state.csrf = "";
  $("#app").className = "";
  $("#app").innerHTML = `<div class="login"><form class="card" novalidate>
    <div class="logo"><img src="${BASE}/logo.svg" alt=""><strong>Tijori</strong></div>
    <div class="error-box hidden"></div>
    <div class="field"><label for="u">${t("username")}</label><input id="u" type="text" autocomplete="username" value="admin" required></div>
    <div class="field"><label for="p">${t("password")}</label><input id="p" type="password" autocomplete="current-password" required></div>
    <button class="btn primary" type="submit">${t("login")}</button></form></div>`;
  const form = $("form"), err = $(".error-box", form);
  $("#p").focus();
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    try {
      const me = await api("POST", "/login", { username: $("#u").value.trim(), password: $("#p").value });
      Object.assign(state, { csrf: me.csrf, user: me.user, role: me.role });
      layout();
      route();
    } catch (ex) {
      err.textContent = ex.message;
      err.classList.remove("hidden");
    }
  });
}

async function boot() {
  try {
    const me = await api("GET", "/me");
    Object.assign(state, { csrf: me.csrf, user: me.user, role: me.role });
    layout();
    route();
  } catch (e) {
    if (e.status !== 401) showLogin();
  }
}

// ---- dashboard --------------------------------------------------------------

const ro = () => state.role === "readonly";

function usageRows(buckets) {
  if (!buckets.length) return `<p class="muted">${t("noBuckets")}</p>`;
  return buckets.map((b) => {
    const p = pct(b.bytes, b.max_size);
    return `<div class="usage-row"><div class="top"><a href="#/browse/${encodeURIComponent(b.name)}"><strong>${esc(b.name)}</strong></a>
      <span class="muted small">${bytes(b.bytes)}${b.max_size ? " / " + bytes(b.max_size) : ""} · ${b.objects} ${t("objects").toLowerCase()}</span></div>
      ${b.max_size ? `<div class="bar"><span data-pct="${p}"></span></div>` : ""}</div>`;
  }).join("");
}

VIEWS.dashboard = async (main) => {
  const o = await api("GET", "/overview");
  state.buckets = o.buckets;
  const v = o.video || {};
  const ic = o.image_cache;
  const lb = o.last_backup;
  main.innerHTML = `<h1>${t("dashboard")}</h1>
    <div class="grid stats">
      <div class="card stat"><div class="label">${t("totalUsed")}</div><div class="value">${bytes(o.total)}</div>
        ${o.max_total ? `<div class="bar"><span data-pct="${pct(o.total, o.max_total)}"></span></div><div class="small muted">${t("quota")}: ${bytes(o.max_total)}</div>` : ""}</div>
      <div class="card stat"><div class="label">${t("objects")}</div><div class="value">${o.objects}</div></div>
      <div class="card stat"><div class="label">${t("bucketsCount")}</div><div class="value">${o.buckets.length}</div></div>
      <div class="card stat"><div class="label">${t("keysCount")}</div><div class="value">${o.keys}</div></div>
    </div>
    <div class="grid two">
      <div class="card"><h2>${t("usageByBucket")}</h2>${usageRows(o.buckets)}</div>
      <div class="card"><h2>${t("server")}</h2><dl class="kv">
        <dt>${t("endpoint")}</dt><dd><code>${esc(o.endpoint)}</code> <button class="btn small" data-copy="${esc(o.endpoint)}">${t("copy")}</button></dd>
        <dt>Region</dt><dd><code>${esc(o.region)}</code></dd>
        <dt>${t("version")}</dt><dd>${esc(o.version)}</dd>
        <dt>${t("uptime")}</dt><dd>${duration(o.uptime)}</dd>
        <dt>${t("freeDisk")}</dt><dd>${bytes(o.disk_free)}</dd>
        <dt>${t("video")}</dt><dd>${v.available ? `<span class="badge ok">${t("available")}</span> <span class="small muted">${esc(v.version || "")}</span>${v.queued ? ` · ${v.queued} queued` : ""}` : `<span class="badge">${t("notFound")}</span>`}</dd>
        ${ic ? `<dt>${t("imageCache")}</dt><dd>${bytes(ic.bytes)} · ${ic.entries} · ${ic.hits}/${ic.hits + ic.misses} hits</dd>` : ""}
        <dt>${t("lastBackup")}</dt><dd>${lb ? `${when(lb.finished)} <span class="badge ${lb.ok ? "ok" : "bad"}">${lb.ok ? t("ok") : t("failed")}</span>` : t("never")}</dd>
      </dl></div>
    </div>`;
  setBars(main);
  $$("[data-copy]", main).forEach((b) => b.addEventListener("click", () => copyText(b.dataset.copy)));
};

// ---- buckets ----------------------------------------------------------------

function bucketFields(b = {}, isNew = true) {
  const sz = (n) => (n ? bytes(n).replace(" ", "") : "");
  return `${isNew ? `<div class="field"><label>${t("name")}</label><input type="text" name="name" placeholder="my-photos" required pattern="[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]">
      <div class="hint">3-63 lowercase letters, digits, dots or hyphens</div></div>` : ""}
    <div class="row"><div class="field"><label>${t("quota")}</label><input type="text" name="max_size" placeholder="10GB" value="${esc(sz(b.max_size))}"></div>
      <div class="field"><label>${t("maxFile")}</label><input type="text" name="max_object_size" placeholder="500MB" value="${esc(sz(b.max_object_size))}"></div></div>
    <label class="check"><input type="checkbox" name="public_read" ${b.public_read ? "checked" : ""}> ${t("public")}</label>
    <div class="hint">${t("publicHint")}</div>
    <label class="check"><input type="checkbox" name="video_process" ${b.video_process ? "checked" : ""}> ${t("video")}</label>
    <div class="hint">${t("videoHint")}</div>`;
}
function bucketValues(form) {
  const nameInput = form.elements.namedItem("name");
  return {
    name: nameInput ? nameInput.value.trim() : undefined,
    max_size: form.elements.max_size.value.trim(),
    max_object_size: form.elements.max_object_size.value.trim(),
    public_read: form.elements.public_read.checked,
    video_process: form.elements.video_process.checked,
  };
}

VIEWS.buckets = async (main) => {
  const list = await api("GET", "/buckets");
  state.buckets = list;
  main.innerHTML = `<div class="head"><h1>${t("buckets")}</h1>${ro() ? "" : `<button class="btn primary" id="new-bucket">+ ${t("newBucket")}</button>`}</div>
    <div class="card">${list.length ? `<table class="table"><thead><tr><th>${t("name")}</th><th class="num">${t("objects")}</th>
      <th>${t("totalUsed")}</th><th class="hide-sm">${t("created")}</th><th></th></tr></thead><tbody>
      ${list.map((b) => `<tr><td><a href="#/browse/${encodeURIComponent(b.name)}"><strong>${esc(b.name)}</strong></a>
        ${b.public_read ? ` <span class="badge public">${t("public")}</span>` : ""}${b.video_process ? ` <span class="badge">${t("video")}</span>` : ""}
        ${b.managed ? `<div class="hint">${t("managed")}</div>` : ""}</td>
        <td class="num">${b.objects}</td>
        <td>${bytes(b.bytes)}${b.max_size ? ` / ${bytes(b.max_size)}<div class="bar"><span data-pct="${pct(b.bytes, b.max_size)}"></span></div>` : ""}</td>
        <td class="hide-sm muted small">${new Date(b.created).toLocaleDateString()}</td>
        <td class="num"><div class="actions">
          <a class="btn small" href="#/browse/${encodeURIComponent(b.name)}">${t("browse")}</a>
          ${ro() ? "" : `${b.managed ? "" : `<button class="btn small" data-edit="${esc(b.name)}">${t("edit")}</button>`}
          <button class="btn small danger" data-del="${esc(b.name)}" data-count="${b.objects}">${t("delete")}</button>`}</div></td></tr>`).join("")}
      </tbody></table>` : `<div class="empty">${t("noBuckets")}</div>`}</div>`;
  setBars(main);
  const nb = $("#new-bucket", main);
  if (nb) nb.addEventListener("click", async () => {
    const ok = await formModal(t("newBucket"), bucketFields(), async (f) => api("POST", "/buckets", bucketValues(f)), t("create"));
    if (ok) { toast(t("ok")); route(); }
  });
  $$("[data-edit]", main).forEach((btn) => btn.addEventListener("click", async () => {
    const b = list.find((x) => x.name === btn.dataset.edit);
    const ok = await formModal(`${t("edit")}: ${b.name}`, bucketFields(b, false), async (f) => {
      const v = bucketValues(f); delete v.name;
      return api("PUT", "/buckets/" + encodeURIComponent(b.name), v);
    });
    if (ok) { toast(t("ok")); route(); }
  }));
  $$("[data-del]", main).forEach((btn) => btn.addEventListener("click", async () => {
    const name = btn.dataset.del, count = +btn.dataset.count;
    const msg = count ? t("confirmFolder", { name }) : t("confirmDelete", { name });
    if (!(await confirmBox(msg))) return;
    try { await api("DELETE", "/buckets/" + encodeURIComponent(name) + (count ? "?force=1" : "")); toast(t("ok")); route(); }
    catch (e) { fail(e); }
  }));
};

// ---- files ------------------------------------------------------------------

VIEWS.files = async (main) => {
  const list = await api("GET", "/buckets");
  if (list.length === 1) { location.hash = "#/browse/" + encodeURIComponent(list[0].name); return; }
  main.innerHTML = `<h1>${t("files")}</h1><div class="card">${list.length ? `<ul class="files">${list.map((b) =>
    `<li class="file folder"><div class="thumb">${icon("buckets")}</div><div class="name">
      <a href="#/browse/${encodeURIComponent(b.name)}">${esc(b.name)}</a><div class="meta">${b.objects} · ${bytes(b.bytes)}</div></div><span></span></li>`).join("")}</ul>`
    : `<div class="empty">${t("noBuckets")} <a href="#/buckets">${t("newBucket")}</a></div>`}</div>`;
};

const isImage = (ct) => /^image\/(jpeg|png|gif|webp)/.test(ct || "");
const isVideo = (ct) => /^video\//.test(ct || "");
const isAudio = (ct) => /^audio\//.test(ct || "");

function browseHash(bucket, prefix) {
  return "#/browse/" + encodeURIComponent(bucket) + (prefix ? "/" + prefix.split("/").filter(Boolean).map(encodeURIComponent).join("/") + "/" : "");
}

VIEWS.browse = async (main, parts) => {
  const bucket = parts[0];
  const prefix = parts.slice(1).filter(Boolean).join("/") + (parts.slice(1).filter(Boolean).length ? "/" : "");
  const buckets = state.buckets.length ? state.buckets : await api("GET", "/buckets");
  const binfo = buckets.find((b) => b.name === bucket) || {};
  const segs = prefix.split("/").filter(Boolean);
  const crumbs = [`<a href="#/files">${t("files")}</a>`, `<a href="${browseHash(bucket, "")}">${esc(bucket)}</a>`]
    .concat(segs.map((s, i) => `<a href="${browseHash(bucket, segs.slice(0, i + 1).join("/"))}">${esc(s)}</a>`)).join(" / ");
  main.innerHTML = `<div class="crumbs">${crumbs}</div>
    <div class="toolbar"><input type="search" id="q" placeholder="${t("search")}" aria-label="${t("search")}">
      ${ro() ? "" : `<button class="btn" id="mkdir">+ ${t("newFolder")}</button>
      <label class="btn primary" for="pick">${t("upload")}</label><input type="file" id="pick" multiple class="hidden">`}</div>
    ${ro() ? "" : `<div class="drop" id="drop">${t("dropHere")} <label for="pick"><a>${t("chooseFiles")}</a></label></div>`}
    <div class="uploads" id="uploads"></div>
    <div class="card"><ul class="files" id="list"><li class="empty">…</li></ul>
      <div class="actions hidden" id="more-wrap"><button class="btn small" id="more">…</button></div></div>`;

  let next = null;
  const listEl = $("#list", main);
  async function load(query, marker) {
    const qs = `?bucket=${encodeURIComponent(bucket)}&prefix=${encodeURIComponent(prefix)}` +
      (query ? `&q=${encodeURIComponent(query)}` : "") + (marker ? `&marker=${encodeURIComponent(marker)}` : "");
    const res = await api("GET", "/objects" + qs);
    if (!marker) listEl.innerHTML = "";
    const rows = [];
    for (const f of res.folders) {
      const name = f.slice(prefix.length).replace(/\/$/, "");
      rows.push(`<li class="file folder"><div class="thumb">${icon("folder")}</div><div class="name">
        <a href="${browseHash(bucket, f)}">${esc(name)}</a><div class="meta">folder</div></div>
        <div class="actions">${ro() ? "" : `<button class="btn small danger" data-delfolder="${esc(f)}">${t("delete")}</button>`}</div></li>`);
    }
    for (const f of res.files) {
      const display = query ? f.key.slice(prefix.length) : f.name;
      const thumb = isImage(f.type)
        ? `<img loading="lazy" alt="" src="${API}/thumb?bucket=${encodeURIComponent(bucket)}&key=${encodeURIComponent(f.key)}&w=88&h=88&fit=cover">`
        : icon(isVideo(f.type) ? "video" : "file");
      rows.push(`<li class="file" data-key="${esc(f.key)}" data-type="${esc(f.type)}"><div class="thumb">${thumb}</div>
        <div class="name"><button data-open>${esc(display)}</button>
        <div class="meta">${bytes(f.size)} · ${new Date(f.modified).toLocaleString()}</div></div>
        <div class="actions"><a class="btn small" href="${objURL(bucket, f.key)}" download>${t("download")}</a>
          <button class="btn small" data-share>${t("shareLink")}</button>
          ${binfo.public_read ? `<button class="btn small" data-public>${t("copyUrl")}</button>` : ""}
          ${ro() ? "" : `<button class="btn small danger" data-del>${t("delete")}</button>`}</div></li>`);
    }
    listEl.insertAdjacentHTML("beforeend", rows.join(""));
    if (!listEl.children.length) listEl.innerHTML = `<li class="empty">${t("emptyFolder")}</li>`;
    next = res.next || null;
    $("#more-wrap", main).classList.toggle("hidden", !next);
  }
  await load("");

  let timer;
  $("#q", main).addEventListener("input", (e) => {
    clearTimeout(timer);
    timer = setTimeout(() => load(e.target.value.trim()).catch(fail), 250);
  });
  $("#more", main).addEventListener("click", () => load($("#q", main).value.trim(), next).catch(fail));

  listEl.addEventListener("click", async (e) => {
    const folderBtn = e.target.closest("[data-delfolder]");
    if (folderBtn) {
      const f = folderBtn.dataset.delfolder;
      if (!(await confirmBox(t("confirmFolder", { name: f })))) return;
      try { const r = await api("DELETE", `/object?bucket=${encodeURIComponent(bucket)}&key=${encodeURIComponent(f)}`); toast(`${r.deleted} deleted`); load(""); }
      catch (ex) { fail(ex); }
      return;
    }
    const li = e.target.closest("li[data-key]");
    if (!li) return;
    const key = li.dataset.key, type = li.dataset.type;
    if (e.target.closest("[data-open]")) preview(bucket, key, type);
    else if (e.target.closest("[data-share]")) shareDialog(bucket, key);
    else if (e.target.closest("[data-public]")) {
      try { const r = await api("POST", "/presign", { bucket, key, method: "GET", expires: 60 }); copyText(r.public_url); }
      catch (ex) { fail(ex); }
    } else if (e.target.closest("[data-del]")) {
      if (!(await confirmBox(t("confirmDelete", { name: key })))) return;
      try { await api("DELETE", `/object?bucket=${encodeURIComponent(bucket)}&key=${encodeURIComponent(key)}`); li.remove(); toast(t("ok")); }
      catch (ex) { fail(ex); }
    }
  });

  if (ro()) return;
  $("#mkdir", main).addEventListener("click", async () => {
    const ok = await formModal(t("newFolder"), `<div class="field"><label>${t("name")}</label><input type="text" name="folder" required></div>`,
      async (f) => {
        const name = f.elements.folder.value.trim().replace(/^\/+|\/+$/g, "");
        if (!name) throw new Error(t("name") + "?");
        return api("POST", "/folder", { bucket, key: prefix + name + "/" });
      }, t("create"));
    if (ok) load("");
  });
  const startUploads = (files) => uploadFiles(bucket, prefix, files, () => load(""));
  $("#pick", main).addEventListener("change", (e) => { startUploads(Array.from(e.target.files)); e.target.value = ""; });
  const drop = $("#drop", main);
  ["dragenter", "dragover"].forEach((ev) => drop.addEventListener(ev, (e) => { e.preventDefault(); drop.classList.add("over"); }));
  ["dragleave", "drop"].forEach((ev) => drop.addEventListener(ev, (e) => { e.preventDefault(); drop.classList.remove("over"); }));
  drop.addEventListener("drop", (e) => startUploads(Array.from(e.dataTransfer.files)));
};

// uploadFiles sends files one at a time with a progress bar each.
async function uploadFiles(bucket, prefix, files, done) {
  const box = $("#uploads");
  for (const file of files) {
    const row = document.createElement("div");
    row.className = "upload-item";
    row.innerHTML = `<div class="top">${esc(file.name)} <span class="muted">${bytes(file.size)}</span></div><div class="bar"><span></span></div>`;
    box.appendChild(row);
    const bar = $(".bar span", row);
    try {
      await new Promise((resolve, reject) => {
        const xhr = new XMLHttpRequest();
        xhr.open("PUT", `${API}/object?bucket=${encodeURIComponent(bucket)}&key=${encodeURIComponent(prefix + file.name)}`);
        xhr.setRequestHeader("X-CSRF-Token", state.csrf);
        if (file.type) xhr.setRequestHeader("Content-Type", file.type);
        xhr.upload.onprogress = (e) => { if (e.lengthComputable) bar.style.width = (100 * e.loaded) / e.total + "%"; };
        xhr.onload = () => {
          if (xhr.status < 300) return resolve();
          let msg = xhr.statusText;
          try { msg = JSON.parse(xhr.responseText).error || msg; } catch (e) { /* not JSON */ }
          reject(new Error(msg));
        };
        xhr.onerror = () => reject(new Error("network error"));
        xhr.send(file);
      });
      bar.style.width = "100%";
      setTimeout(() => row.remove(), 1500);
    } catch (e) {
      row.querySelector(".bar").classList.add("full");
      toast(`${file.name}: ${e.message}`, true);
    }
  }
  done();
}

function preview(bucket, key, type) {
  const src = objURL(bucket, key, "&inline=1");
  let body;
  if (isImage(type) || type === "image/svg+xml") body = `<img src="${src}" alt="${esc(key)}">`;
  else if (isVideo(type)) body = `<video src="${src}" controls playsinline preload="metadata"></video>`;
  else if (isAudio(type)) body = `<audio src="${src}" controls></audio>`;
  else body = `<div class="empty">${esc(type || "file")}<br><br><a class="btn" href="${src}" target="_blank" rel="noopener">${t("browse")}</a></div>`;
  modal(`<h2 class="mono">${esc(key)}</h2><div class="preview">${body}</div>
    <div class="foot"><a class="btn" href="${objURL(bucket, key)}" download>${t("download")}</a>
    <button class="btn primary" data-close="no">${t("close")}</button></div>`, { wide: true });
}

function shareDialog(bucket, key) {
  const opts = [[3600, "1 hour"], [86400, "1 day"], [604800, "7 days"], [900, "15 minutes"]];
  modal(`<h2>${t("shareLink")}</h2><p class="mono small">${esc(bucket)}/${esc(key)}</p>
    <div class="field"><label>${t("expiresIn")}</label><select name="exp">${opts.map(([s, l]) => `<option value="${s}">${l}</option>`).join("")}</select></div>
    <div class="secret hidden"><code></code><button class="btn small" data-copyurl>${t("copy")}</button></div>
    <div class="hint" id="signed"></div>
    <div class="foot"><button class="btn" data-close="no">${t("close")}</button><button class="btn primary" data-gen>${t("generate")}</button></div>`, {
    onMount(m) {
      $("[data-gen]", m).addEventListener("click", async () => {
        try {
          const r = await api("POST", "/presign", { bucket, key, method: "GET", expires: +$("select", m).value });
          $(".secret", m).classList.remove("hidden");
          $(".secret code", m).textContent = r.url;
          $("#signed", m).textContent = `Signed with key ${r.signed_with}. Revoking that key breaks the link.`;
        } catch (e) { fail(e); }
      });
      $("[data-copyurl]", m).addEventListener("click", () => copyText($(".secret code", m).textContent));
    },
  });
}

// ---- keys -------------------------------------------------------------------

VIEWS.keys = async (main) => {
  const [keys, buckets] = await Promise.all([api("GET", "/keys"), api("GET", "/buckets")]);
  main.innerHTML = `<div class="head"><h1>${t("keys")}</h1>${ro() ? "" : `<button class="btn primary" id="new-key">+ ${t("newKey")}</button>`}</div>
    <div class="card">${keys.length ? `<table class="table"><thead><tr><th>${t("accessKey")}</th><th>${t("buckets")}</th>
      <th class="hide-sm">${t("permission")}</th><th class="hide-sm">${t("created")}</th><th></th></tr></thead><tbody>
      ${keys.map((k) => `<tr><td><code>${esc(k.access_key)}</code><div class="hint">${esc(k.name || "")}</div></td>
        <td>${k.buckets.includes("*") ? t("allBuckets") : k.buckets.map(esc).join(", ")}</td>
        <td class="hide-sm">${t("perm_" + k.permission)}</td>
        <td class="hide-sm muted small">${k.from_env ? "env" : new Date(k.created).toLocaleDateString()}</td>
        <td class="num">${ro() || k.from_env ? "" : `<button class="btn small danger" data-revoke="${esc(k.access_key)}" data-name="${esc(k.name || k.access_key)}">${t("revoke")}</button>`}</td></tr>`).join("")}
      </tbody></table>` : `<div class="empty">No keys yet.</div>`}</div>`;

  $$("[data-revoke]", main).forEach((b) => b.addEventListener("click", async () => {
    if (!(await confirmBox(t("confirmRevoke", { name: b.dataset.name })))) return;
    try { await api("DELETE", "/keys/" + encodeURIComponent(b.dataset.revoke)); toast(t("ok")); route(); } catch (e) { fail(e); }
  }));
  const nk = $("#new-key", main);
  if (!nk) return;
  nk.addEventListener("click", async () => {
    const fields = `<div class="field"><label>${t("name")}</label><input type="text" name="kname" placeholder="laravel-app"></div>
      <div class="field"><label>${t("permission")}</label><select name="perm">
        <option value="readwrite">${t("perm_readwrite")}</option><option value="read">${t("perm_read")}</option><option value="write">${t("perm_write")}</option></select></div>
      <label>${t("buckets")}</label>
      <label class="check"><input type="checkbox" name="all" value="*" checked> ${t("allBuckets")}</label>
      ${buckets.map((b) => `<label class="check"><input type="checkbox" name="bucket" value="${esc(b.name)}"> ${esc(b.name)}</label>`).join("")}`;
    const created = await formModal(t("newKey"), fields, async (f) => {
      const all = f.elements.all.checked;
      const picked = $$("input[name=bucket]:checked", f).map((i) => i.value);
      if (!all && !picked.length) throw new Error("Pick at least one bucket.");
      return api("POST", "/keys", { name: f.elements.kname.value, permission: f.elements.perm.value, buckets: all ? ["*"] : picked });
    }, t("create"));
    if (created && created.secret_key) showSecret(created);
    route();
  });
};

async function showSecret(k) {
  const o = await api("GET", "/overview").catch(() => ({ endpoint: location.origin, region: "auto" }));
  const bucket = k.buckets[0] === "*" ? "my-bucket" : k.buckets[0];
  const laravel = `FILESYSTEM_DISK=s3
AWS_ACCESS_KEY_ID=${k.access_key}
AWS_SECRET_ACCESS_KEY=${k.secret_key}
AWS_DEFAULT_REGION=${o.region}
AWS_BUCKET=${bucket}
AWS_ENDPOINT=${o.endpoint}
AWS_USE_PATH_STYLE_ENDPOINT=true`;
  const node = `const s3 = new S3Client({
  endpoint: "${o.endpoint}",
  region: "${o.region}",
  forcePathStyle: true,
  credentials: {
    accessKeyId: "${k.access_key}",
    secretAccessKey: "${k.secret_key}",
  },
});`;
  await modal(`<h2>${t("newKey")}</h2><div class="error-box">${t("secretOnce")}</div>
    <label>${t("accessKey")}</label><div class="secret"><code>${esc(k.access_key)}</code><button class="btn small" data-c="${esc(k.access_key)}">${t("copy")}</button></div>
    <label>${t("secretKey")}</label><div class="secret"><code>${esc(k.secret_key)}</code><button class="btn small" data-c="${esc(k.secret_key)}">${t("copy")}</button></div>
    <label>Laravel .env</label><pre class="code">${esc(laravel)}</pre><div class="actions"><button class="btn small" data-c="${esc(laravel)}">${t("copy")}</button></div><br>
    <label>Node.js</label><pre class="code">${esc(node)}</pre><div class="actions"><button class="btn small" data-c="${esc(node)}">${t("copy")}</button></div>
    <div class="foot"><button class="btn primary" data-close="ok">${t("close")}</button></div>`, {
    onMount(m) { $$("[data-c]", m).forEach((b) => b.addEventListener("click", () => copyText(b.dataset.c))); },
  });
}

// ---- backup -----------------------------------------------------------------

VIEWS.backup = async (main) => {
  const b = await api("GET", "/backup");
  const l = b.last;
  main.innerHTML = `<h1>${t("backup")}</h1><div class="grid two">
    <div class="card"><dl class="kv">
      <dt>${t("destination")}</dt><dd>${b.configured ? `<code>${esc(b.destination)}</code>` : `<span class="muted">${t("notConfigured")}</span>`}</dd>
      <dt>${t("schedule")}</dt><dd>${esc(b.schedule || "–")}${b.keep ? ` · keep ${b.keep}` : ""}</dd>
      <dt>${t("lastBackup")}</dt><dd>${l ? `${when(l.finished)} <span class="badge ${l.ok ? "ok" : "bad"}">${l.ok ? t("ok") : t("failed")}</span>` : t("never")}</dd>
      ${l ? `<dt>Copied</dt><dd>${l.copied} files, ${bytes(l.bytes)} (${l.skipped} unchanged)</dd>` : ""}
      ${l && l.error ? `<dt>Error</dt><dd class="small">${esc(l.error)}</dd>` : ""}
    </dl><br>${ro() ? "" : `<button class="btn primary" id="run" ${!b.configured || b.running ? "disabled" : ""}>${b.running ? t("running") : t("runBackup")}</button>`}</div>
    <div class="card"><h2>How it works</h2><p class="small">Backups copy every bucket plus your keys to the destination. Only changed files are copied again.
      Set the destination in <code>tijori.toml</code>:</p>
      <pre class="code">[backup]
to = "/mnt/backup/tijori"
# to = "sftp://user@host/backups/tijori"
# to = "s3://bucket?endpoint=https://..."
schedule = "daily 03:00"
keep = 7</pre><p class="small">You can also run <code>tijori backup</code> or <code>tijori export</code> from a terminal.</p></div></div>`;
  const run = $("#run", main);
  if (run) run.addEventListener("click", async () => {
    try { await api("POST", "/backup"); toast(t("running")); run.disabled = true; setTimeout(route, 3000); } catch (e) { fail(e); }
  });
  if (b.running) setTimeout(() => { if (location.hash === "#/backup") route(); }, 3000);
};

// ---- settings ---------------------------------------------------------------

VIEWS.settings = async (main) => {
  const s = await api("GET", "/settings");
  const theme = store("tijori.theme") || "auto";
  main.innerHTML = `<h1>${t("settings")}</h1><div class="grid two">
    <div class="card"><h2>${t("theme")} &amp; ${t("language")}</h2>
      <div class="row"><div class="field"><label>${t("theme")}</label><select id="theme">
        ${["auto", "light", "dark"].map((v) => `<option value="${v}" ${v === theme ? "selected" : ""}>${t(v)}</option>`).join("")}</select></div>
      <div class="field"><label>${t("language")}</label><select id="lang">
        <option value="en" ${lang === "en" ? "selected" : ""}>English</option><option value="hi" ${lang === "hi" ? "selected" : ""}>हिन्दी</option></select></div></div>
      <h2>${t("changePassword")}</h2><form id="pw" novalidate><div class="error-box hidden"></div>
        <div class="field"><label>${t("currentPassword")}</label><input type="password" name="cur" autocomplete="current-password"></div>
        <div class="field"><label>${t("newPassword")}</label><input type="password" name="new" autocomplete="new-password" minlength="8"></div>
        <button class="btn primary" type="submit">${t("save")}</button></form></div>
    <div class="card"><h2>${t("config")}</h2><p class="small muted">${esc(s.file)}</p>
      ${(s.warnings || []).map((w) => `<div class="error-box">${esc(w)}</div>`).join("")}
      <pre class="code">${esc(s.toml)}</pre></div></div>`;
  $("#theme", main).addEventListener("change", (e) => { store("tijori.theme", e.target.value); applyTheme(); });
  $("#lang", main).addEventListener("change", (e) => { store("tijori.lang", e.target.value); location.reload(); });
  $("#pw", main).addEventListener("submit", async (e) => {
    e.preventDefault();
    const f = e.target, err = $(".error-box", f);
    try {
      await api("POST", "/password", { current: f.elements.cur.value, new: f.elements.new.value });
      toast(t("ok"));
      showLogin();
    } catch (ex) { err.textContent = ex.message; err.classList.remove("hidden"); }
  });
};

boot();
