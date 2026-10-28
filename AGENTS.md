# Agent guide

## Privacy first

- Do not open `thundr-go-source.zip`; it contains runtime/private entries.
- Do not read or print credential files, account data, saved settings, proxies, browser profiles, sessions, WebView state, or private logs.
- Use `project-map.json` and `tools/project-manager.cjs` for scoped source discovery. Do not fall back to a repository-wide grep when a task can be scoped.
- If a needed file is not in the map, ask before expanding scope. Never add sensitive/runtime paths to the map.

## Scoping workflow

1. Run `pm route "task"` (or `pm.cmd` on Windows) and confirm the intended interface.
2. Run `pm map --scope NAME` and `pm verify --scope NAME`.
3. Use `pm find` and bounded `pm read` inside that scope.
4. Keep desktop UI (`src/frontend/dist`) separate from the legacy web dashboard (`src/internal/dash/index.html`).
5. Treat generated Wails bindings as read-only; regenerate them through the build when needed.

## Editing

- Preview with `pm edit`; show the diff and get approval before `--apply --confirm`.
- Use exact, minimal replacements. Do not bypass occurrence-count or stale-file checks.
- Preserve existing formatting and line endings.
- Run the suggested checks after changes. Do not launch the bot or perform live website automation as part of a tooling/UI task.
