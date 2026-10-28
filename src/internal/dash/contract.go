// contract.go — design-kit API contract (08-API-CONTRACT.md) implemented on
// top of the existing dash internals. One surface, two transports: the Wails
// binding (DesktopApp in package main) and the HTTP mux both call these.
//
// Mapping decisions (the 30%) live here, NOT in the frontend:
//
//	setup        -> settings.txt keys (workspace_name, bot_mode, …)
//	connections  -> use_proxy + proxy_scheme + proxies.txt
//	credentials  -> message_settings.json ai_* fields
//	actions      -> chat_mode + msg/chat.txt + snaps.txt (+ folder_path)
//	timings      -> the six real worker knobs in settings.txt
//	profile      -> settings.txt keys (profile_name, browser_backend, headless)
package dash

import (
	"fmt"
	"strings"
	"time"

	"thundr/internal/proxyurl"
	"thundr/internal/reply"
	"thundr/internal/store"
)

// LogEntry is one structured row in the kit live-log contract.
type LogEntry struct {
	ID   int    `json:"id"`
	Time string `json:"time"`
	Kind string `json:"kind"` // in | out | skip | system | error
	Text string `json:"text"`
	Meta string `json:"meta,omitempty"`
}

// classifyLog maps a flat "HH:MM:SS text" line onto the kit's five kinds.
// Heuristics, not truth: the five kinds are a presentation aid, the line
// itself is never altered.
func classifyLog(id int, line string) LogEntry {
	timePart := ""
	text := line
	if len(line) > 9 && line[2] == ':' && line[5] == ':' {
		timePart = line[:8]
		text = strings.TrimSpace(line[9:])
	}
	kind := "system"
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "error"), strings.Contains(lower, "fail"), strings.Contains(lower, "banned"):
		kind = "error"
	case strings.Contains(lower, "skip"):
		kind = "skip"
	case strings.Contains(lower, " sent"), strings.Contains(lower, "reply"), strings.Contains(lower, "share"):
		kind = "out"
	case strings.Contains(lower, "match"), strings.Contains(lower, "partner"),
		strings.Contains(lower, "received"), strings.Contains(lower, "incoming"):
		kind = "in"
	}
	return LogEntry{ID: id, Time: timePart, Kind: kind, Text: text}
}

// kitStrategy maps the stored chat_mode onto the UI's three radio cards.
func kitStrategy(chatMode string) string {
	switch chatMode {
	case reply.ModeFolder:
		return "folder"
	case "api", "eva":
		return "model"
	default:
		return "single"
	}
}

// mergeTargets joins snaps.txt and telegram.txt so the single targets box
// shows every saved target (the same lines a Save splits back into both).
func mergeTargets(st *store.Store) string {
	snaps := strings.TrimRight(st.ReadFile("snaps"), "\r\n\t ")
	tgs := strings.TrimRight(st.ReadFile("telegram"), "\r\n\t ")
	switch {
	case snaps == "":
		return tgs
	case tgs == "":
		return snaps
	default:
		return snaps + "\n" + tgs
	}
}

func boolStr(v any) string {
	if b, ok := v.(bool); ok && b {
		return "true"
	}
	return "false"
}

func strOf(values map[string]any, key string) string {
	if v, ok := values[key]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}

// browserBackend normalises the profile.backend field. This project has exactly
// one browser driver (chromedp over CDP, real visible Chrome), so every value —
// including the reference kit's "rod" — resolves to it. Reporting "rod" here
// would advertise a backend that cannot be selected.
func browserBackend(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "rod", "chromedp", "cdp", "show_mode", "":
		return "chromedp"
	default:
		return "chromedp"
	}
}

// ContractSnapshot is the kit-shaped GetSnapshot payload.
func (a *App) ContractSnapshot() map[string]any {
	snap := a.GetSnapshot() // reuses the mutex + stats delta accumulation
	st := a.st

	msg := reply.DefaultSettings()
	var stored reply.Settings
	if st.ReadJSON("message_settings.json", &stored) {
		msg = mergeReplySettings(msg, stored)
	}

	settings := map[string]any{
		"setup": map[string]any{
			"name":            st.Get("workspace_name", "thundr"),
			"mode":            st.Get("bot_mode", "visible"),
			"concurrency":     st.Int("concurrency", 2),
			"timeout_seconds": st.Int("timeout_seconds", 60),
			"headless":        st.Bool("headless", false),
			"notes":           st.Get("notes", ""),
		},
		"connections": map[string]any{
			"enabled": st.Bool("use_proxy", false),
			"scheme":  proxyurl.Scheme(st),
			"entries": st.ReadFile("proxies"),
		},
		"credentials": map[string]any{
			"base_url":    msg.AIBaseURL,
			"api_key":     msg.AIAPIKey,
			"model":       msg.AIModel,
			"temperature": msg.APITemperature,
		},
		"actions": map[string]any{
			"strategy":    kitStrategy(msg.ChatMode),
			"sequence":    st.ReadFile("chat"),
			"targets":     mergeTargets(st),
			"folder_path": msg.FolderPath,
		},
		"timings": map[string]any{
			"reply_min":    st.Float("stranger_response_time_min", 2),
			"reply_max":    st.Float("stranger_response_time_max", 5),
			"silent_min":   st.Float("partner_no_response_secs_min", 10),
			"silent_max":   st.Float("partner_no_response_secs_max", 15),
			"reroll_min":   st.Float("reroll_delay_secs_min", 6),
			"reroll_max":   st.Float("reroll_delay_secs_max", 12),
			"instant_skip": st.Float("instant_skip_secs", 5),
			"work_time":    st.Float("thread_work_time", 600),
			"no_match":     st.Float("search_no_match_secs", 75),
			"sends_first":  st.Bool("bot_sends_first", false),
		},
		"profile": map[string]any{
			"name": st.Get("profile_name", "default"),
			// The only backend this project actually drives is chromedp (CDP).
			// The kit contract's "rod" default was a leftover from the reference
			// design; reporting it here advertised a backend that does not exist.
			"backend": browserBackend(st.Get("browser_backend", "chromedp")),
			"headless": st.Bool("headless", false),
		},
	}

	running, failed := 0, 0
	for _, s := range snap.BotStatus {
		switch s.State {
		case "running", "starting":
			running++
		case "error", "banned":
			failed++
		}
	}
	stats := map[string]any{
		"running":         running,
		"profiles":        len(snap.Accounts),
		"accounts":        len(snap.Accounts),
		"chats":           snap.Stats.Chats,
		"replies":         snap.Stats.Replies,
		"snap_shares":     snap.Stats.SnapShares,
		"telegram_shares": snap.Stats.TelegramShares,
		"banned":          snap.Stats.Banned,
		"failed":          failed,
	}

	items := make([]map[string]any, 0, len(snap.Accounts))
	for _, acc := range snap.Accounts {
		state, updated := "ok", ""
		if bs, ok := snap.BotStatus[acc.UserID]; ok && bs.State != "" {
			state = bs.State
			updated = bs.Updated
		}
		session := "—"
		if acc.HasSession {
			session = "saved"
		}
		items = append(items, map[string]any{
			"name":    acc.Email,
			"profile": session,
			"state":   state,
			"updated": updated,
		})
	}

	return map[string]any{
		"settings": settings,
		"strategy": kitStrategy(msg.ChatMode),
		"stats":    stats,
		"items":    items,
		"runtime": map[string]any{
			"running":  snap.BotsRunning,
			"kind":     "go",
			"data_dir": st.Dir,
			"version":  "thundr-1.0",
		},
	}
}

// SaveSection persists one kit settings section and returns the fresh
// snapshot (the contract rule: every mutation returns the new state).
func (a *App) SaveSection(section string, values map[string]any) (map[string]any, error) {
	st := a.st
	switch section {
	case "setup":
		// Partial saves are normal: StartRun sends only mode/concurrency.
		// Only keys present in values may overwrite stored settings; an
		// explicit false/empty value is still a real update.
		updates := map[string]string{}
		if _, ok := values["name"]; ok {
			updates["workspace_name"] = strOf(values, "name")
		}
		if _, ok := values["mode"]; ok {
			updates["bot_mode"] = normalizeMode(strOf(values, "mode"), st.Get("bot_mode", "visible"))
		}
		if _, ok := values["concurrency"]; ok {
			updates["concurrency"] = strOf(values, "concurrency")
		}
		if _, ok := values["timeout_seconds"]; ok {
			updates["timeout_seconds"] = strOf(values, "timeout_seconds")
		}
		if v, ok := values["headless"]; ok {
			updates["headless"] = boolStr(v)
		}
		if _, ok := values["notes"]; ok {
			updates["notes"] = strOf(values, "notes")
		}
		if len(updates) > 0 {
			st.SaveSettings(updates)
		}
	case "connections":
		scheme := proxyurl.NormalizeScheme(strOf(values, "scheme"))
		if scheme == "" {
			scheme = proxyurl.Scheme(st)
		}
		st.SaveSettings(map[string]string{
			"use_proxy":    boolStr(values["enabled"]),
			"proxy_scheme": scheme,
			// legacy key: the old dashboard wrote "proxytype" and older
			// settings.txt files still carry it, so keep both in sync.
			"proxytype": scheme,
		})
		st.WriteFile("proxies", strOf(values, "entries"))
	case "credentials":
		a.updateReplySettings(func(s *reply.Settings) {
			if v := strOf(values, "base_url"); v != "" {
				s.AIBaseURL = v
			}
			if v := strOf(values, "api_key"); v != "" {
				s.AIAPIKey = v
			}
			if v := strOf(values, "model"); v != "" {
				s.AIModel = v
			}
			if f, ok := values["temperature"].(float64); ok && f > 0 {
				s.APITemperature = f
			}
		})
	case "actions":
		chatMode := "single"
		switch strOf(values, "strategy") {
		case "folder":
			chatMode = reply.ModeFolder
		case "model":
			chatMode = "api"
		}
		a.updateReplySettings(func(s *reply.Settings) {
			s.ChatMode = chatMode
			if fp := strOf(values, "folder_path"); fp != "" {
				s.FolderPath = fp
			}
		})
		st.WriteFile("chat", strOf(values, "sequence"))
		// One UI box, two pools: @handles are telegram targets (the worker
		// reads them from telegram.txt), everything else is a snap target.
		var snaps, tgs []string
		for _, line := range strings.Split(strOf(values, "targets"), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if strings.HasPrefix(line, "@") {
				tgs = append(tgs, line)
			} else {
				snaps = append(snaps, line)
			}
		}
		st.WriteFile("snaps", strings.Join(snaps, "\n"))
		st.WriteFile("telegram", strings.Join(tgs, "\n"))
	case "timings":
		st.SaveSettings(map[string]string{
			"stranger_response_time_min":   strOf(values, "reply_min"),
			"stranger_response_time_max":   strOf(values, "reply_max"),
			"partner_no_response_secs_min": strOf(values, "silent_min"),
			"partner_no_response_secs_max": strOf(values, "silent_max"),
			"reroll_delay_secs_min":        strOf(values, "reroll_min"),
			"reroll_delay_secs_max":        strOf(values, "reroll_max"),
			"instant_skip_secs":            strOf(values, "instant_skip"),
			"thread_work_time":             strOf(values, "work_time"),
			"search_no_match_secs":         strOf(values, "no_match"),
			"bot_sends_first":              boolStr(values["sends_first"]),
		})
	case "profile":
		st.SaveSettings(map[string]string{
			"profile_name":        strOf(values, "name"),
			"browser_backend": browserBackend(strOf(values, "backend")),
			"headless":        boolStr(values["headless"]),
		})
	default:
		return nil, fmt.Errorf("unknown settings section %q", section)
	}
	return a.ContractSnapshot(), nil
}

// updateReplySettings load → mutate → save for message_settings.json.
func (a *App) updateReplySettings(mut func(*reply.Settings)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := reply.DefaultSettings()
	var stored reply.Settings
	if a.st.ReadJSON("message_settings.json", &stored) {
		s = mergeReplySettings(s, stored)
	}
	mut(&s)
	a.st.WriteJSON("message_settings.json", s)
}

// TestConnections runs the existing proxy check per line and returns the
// kit result shape: {entry, ok, latency_ms, message}.
func (a *App) TestConnections(entriesRaw string) []map[string]any {
	out := make([]map[string]any, 0, 8)
	for _, line := range strings.Split(entriesRaw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		res := testProxy(line)
		row := map[string]any{"entry": line, "ok": res["ok"] == true, "latency_ms": res["ms"]}
		if msg, ok := res["error"].(string); ok && msg != "" {
			row["message"] = msg
		} else if status, ok := res["status"].(int); ok {
			row["message"] = fmt.Sprintf("HTTP %d", status)
		}
		out = append(out, row)
	}
	return out
}

// TestAPICall makes one model call and returns a short human sentence.
func (a *App) TestAPICall(baseURL, apiKey, model string, temperature float64) (string, error) {
	if baseURL == "" {
		baseURL = "https://api.featherless.ai/v1"
	}
	if temperature == 0 {
		temperature = 0.9
	}
	t0 := time.Now()
	replyText, err := reply.TestAPI(baseURL, apiKey, model, temperature)
	if err != nil {
		return "", err
	}
	snippet := strings.TrimSpace(replyText)
	if len(snippet) > 60 {
		snippet = snippet[:60] + "…"
	}
	return fmt.Sprintf("OK · %d ms · %q", time.Since(t0).Milliseconds(), snippet), nil
}

// LogsAfter is the versioned log poll: only entries with id > after.
func (a *App) LogsAfter(after int) map[string]any {
	a.mu.Lock()
	version := a.logSeq
	cleared := a.clearedAt > after
	entries := make([]LogEntry, 0, len(a.logEntries))
	for _, e := range a.logEntries {
		if e.ID > after {
			entries = append(entries, e)
		}
	}
	running := len(a.cancels) > 0
	a.mu.Unlock()
	return map[string]any{"version": version, "entries": entries, "cleared": cleared, "running": running}
}

// ClearLog truncates both log buffers. Ids keep increasing so polls never
// repeat an entry.
func (a *App) ClearLog() {
	a.mu.Lock()
	a.logEntries = nil
	a.logBuf = nil
	a.clearedAt = a.logSeq
	a.mu.Unlock()
}

// LoadFolderDefault loads sequences from the saved folder_path.
func (a *App) LoadFolderDefault() (map[string]any, error) {
	msg := reply.DefaultSettings()
	var stored reply.Settings
	if a.st.ReadJSON("message_settings.json", &stored) {
		msg = mergeReplySettings(msg, stored)
	}
	if strings.TrimSpace(msg.FolderPath) == "" {
		return nil, fmt.Errorf("no folder path saved — set one in Actions first")
	}
	n, err := a.LoadSequences(msg.FolderPath)
	if err != nil {
		return nil, err
	}
	return map[string]any{"count": n, "names": []string{}}, nil
}

// OpenRootFolder opens the data directory itself in the OS file manager.
func (a *App) OpenRootFolder() string {
	dir := a.st.Dir
	openFolder(dir)
	return dir
}
