# NOTES.md — Thundr Console (design-kit 30% decisions)

Built from `project design kit` (chitchat-design-kit v1.0.0). This file records the 30%
decisions made before coding, per `10-REBRAND-FOR-NEW-SITE.md` Step 0.

## Step 0 · Decisions

```
PRODUCT NAME        : Thundr Console
INITIALS (2 chars)  : TC
SUBTITLE (1 line)   : Bot operations · Go + WebView
MODE A (name)       : Studio   job: configure → run once → watch
MODE B (name)       : Accounts job: register/manage accounts + always-on worker
SECTION NAMES       :  A: 1 Setup · 2 Connections · 3 Actions · 4 Run · 5 Live Logs
                       B: 1 Register · 2 Connections · 3 Messages · 4 Timings · 5 Live Logs
SETTINGS STRUCTS    :  setup{name,mode,concurrency,timeout_seconds,headless,notes}
                       connections{enabled,scheme,entries}      (proxies)
                       credentials{base_url,api_key,model,temperature}
                       actions{strategy,sequence,targets}
                       timings{poll_min/max,action_min/max,timeout_min/max,retry_min/max}
                       profile{name,backend,headless}
CREDENTIALS         : base_url / api_key / model  (OpenAI-compatible /chat/completions)
TEST verb           : "Test Connections" tests proxy lines (existing TestProxy)
                      "Test API" does one model call (NEW, openai-client pattern)
RUN verb            : "Run" saves all sections → StartBots(uids, mode)
LOG KINDS           : in / out / skip / system / error  (all five reused)
DATA FOLDER         : data/  contains proxies.txt, snaps.txt, telegram.txt,
                      interests.txt, msg/chat.txt, sessions/, msg sequences
LIST TABLE          : columns: Account · Profile · State · Updated
TARGET PLATFORM     : desktop (Wails) primary; HTTP fallback already in dash.Serve
```

## Backend mapping (kit contract → existing thundr code)

| Kit method | thundr-go | Action |
|---|---|---|
| `GetSnapshot` | `dash.App.GetSnapshot` via `DesktopApp.GetState` | shape adapter in Go |
| `SaveSettings(section, values)` | `Store.SaveSettings(map)` | section-aware wrapper |
| `TestConnection` | `dash.App.TestProxyEntry` | reuse as-is |
| `TestAPI` | — | NEW (stdlib http, 1 MB cap, timeout) |
| `StartRun` / `StopRun` | `StartBots` / `StopBots` | wrapper returning `{ok,message}` |
| `LaunchLogin` | `OpenChrome(target, uid)` | wrapper |
| `GetLogs(after)` | event-only today | NEW log ring with version cursor |
| `ClearLogs` | — | NEW |
| `OpenDataFolder` | `dash.App.OpenDataFolder` | reuse |
| `LoadFolder` | `LoadSequences` | reuse |
| `RegisterAccounts` | `dash.App.RegisterAccounts` | Mode B card 1 |
| `DeleteAccount` | `Store.DeleteAccount` | table row action |

## Deviations from kit (deliberate)

- **No BadgerDB** — kit's own decision tree allows JSON files for a handful of values;
  thundr's existing `Store` (settings.txt, message_settings.json) is kept.
- **Two transports wired**: Wails binding + in-page mock. HTTP mode already exists in
  `dash.Serve`; the bridge probe stays.
- Account file editors (snaps/telegram/interests/chat) live in Mode B §3 Messages via
  GetFile/SaveFile — presented as strategy textareas, not new components.
