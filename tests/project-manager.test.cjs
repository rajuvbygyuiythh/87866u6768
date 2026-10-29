'use strict';

// project manager tests: isolated fixtures only; no runtime data or credentials.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const { createManager, isDenied, relativePath, newlineInfo } = require('../tools/project-manager.cjs');

const TOOL = path.join(__dirname, '..', 'tools', 'project-manager.cjs');

function fixture(t, overrides = {}) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'pm-test-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const map = {
    version: 1,
    project: 'fixture',
    default_scope: 'alpha',
    scopes: {
      alpha: {
        description: 'Alpha source',
        keywords: ['alpha', 'ui'],
        checks: ['node -e "require(\'fs\').writeFileSync(\'pwned\',\'1\')"'],
        files: [
          { path: 'a.js', role: 'Alpha behavior', editable: true, anchors: ['function alpha'] },
          { path: 'b.js', role: 'Reference only', editable: false, anchors: ['reference'] },
        ],
      },
      beta: {
        description: 'Beta source',
        keywords: ['beta'],
        checks: [],
        files: [{ path: 'other.js', role: 'Beta behavior', editable: true, anchors: ['beta'] }],
      },
    },
  };
  if (overrides.map) overrides.map(map);
  fs.writeFileSync(path.join(root, 'project-map.json'), JSON.stringify(map, null, 2));
  fs.writeFileSync(path.join(root, 'a.js'), 'function alpha() {\n  return 1;\n}\n');
  fs.writeFileSync(path.join(root, 'b.js'), 'reference\n');
  fs.writeFileSync(path.join(root, 'other.js'), 'beta\n');
  fs.writeFileSync(path.join(root, 'unlisted.js'), 'unlisted\n');
  try { fs.symlinkSync('a.js', path.join(root, 'link.js')); } catch (_) { /* platform may forbid symlinks */ }
  if (overrides.files) overrides.files(root);
  return { root, manager: createManager(root) };
}

function code(fn) {
  try { fn(); } catch (error) { return error.code; }
  return undefined;
}

test('project manager denies runtime, credential, log, and archive paths', () => {
  for (const rel of [
    'data/settings.txt', 'data/registered_creds.txt', 'browser-profiles/x/Cookies',
    'sessions/a.json', 'webview/Local Storage', '.env', '.env.local', 'app.log',
    'source.zip', 'api_key.json', 'cookies.json', 'auth-token.txt',
  ]) assert.equal(isDenied(rel), true, rel);
  assert.equal(isDenied('src/frontend/dist/app.js'), false);
});

test('project manager rejects traversal and absolute paths', () => {
  for (const rel of ['../x.js', '/x.js', 'C:\\x.js', 'a/../b.js', '', 'a//b.js']) {
    assert.throws(() => relativePath(rel), /path/i);
  }
  assert.equal(relativePath('src\\app.js'), 'src/app.js');
});

test('project manager refuses to load a map that allowlists sensitive paths', (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'pm-bad-map-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  fs.writeFileSync(path.join(root, 'project-map.json'), JSON.stringify({
    version: 1, default_scope: 'bad',
    scopes: { bad: { description: 'bad', keywords: [], checks: [], files: [
      { path: 'data/registered_creds.txt', role: 'bad', editable: true, anchors: [] },
    ] } },
  }));
  assert.equal(code(() => createManager(root)), 'E_DENIED');
});

test('project manager scopes find/read and never falls back to unlisted files', (t) => {
  const { manager } = fixture(t);
  const found = manager.find('alpha', { scope: 'alpha' });
  assert.equal(found.results[0].path, 'a.js');
  assert.equal(manager.find('beta', { scope: 'alpha' }).results.length, 0);
  assert.equal(manager.find('beta', { scope: 'beta' }).results[0].path, 'other.js');
  assert.equal(manager.read('a.js', { scope: 'alpha', start: 1, lines: 2 }).content.length, 2);
  assert.equal(code(() => manager.read('unlisted.js', { scope: 'alpha' })), 'E_SCOPE');
  assert.equal(code(() => manager.read('other.js', { scope: 'alpha' })), 'E_SCOPE');
  assert.equal(code(() => manager.read('../outside.js', { scope: 'alpha' })), 'E_PATH');
  assert.equal(code(() => manager.read('data/settings.txt', { scope: 'alpha' })), 'E_DENIED');
});

test('project manager rejects symlinked allowlisted files', (t) => {
  const { root, manager } = fixture(t, {
    map: (map) => map.scopes.alpha.files.push({ path: 'link.js', role: 'Symlink guard', editable: true, anchors: ['alpha'] }),
  });
  if (!fs.lstatSync(path.join(root, 'link.js')).isSymbolicLink()) return;
  assert.equal(code(() => manager.read('link.js', { scope: 'alpha' })), 'E_SYMLINK');
});

test('project manager route uses metadata and checks never execute commands', (t) => {
  const { root, manager } = fixture(t);
  assert.equal(manager.route('alpha ui').candidates[0].scope, 'alpha');
  const checks = manager.checks('alpha');
  assert.equal(checks.commands.length, 1);
  assert.equal(fs.existsSync(path.join(root, 'pwned')), false);
});

test('project manager verify reports stale anchors', (t) => {
  const { manager } = fixture(t, { map: (map) => { map.scopes.alpha.files[0].anchors = ['missing-symbol']; } });
  const result = manager.verify('alpha');
  assert.equal(result.ok, false);
  assert.equal(result.scopes[0].files[0].stale, true);
});

test('project manager redacts secret-shaped source output and refuses secret edits', (t) => {
  const secretLine = 'const apiKey = "sk-abcdefghijklmnopqrstuvwx";';
  const { manager } = fixture(t, { files: (root) => fs.writeFileSync(path.join(root, 'a.js'), `${secretLine}\n`) });
  const read = manager.read('a.js', { scope: 'alpha' });
  assert.equal(read.content[0].text, '[redacted: possible secret]');
  const found = manager.find('apiKey', { scope: 'alpha' });
  assert.equal(found.results[0].matches[0].text, '[redacted: possible secret]');
  assert.equal(code(() => manager.edit('a.js', { scope: 'alpha', old: secretLine, new: 'const apiKey = "";' })), 'E_SECRET');
});

test('project manager edit previews without writing and applies only with the exact plan', (t) => {
  const { root, manager } = fixture(t);
  const file = path.join(root, 'a.js');
  const before = fs.readFileSync(file, 'utf8');
  const preview = manager.edit('a.js', { scope: 'alpha', old: 'return 1;', new: 'return 2;' });
  assert.equal(preview.applied, false);
  assert.match(preview.plan, /^[a-f0-9]{64}$/);
  assert.equal(fs.readFileSync(file, 'utf8'), before);
  assert.equal(code(() => manager.edit('a.js', { scope: 'alpha', old: 'return 1;', new: 'return 2;', apply: true, confirm: '0'.repeat(64) })), 'E_CONFIRM');
  assert.equal(fs.readFileSync(file, 'utf8'), before);
  const applied = manager.edit('a.js', { scope: 'alpha', old: 'return 1;', new: 'return 2;', apply: true, confirm: preview.plan });
  assert.equal(applied.applied, true);
  assert.match(fs.readFileSync(file, 'utf8'), /return 2;/);
});

test('project manager edit rejects stale, ambiguous, readonly, and out-of-scope writes', (t) => {
  const { root, manager } = fixture(t);
  const file = path.join(root, 'a.js');
  const preview = manager.edit('a.js', { scope: 'alpha', old: 'return 1;', new: 'return 2;' });
  fs.writeFileSync(file, 'function alpha() {\n  return 3;\n}\n');
  assert.equal(code(() => manager.edit('a.js', { scope: 'alpha', old: 'return 1;', new: 'return 2;', apply: true, confirm: preview.plan })), 'E_MATCH');

  fs.writeFileSync(file, 'x\nx\n');
  assert.equal(code(() => manager.edit('a.js', { scope: 'alpha', old: 'x', new: 'y' })), 'E_MATCH');
  const multi = manager.edit('a.js', { scope: 'alpha', old: 'x', new: 'y', occurrences: 2 });
  assert.equal(multi.occurrences, 2);
  assert.equal(code(() => manager.edit('b.js', { scope: 'alpha', old: 'reference', new: 'changed' })), 'E_READONLY');
  assert.equal(code(() => manager.edit('other.js', { scope: 'alpha', old: 'beta', new: 'changed' })), 'E_SCOPE');
});

test('project manager preserves CRLF, LF, and UTF-8 BOM and rejects mixed endings', (t) => {
  const { root, manager } = fixture(t);
  const file = path.join(root, 'a.js');

  fs.writeFileSync(file, 'one\r\ntwo\r\n');
  let preview = manager.edit('a.js', { scope: 'alpha', old: 'one', new: 'ONE' });
  manager.edit('a.js', { scope: 'alpha', old: 'one', new: 'ONE', apply: true, confirm: preview.plan });
  assert.equal(fs.readFileSync(file, 'utf8'), 'ONE\r\ntwo\r\n');

  fs.writeFileSync(file, Buffer.concat([Buffer.from([0xef, 0xbb, 0xbf]), Buffer.from('hello\n')]));
  preview = manager.edit('a.js', { scope: 'alpha', old: 'hello', new: 'world' });
  const applied = manager.edit('a.js', { scope: 'alpha', old: 'hello', new: 'world', apply: true, confirm: preview.plan });
  const bytes = fs.readFileSync(file);
  assert.deepEqual([...bytes.subarray(0, 3)], [0xef, 0xbb, 0xbf]);
  assert.equal(bytes.toString('utf8'), '\ufeffworld\n');
  assert.equal(applied.afterHash, require('node:crypto').createHash('sha256').update(bytes).digest('hex'));

  fs.writeFileSync(file, 'one\r\ntwo\n');
  assert.equal(code(() => manager.edit('a.js', { scope: 'alpha', old: 'one', new: 'ONE' })), 'E_NEWLINE');
  assert.deepEqual(newlineInfo('a\r\nb\r\n'), { mixed: false, value: '\r\n' });
  assert.deepEqual(newlineInfo('a\nb\n'), { mixed: false, value: '\n' });
});

test('project manager CLI help, JSON scopes, and unknown-command errors', () => {
  const help = spawnSync(process.execPath, [TOOL, 'help'], { encoding: 'utf8' });
  assert.equal(help.status, 0);
  assert.match(help.stdout, /Scoped project manager/);

  const scopes = spawnSync(process.execPath, [TOOL, 'scopes', '--json'], { encoding: 'utf8' });
  assert.equal(scopes.status, 0);
  assert.equal(JSON.parse(scopes.stdout).find((scope) => scope.default).name, 'desktop-ui');

  const unknown = spawnSync(process.execPath, [TOOL, 'not-a-command', '--json'], { encoding: 'utf8' });
  assert.equal(unknown.status, 2);
  assert.equal(JSON.parse(unknown.stderr).ok, false);
});

test('project manager can scope YAML workflow files when allowlisted', (t) => {
  const { root, manager } = fixture(t, {
    map: (map) => map.scopes.alpha.files.push({ path: 'ci.yml', role: 'CI workflow', editable: true, anchors: ['name: CI'] }),
    files: (root) => fs.writeFileSync(path.join(root, 'ci.yml'), 'name: CI\n'),
  });
  assert.equal(isDenied('.github/workflows/ci.yml'), false);
  assert.equal(manager.read('ci.yml', { scope: 'alpha' }).content[0].text, 'name: CI');
});
