#!/usr/bin/env node
'use strict';

// Scoped, dependency-free project helper. It never executes project commands,
// opens archives, walks the repository, or follows symlinks. Every content read
// must resolve to an explicit, non-sensitive entry in project-map.json.

const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const { parseArgs } = require('node:util');

const MAX_FILE_BYTES = 1024 * 1024;
const MAX_READ_LINES = 200;
const MAX_FIND_FILES = 100;
const ALLOWED_EXTENSIONS = new Set(['.go', '.js', '.cjs', '.mjs', '.ts', '.html', '.css', '.json', '.md', '.cmd', '.sh']);
const RUNTIME_DIRS = new Set(['data', 'browser-profiles', 'sessions', 'webview', 'node_modules', '.git', 'coverage', 'dist-old']);
const ARCHIVE_EXTENSIONS = new Set(['.zip', '.7z', '.rar', '.tar', '.gz', '.tgz', '.bz2', '.xz']);
const SENSITIVE_BASENAMES = new Set([
  'registered_accounts.txt', 'registered_creds.txt', 'proxies.txt', 'settings.txt',
  'message_settings.json', 'telegram.txt', 'snaps.txt', 'interests.txt',
  'loaded_sequences.json', 'chat.txt', '.netrc', '.git-credentials',
]);
const SECRET_PATTERNS = [
  /sk-[A-Za-z0-9_-]{16,}/,
  /-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----/,
  /\bBearer\s+[A-Za-z0-9._~+/-]{16,}/i,
  /\b(?:api[_-]?key|access[_-]?token|auth[_-]?token|password|secret)\b\s*["']?\s*[:=]\s*["'][^"'\r\n]{8,}["']/i,
  /[a-z][a-z0-9+.-]*:\/\/[^\s/:@]+:[^\s/@]+@/i,
];

class ManagerError extends Error {
  constructor(message, code = 'E_MANAGER') {
    super(message);
    this.name = 'ManagerError';
    this.code = code;
  }
}

function fail(message, code) {
  throw new ManagerError(message, code);
}

function sha256(value) {
  return crypto.createHash('sha256').update(value).digest('hex');
}

function relativePath(input) {
  if (typeof input !== 'string' || !input || input.includes('\0')) fail('Invalid relative path.', 'E_PATH');
  const normalized = input.replace(/\\/g, '/');
  if (normalized.startsWith('/') || /^[A-Za-z]:/.test(normalized)) fail('Absolute paths are not allowed.', 'E_PATH');
  const parts = normalized.split('/');
  if (parts.some((part) => !part || part === '.' || part === '..')) fail('Path traversal is not allowed.', 'E_PATH');
  return parts.join('/');
}

function isDenied(relative) {
  const parts = relative.split('/');
  const base = parts[parts.length - 1].toLowerCase();
  const ext = path.posix.extname(base);
  if (parts.some((part) => RUNTIME_DIRS.has(part.toLowerCase()) || part.toLowerCase().startsWith('.env'))) return true;
  if (SENSITIVE_BASENAMES.has(base) || ARCHIVE_EXTENSIONS.has(ext)) return true;
  if (/(^|[-_.])(creds?|credentials?|secrets?|cookies?|tokens?|api[-_]?key|apikey|private[-_]?key|access[-_]?key)([-_.]|$)/i.test(base)) return true;
  if (/\.(log|pem|key|p12|pfx|sqlite|db)$/i.test(base)) return true;
  return false;
}

function assertPlainFile(root, absolute) {
  const rel = path.relative(root, absolute);
  if (!rel || rel.startsWith('..') || path.isAbsolute(rel)) fail('Path escapes the project root.', 'E_PATH');
  let current = root;
  for (const part of rel.split(path.sep)) {
    current = path.join(current, part);
    let stat;
    try {
      stat = fs.lstatSync(current);
    } catch (error) {
      if (error.code === 'ENOENT') fail(`File is missing: ${rel.replace(/\\/g, '/')}`, 'E_MISSING');
      throw error;
    }
    if (stat.isSymbolicLink()) fail(`Symlinks are not allowed: ${rel.replace(/\\/g, '/')}`, 'E_SYMLINK');
  }
  const stat = fs.lstatSync(current);
  if (!stat.isFile()) fail(`Not a regular file: ${rel.replace(/\\/g, '/')}`, 'E_PATH');
  if (stat.size > MAX_FILE_BYTES) fail(`File exceeds the ${MAX_FILE_BYTES} byte read limit.`, 'E_SIZE');
  return stat;
}

function decode(bytes) {
  if (bytes.includes(0)) fail('Binary files are not readable by this helper.', 'E_BINARY');
  try {
    return new TextDecoder('utf-8', { fatal: true }).decode(bytes);
  } catch (_) {
    fail('File is not valid UTF-8.', 'E_ENCODING');
  }
}

function looksSecret(text) {
  return SECRET_PATTERNS.some((pattern) => pattern.test(text));
}

function safeLine(line) {
  return looksSecret(line) ? '[redacted: possible secret]' : line;
}

function newlineInfo(text) {
  const crlf = (text.match(/\r\n/g) || []).length;
  const lf = (text.match(/\n/g) || []).length;
  const cr = (text.match(/\r/g) || []).length;
  if (crlf && (lf > crlf || cr > crlf)) return { mixed: true, value: '\n' };
  if (!crlf && lf && cr) return { mixed: true, value: '\n' };
  return { mixed: false, value: crlf ? '\r\n' : cr ? '\r' : '\n' };
}

function normalizeNewlines(text, newline) {
  return text.replace(/\r\n|\r|\n/g, newline);
}

function validateMap(map) {
  if (!map || map.version !== 1 || !map.scopes || typeof map.scopes !== 'object' || Array.isArray(map.scopes)) {
    fail('project-map.json must use version 1 and a scopes object.', 'E_MAP');
  }
  const names = Object.keys(map.scopes);
  if (!names.length || !names.includes(map.default_scope)) fail('Map needs scopes and a valid default_scope.', 'E_MAP');
  for (const name of names) {
    const scope = map.scopes[name];
    if (!/^[a-z0-9-]+$/.test(name) || !scope || !Array.isArray(scope.files) || !scope.files.length) {
      fail(`Invalid scope: ${name}`, 'E_MAP');
    }
    if (typeof scope.description !== 'string' || !Array.isArray(scope.keywords) || !Array.isArray(scope.checks)) fail(`Scope needs a description, keywords, and checks: ${name}`, 'E_MAP');
    if (scope.keywords.some((word) => typeof word !== 'string') || scope.checks.some((command) => typeof command !== 'string')) fail(`Scope keywords/checks must be strings: ${name}`, 'E_MAP');
    const seen = new Set();
    for (const entry of scope.files) {
      if (!entry || typeof entry.path !== 'string' || typeof entry.role !== 'string' || typeof entry.editable !== 'boolean' || !Array.isArray(entry.anchors)) {
        fail(`Invalid file entry in scope ${name}.`, 'E_MAP');
      }
      const rel = relativePath(entry.path);
      if (rel !== entry.path || seen.has(rel)) fail(`Duplicate or non-canonical map path: ${entry.path}`, 'E_MAP');
      if (isDenied(rel)) fail(`Sensitive/runtime path is forbidden even in the allowlist: ${rel}`, 'E_DENIED');
      const ext = path.posix.extname(rel);
      if (!ALLOWED_EXTENSIONS.has(ext) && rel !== 'pm') fail(`Unsupported source extension: ${rel}`, 'E_MAP');
      if (entry.anchors.some((anchor) => typeof anchor !== 'string' || !anchor || looksSecret(anchor))) {
        fail(`Invalid anchor in ${rel}`, 'E_MAP');
      }
      seen.add(rel);
    }
  }
  return map;
}

function countOccurrences(text, needle) {
  let count = 0;
  let offset = 0;
  while ((offset = text.indexOf(needle, offset)) !== -1) {
    count += 1;
    offset += needle.length;
  }
  return count;
}

function splitLines(text) {
  if (text === '') return [];
  const lines = text.split(/\r\n|\r|\n/);
  if (lines.length > 1 && lines[lines.length - 1] === '') lines.pop();
  return lines;
}

function unifiedDiff(relative, before, after) {
  const a = splitLines(before);
  const b = splitLines(after);
  let prefix = 0;
  while (prefix < a.length && prefix < b.length && a[prefix] === b[prefix]) prefix += 1;
  let suffix = 0;
  while (suffix < a.length - prefix && suffix < b.length - prefix && a[a.length - 1 - suffix] === b[b.length - 1 - suffix]) suffix += 1;
  const aEnd = a.length - suffix;
  const bEnd = b.length - suffix;
  const start = Math.max(0, prefix - 3);
  const end = Math.min(a.length, aEnd + 3);
  const out = [`--- a/${relative}`, `+++ b/${relative}`];
  const oldCount = end - start;
  const newCount = oldCount - (aEnd - prefix) + (bEnd - prefix);
  out.push(`@@ -${start + 1},${oldCount} +${start + 1},${newCount} @@`);
  for (let i = start; i < prefix; i += 1) out.push(` ${safeLine(a[i])}`);
  for (let i = prefix; i < aEnd; i += 1) out.push(`-${safeLine(a[i])}`);
  for (let i = prefix; i < bEnd; i += 1) out.push(`+${safeLine(b[i])}`);
  for (let i = aEnd; i < end; i += 1) out.push(` ${safeLine(a[i])}`);
  if (!/\r\n|\r|\n$/.test(before)) out.push('\\ No newline at end of old file');
  if (!/\r\n|\r|\n$/.test(after)) out.push('\\ No newline at end of new file');
  return out.join('\n');
}

function positiveInteger(value, fallback, max, label) {
  if (value === undefined) return fallback;
  if (!/^\d+$/.test(String(value))) fail(`${label} must be a positive integer.`, 'E_ARG');
  const number = Number(value);
  if (!Number.isSafeInteger(number) || number < 1 || number > max) fail(`${label} must be between 1 and ${max}.`, 'E_ARG');
  return number;
}

function createManager(rootDir) {
  const root = fs.realpathSync(path.resolve(rootDir));
  const mapAbsolute = path.join(root, 'project-map.json');
  assertPlainFile(root, mapAbsolute);
  const map = validateMap(JSON.parse(decode(fs.readFileSync(mapAbsolute))));

  function scopeName(name) {
    const selected = name === undefined ? map.default_scope : name;
    if (typeof selected !== 'string' || !Object.hasOwn(map.scopes, selected)) fail(`Unknown scope: ${selected}. Run the scopes command.`, 'E_SCOPE');
    return selected;
  }

  function entryFor(relative, scope, forEdit = false) {
    const rel = relativePath(relative);
    if (isDenied(rel)) fail('Sensitive, runtime, archive, or credential paths are never readable.', 'E_DENIED');
    const selected = scopeName(scope);
    const entry = map.scopes[selected].files.find((item) => item.path === rel);
    if (!entry) fail(`${rel} is not in scope ${selected}. Choose the correct scope; the helper will not search elsewhere.`, 'E_SCOPE');
    if (forEdit && !entry.editable) fail(`${rel} is reference-only in scope ${selected}.`, 'E_READONLY');
    return { entry, selected, absolute: path.join(root, ...rel.split('/')) };
  }

  function readEntry(target) {
    const stat = assertPlainFile(root, target.absolute);
    const bytes = fs.readFileSync(target.absolute);
    if (bytes.length !== stat.size) fail('File changed while reading; retry.', 'E_STALE');
    const bom = bytes.length >= 3 && bytes[0] === 0xef && bytes[1] === 0xbb && bytes[2] === 0xbf;
    return { bytes, text: decode(bytes), hash: sha256(bytes), mode: stat.mode & 0o777, bom };
  }

  function anchorReport(entry, text) {
    const lines = splitLines(text);
    return entry.anchors.map((anchor) => {
      const matches = [];
      lines.forEach((line, index) => {
        if (line.includes(anchor) && matches.length < 20) matches.push(index + 1);
      });
      return { anchor, lines: matches, stale: matches.length === 0 };
    });
  }

  function inspectScope(name) {
    const selected = scopeName(name);
    const scope = map.scopes[selected];
    const files = scope.files.map((entry) => {
      const target = entryFor(entry.path, selected);
      try {
        const { text } = readEntry(target);
        const anchors = anchorReport(entry, text);
        return { path: entry.path, role: entry.role, editable: entry.editable, exists: true, anchors, stale: anchors.some((a) => a.stale) };
      } catch (error) {
        if (!(error instanceof ManagerError)) throw error;
        return { path: entry.path, role: entry.role, editable: entry.editable, exists: false, anchors: [], stale: true, error: error.message };
      }
    });
    return { scope: selected, description: scope.description, files, ok: files.every((file) => file.exists && !file.stale) };
  }

  function scopes() {
    return Object.entries(map.scopes).map(([name, scope]) => ({
      name, description: scope.description, keywords: scope.keywords, files: scope.files.length, default: name === map.default_scope,
    }));
  }

  function verify(name) {
    const names = name === undefined ? Object.keys(map.scopes) : [scopeName(name)];
    const results = names.map(inspectScope);
    return { ok: results.every((result) => result.ok), scopes: results };
  }

  function route(task) {
    if (typeof task !== 'string' || !task.trim()) fail('Provide a short task description.', 'E_ARG');
    const words = task.toLowerCase().match(/[a-z0-9_-]+/g) || [];
    const ranked = Object.entries(map.scopes).map(([name, scope]) => {
      const keywords = scope.keywords.map((word) => word.toLowerCase());
      const hits = words.filter((word) => keywords.includes(word));
      const files = scope.files.map((entry) => {
        const haystack = `${entry.path} ${entry.role} ${entry.anchors.join(' ')}`.toLowerCase();
        const score = words.filter((word) => haystack.includes(word)).length;
        return { path: entry.path, role: entry.role, editable: entry.editable, score };
      }).filter((file) => file.score > 0).sort((a, b) => b.score - a.score || a.path.localeCompare(b.path));
      return { scope: name, score: hits.length * 3 + files.reduce((sum, file) => sum + file.score, 0), matchedKeywords: hits, files };
    }).filter((item) => item.score > 0).sort((a, b) => b.score - a.score || a.scope.localeCompare(b.scope));
    return { task, candidates: ranked.slice(0, 3), note: 'Suggestions only. Confirm the scope before reading or editing.' };
  }

  function find(query, options = {}) {
    if (typeof query !== 'string' || !query) fail('Provide a search query.', 'E_ARG');
    const selected = scopeName(options.scope);
    const limit = positiveInteger(options.limit, 20, MAX_FIND_FILES, 'limit');
    let matcher;
    try {
      matcher = options.regex ? new RegExp(query, options.caseSensitive ? '' : 'i') : null;
    } catch (error) {
      fail(`Invalid regular expression: ${error.message}`, 'E_ARG');
    }
    const matchesQuery = (value) => {
      if (matcher) {
        matcher.lastIndex = 0;
        return matcher.test(value);
      }
      return options.caseSensitive ? value.includes(query) : value.toLowerCase().includes(query.toLowerCase());
    };
    const results = [];
    for (const entry of map.scopes[selected].files) {
      const target = entryFor(entry.path, selected);
      const { text } = readEntry(target);
      const metadata = [entry.path, entry.role, ...entry.anchors];
      const metadataHits = metadata.filter(matchesQuery).length;
      const hits = [];
      let total = 0;
      splitLines(text).forEach((line, index) => {
        if (matchesQuery(line)) {
          total += 1;
          if (hits.length < 5) hits.push({ line: index + 1, text: safeLine(line.trim()).slice(0, 300) });
        }
      });
      const score = metadataHits * 5 + total;
      if (score > 0) results.push({ path: entry.path, role: entry.role, editable: entry.editable, score, totalContentMatches: total, matches: hits });
    }
    results.sort((a, b) => b.score - a.score || a.path.localeCompare(b.path));
    return { scope: selected, query, results: results.slice(0, limit), totalFiles: results.length };
  }

  function read(relative, options = {}) {
    const target = entryFor(relative, options.scope);
    const { text } = readEntry(target);
    const lines = splitLines(text);
    const start = positiveInteger(options.start, 1, Math.max(1, lines.length), 'start');
    const count = positiveInteger(options.lines, 60, MAX_READ_LINES, 'lines');
    const end = Math.min(lines.length, start + count - 1);
    return {
      path: target.entry.path, scope: target.selected, start, end, totalLines: lines.length,
      content: lines.slice(start - 1, end).map((line, index) => ({ line: start + index, text: safeLine(line) })),
    };
  }

  function checks(scope) {
    const selected = scopeName(scope);
    return { scope: selected, commands: [...map.scopes[selected].checks], note: 'Suggested commands only; the helper never executes them.' };
  }

  function edit(relative, options = {}) {
    const target = entryFor(relative, options.scope, true);
    if (typeof options.old !== 'string' || !options.old) fail('edit requires non-empty --old text.', 'E_ARG');
    if (typeof options.new !== 'string') fail('edit requires --new text (empty is allowed).', 'E_ARG');
    if (looksSecret(options.old) || looksSecret(options.new)) fail('Refusing to edit text that looks like a secret.', 'E_SECRET');
    const expected = positiveInteger(options.occurrences, 1, 100, 'occurrences');
    if (Boolean(options.apply) !== Boolean(options.confirm)) fail('Apply requires both --apply and --confirm PLAN_ID. Preview is the default.', 'E_CONFIRM');
    const current = readEntry(target);
    const newline = newlineInfo(current.text);
    if (newline.mixed) fail('Mixed line endings; normalize the file deliberately before using exact edit.', 'E_NEWLINE');
    const oldText = normalizeNewlines(options.old, newline.value);
    const newText = normalizeNewlines(options.new, newline.value);
    if (oldText === newText) fail('Old and new text are identical.', 'E_ARG');
    const occurrences = countOccurrences(current.text, oldText);
    if (occurrences !== expected) fail(`Expected ${expected} exact occurrence(s), found ${occurrences}. No write performed.`, 'E_MATCH');
    const afterText = current.text.split(oldText).join(newText);
    const afterBytes = Buffer.concat([
      current.bom ? Buffer.from([0xef, 0xbb, 0xbf]) : Buffer.alloc(0),
      Buffer.from(afterText, 'utf8'),
    ]);
    const plan = sha256(JSON.stringify([target.selected, target.entry.path, current.hash, oldText, newText, expected]));
    const diff = unifiedDiff(target.entry.path, current.text, afterText);
    const result = { path: target.entry.path, scope: target.selected, occurrences, beforeHash: current.hash, plan, diff, applied: false };
    if (!options.apply) return result;
    if (typeof options.confirm !== 'string' || !/^[a-f0-9]{64}$/.test(options.confirm)) fail('Invalid plan id; preview the edit again.', 'E_CONFIRM');
    const supplied = Buffer.from(options.confirm, 'hex');
    const expectedPlan = Buffer.from(plan, 'hex');
    if (supplied.length !== expectedPlan.length || !crypto.timingSafeEqual(supplied, expectedPlan)) fail('Plan id does not match this exact edit and file version.', 'E_CONFIRM');
    const recheck = readEntry(target);
    if (recheck.hash !== current.hash) fail('File changed after preview; no write performed.', 'E_STALE');
    const temp = path.join(path.dirname(target.absolute), `.pm-${crypto.randomBytes(10).toString('hex')}.tmp`);
    let fd;
    try {
      fd = fs.openSync(temp, 'wx', current.mode);
      fs.writeFileSync(fd, afterBytes);
      fs.fsyncSync(fd);
      fs.closeSync(fd);
      fd = undefined;
      fs.renameSync(temp, target.absolute);
    } catch (error) {
      if (fd !== undefined) fs.closeSync(fd);
      try { fs.unlinkSync(temp); } catch (_) { /* best-effort cleanup */ }
      throw error;
    }
    return { ...result, applied: true, afterHash: sha256(afterBytes) };
  }

  return { root, map, scopes, inspectScope, verify, route, find, read, checks, edit };
}

const HELP = `Scoped project manager

Usage:
  node tools/project-manager.cjs scopes [--json]
  node tools/project-manager.cjs route "desktop logs filter" [--json]
  node tools/project-manager.cjs map [--scope desktop-ui] [--json]
  node tools/project-manager.cjs find QUERY [--scope desktop-ui] [--regex] [--case-sensitive] [--limit 20] [--json]
  node tools/project-manager.cjs read PATH [--scope desktop-ui] [--start 1] [--lines 60] [--json]
  node tools/project-manager.cjs edit PATH --old TEXT --new TEXT [--occurrences 1] [--scope desktop-ui]
  node tools/project-manager.cjs edit PATH --old TEXT --new TEXT --apply --confirm PLAN_ID
  node tools/project-manager.cjs verify [--scope desktop-ui] [--json]
  node tools/project-manager.cjs checks [--scope desktop-ui] [--json]

Privacy: explicit allowlist only; no archive, runtime data, credentials, symlinks, or shell execution.
Edits: preview by default; exact occurrence count; plan id binds scope, path, content hash, and replacement.
`;

function printResult(value, json) {
  if (json) {
    process.stdout.write(`${JSON.stringify(value, null, 2)}\n`);
    return;
  }
  if (typeof value === 'string') {
    process.stdout.write(value);
    return;
  }
  if (value.diff) {
    process.stdout.write(`${value.diff}\n\n${value.applied ? 'Applied' : 'Preview only'}: ${value.path}\nPlan: ${value.plan}\n`);
    if (!value.applied) process.stdout.write('Review the diff, then repeat with --apply --confirm PLAN_ID.\n');
    return;
  }
  if (value.content) {
    process.stdout.write(`${value.path} (${value.start}-${value.end} of ${value.totalLines})\n`);
    for (const line of value.content) process.stdout.write(`${String(line.line).padStart(5, ' ')} | ${line.text}\n`);
    return;
  }
  if (value.results) {
    process.stdout.write(`Scope: ${value.scope}; matching files: ${value.totalFiles}\n`);
    for (const file of value.results) {
      process.stdout.write(`\n${file.path} ${file.editable ? '[edit]' : '[reference]'} — ${file.role}\n`);
      for (const hit of file.matches) process.stdout.write(`  ${hit.line}: ${hit.text}\n`);
    }
    return;
  }
  if (value.candidates) {
    for (const candidate of value.candidates) {
      process.stdout.write(`${candidate.scope} (score ${candidate.score})\n`);
      for (const file of candidate.files.slice(0, 5)) process.stdout.write(`  ${file.path} — ${file.role}\n`);
    }
    if (!value.candidates.length) process.stdout.write('No metadata match. Run scopes, then use find inside a confirmed scope.\n');
    return;
  }
  if (value.commands) {
    process.stdout.write(`Suggested checks for ${value.scope} (not executed):\n`);
    for (const command of value.commands) process.stdout.write(`  ${command}\n`);
    return;
  }
  process.stdout.write(`${JSON.stringify(value, null, 2)}\n`);
}

function main(argv = process.argv.slice(2)) {
  if (!argv.length || argv[0] === 'help' || argv.includes('--help')) {
    process.stdout.write(HELP);
    return 0;
  }
  const [command, ...rest] = argv;
  let values = {};
  let positionals = [];
  try {
    const parsed = parseArgs({
      args: rest,
      allowPositionals: true,
      strict: true,
      options: {
        scope: { type: 'string' }, json: { type: 'boolean', default: false },
        regex: { type: 'boolean', default: false }, 'case-sensitive': { type: 'boolean', default: false },
        limit: { type: 'string' }, start: { type: 'string' }, lines: { type: 'string' },
        old: { type: 'string' }, new: { type: 'string' }, occurrences: { type: 'string' },
        apply: { type: 'boolean', default: false }, confirm: { type: 'string' },
      },
    });
    values = parsed.values;
    positionals = parsed.positionals;
    const manager = createManager(path.join(__dirname, '..'));
    let result;
    switch (command) {
      case 'scopes': result = manager.scopes(); break;
      case 'route': result = manager.route(positionals[0]); break;
      case 'map': result = manager.inspectScope(values.scope); break;
      case 'find': result = manager.find(positionals[0], { scope: values.scope, regex: values.regex, caseSensitive: values['case-sensitive'], limit: values.limit }); break;
      case 'read': result = manager.read(positionals[0], { scope: values.scope, start: values.start, lines: values.lines }); break;
      case 'edit': result = manager.edit(positionals[0], { scope: values.scope, old: values.old, new: values.new, occurrences: values.occurrences, apply: values.apply, confirm: values.confirm }); break;
      case 'verify': result = manager.verify(values.scope); break;
      case 'checks': result = manager.checks(values.scope); break;
      default: fail(`Unknown command: ${command}. Run help.`, 'E_ARG');
    }
    printResult(result, values.json);
    return result && result.ok === false ? 1 : 0;
  } catch (error) {
    const message = error instanceof ManagerError || error.code === 'ERR_PARSE_ARGS_UNKNOWN_OPTION' || error.code === 'ERR_PARSE_ARGS_INVALID_OPTION_VALUE'
      ? error.message : `Project manager failed: ${error.code || error.name}`;
    if (values.json) process.stderr.write(`${JSON.stringify({ ok: false, error: message })}\n`);
    else process.stderr.write(`error: ${message}\n`);
    return 2;
  }
}

if (require.main === module) process.exitCode = main();
module.exports = { createManager, ManagerError, isDenied, relativePath, newlineInfo, unifiedDiff };
