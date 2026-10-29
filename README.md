# Thundr Go — clean source

This branch contains the application source code only. Account files, sessions,
credentials, saved settings, proxies, browser profiles, WebView state, and private
logs are **not** included. The first run creates an empty local `data/` directory.

> Privacy note: the current tree/download is source-only. Older Git history was
> not rewritten by this cleanup, so review history separately before publishing a
> fork or mirror.

## Quick run (HTTP dashboard)

Requirements: Go 1.26.3 or newer.

From the repository root on Windows:

```bat
run-server.bat
```

Or manually, with an explicit data directory:

```bat
cd src
go run . -server -no-browser -data ..\data
```

Open http://127.0.0.1:8787

Useful flags:

| Flag | Meaning |
|---|---|
| `-server` | Run the headless HTTP dashboard only, without a desktop window. |
| `-no-browser` | In server mode, do not auto-open a browser. |
| `-port N` | Change the dashboard/API port from the default `8787`. |
| `-data DIR` | Choose the runtime data directory. |

The HTTP server is local-only. Use `localhost`, `127.0.0.1`, or `::1`; LAN IPs or
proxy hostnames are rejected.

## Desktop app (Wails)

Requirements: Go 1.26.3+, Wails CLI v2.17.0, and WebView2 Runtime on Windows.

```bat
cd src
wails dev
```

Build a Windows executable:

```bat
cd src
wails build
```

Or from the repository root:

```bat
build-desktop.bat
```

The desktop window uses the newer Console UI in `src/frontend/dist`. It also
starts the local HTTP API/dashboard on `127.0.0.1:8787` as a fallback.

## Architecture

There are two different user interfaces, but one shared Go engine:

```text
Desktop window (Wails)
  └─ src/frontend/dist          newer Console UI
       └─ DesktopApp bindings   src/app_desktop.go, src/app_contract.go
            └─ dash.App         src/internal/dash

Headless HTTP mode (-server)
  └─ src/internal/dash/index.html   legacy web dashboard
       └─ /api routes               src/internal/dash
            └─ dash.App             same engine

dash.App
  ├─ store.Store       atomic local state files
  ├─ bot.Bot           headless WebSocket mode
  ├─ browser           visible Chrome/CDP mode
  └─ reply.Manager     single/folder/EVA/API reply engines
```

Important directories:

| Path | Purpose |
|---|---|
| `src/frontend/dist` | Desktop Console UI (HTML/JS/CSS, no bundler). |
| `src/internal/dash` | Shared dashboard engine, HTTP routes, legacy UI. |
| `src/internal/store` | Local settings/account/file persistence. |
| `src/internal/bot` | Headless WebSocket bot transport. |
| `src/internal/browser` | Visible Chrome/CDP automation. |
| `src/internal/reply` | Reply/message engines. |
| `tests` | Node contract/tooling tests. |
| `tools/project-manager.cjs` | Scoped source discovery/edit helper. |
| `data/` | Runtime state created on first run; never commit it. |

Do not confuse the desktop UI (`src/frontend/dist`) with the legacy web dashboard
(`src/internal/dash/index.html`). They do not have identical features.

## Reliability and safety behavior

- State files are written atomically: same-directory temporary file, sync, then
  rename. Existing permissions are preserved where possible.
- Store reads/writes are concurrency-safe. Append-only account/credential files
  still use append semantics.
- Save APIs propagate disk/write errors to the UI. A failed save should not show a
  false “saved” message, and desktop drafts are kept when a save fails.
- The local HTTP server rejects non-loopback `Host` headers and mismatched
  `Origin` headers.
- POST/PUT/PATCH request bodies are capped at 4 MiB.
- HTTP timeouts are enabled, and Ctrl+C/SIGTERM/desktop shutdown drain the server
  gracefully.
- Demo mode is explicit: open the desktop UI with `?demo=1`. Without a real
  backend or explicit demo mode, the UI reports that the backend is unavailable.

## Tests and CI

Node-based contract/tooling tests (Node.js 22 recommended):

```bat
node --check src/frontend/dist/app.js
node --check src/frontend/dist/js/mock-backend.js
node --test tests/contract.test.cjs tests/project-manager.test.cjs
```

Go checks:

```bat
cd src
gofmt -l .
go vet ./...
go build ./...
go test ./internal/...
go test -race ./internal/...
```

GitHub CI runs:

- Node syntax checks and contract/tooling tests.
- Go format check, vet, internal tests, and race tests.
- Windows `go build ./...` and `go vet ./...` for the desktop/main package.

The tests do not launch the bot or perform live website automation.

## Scoped project manager

Use the helper to find and edit only task-relevant source files:

```bat
pm.cmd scopes
pm.cmd route "desktop logs filter"
pm.cmd map --scope desktop-ui
pm.cmd find renderLogs --scope desktop-ui
pm.cmd read src/frontend/dist/app.js --scope desktop-ui --start 1 --lines 40
pm.cmd verify --scope desktop-ui
```

On macOS/Linux/Git Bash, use `./pm` instead of `pm.cmd`. See
[`PROJECT_MANAGER.md`](PROJECT_MANAGER.md) for the full workflow. The helper never
opens runtime/private state and does not execute shell commands from map checks.

## Troubleshooting

| Symptom | Likely cause and fix |
|---|---|
| `go` is not recognized | Install Go 1.26.3+ and reopen the terminal so `PATH` updates. |
| `wails` is not recognized | Install the Wails CLI matching the project (v2.17.0) and ensure Go’s bin directory is on `PATH`. |
| Desktop window is blank or fails to start | Install/repair Microsoft Edge WebView2 Runtime, then rebuild with `wails build`. |
| `address already in use` / port 8787 busy | Stop the other instance, or run with `-port 8788` and open that port. |
| Browser shows `forbidden host` | Open the dashboard via `http://127.0.0.1:8787` or `http://localhost:8787`, not a LAN IP or proxy hostname. |
| Browser shows `forbidden origin` | The page was loaded from a different origin. Use the local dashboard URL directly. |
| `request body too large` | A save/import exceeded the 4 MiB HTTP body cap. Split the input or save a smaller file. |
| Save failed / settings did not persist | Check that the data directory is writable, the disk is not full, and no file has been replaced by a directory. The UI should keep unsaved drafts after a failed save. |
| UI says backend unavailable | Start the Go server/desktop app, or use `?demo=1` for explicit demo mode. |
| Visible-browser features fail | Install Chrome and check the configured debug port/profile. CI and unit tests do not require Chrome. |
| Data appears in the wrong folder | Pass `-data DIR` explicitly. `run-server.bat` uses the repository-root `data/` folder. |

## Privacy

Do not commit `data/`, `sessions/`, `browser-profiles/`, `webview/`,
`registered_*.txt`, `settings.txt`, `proxies.txt`, `message_settings.json`,
`snaps.txt`, `telegram.txt`, or log files. See `.gitignore`.

The scoped project manager also denies runtime/private paths, archives, logs, and
credential-shaped files even if someone tries to add them to the map.
