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
