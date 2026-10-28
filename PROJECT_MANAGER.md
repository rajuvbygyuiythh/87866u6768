# Scoped Project Manager

A small, dependency-free helper for finding the right source file, staying inside a task scope, reading only the needed snippet, and previewing exact edits. It is a **developer tool**, not part of the bot runtime.

## Quick start

Requires Node.js 18+ (already used by the existing contract tests).

```sh
# Windows
pm.cmd scopes
pm.cmd route "desktop logs filter"
pm.cmd map --scope desktop-ui
pm.cmd find activateTab --scope desktop-ui
pm.cmd read src/frontend/dist/app.js --scope desktop-ui --start 157 --lines 20

# macOS / Linux / Git Bash
./pm scopes
./pm find activateTab --scope desktop-ui
```

Or call Node directly from the repository root:

```sh
node tools/project-manager.cjs help
```

## Privacy and access model

- **Explicit allowlist only.** Content reads are limited to files listed in `project-map.json` for the selected scope.
- **Never opens runtime/private state.** `data/`, browser profiles, sessions, WebView state, logs, credential files, and archives are denied even if someone tries to add them to the map.
- **No repository-wide scan.** `find` searches only the selected scope; `route` uses map metadata only.
- **No symlinks, absolute paths, or path traversal.**
- **No shell execution.** `checks` prints suggested commands; it never runs them.
- **Bounded reads.** Files over 1 MiB are rejected; `read` returns at most 200 lines.
- **Secret-shaped output is redacted.** This is a safety net, not permission to put secrets in source files.

The original `thundr-go-source.zip` is intentionally outside the helper's access model. It contains runtime/private entries and must not be opened by this tool.

## Scopes

| Scope | Purpose | Write access through helper |
|---|---|---|
| `desktop-ui` | Desktop Console HTML/JS/CSS, mock backend, UI contract tests | Yes, allowlisted files only |
| `desktop-contract` | Wails bindings and shared HTTP shapes | Read-only reference |
| `desktop-shell` | Startup, flags, embedding, packaging metadata | Read-only reference |
| `web-dashboard` | Separate legacy HTTP dashboard | Read-only reference |
| `project-tooling` | This helper, map, tests, and guide | Tests/docs only |

The default scope is `desktop-ui`. The desktop UI (`src/frontend/dist`) and legacy web dashboard (`src/internal/dash/index.html`) are different interfaces; do not edit one when the task means the other.

## Commands

```sh
pm scopes [--json]
pm route "short task description" [--json]
pm map [--scope NAME] [--json]
pm find QUERY [--scope NAME] [--regex] [--case-sensitive] [--limit 20] [--json]
pm read PATH [--scope NAME] [--start 1] [--lines 60] [--json]
pm edit PATH --old TEXT --new TEXT [--occurrences 1] [--scope NAME]
pm edit PATH --old TEXT --new TEXT --apply --confirm PLAN_ID [--scope NAME]
pm verify [--scope NAME] [--json]
pm checks [--scope NAME] [--json]
```

Use `pm.cmd` on Windows. Add `--json` for machine-readable output.

## Accurate edit workflow

1. **Route and confirm scope.**
   ```sh
   pm route "desktop logs filter"
   pm map --scope desktop-ui
   ```
2. **Find the exact symbol and read a bounded snippet.**
   ```sh
   pm find renderLogs --scope desktop-ui
   pm read src/frontend/dist/app.js --scope desktop-ui --start 486 --lines 50
   ```
3. **Preview an exact replacement.** The old text must occur exactly once by default.
   ```sh
   pm edit src/frontend/dist/app.js --scope desktop-ui \
     --old 'pollLogsMs: 1000' \
     --new 'pollLogsMs: 1500'
   ```
4. **Review the diff and plan id.** Nothing is written during preview.
5. **Apply only after approval**, repeating the same scope, old text, new text, and occurrence count:
   ```sh
   pm edit src/frontend/dist/app.js --scope desktop-ui \
     --old 'pollLogsMs: 1000' \
     --new 'pollLogsMs: 1500' \
     --apply --confirm PLAN_ID
   ```

The plan id binds the scope, path, current content hash, replacement text, and occurrence count. Apply fails if the file changed, the match count differs, the plan id differs, the file is reference-only, or the replacement looks like a secret. Writes use a same-directory temporary file and atomic rename. Existing LF/CRLF line endings and UTF-8 BOM are preserved; mixed line endings are rejected.

## Suggested verification

The helper does not run checks automatically. For desktop UI work:

```sh
node --check src/frontend/dist/app.js
node --check src/frontend/dist/js/mock-backend.js
node --test tests/contract.test.cjs
node --test tests/project-manager.test.cjs
node tools/project-manager.cjs verify
```

Go checks require the project's Go toolchain:

```sh
cd src
go test ./...
go build ./...
```

## Maintaining the map

`project-map.json` is the source of truth for access. Each entry has:

- `path`: canonical repository-relative source path.
- `role`: why the file belongs to the scope.
- `editable`: whether the helper may write it.
- `anchors`: stable text snippets used to detect stale mapping.

When adding a file, keep the scope narrow, prefer stable anchors over line numbers, and run `pm verify`. Never add runtime data, credentials, logs, archives, generated bindings, or browser profiles. Generated Wails bindings should be regenerated by the build, not hand-edited.

## Limitations

- This is a local developer helper, not a security sandbox against a malicious same-user process.
- Secret detection is heuristic; the primary protection is the explicit source allowlist.
- `route` is a metadata suggestion, not an automatic decision to edit.
- The helper does not build, launch, log in to, or automate the target website.
