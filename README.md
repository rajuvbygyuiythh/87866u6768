# Thundr Go — clean source

This branch contains the application source code only. Account files, sessions,
credentials, saved settings, proxies, browser profiles, WebView state, and private
logs are **not** included. The first run creates an empty local `data/` directory.

## Quick run (HTTP dashboard)

Requirements: Go 1.26.3 or newer.

```bat
cd src
go run . -server -no-browser
```

Open http://127.0.0.1:8787

Or from the repository root on Windows:

```bat
run-server.bat
```

## Desktop app (Wails)

Requirements: Go 1.26.3+, Wails CLI v2.17.0, WebView2 Runtime on Windows.

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

## Tests

Node-based contract/tooling tests:

```bat
node --test tests/contract.test.cjs tests/project-manager.test.cjs
```

Go tests:

```bat
cd src
go test ./internal/...
```

## Privacy

Do not commit `data/`, `sessions/`, `browser-profiles/`, `webview/`,
`registered_*.txt`, `settings.txt`, `proxies.txt`, `message_settings.json`,
`snaps.txt`, `telegram.txt`, or log files. See `.gitignore`.
