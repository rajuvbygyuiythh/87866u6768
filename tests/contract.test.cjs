#!/usr/bin/env node
// =============================================================================
//  contract.test.cjs — method-parity check (adapted from the design kit's
//  extras/contract.test.cjs). Bound struct here is DesktopApp (package main),
//  delegating to internal/dash. Run:  node --test tests/contract.test.cjs
// =============================================================================
"use strict";

const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("path");

const ROOT = path.join(__dirname, "..");
const PATHS = {
  appJs: path.join(ROOT, "src", "frontend", "dist", "app.js"),
  mockJs: path.join(ROOT, "src", "frontend", "dist", "js", "mock-backend.js"),
  goFiles: [path.join(ROOT, "src", "app_contract.go"), path.join(ROOT, "src", "app_desktop.go")],
  html: path.join(ROOT, "src", "frontend", "dist", "index.html"),
};

const read = (file) => fs.readFileSync(file, "utf8");

function invokedMethods(js) {
  const found = new Set();
  for (const m of js.matchAll(/bridge\.invoke\(\s*"([A-Za-z0-9_]+)"/g)) found.add(m[1]);
  return found;
}

function mockMethods(js) {
  const found = new Set();
  for (const m of js.matchAll(/^\s{4}async\s+([A-Za-z0-9_]+)\s*\(/gm)) found.add(m[1]);
  return found;
}

/** Exported methods on the bound struct: func (d *DesktopApp) X( */
function goMethods(go) {
  const found = new Set();
  for (const m of go.matchAll(/func \((?:a|d) \*(?:App|DesktopApp)\) ([A-Z][A-Za-z0-9_]*)\(/g)) found.add(m[1]);
  return found;
}

function jsIds(js) {
  const found = new Set();
  for (const m of js.matchAll(/byId\(\s*"([A-Za-z0-9_-]+)"/g)) found.add(m[1]);
  for (const m of js.matchAll(/getElementById\(\s*"([A-Za-z0-9_-]+)"/g)) found.add(m[1]);
  return found;
}

function htmlIds(html) {
  const found = new Set();
  for (const m of html.matchAll(/\bid="([A-Za-z0-9_-]+)"/g)) found.add(m[1]);
  return found;
}

const appJs = read(PATHS.appJs);
const mockJs = read(PATHS.mockJs);
const goSrc = PATHS.goFiles.map(read).join("\n");
const html = read(PATHS.html);

test("every method called by the UI exists in the mock", () => {
  const mock = mockMethods(mockJs);
  const missing = [...invokedMethods(appJs)].filter((m) => !mock.has(m));
  assert.deepEqual(missing, [], `mock is missing: ${missing.join(", ")}`);
});

test("every method called by the UI exists on the bound Go struct", () => {
  const go = goMethods(goSrc);
  const missing = [...invokedMethods(appJs)].filter((m) => !go.has(m));
  assert.deepEqual(missing, [], `Go binding is missing: ${missing.join(", ")}`);
});

test("bound Go surface is complete (>= 11 contract methods)", () => {
  const exported = goMethods(goSrc);
  assert.ok(exported.size >= 11, `expected >=11 bound methods, found ${exported.size}`);
});

test("every DOM id the JS uses exists in index.html", () => {
  const ids = htmlIds(html);
  const missing = [...jsIds(appJs)].filter((id) => !ids.has(id));
  assert.deepEqual(missing, [], `index.html is missing ids: ${missing.join(", ")}`);
});

test("no colour literals leaked into JS/HTML styles", () => {
  const bad = [];
  const styleAttr = /style="[^"]*#[0-9a-fA-F]{3,8}/g;
  for (const file of [html, appJs]) {
    const hits = file.match(styleAttr);
    if (hits) bad.push(...hits);
  }
  assert.deepEqual(bad, [], "inline colours bypass the design tokens");
});

test("no original-product strings left behind", () => {
  const offenders = [];
  const banned = [/chitchat/i, /evachat/i, /persona/i];
  for (const [file, content] of Object.entries({ appJs, html, goSrc })) {
    for (const re of banned) {
      if (re.test(content)) offenders.push(`${file}: ${re}`);
    }
  }
  assert.deepEqual(offenders, []);
});

test("secrets are never committed", () => {
  const files = [appJs, html, goSrc, mockJs];
  const hits = files.flatMap((content) => content.match(/sk-[A-Za-z0-9]{16,}/g) || []);
  assert.deepEqual(hits, [], "found something shaped like a real API key");
});

// ---------------------------------------------------------------------------
// Draft protection behavior (minimal DOM stub, no browser required)
// ---------------------------------------------------------------------------
function loadAppForDraftTests() {
  const vm = require("node:vm");
  const elements = new Map();
  const makeEl = (tag, id, name, type) => {
    const listeners = {};
    const el = {
      tagName: String(tag).toUpperCase(),
      id: id || "",
      name: name || "",
      type: type || "",
      value: "",
      checked: false,
      hidden: false,
      textContent: "",
      style: {},
      dataset: {},
      classList: { toggle() {}, add() {}, remove() {} },
      addEventListener(event, fn) { (listeners[event] = listeners[event] || []).push(fn); },
      dispatch(event) { for (const fn of listeners[event] || []) fn({ target: el }); },
      appendChild() {},
      replaceChildren() {},
      querySelector() { return null; },
      remove() {},
    };
    if (id) elements.set(id, el);
    return el;
  };
  const document = {
    activeElement: null,
    documentElement: { dataset: {} },
    getElementById: (id) => elements.get(id) || null,
    createElement: (tag) => makeEl(tag),
    createDocumentFragment: () => ({ appendChild() {} }),
    querySelectorAll: (selector) => {
      if (selector === "input[id], textarea[id], select[id]") {
        return [...elements.values()].filter((el) => el.id && ["INPUT", "TEXTAREA", "SELECT"].includes(el.tagName));
      }
      if (selector === ".mode-option") return [];
      if (selector === 'input[name="msg-strategy"]') {
        return [...elements.values()].filter((el) => el.name === "msg-strategy");
      }
      return [];
    },
    querySelector: (selector) => {
      const m = selector.match(/input\[name="msg-strategy"\]\[value="([^"]+)"\]/);
      if (m) return [...elements.values()].find((el) => el.name === "msg-strategy" && el.value === m[1]) || null;
      return null;
    },
  };
  const context = vm.createContext({
    document,
    window: { addEventListener() {}, runtime: {} },
    localStorage: { getItem: () => null, setItem() {} },
    setInterval() {},
    setTimeout() { return 0; },
    clearTimeout() {},
    console,
  });
  const expose = "\n;globalThis.__draftTest = { dirtyFields, bindDraftTracking, markDirty, clearSectionDirty, setValue, saveSection, setStrategy, refreshSnapshot, browseFolder, httpInvoke, bridge };";
  vm.runInContext(appJs + expose, context);
  return { context, api: context.__draftTest, makeEl, document };
}

test("draft tracking protects blurred fields from snapshot writes", () => {
  const { api, makeEl, document } = loadAppForDraftTests();
  const entries = makeEl("textarea", "proxy-entries");
  entries.value = "local draft";
  api.bindDraftTracking();
  entries.dispatch("input");
  document.activeElement = null;
  api.setValue("proxy-entries", "server value");
  assert.equal(entries.value, "local draft");
  api.clearSectionDirty("connections");
  api.setValue("proxy-entries", "server value");
  assert.equal(entries.value, "server value");
});

test("radio strategy draft is protected until forced or saved", () => {
  const { api, makeEl } = loadAppForDraftTests();
  const single = makeEl("input", "msg-strategy-single", "msg-strategy", "radio");
  single.value = "single";
  single.checked = true;
  const folder = makeEl("input", "msg-strategy-folder", "msg-strategy", "radio");
  folder.value = "folder";
  api.bindDraftTracking();
  folder.dispatch("change");
  assert.ok(api.dirtyFields.has("msg-strategy"));
  api.setStrategy("folder");
  assert.equal(folder.checked, false);
  api.setStrategy("folder", true);
  assert.equal(folder.checked, true);
});

test("save success clears only its section; save failure keeps drafts", async () => {
  const { api, makeEl } = loadAppForDraftTests();
  makeEl("p", "proxy-status");
  makeEl("div", "toast-wrap");
  api.dirtyFields.add("proxy-entries");
  api.dirtyFields.add("set-reply-min");
  api.bridge.invoke = async () => null;
  await api.saveSection("connections", {}, "proxy-status");
  assert.equal(api.dirtyFields.has("proxy-entries"), false);
  assert.equal(api.dirtyFields.has("set-reply-min"), true);
  api.dirtyFields.add("proxy-entries");
  api.bridge.invoke = async () => { throw new Error("offline"); };
  await api.saveSection("connections", {}, "proxy-status");
  assert.equal(api.dirtyFields.has("proxy-entries"), true);
});

test("manual snapshot refresh keeps unsaved drafts", async () => {
  const { context, api } = loadAppForDraftTests();
  api.dirtyFields.add("proxy-entries");
  api.bridge.invoke = async () => ({});
  require("node:vm").runInContext("applySnapshot = () => {};", context);
  await api.refreshSnapshot(false);
  assert.equal(api.dirtyFields.has("proxy-entries"), true);
});

// ---------------------------------------------------------------------------
// HTTP route/method parity (Step 3)
// ---------------------------------------------------------------------------
function stubFetch(context) {
  const calls = [];
  context.fetch = async (url, init = {}) => {
    calls.push({ url, method: init.method || "GET", body: init.body });
    return { ok: true, json: async () => ({ ok: true }) };
  };
  return calls;
}

test("HTTP bridge sends POST for StopRun and ClearLogs", async () => {
  const { context, api } = loadAppForDraftTests();
  const calls = stubFetch(context);
  await api.httpInvoke("StopRun");
  await api.httpInvoke("ClearLogs");
  assert.deepEqual(calls, [
    { url: "/api/StopRun", method: "POST", body: "{}" },
    { url: "/api/ClearLogs", method: "POST", body: "{}" },
  ]);
});

test("HTTP bridge maps RegisterAccounts to the legacy register route", async () => {
  const { context, api } = loadAppForDraftTests();
  const calls = stubFetch(context);
  await api.httpInvoke("RegisterAccounts", 3);
  await api.httpInvoke("RegisterAccounts");
  assert.deepEqual(calls.map((call) => [call.url, call.method, JSON.parse(call.body)]), [
    ["/api/register", "POST", { count: 3 }],
    ["/api/register", "POST", { count: 1 }],
  ]);
});

test("HTTP bridge keeps browser status GET and maps OpenBrowser payload", async () => {
  const { context, api } = loadAppForDraftTests();
  const calls = stubFetch(context);
  await api.httpInvoke("GetBrowserStatus");
  await api.httpInvoke("OpenBrowser", "bot", "user-1");
  assert.equal(calls[0].url, "/api/browser/status");
  assert.equal(calls[0].method, "GET");
  assert.equal(calls[0].body, undefined);
  assert.equal(calls[1].url, "/api/chrome/open");
  assert.equal(calls[1].method, "POST");
  assert.deepEqual(JSON.parse(calls[1].body), { target: "bot", uid: "user-1" });
});

test("Browse folder in HTTP mode explains the desktop-only picker without a request", async () => {
  const { context, api, makeEl } = loadAppForDraftTests();
  const status = makeEl("p", "msg-folder-status");
  const input = makeEl("input", "msg-folder-path");
  let focused = false;
  input.focus = () => { focused = true; };
  api.bridge.kind = "http";
  let requested = false;
  context.fetch = async () => { requested = true; throw new Error("unexpected fetch"); };
  await api.browseFolder();
  assert.equal(requested, false);
  assert.equal(focused, true);
  assert.match(status.textContent, /desktop app/i);
  assert.match(status.textContent, /Type the folder path/i);
});

test("Browse folder in desktop mode still fills and marks the path dirty", async () => {
  const { api, makeEl } = loadAppForDraftTests();
  const status = makeEl("p", "msg-folder-status");
  const input = makeEl("input", "msg-folder-path");
  api.bridge.kind = "wails";
  api.bridge.api = { BrowseMessageFolder: async () => "D:\\sms" };
  await api.browseFolder();
  assert.equal(input.value, "D:\\sms");
  assert.ok(api.dirtyFields.has("msg-folder-path"));
  assert.match(status.textContent, /Folder selected/i);
});
