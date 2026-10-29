# Changelog

All notable changes to this clean source branch are documented here.

## [Unreleased]

### Added

- Scoped project manager (`pm`, `pm.cmd`, `project-map.json`) for allowlisted
  source discovery, bounded reads, exact edit previews, and scope verification.
- CI quality gates: Node syntax/contract tests, Go format check, Go vet, internal
  Go tests, race tests, and Windows desktop/main-package build + vet.
- Atomic state-file writes with same-directory temp files, sync, rename, and
  cleanup on failure.
- Checked store write APIs and regression tests for write-error propagation.
- HTTP hardening tests for loopback Host/Origin guards, body limits, and graceful
  shutdown.
- Documentation for architecture, run modes, privacy, CI, and troubleshooting.
- Keyboard-accessible desktop tabs with ARIA tab semantics, roving tabindex,
  arrow/Home/End navigation, and a skip-to-content link.
- Visible focus indicators and reduced-motion support for the desktop UI.

### Changed

- Desktop save flows report success/failure explicitly and keep unsaved drafts
  when a save fails.
- Legacy dashboard settings sync is dirty-safe and shares server-side log
  clearing with the desktop contract.
- Demo mode is explicit (`?demo=1`); unavailable backends are surfaced instead of
  silently falling back to mock data.
- Go source formatting is normalized so `gofmt -l .` is a CI gate.
- GitHub Actions use current major versions and the Go module cache points at
  `src/go.sum`.
- Desktop status/log regions are labelled for assistive technology, and inactive
  tab panels are hidden from the accessibility tree.

### Fixed

- Partial setup saves no longer erase unrelated settings.
- Snapshot refreshes no longer overwrite blurred/unsaved desktop form drafts.
- Desktop HTTP bridge route/method mismatches for stop, clear, register, browser
  status, and browser launch calls.
- Disk/write failures are propagated through store, dashboard contract, legacy
  HTTP routes, and desktop/legacy UI save handlers instead of producing false
  “saved” results.
- Local HTTP server shutdown is graceful for headless Ctrl+C/SIGTERM and desktop
  close paths.

### Security

- The local dashboard rejects non-loopback Host headers and mismatched Origin
  headers.
- POST/PUT/PATCH bodies are capped at 4 MiB, including chunked bodies.
- HTTP server timeouts are enabled.
- Clean source export excludes runtime/private state from the current tree, and
  `.gitignore` guards common private paths.

### Clean-source reliability steps

| Step | Commit | Summary |
|---|---|---|
| Source export | `28e4a8d` | Clean source, run/build scripts, CI, partial setup save fix. |
| Draft safety | `767b588` | Protect unsaved desktop drafts from snapshot refreshes. |
| HTTP parity | `e17c4d4` | Fix desktop HTTP route/method parity. |
| Demo/backend state | `6834780` | Explicit demo mode and unavailable-backend messaging. |
| Legacy sync | `bf80a5e` | Dirty-safe legacy settings sync and shared log clearing. |
| A | `9ecd18e` | Atomic, concurrency-safe store writes. |
| B | `c0ff754` | Local HTTP hardening and graceful shutdown. |
| C | `6b257c8` | Propagate state write failures to save results. |
| D | `9fa918e` | CI quality gates and Go formatting normalization. |
