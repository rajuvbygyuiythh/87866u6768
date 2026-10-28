// =============================================================================
//  app.js — Thundr dashboard shell (6 flat tabs: Home / Proxy / Messages /
//  Browser / Settings / Logs). One invoke() bridge, one form store, live logs.
// =============================================================================

const CONFIG = {
  brand: { mark: "TC", title: "Thundr Console", subtitle: "Bot operations · Go + WebView" },
  backend: "auto", // "auto" | "wails" | "http" | "mock"
  apiPrefix: "/api",
  pollLogsMs: 1000,
  pollStatsMs: 3000,
  theme: "forest",
};

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------
const state = {
  snapshot: null,
  tab: "home",
  running: false,
  busy: false,
  logs: { after: 0, entries: [], pending: false },
  bridgeKind: "unknown",
  loadedSequences: 0,
};

// Draft protection: server snapshots never stomp a field the user is editing.
const dirtyFields = new Set();
const SECTION_FIELDS = {
  setup: ["home-workers"],
  connections: ["proxy-enabled", "proxy-scheme", "proxy-entries"],
  credentials: ["msg-cred-base-url", "msg-cred-api-key", "msg-cred-model", "msg-cred-temperature"],
  actions: ["msg-strategy", "msg-sequence", "msg-targets", "msg-folder-path"],
  timings: [
    "set-reply-min", "set-reply-max", "set-silent-min", "set-silent-max",
    "set-reroll-min", "set-reroll-max", "set-instskip", "set-worktime",
    "set-nomatch", "set-sends-first",
  ],
};
const FIELD_SECTION = {};
for (const [section, ids] of Object.entries(SECTION_FIELDS)) {
  for (const id of ids) FIELD_SECTION[id] = section;
}
const RADIO_GROUPS = { "msg-strategy": "actions" };

function draftKey(el) {
  if (!el) return "";
  if (el.name && RADIO_GROUPS[el.name]) return el.name;
  return el.id || "";
}

function markDirty(key) {
  if (FIELD_SECTION[key]) dirtyFields.add(key);
}

function clearSectionDirty(section) {
  for (const key of [...dirtyFields]) {
    if (FIELD_SECTION[key] === section) dirtyFields.delete(key);
  }
}

function bindDraftTracking() {
  document.querySelectorAll("input[id], textarea[id], select[id]").forEach((el) => {
    const key = draftKey(el);
    if (!FIELD_SECTION[key]) return;
    const handler = () => markDirty(key);
    el.addEventListener("input", handler);
    el.addEventListener("change", handler);
  });
}

// ---------------------------------------------------------------------------
// Tiny helpers
// ---------------------------------------------------------------------------
const byId = (id) => document.getElementById(id);
const text = (id) => { const el = byId(id); return el ? String(el.value || "").trim() : ""; };
const num = (id) => { const el = byId(id); return el ? Number(el.value || 0) : 0; };
const checked = (id) => { const el = byId(id); return !!(el && el.checked); };

function setValue(id, val, force) {
  const el = byId(id);
  if (!el) return;
  if (!force && (el === document.activeElement || dirtyFields.has(id))) return;
  if (el.type === "checkbox") el.checked = !!val;
  else el.value = val ?? "";
}

function setStatus(id, message, kind) {
  const el = byId(id);
  if (!el) return;
  el.textContent = message || "";
  el.classList.toggle("status-error", kind === "error");
  el.classList.toggle("status-ok", kind === "ok");
}

function toast(message, kind) {
  const wrap = byId("toast-wrap");
  if (!wrap) return;
  const el = document.createElement("div");
  el.className = "toast" + (kind ? " " + kind : "");
  el.textContent = message;
  wrap.appendChild(el);
  setTimeout(() => el.remove(), 4200);
}
// ---------------------------------------------------------------------------
// Bridge: Wails binding OR HTTP /api OR explicit ?demo=1 mock — one call site.
// ---------------------------------------------------------------------------
const BACKEND_UNAVAILABLE_MESSAGE = "Backend unavailable. Start the Go server or desktop app, then click Retry. For UI-only demo, open with ?demo=1.";

function demoRequested() {
  if (CONFIG.backend === "mock") return true;
  try {
    return new URLSearchParams(window.location.search || "").get("demo") === "1";
  } catch (_) {
    return false;
  }
}

let demoLoadPromise = null;
function loadDemoBackend() {
  if (window.__consoleMock) return Promise.resolve(window.__consoleMock);
  if (!demoLoadPromise) {
    demoLoadPromise = new Promise((resolve, reject) => {
      const script = document.createElement("script");
      script.src = "./js/mock-backend.js";
      script.onload = () => {
        if (window.__consoleMock) resolve(window.__consoleMock);
        else reject(new Error("Demo backend did not initialize."));
      };
      script.onerror = () => reject(new Error("Demo backend failed to load."));
      document.head.appendChild(script);
    }).catch((error) => {
      demoLoadPromise = null;
      throw error;
    });
  }
  return demoLoadPromise;
}

const bridge = {
  kind: "unknown",
  api: null,
  async invoke(method, ...args) {
    try {
      if (this.kind === "wails" || this.kind === "mock") {
        if (!this.api || typeof this.api[method] !== "function") {
          throw new Error(`Backend method missing: ${method}`);
        }
        return await this.api[method](...args);
      }
      if (this.kind === "http") return await httpInvoke(method, ...args);
      throw new Error(BACKEND_UNAVAILABLE_MESSAGE);
    } catch (error) {
      reportFrontendError(`Backend ${method} failed`, error);
      throw error;
    }
  },
};

// A few bound methods have legacy HTTP routes with different paths/verbs.
const HTTP_ALIAS = {
  GetBrowserStatus: { path: "browser/status", verb: "GET" },
  CloseBrowsers: { path: "browser/close", verb: "POST" },
  OpenBrowser: { path: "chrome/open", verb: "POST", body: (target, uid) => ({ target: target || "register", uid: uid || "" }) },
  RegisterAccounts: { path: "register", verb: "POST", body: (count) => ({ count: Number(count) > 0 ? Number(count) : 1 }) },
  StopRun: { path: "StopRun", verb: "POST" },
  ClearLogs: { path: "ClearLogs", verb: "POST" },
};

async function httpInvoke(method, ...args) {
  const alias = HTTP_ALIAS[method];
  const url = `${CONFIG.apiPrefix}/${alias ? alias.path : method}`;
  let payload;
  if (alias && typeof alias.body === "function") payload = alias.body(...args);
  else if (alias && alias.verb === "POST") payload = {};
  else if (args.length === 1) payload = args[0];
  else if (args.length > 1) payload = { section: args[0], values: args[1] };
  const verb = alias && alias.verb ? alias.verb : (payload === undefined ? "GET" : "POST");
  const res = await fetch(url, {
    method: verb,
    headers: { "Content-Type": "application/json" },
    body: payload === undefined ? undefined : JSON.stringify(payload),
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || `HTTP ${res.status}`);
  return data;
}

async function resolveBridge() {
  bridge.api = null;

  if (demoRequested()) {
    try {
      const mockApi = await loadDemoBackend();
      if (mockApi) {
        bridge.kind = "mock";
        bridge.api = mockApi;
        return;
      }
    } catch (error) {
      reportFrontendError("Demo backend load failed", error);
    }
    bridge.kind = "unavailable";
    return;
  }

  const wailsApi = window.go && window.go.main && (window.go.main.DesktopApp || window.go.main.App);
  if (CONFIG.backend !== "http" && wailsApi) {
    bridge.kind = "wails";
    bridge.api = wailsApi;
    return;
  }

  try {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), 900);
    const res = await fetch(`${CONFIG.apiPrefix}/GetSnapshot`, { signal: controller.signal });
    clearTimeout(timer);
    if (res.ok) {
      bridge.kind = "http";
      return;
    }
  } catch (_) { /* no HTTP backend listening */ }

  bridge.kind = "unavailable";
}

function reportFrontendError(context, error) {
  try {
    const runtime = window.runtime || {};
    if (typeof runtime.LogError === "function") {
      const detail = error && error.stack ? error.stack : String(error);
      runtime.LogError(`Frontend | ${context} | ${detail}`);
    }
  } catch (_) { /* logging bridge unavailable */ }
}

function subscribeEvents() {
  const runtime = window.runtime || {};
  if (typeof runtime.EventsOn !== "function") return;
  runtime.EventsOn("ui:log", () => { refreshLogs(); });
  runtime.EventsOn("ui:running", (running) => {
    state.running = !!running;
    renderRunning();
  });
}
// ---------------------------------------------------------------------------
// Routing: 6 flat tabs (no subnav — every panel is one card stack)
// ---------------------------------------------------------------------------
function activateTab(tab) {
  state.tab = tab;
  document.querySelectorAll(".nav-button").forEach((btn) => {
    btn.classList.toggle("active", btn.dataset.tab === tab);
  });
  document.querySelectorAll(".tab-panel").forEach((panel) => {
    panel.classList.toggle("active", panel.dataset.panel === tab);
  });
  if (tab === "logs") refreshLogs();
}

function renderRunning() {
  const pill = byId("running-pill");
  if (pill) {
    pill.textContent = state.running ? "Running" : "Stopped";
    pill.className = "pill " + (state.running ? "running" : "stopped");
  }

  const badge = byId("home-run-badge");
  if (badge) {
    badge.textContent = state.running ? "Running" : "Idle";
    badge.classList.toggle("live", state.running);
  }

  const start = byId("home-start");
  const stop = byId("home-stop");
  if (start) start.disabled = state.running || state.busy;
  if (stop) stop.disabled = !state.running;
}

function setBusy(busy) {
  state.busy = busy;
  renderRunning();
}

function backendLabel() {
  return { wails: "wails", http: "http", mock: "demo", unavailable: "unavailable", unknown: "…" }[bridge.kind] || bridge.kind;
}

function renderBackendPill() {
  const pill = byId("backend-pill");
  if (pill) {
    pill.textContent = backendLabel();
    pill.className = "pill " + (bridge.kind === "wails" || bridge.kind === "http" ? "running" : "stopped");
  }
  const demoBanner = byId("demo-banner");
  if (demoBanner) demoBanner.hidden = bridge.kind !== "mock";
  const unavailableBanner = byId("backend-unavailable-banner");
  if (unavailableBanner) unavailableBanner.hidden = bridge.kind !== "unavailable";
  if (bridge.kind === "unavailable") setStatus("home-status", BACKEND_UNAVAILABLE_MESSAGE, "error");
}

async function retryBackend() {
  setStatus("home-status", "Checking backend…");
  await resolveBridge();
  renderBackendPill();
  if (bridge.kind !== "unavailable") {
    setStatus("home-status", "Backend connected.", "ok");
    await refreshSnapshot(true).catch(() => {});
  }
}

// ---------------------------------------------------------------------------
// Read / write settings — one reader per panel
// ---------------------------------------------------------------------------
function readProxy() {
  return {
    enabled: checked("proxy-enabled"),
    scheme: text("proxy-scheme") || "http",
    entries: text("proxy-entries"),
  };
}

function readCredentials() {
  return {
    base_url: text("msg-cred-base-url"),
    api_key: text("msg-cred-api-key"),
    model: text("msg-cred-model"),
    temperature: Number(text("msg-cred-temperature")) || 0.9,
  };
}

function readStrategy() {
  const picked = document.querySelector('input[name="msg-strategy"]:checked');
  return picked ? picked.value : "single";
}

function readActions() {
  return {
    strategy: readStrategy(),
    sequence: text("msg-sequence"),
    targets: text("msg-targets"),
    folder_path: text("msg-folder-path"),
  };
}

function readTimings() {
  return {
    reply_min: num("set-reply-min"),
    reply_max: num("set-reply-max"),
    silent_min: num("set-silent-min"),
    silent_max: num("set-silent-max"),
    reroll_min: num("set-reroll-min"),
    reroll_max: num("set-reroll-max"),
    instant_skip: num("set-instskip"),
    work_time: num("set-worktime"),
    no_match: num("set-nomatch"),
    sends_first: checked("set-sends-first"),
  };
}

// ---------------------------------------------------------------------------
// Snapshot application
// ---------------------------------------------------------------------------
function applySnapshot(snapshot) {
  if (!snapshot) return;
  state.snapshot = snapshot;
  const s = snapshot.settings || {};

  setValue("proxy-enabled", s.connections?.enabled);
  setValue("proxy-scheme", s.connections?.scheme || "http");
  setValue("proxy-entries", s.connections?.entries);

  setValue("msg-cred-base-url", s.credentials?.base_url);
  setValue("msg-cred-api-key", s.credentials?.api_key);
  setValue("msg-cred-model", s.credentials?.model);
  setValue("msg-cred-temperature", s.credentials?.temperature ?? 0.9);

  setValue("msg-sequence", s.actions?.sequence);
  setValue("msg-targets", s.actions?.targets);
  setValue("msg-folder-path", s.actions?.folder_path);

  const t = s.timings || {};
  setValue("set-reply-min", t.reply_min ?? 2);
  setValue("set-reply-max", t.reply_max ?? 5);
  setValue("set-silent-min", t.silent_min ?? 10);
  setValue("set-silent-max", t.silent_max ?? 15);
  setValue("set-reroll-min", t.reroll_min ?? 6);
  setValue("set-reroll-max", t.reroll_max ?? 12);
  setValue("set-instskip", t.instant_skip ?? 5);
  setValue("set-worktime", t.work_time ?? 600);
  setValue("set-nomatch", t.no_match ?? 75);
  setValue("set-sends-first", t.sends_first === true);

  setValue("home-workers", (s.setup?.concurrency ?? 1) || 1);

  if (snapshot.strategy) setStrategy(snapshot.strategy);
  renderStats(snapshot.stats || {});
  state.running = !!(snapshot.runtime && snapshot.runtime.running);
  renderRunning();
}

function renderStats(stats) {
  setValue("stat-accounts", stats.accounts ?? stats.profiles ?? 0, true);
  setValue("stat-running", stats.running ?? 0, true);
  setValue("stat-chats", stats.chats ?? 0, true);
  setValue("stat-snaps", stats.snap_shares ?? 0, true);
  setValue("stat-tg", stats.telegram_shares ?? 0, true);
  setValue("stat-banned", stats.banned ?? 0, true);
}

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------
function setStrategy(strategy, force) {
  if (!force && dirtyFields.has("msg-strategy")) return;
  const radio = document.querySelector(`input[name="msg-strategy"][value="${strategy}"]`);
  if (radio) radio.checked = true;
  document.querySelectorAll(".mode-option").forEach((option) => {
    const input = option.querySelector('input[name="msg-strategy"]');
    if (input) option.classList.toggle("active", input.checked);
  });
  const single = byId("msg-section-single");
  const folder = byId("msg-section-folder");
  const model = byId("msg-section-model");
  if (single) single.style.display = strategy === "single" ? "" : "none";
  if (folder) folder.style.display = strategy === "folder" ? "" : "none";
  if (model) model.style.display = strategy === "model" ? "" : "none";
  // API set card belongs to the API / model strategy only.
  const apiCard = byId("msg-api-card");
  if (apiCard) apiCard.style.display = strategy === "model" ? "" : "none";
}

async function saveSection(section, values, statusId) {
  try {
    setStatus(statusId, "Saving…");
    const snapshot = await bridge.invoke("SaveSettings", section, values);
    clearSectionDirty(section);
    if (snapshot) applySnapshot(snapshot);
    setStatus(statusId, "Saved.", "ok");
    toast(`${section} saved.`);
    return true;
  } catch (error) {
    setStatus(statusId, error.message || String(error), "error");
    return false;
  }
}
// ---------------------------------------------------------------------------
// Proxy + API tests
// ---------------------------------------------------------------------------
async function testConnections() {
  const statusId = "proxy-status";
  const box = byId("proxy-results");
  try {
    setStatus(statusId, "Testing…");
    box.replaceChildren();
    const res = await bridge.invoke("TestConnection", readProxy());
    const results = (res && res.results) || [];
    if (!results.length) { setStatus(statusId, "No entries to test."); return; }
    results.forEach((r) => {
      const p = document.createElement("p");
      p.className = r.ok ? "proxy-test-pass" : "proxy-test-fail";
      p.textContent = `${r.entry} — ${r.ok ? "PASS" : "FAIL"} (${r.latency_ms ?? 0} ms) ${r.message || ""}`.trim();
      box.appendChild(p);
    });
    const passed = results.filter((r) => r.ok).length;
    setStatus(statusId, `${passed}/${results.length} passed.`, passed === results.length ? "ok" : "error");
  } catch (error) {
    setStatus(statusId, error.message || String(error), "error");
  }
}

async function testApi() {
  try {
    setStatus("msg-api-status", "Calling the API…");
    const message = await bridge.invoke("TestAPI", readCredentials());
    setStatus("msg-api-status", String(message || "OK"), "ok");
  } catch (error) {
    setStatus("msg-api-status", error.message || String(error), "error");
  }
}

// ---------------------------------------------------------------------------
// Run control (Home): Start -> mode cards -> Run / API workers; Stop kills all.
// ---------------------------------------------------------------------------
function showModeCards(show) {
  const cards = byId("home-mode-cards");
  if (cards) cards.hidden = !show;
}

async function startRunFlow(mode) {
  if (state.running || state.busy) return;
  setBusy(true);
  try {
    const workers = Math.min(10, Math.max(1, Math.round(num("home-workers") || 1)));
    setStatus("home-status", `Saving ${mode} mode…`);
    await bridge.invoke("SaveSettings", "setup", { mode, concurrency: workers });
    clearSectionDirty("setup");
    state.logs.entries = [];
    state.logs.after = 0;
    renderLogs();
    showModeCards(false);
    setStatus("home-status", `Starting ${mode === "ws" ? "API" : "browser"} workers…`);
    const res = await bridge.invoke("StartRun", {});
    const ok = !!(res && res.ok);
    state.running = ok;
    renderRunning();
    setStatus("home-status", (res && res.message) || (ok ? "Started." : "Failed to start."), ok ? "ok" : "error");
  } catch (error) {
    setStatus("home-status", error.message || String(error), "error");
  } finally {
    setBusy(false);
  }
}

async function stopRun() {
  try {
    setStatus("home-status", "Stopping workers and closing their browsers…");
    await bridge.invoke("StopRun");
    state.running = false;
    renderRunning();
    setStatus("home-status", "Stopped. Worker browsers close automatically.", "ok");
  } catch (error) {
    setStatus("home-status", error.message || String(error), "error");
  }
}

// ---------------------------------------------------------------------------
// Browser tab: one-click auto register (session saved, browser closes)
// ---------------------------------------------------------------------------
async function registerAccounts() {
  try {
    setStatus("browser-register-status", "Registering — watch the Live logs…");
    const res = await bridge.invoke("RegisterAccounts", 1);
    const ok = !!(res && res.ok);
    setStatus("browser-register-status", (res && res.message) || (ok ? "Started." : "Failed."), ok ? "ok" : "error");
  } catch (error) {
    setStatus("browser-register-status", error.message || String(error), "error");
  }
}

// ---------------------------------------------------------------------------
// Folder actions (Messages)
// ---------------------------------------------------------------------------
async function loadFolder() {
  const path = text("msg-folder-path");
  if (!path) {
    setStatus("msg-folder-status", "Type a folder path or click Browse first.", "error");
    return;
  }
  try {
    setStatus("msg-folder-status", "Loading…");
    const res = await bridge.invoke("LoadMessageFolder", { path });
    if (res && res.ok === false) throw new Error(res.error || "Load failed.");
    state.loadedSequences = (res && res.count) || 0;
    setStatus("msg-folder-status", `${state.loadedSequences} sequences loaded.`, "ok");
  } catch (error) {
    setStatus("msg-folder-status", error.message || String(error), "error");
  }
}

async function browseFolder() {
  if (bridge.kind !== "wails" && bridge.kind !== "mock") {
    setStatus("msg-folder-status", "Folder picker is only available in the desktop app. Type the folder path here, then click Load Folder.", "error");
    const input = byId("msg-folder-path");
    if (input && typeof input.focus === "function") input.focus();
    return;
  }
  try {
    const dir = await bridge.invoke("BrowseMessageFolder");
    const picked = String(dir || "").trim();
    if (!picked) return; // user cancelled the dialog
    setValue("msg-folder-path", picked, true);
    markDirty("msg-folder-path");
    setStatus("msg-folder-status", "Folder selected — click Load Folder.");
  } catch (error) {
    toast(error.message || String(error), "error");
  }
}

async function cleanLoaded() {
  try {
    await bridge.invoke("CleanLoadedMessages", {});
    state.loadedSequences = 0;
    setStatus("msg-folder-status", "Loaded sequences cleaned.", "ok");
  } catch (error) {
    setStatus("msg-folder-status", error.message || String(error), "error");
  }
}

// ---------------------------------------------------------------------------
// Live logs
// ---------------------------------------------------------------------------
function logKindLabel(kind) {
  return { in: "IN", out: "OUT", skip: "SKIP", system: "SYS", error: "ERR" }[kind] || String(kind || "").toUpperCase();
}

function logFilterMatch(entry, filter) {
  if (filter === "io") return entry.kind === "in" || entry.kind === "out";
  if (filter === "system") return entry.kind === "system" || entry.kind === "skip";
  if (filter === "errors") return entry.kind === "error";
  return true;
}

function renderLogs() {
  const list = byId("logs-list");
  const filterEl = byId("logs-filter");
  const emptyEl = byId("logs-empty");
  if (!list) return;
  const filter = filterEl ? filterEl.value : "all";
  const nearBottom = list.scrollHeight - list.scrollTop - list.clientHeight < 60;
  const fragment = document.createDocumentFragment();
  let shown = 0;

  for (const entry of state.logs.entries) {
    if (!logFilterMatch(entry, filter)) continue;
    shown += 1;
    const row = document.createElement("div");
    row.className = `log-row log-${entry.kind}`;

    const time = document.createElement("span");
    time.className = "log-time";
    time.textContent = entry.time || "";

    const badge = document.createElement("span");
    badge.className = "log-badge";
    badge.textContent = logKindLabel(entry.kind);

    const body = document.createElement("div");
    body.className = "log-body";
    const line = document.createElement("p");
    line.className = "log-text";
    line.textContent = entry.kind === "in" || entry.kind === "out" ? `"${entry.text || ""}"` : (entry.text || "");
    body.appendChild(line);

    if (entry.meta) {
      const meta = document.createElement("p");
      meta.className = "log-meta";
      meta.textContent = entry.meta;
      body.appendChild(meta);
    }

    row.append(time, badge, body);
    fragment.appendChild(row);
  }

  list.replaceChildren(fragment);
  if (emptyEl) emptyEl.hidden = shown > 0;
  if (nearBottom) list.scrollTop = list.scrollHeight;
}

async function refreshLogs() {
  const bucket = state.logs;
  if (!bucket || bucket.pending) return;
  bucket.pending = true;
  try {
    const res = await bridge.invoke("GetLogs", bucket.after);
    if (res && Array.isArray(res.entries) && res.entries.length) {
      bucket.entries.push(...res.entries);
      bucket.after = res.version ?? bucket.entries.length;
      renderLogs();
    }
    if (res && res.cleared) {
      bucket.entries = [];
      bucket.after = res.version || 0;
      renderLogs();
    }
  } catch (_) {
    /* transient — the next poll retries */
  } finally {
    bucket.pending = false;
  }
}

async function clearLogs() {
  try {
    await bridge.invoke("ClearLogs");
    state.logs.entries = [];
    state.logs.after = 0;
    renderLogs();
  } catch (error) {
    toast(error.message || String(error), "error");
  }
}

async function refreshSnapshot(silent) {
  try {
    const snapshot = await bridge.invoke("GetSnapshot");
    // Manual refresh keeps unsaved drafts; only a successful save clears them.
    applySnapshot(snapshot);
    return snapshot;
  } catch (error) {
    if (!silent) toast(error.message || String(error), "error");
    throw error;
  }
}

// ---------------------------------------------------------------------------
// Wiring
// ---------------------------------------------------------------------------
function bindForms() {
  bindDraftTracking();
  document.querySelectorAll('input[name="msg-strategy"]').forEach((input) => {
    input.addEventListener("change", () => {
      markDirty("msg-strategy");
      setStrategy(readStrategy(), true);
    });
  });
  setStrategy(readStrategy(), true);
  const theme = byId("theme-select");
  if (theme) {
    theme.value = localStorage.getItem("kit-theme") || CONFIG.theme;
    document.documentElement.dataset.theme = theme.value;
  }
}

function bindActions() {
  const on = (id, handler) => {
    const el = byId(id);
    if (el) el.addEventListener("click", handler);
  };

  on("demo-hide", () => { const b = byId("demo-banner"); if (b) b.hidden = true; });
  on("backend-retry", retryBackend);
  on("home-refresh", () => refreshSnapshot(false));
  on("proxy-refresh", () => refreshSnapshot(false));
  on("msg-refresh", () => refreshSnapshot(false));
  on("browser-refresh", () => refreshSnapshot(false));
  on("settings-refresh", () => refreshSnapshot(false));
  on("logs-refresh", () => { refreshLogs(); refreshSnapshot(true); });

  const theme = byId("theme-select");
  if (theme) {
    theme.addEventListener("change", (event) => {
      document.documentElement.dataset.theme = event.target.value;
      localStorage.setItem("kit-theme", event.target.value);
    });
  }

  // Home run control
  on("home-start", () => showModeCards(true));
  on("home-mode-cancel", () => showModeCards(false));
  on("home-start-visible", () => startRunFlow("visible"));
  on("home-start-ws", () => startRunFlow("ws"));
  on("home-stop", stopRun);

  // Proxy
  on("proxy-save", () => saveSection("connections", readProxy(), "proxy-status"));
  on("proxy-test", testConnections);

  // Messages
  // Messages — read BOTH form values before saving: the first SaveSettings
  // reply applies a fresh snapshot that would otherwise overwrite the
  // targets/sequence textareas before the second save reads them.
  on("msg-save", async () => {
    const credentials = readCredentials();
    const actions = readActions();
    const credentialsSaved = await saveSection("credentials", credentials, "msg-status");
    if (!credentialsSaved) return;
    await saveSection("actions", actions, "msg-status");
  });
  on("msg-load-folder", loadFolder);
  on("msg-browse-folder", browseFolder);
  on("msg-clean-folder", cleanLoaded);
  on("msg-api-test", testApi);

  // Browser
  on("browser-register", registerAccounts);

  // Settings
  on("set-save", () => saveSection("timings", readTimings(), "set-status"));

  // Logs
  const filter = byId("logs-filter");
  if (filter) filter.addEventListener("change", () => renderLogs());
  on("logs-clear", clearLogs);
}

async function bootstrap() {
  document.querySelectorAll(".nav-button").forEach((btn) => {
    btn.addEventListener("click", () => activateTab(btn.dataset.tab));
  });
  bindForms();
  bindActions();
  activateTab("home");

  await resolveBridge();
  renderBackendPill();
  subscribeEvents();

  if (bridge.kind !== "unavailable") {
    let attempts = 0;
    while (attempts < 25) {
      attempts += 1;
      try {
        await refreshSnapshot(true);
        break;
      } catch (_) {
        await new Promise((resolve) => setTimeout(resolve, 200));
      }
    }
  }

  setInterval(() => { if (bridge.kind !== "unavailable") refreshLogs(); }, CONFIG.pollLogsMs);
  setInterval(() => { if (bridge.kind !== "unavailable") refreshSnapshot(true).catch(() => {}); }, CONFIG.pollStatsMs);
}

window.addEventListener("error", (event) => {
  reportFrontendError("Uncaught error", event.error || event.message);
});
window.addEventListener("unhandledrejection", (event) => {
  reportFrontendError("Unhandled promise rejection", event.reason);
});
window.addEventListener("DOMContentLoaded", () => {
  bootstrap().catch((error) => {
    reportFrontendError("Bootstrap failed", error);
    setStatus("home-status", error.message || String(error), "error");
  });
});