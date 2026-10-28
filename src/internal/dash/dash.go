// Package dash is the Go port of dashboard.py: an embedded-UI control panel
// with in-process bot management (goroutines instead of subprocesses).
package dash

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"thundr/internal/bot"
	"thundr/internal/browser"
	"thundr/internal/proxyurl"
	"thundr/internal/redact"
	"thundr/internal/reply"
	"thundr/internal/store"
)

//go:embed index.html
var indexHTML []byte

type registerStatus struct {
	Running bool   `json:"running"`
	Message string `json:"message"`
	Done    bool   `json:"done"`
}

type App struct {
	st *store.Store

	mu       sync.Mutex
	register registerStatus
	cancels  map[string]context.CancelFunc // uid -> stop func
	events   chan bot.Event
	logBuf   []string

	// Structured, versioned log ring for the kit contract (GetLogs/ClearLogs).
	// Appended in lockstep with logBuf; ids never reuse after a clear.
	logSeq     int
	logEntries []LogEntry
	clearedAt  int

	// OnLog, when set, is invoked with each formatted log line after it is
	// appended (the desktop shell forwards these to the frontend as events).
	OnLog func(string)

	stats    SessionStats
	statSeen map[string]bot.Status // per-worker last-seen counters for delta accumulation

	drainOnce  sync.Once
	httpServer *http.Server
}

func Open(dataDir string) *App {
	return &App{
		st:       store.Open(dataDir),
		cancels:  map[string]context.CancelFunc{},
		events:   make(chan bot.Event, 512),
		statSeen: map[string]bot.Status{},
	}
}

func (a *App) appendLog(line string) {
	line = redact.Redact(line) // credentials never reach the UI/event hook
	a.mu.Lock()
	a.logBuf = append(a.logBuf, line)
	if len(a.logBuf) > 400 {
		a.logBuf = a.logBuf[len(a.logBuf)-400:]
	}
	a.logSeq++
	a.logEntries = append(a.logEntries, classifyLog(a.logSeq, line))
	if len(a.logEntries) > 400 {
		a.logEntries = a.logEntries[len(a.logEntries)-400:]
	}
	hook := a.OnLog
	a.mu.Unlock()
	if hook != nil {
		hook(line)
	}
}

// drainEvents moves bot events into the dashboard log buffer.
func (a *App) drainEvents() {
	for ev := range a.events {
		a.appendLog(ev.At + " " + ev.Text)
	}
}

// liveBots tracks running bots for status reporting.
var liveBots = struct {
	sync.Mutex
	m map[string]func() bot.Status
}{m: map[string]func() bot.Status{}}

// ------------------------------------------------------------------ handlers

func (a *App) state(w http.ResponseWriter, _ *http.Request) {
	// one source of truth: the same snapshot the desktop shell renders.
	snap := a.GetSnapshot()
	writeJSON(w, map[string]any{
		"settings":   snap.Settings,
		"accounts":   snap.Accounts,
		"bot_status": snap.BotStatus,
		"register":   snap.Register,
		"stats":      snap.Stats,
		"bots": map[string]any{
			"running": snap.BotsRunning,
			"message": snap.BotsMessage,
		},
	})
}

func (a *App) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("content-type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
	})
	mux.HandleFunc("GET /api/state", a.state)
	mux.HandleFunc("GET /api/logs", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		buf := append([]string{}, a.logBuf...)
		a.mu.Unlock()
		out := ""
		if n := len(buf); n > 0 {
			start := 0
			if n > 120 {
				start = n - 120
			}
			for _, l := range buf[start:] {
				out += l + "\n"
			}
		}
		writeJSON(w, map[string]string{"content": out})
	})
	mux.HandleFunc("POST /api/settings", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		a.st.SaveSettings(body)
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/file/get", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeJSON(w, map[string]string{"content": a.st.ReadFile(body.Name)})
	})
	mux.HandleFunc("POST /api/file/save", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name    string `json:"name"`
			Content string `json:"content"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeJSON(w, map[string]bool{"ok": a.st.WriteFile(body.Name, body.Content)})
	})
	mux.HandleFunc("POST /api/proxy/test", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Proxy string `json:"proxy"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeJSON(w, testProxy(body.Proxy))
	})

	// ---- message modes (ChitChat-style) --------------------------------------
	mux.HandleFunc("POST /api/message/get", func(w http.ResponseWriter, _ *http.Request) {
		s := reply.DefaultSettings()
		var stored reply.Settings
		if a.st.ReadJSON("message_settings.json", &stored) {
			s = mergeReplySettings(s, stored)
		}
		writeJSON(w, map[string]any{
			"settings":          s,
			"sequences_loaded":  len(reply.FolderSequences(a.st)),
			"has_share_targets": len(a.st.LoadList("snaps")) > 0 || len(a.st.LoadList("telegram")) > 0,
		})
	})
	mux.HandleFunc("POST /api/message/save", func(w http.ResponseWriter, r *http.Request) {
		var s reply.Settings
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if s.ChatMode == "" {
			s.ChatMode = reply.ModeSingle
		}
		ok := a.st.WriteJSON("message_settings.json", s)
		writeJSON(w, map[string]any{"ok": ok})
	})
	mux.HandleFunc("POST /api/sequences/load", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		path := strings.TrimSpace(body.Path)
		if path == "" {
			writeJSON(w, map[string]any{"ok": false, "error": "empty folder path"})
			return
		}
		n, err := reply.LoadFolderSequences(a.st, path)
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		a.appendLog(time.Now().Format("15:04:05") + fmt.Sprintf(" [message] loaded %d sequences from %s", n, path))
		writeJSON(w, map[string]any{"ok": true, "count": n})
	})
	mux.HandleFunc("POST /api/sequences/clean", func(w http.ResponseWriter, _ *http.Request) {
		reply.CleanFolderSequences(a.st)
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/apichat/test", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			BaseURL     string  `json:"base_url"`
			APIKey      string  `json:"api_key"`
			Model       string  `json:"model"`
			Temperature float64 `json:"temperature"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.BaseURL == "" {
			body.BaseURL = "https://api.featherless.ai/v1"
		}
		if body.Temperature == 0 {
			body.Temperature = 0.9
		}
		t0 := time.Now()
		replyText, err := reply.TestAPI(body.BaseURL, body.APIKey, body.Model, body.Temperature)
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "reply": replyText, "ms": time.Since(t0).Milliseconds()})
	})
	mux.HandleFunc("POST /api/open-folder", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Which string `json:"which"` // eva | api
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		var dir string
		switch body.Which {
		case "eva":
			if err := reply.SeedEVA(a.st); err != nil {
				writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			dir = a.st.DirPath("eva_chat")
		case "api":
			if err := reply.SeedAPI(a.st); err != nil {
				writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			dir = a.st.DirPath("api_chat")
		default:
			writeJSON(w, map[string]any{"ok": false, "error": "unknown folder"})
			return
		}
		openFolder(dir)
		writeJSON(w, map[string]any{"ok": true, "path": dir})
	})
	mux.HandleFunc("POST /api/register", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Count int `json:"count"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		ok, msg := a.startRegister(body.Count)
		writeJSON(w, map[string]any{"ok": ok, "message": msg})
	})
	mux.HandleFunc("POST /api/bots/start", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			UIDs []string `json:"uids"`
			Mode string   `json:"mode"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		ok, msg := a.startBots(body.UIDs, body.Mode)
		writeJSON(w, map[string]any{"ok": ok, "message": msg})
	})
	mux.HandleFunc("POST /api/bots/stop", func(w http.ResponseWriter, _ *http.Request) {
		ok, msg := a.stopBots()
		writeJSON(w, map[string]any{"ok": ok, "message": msg})
	})
	mux.HandleFunc("POST /api/chrome/open", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Target string `json:"target"` // "register" or "bot"
			UID    string `json:"uid"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		ok, attached := a.OpenChrome(body.Target, body.UID)
		status := browser.Status(a.st)
		problem, _ := status["problem"].(string)
		msg := ""
		switch {
		case ok && attached:
			msg = "browser is up (debug port answering)"
		case ok && !attached:
			msg = "browser launched, debug port not answering yet"
		case problem != "":
			msg = problem
		default:
			msg = "browser did not open - see live logs"
		}
		writeJSON(w, map[string]any{"ok": ok, "attached": attached, "message": msg, "problem": problem})
	})
	// Browser diagnostics: what the dashboard needs to tell the operator why a
	// browser did not open (chrome missing, watcher task missing, port down).
	mux.HandleFunc("GET /api/browser/status", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, browser.Status(a.st))
	})
	mux.HandleFunc("POST /api/browser/close", func(w http.ResponseWriter, _ *http.Request) {
		browser.CloseAll(a.st)
		a.appendLog(time.Now().Format("15:04:05") + " [chrome] every browser closed (register, manual, worker base)")
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/accounts/delete", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			UserID string `json:"user_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		a.st.DeleteAccount(body.UserID)
		writeJSON(w, map[string]bool{"ok": true})
	})

	// ---- design-kit contract (same shapes as the Wails-bound methods) ---------
	mux.HandleFunc("GET /api/GetSnapshot", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, a.ContractSnapshot())
	})
	mux.HandleFunc("POST /api/SaveSettings", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Section string         `json:"section"`
			Values  map[string]any `json:"values"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		snap, err := a.SaveSection(body.Section, body.Values)
		writeContractJSON(w, snap, err)
	})
	mux.HandleFunc("POST /api/TestConnection", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Entries string `json:"entries"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeJSON(w, map[string]any{"results": a.TestConnections(body.Entries)})
	})
	mux.HandleFunc("POST /api/TestAPI", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			BaseURL     string  `json:"base_url"`
			APIKey      string  `json:"api_key"`
			Model       string  `json:"model"`
			Temperature float64 `json:"temperature"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		msg, err := a.TestAPICall(body.BaseURL, body.APIKey, body.Model, body.Temperature)
		writeContractJSON(w, msg, err)
	})
	mux.HandleFunc("POST /api/StartRun", func(w http.ResponseWriter, _ *http.Request) {
		ok, msg := a.startBots(nil, a.st.Get("bot_mode", "visible"))
		writeJSON(w, map[string]any{"ok": ok, "message": msg})
	})
	mux.HandleFunc("POST /api/StopRun", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = a.stopBots()
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/LaunchLogin", func(w http.ResponseWriter, _ *http.Request) {
		ok, attached := a.OpenChrome("register", "")
		writeJSON(w, map[string]any{"ok": ok, "attached": attached})
	})
	mux.HandleFunc("POST /api/GetLogs", func(w http.ResponseWriter, r *http.Request) {
		// the JS bridge posts the bare cursor number as the body
		var raw any
		_ = json.NewDecoder(r.Body).Decode(&raw)
		after := 0
		switch v := raw.(type) {
		case float64:
			after = int(v)
		case map[string]any:
			if n, ok := v["after"].(float64); ok {
				after = int(n)
			}
		}
		writeJSON(w, a.LogsAfter(after))
	})
	mux.HandleFunc("POST /api/ClearLogs", func(w http.ResponseWriter, _ *http.Request) {
		a.ClearLog()
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/OpenDataFolder", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, a.OpenRootFolder())
	})
	mux.HandleFunc("POST /api/LoadFolder", func(w http.ResponseWriter, _ *http.Request) {
		res, err := a.LoadFolderDefault()
		writeContractJSON(w, res, err)
	})
	mux.HandleFunc("POST /api/LoadMessageFolder", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		n, err := a.LoadSequences(body.Path)
		writeContractJSON(w, map[string]any{"ok": true, "count": n}, err)
	})
	mux.HandleFunc("POST /api/CleanLoadedMessages", func(w http.ResponseWriter, _ *http.Request) {
		a.CleanSequences()
		writeJSON(w, map[string]any{"ok": true})
	})
	return mux
}

// writeContractJSON is the kit error shape: {"error":"..."} + 400 on failure.
func writeContractJSON(w http.ResponseWriter, v any, err error) {
	if err != nil {
		w.Header().Set("content-type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, v)
}

// ------------------------------------------------------------------ actions

func (a *App) startRegister(count int) (bool, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.register.Running {
		return false, "registration already running"
	}
	if count < 1 {
		count = 1
	}
	if count > 20 {
		count = 20
	}
	a.register = registerStatus{Running: true, Message: "launching visible browser..."}
	go func() {
		log := func(msg string) {
			a.mu.Lock()
			a.register.Message = msg
			a.mu.Unlock()
			a.appendLog(time.Now().Format("15:04:05") + " [register] " + msg)
		}
		proxy := a.proxyURL()
		accs := browser.RegisterAccounts(context.Background(), a.st, count, proxy, log)
		a.mu.Lock()
		a.register.Running = false
		a.register.Done = true
		a.register.Message = fmt.Sprintf("registered %d/%d", len(accs), count)
		a.mu.Unlock()
	}()
	return true, "started"
}

func (a *App) startBots(uids []string, mode string) (bool, string) {
	mode = normalizeMode(mode, a.st.Get("bot_mode", "visible"))
	// Persist the run mode: the desktop UI's StartRun() reads settings.txt
	// instead of taking a payload, so the legacy dashboard's radio has to
	// write it or the two UIs disagree after a restart.
	a.st.SaveSettings(map[string]string{"bot_mode": mode})
	var accounts []store.Account
	for _, acc := range a.st.Accounts() {
		if len(uids) > 0 {
			found := false
			for _, u := range uids {
				if u == acc.UserID {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		accounts = append(accounts, acc)
	}
	if len(accounts) == 0 {
		return false, "no accounts selected (need sessions for visible mode)"
	}
	// Workers (threads): how many accounts run together. The dashboard's
	// Home input persists this as "concurrency" (setup section); 0/blank
	// settings fall back to 1 — exactly one worker per digit the user types.
	workers := a.st.Int("concurrency", 1)
	if workers < 1 {
		workers = 1
	}
	if workers > 10 {
		workers = 10
	}
	if len(accounts) > workers {
		accounts = accounts[:workers]
		a.appendLog(fmt.Sprintf("%s [worker] concurrency=%d - starting the first %d of %d accounts (raise Workers to run more)",
			time.Now().Format("15:04:05"), workers, workers, len(accounts)+0))
	}
	// Reference-style worker plan: proxies assigned round-robin, each visible
	// worker gets its own chrome debug port, status carries UA/proxy identity.
	proxyLines := a.st.LoadList("proxies")
	useProxy := a.st.Bool("use_proxy", false)
	proxyScheme := proxyurl.Scheme(a.st)
	basePort := 9222
	if p := a.st.Get("chrome_attach_url", ""); p != "" {
		if i := lastIndexByte(p, ':'); i >= 0 {
			if n, err := strconv.Atoi(strings.TrimSuffix(p[i+1:], "/")); err == nil && n > 0 {
				basePort = n
			}
		}
	}
	if mode == "visible" {
		// windows must equal the thread amount: any register/manual chrome left
		// open is NOT a worker — close it before launching the workers.
		browser.CloseChromeOn(browser.RegisterURL(a.st))
		browser.CloseChromeOn(browser.ManualURL(a.st))
	}
	started := 0
	// ports come from the pool of FREE ports (basePort upward): the old
	// "basePort + call index" collided with already-running workers from a
	// previous Start call — two bots then drove the SAME chrome window.
	liveBots.Lock()
	usedPorts := map[int]bool{}
	for _, s := range visStatus {
		if s.Port > 0 && s.State != "" && s.State != "finished" && s.State != "error" && s.State != "banned" {
			usedPorts[s.Port] = true
		}
	}
	liveBots.Unlock()
	nextPort := basePort
	for idx, acc := range accounts {
		a.mu.Lock()
		if _, exists := a.cancels[acc.UserID]; exists {
			a.mu.Unlock()
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		a.cancels[acc.UserID] = cancel
		a.mu.Unlock()

		proxy := ""
		proxyLabel := "direct"
		if useProxy && len(proxyLines) > 0 {
			line := proxyLines[idx%len(proxyLines)]
			// The scheme comes from the dashboard (http / https / socks5); before
			// this every line was forced to http:// and socks5 never connected.
			proxy = proxyurl.Normalize(line, proxyScheme)
			proxyLabel = proxyurl.Label(proxy)
		}
		uaSummary := "Chrome/" + store.ChromeMajor(a.st.UserAgentFor(acc.UserID))
		for usedPorts[nextPort] {
			nextPort++
		}
		workerPort := nextPort
		usedPorts[nextPort] = true
		nextPort++
		workerAttach := fmt.Sprintf("http://127.0.0.1:%d", workerPort)
		if mode != "visible" {
			workerAttach = "" // ws mode needs no chrome
		}
		a.appendLog(fmt.Sprintf("%s [worker] %s -> port=%d proxy=%s ua=%s",
			time.Now().Format("15:04:05"), acc.UserID[:8], workerPort, proxyLabel, uaSummary))
		if mode != "ws" && !acc.HasSession {
			a.appendLog(fmt.Sprintf("%s [worker] %s -> no saved profile or session: this account needs a manual login first",
				time.Now().Format("15:04:05"), acc.UserID[:8]))
		}

		if mode == "ws" {
			b := bot.New(a.st, acc, proxy, a.events)
			liveBots.Lock()
			liveBots.m[acc.UserID] = b.Status
			liveBots.Unlock()
			go func(acc store.Account, b *bot.Bot) {
				b.Run(ctx)
				if b.Banned() {
					a.removeBannedAccount(acc)
				}
				a.cleanupBot(acc.UserID)
			}(acc, b)
		} else {
			cb := browser.Callbacks{
				Log: func(uid, state, msg string) {
					a.events <- bot.Event{UID: uid, Text: msg, At: time.Now().Format("15:04:05")}
				},
				Status: func(uid string, matches, replies, line int) {
					liveBots.Lock()
					if _, ok := visStatus[uid]; !ok {
						visStatus[uid] = &bot.Status{Tag: uid[:8]}
					}
					s := visStatus[uid]
					s.Matches, s.Replies, s.Line = matches, replies, line
					s.Updated = time.Now().Format("15:04:05")
					if s.State == "" || s.State == "starting" {
						s.State = "running"
					}
					liveBots.Unlock()
				},
				MatchEnd: func(uid, cls string, why []string) {
					liveBots.Lock()
					if s, ok := visStatus[uid]; ok {
						switch cls {
						case "bot":
							s.Bots++
						case "real":
							s.Reals++
						default:
							s.Unknown++
						}
					}
					liveBots.Unlock()
				},
				Share: func(uid, platform string) {
					liveBots.Lock()
					if s, ok := visStatus[uid]; ok {
						switch platform {
						case "snap":
							s.SnapShares++
						case "both":
							s.SnapShares++
							s.TelegramShares++
						default: // "tg"
							s.TelegramShares++
						}
					}
					liveBots.Unlock()
				},
			}
			st := &bot.Status{State: "starting", Tag: acc.UserID[:8], Proxy: proxyLabel, UA: uaSummary, Port: workerPort}
			visStatus[acc.UserID] = st
			liveBots.Lock()
			liveBots.m[acc.UserID] = func() bot.Status { return *st }
			liveBots.Unlock()
			go func(acc store.Account, attach string) {
				if err := browser.RunVisibleBot(ctx, a.st, acc, proxy, cb, attach); err != nil {
					a.events <- bot.Event{UID: acc.UserID, Text: "[" + acc.UserID[:8] + "] error: " + err.Error(), At: time.Now().Format("15:04:05")}
					var banErr browser.BannedError
					if errors.As(err, &banErr) {
						st.State = "banned"
						a.removeBannedAccount(acc)
					} else {
						st.State = "error"
					}
					st.LastEvent = err.Error()
				} else {
					st.State = "finished"
				}
				a.cleanupBot(acc.UserID)
			}(acc, workerAttach)
		}
		started++
	}
	return started > 0, fmt.Sprintf("%d bot(s) started (%s mode)", started, mode)
}

var visStatus = map[string]*bot.Status{}

// removeBannedAccount drops a banned account from the store (reference
// behaviour) and cleans up its chrome profile in the background.
func (a *App) removeBannedAccount(acc store.Account) {
	a.mu.Lock()
	a.stats.Banned++ // in-memory session counter (Home tab card)
	a.mu.Unlock()
	a.appendLog(fmt.Sprintf("%s [banned] account REMOVED from store: %s (%s)",
		time.Now().Format("15:04:05"), acc.Email, acc.UserID[:8]))
	a.st.DeleteAccount(acc.UserID)
	go func() {
		time.Sleep(3 * time.Second) // let its chrome close first
		if err := a.st.RemoveProfile(acc.UserID); err != nil {
			a.appendLog(time.Now().Format("15:04:05") + " [banned] profile cleanup: " + err.Error())
		}
	}()
}

func (a *App) cleanupBot(uid string) {
	a.mu.Lock()
	delete(a.cancels, uid)
	delete(a.statSeen, uid)
	a.mu.Unlock()
	liveBots.Lock()
	delete(liveBots.m, uid)
	liveBots.Unlock()
}

func (a *App) stopBots() (bool, string) {
	a.mu.Lock()
	n := len(a.cancels)
	for _, cancel := range a.cancels {
		cancel()
	}
	a.mu.Unlock()
	if n == 0 {
		return false, "no bots running"
	}
	// Stop must return the desktop to 0 bot windows: after the goroutines
	// detach, close every worker chrome over its debug port.
	go func() {
		time.Sleep(2 * time.Second)
		liveBots.Lock()
		ports := map[int]bool{}
		for _, s := range visStatus {
			if s.Port > 0 {
				ports[s.Port] = true
			}
		}
		liveBots.Unlock()
		for p := range ports {
			browser.CloseChromeOn(fmt.Sprintf("http://127.0.0.1:%d", p))
		}
	}()
	return true, fmt.Sprintf("stopping %d bot(s)", n)
}

func (a *App) proxyURL() string {
	if !a.st.Bool("use_proxy", false) {
		return ""
	}
	proxies := a.st.LoadList("proxies")
	if len(proxies) == 0 {
		return ""
	}
	return proxyurl.Normalize(proxies[0], proxyurl.Scheme(a.st))
}

// normalizeMode keeps bot_mode readable: only "visible" and "ws" are real.
// Empty or unknown values fall back to the given default, then to visible.
func normalizeMode(mode, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "visible":
		return "visible"
	case "ws", "websocket", "headless":
		return "ws"
	}
	switch strings.ToLower(strings.TrimSpace(fallback)) {
	case "ws":
		return "ws"
	}
	return "visible"
}

func lastIndexByte(s string, c byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func splitN(s, sep string, n int) []string {
	return strings.SplitN(s, sep, n)
}

// mergeReplySettings overlays stored values on the defaults (same rules as
// reply.NewManager, so the dashboard shows exactly what bots will use).
func mergeReplySettings(base, o reply.Settings) reply.Settings {
	if o.ChatMode != "" {
		base.ChatMode = o.ChatMode
	}
	if o.SharePlatform != "" {
		base.SharePlatform = o.SharePlatform
	}
	base.FolderPath = o.FolderPath
	if o.AIBaseURL != "" {
		base.AIBaseURL = o.AIBaseURL
	}
	base.AIAPIKey = o.AIAPIKey
	if o.AIModel != "" {
		base.AIModel = o.AIModel
	}
	if o.APITemperature != 0 {
		base.APITemperature = o.APITemperature
	}
	if o.APIShareTurn > 0 {
		base.APIShareTurn = o.APIShareTurn
	}
	return base
}

func openFolder(dir string) {
	if runtime.GOOS == "windows" {
		_ = exec.Command("explorer", dir).Start()
		return
	}
	_ = exec.Command("xdg-open", dir).Start()
}

func testProxy(proxy string) map[string]any {
	if proxy == "" {
		return map[string]any{"ok": false, "error": "empty proxy"}
	}
	u := proxy
	if parts := splitN(proxy, ":", 4); len(parts) == 4 {
		u = "http://" + parts[2] + ":" + parts[3] + "@" + parts[0] + ":" + parts[1]
	} else if !strings.Contains(u, "://") {
		u = "http://" + u // host:port with no scheme
	}
	pu, err := url.Parse(u)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	client := &http.Client{
		Timeout:   12 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyURL(pu)},
	}
	t0 := time.Now()
	resp, err := client.Get("https://thundr.com/api/geo")
	ms := time.Since(t0).Milliseconds()
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	defer resp.Body.Close()
	buf := make([]byte, 120)
	n, _ := resp.Body.Read(buf)
	return map[string]any{"ok": resp.StatusCode == 200, "ms": ms,
		"status": resp.StatusCode, "body": string(buf[:n])}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("content-type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// ------------------------------------------------------------------ desktop API
// Exported surface used by the wails desktop shell (app_desktop.go). These wrap
// the same internals as the HTTP handlers so both UIs behave identically.

// Snapshot is everything the desktop frontend needs for one render pass.
type Snapshot struct {
	Settings    map[string]string     `json:"settings"`
	Accounts    []store.Account       `json:"accounts"`
	BotStatus   map[string]bot.Status `json:"bot_status"`
	Register    registerStatus        `json:"register"`
	BotsRunning bool                  `json:"bots_running"`
	BotsMessage string                `json:"bots_message"`
	Logs        []string              `json:"logs"`
	Stats       SessionStats          `json:"stats"`
}

// SessionStats are in-memory lifetime counters of THIS app run (reset to 0
// every time the dashboard app starts — never persisted).
type SessionStats struct {
	Chats          int `json:"chats"`
	Replies        int `json:"replies"`
	SnapShares     int `json:"snap_shares"`
	TelegramShares int `json:"telegram_shares"`
	Banned         int `json:"banned"`
}

// StartEventDrain launches the bot-event -> log pump exactly once per App.
func (a *App) StartEventDrain() {
	a.drainOnce.Do(func() { go a.drainEvents() })
}

// GetSnapshot returns the full UI state plus the recent log tail.
func (a *App) GetSnapshot() Snapshot {
	a.mu.Lock()
	reg := a.register
	running := len(a.cancels)
	logs := append([]string{}, a.logBuf...)
	a.mu.Unlock()

	liveBots.Lock()
	statuses := map[string]bot.Status{}
	for uid, fn := range liveBots.m {
		statuses[uid] = fn()
	}
	liveBots.Unlock()

	// session counters: accumulate per-worker deltas (chats/replies/shares) —
	// in-memory only, so every app start begins at 0.
	a.mu.Lock()
	for uid, cur := range statuses {
		prev := a.statSeen[uid]
		if d := cur.Matches - prev.Matches; d > 0 {
			a.stats.Chats += d
		}
		if d := cur.Replies - prev.Replies; d > 0 {
			a.stats.Replies += d
		}
		if d := cur.SnapShares - prev.SnapShares; d > 0 {
			a.stats.SnapShares += d
		}
		if d := cur.TelegramShares - prev.TelegramShares; d > 0 {
			a.stats.TelegramShares += d
		}
		a.statSeen[uid] = cur
	}
	stats := a.stats
	a.mu.Unlock()

	return Snapshot{
		Settings:    a.st.Settings(),
		Accounts:    a.st.Accounts(),
		BotStatus:   statuses,
		Register:    reg,
		BotsRunning: running > 0,
		BotsMessage: fmt.Sprintf("%d bot(s) running", running),
		Logs:        logs,
		Stats:       stats,
	}
}

// Store exposes the data store for settings/files/lists.
func (a *App) Store() *store.Store { return a.st }

// TestProxyEntry checks one proxy line against the thundr geo endpoint.
func (a *App) TestProxyEntry(proxy string) map[string]any { return testProxy(proxy) }

// RegisterAccounts kicks off visible-browser registration (async).
func (a *App) RegisterAccounts(count int) (bool, string) { return a.startRegister(count) }

// StartBots launches visible or ws bots for the given accounts.
func (a *App) StartBots(uids []string, mode string) (bool, string) { return a.startBots(uids, mode) }

// StopBots cancels all running bots.
func (a *App) StopBots() (bool, string) { return a.stopBots() }

// OpenChrome opens/attaches the register or account chrome via the watcher.
func (a *App) OpenChrome(target, uid string) (bool, bool) {
	browser.AttachURL = a.st.Get("chrome_attach_url", "")
	if browser.AttachURL == "" {
		// A fresh copy has no settings.txt yet; the worker/register ports are
		// fixed (9222 / 9300 / 9301) so a missing key must not block the
		// browser. Persist the default so the next launch and the bots agree.
		browser.AttachURL = "http://127.0.0.1:9222"
		a.st.SaveSettings(map[string]string{"chrome_attach_url": browser.AttachURL})
		a.appendLog(time.Now().Format("15:04:05") + " [chrome] chrome_attach_url was empty - defaulted to " + browser.AttachURL)
	}
	logFn := func(m string) { a.appendLog(time.Now().Format("15:04:05") + " [chrome] " + m) }
	ok := false
	attached := false
	if target == "bot" && uid != "" {
		ok = browser.EnsureBotChrome(a.st, uid, logFn)
		attached = browser.DebugPortUpOn(browser.ManualURL(a.st))
	} else {
		ok = browser.EnsureRegisterChrome(a.st, logFn)
		attached = browser.DebugPortUpOn(browser.RegisterURL(a.st))
	}
	return ok, attached
}

// GetBrowserStatus is the browser diagnostic block (chrome, watcher task,
// debug ports) the accounts panel renders.
func (a *App) BrowserStatus() map[string]any { return browser.Status(a.st) }

// CloseBrowsers closes register/manual/worker-base chrome windows.
func (a *App) CloseBrowsers() { browser.CloseAll(a.st) }

// GetMessageSettings returns the effective reply settings + load state.
func (a *App) GetMessageSettings() map[string]any {
	s := reply.DefaultSettings()
	var stored reply.Settings
	if a.st.ReadJSON("message_settings.json", &stored) {
		s = mergeReplySettings(s, stored)
	}
	return map[string]any{
		"settings":          s,
		"sequences_loaded":  len(reply.FolderSequences(a.st)),
		"has_share_targets": len(a.st.LoadList("snaps")) > 0 || len(a.st.LoadList("telegram")) > 0,
	}
}

// SaveMessageSettings persists the reply settings.
func (a *App) SaveMessageSettings(s reply.Settings) bool {
	if s.ChatMode == "" {
		s.ChatMode = reply.ModeSingle
	}
	return a.st.WriteJSON("message_settings.json", s)
}

// LoadSequences loads folder sequences from a directory of .txt files and
// remembers the path as the saved folder_path (so restarts + the default
// Load Folder reuse it).
func (a *App) LoadSequences(path string) (int, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return 0, fmt.Errorf("empty folder path")
	}
	n, err := reply.LoadFolderSequences(a.st, path)
	if err != nil {
		return 0, err
	}
	a.updateReplySettings(func(s *reply.Settings) { s.FolderPath = path })
	a.appendLog(time.Now().Format("15:04:05") + fmt.Sprintf(" [message] loaded %d sequences from %s", n, path))
	return n, nil
}

// CleanSequences normalises the loaded sequences.
func (a *App) CleanSequences() { reply.CleanFolderSequences(a.st) }

// OpenDataFolder seeds + opens the eva/api chat data folder; returns its path.
func (a *App) OpenDataFolder(which string) (string, error) {
	var dir string
	switch which {
	case "eva":
		if err := reply.SeedEVA(a.st); err != nil {
			return "", err
		}
		dir = a.st.DirPath("eva_chat")
	case "api":
		if err := reply.SeedAPI(a.st); err != nil {
			return "", err
		}
		dir = a.st.DirPath("api_chat")
	default:
		return "", fmt.Errorf("unknown folder")
	}
	openFolder(dir)
	return dir, nil
}

// Serve starts the dashboard on addr and blocks until the listener closes.
func (a *App) Serve(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return a.serve(ln)
}

// serve runs the hardened HTTP server on an existing listener. Shutdown closes
// the listener and drains in-flight requests; Serve then returns nil.
func (a *App) serve(ln net.Listener) error {
	a.StartEventDrain()
	srv := &http.Server{
		Handler:           a.handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	a.mu.Lock()
	a.httpServer = srv
	a.mu.Unlock()
	fmt.Printf("Thundr Bot dashboard -> http://%s\n", ln.Addr())
	err := srv.Serve(ln)
	a.mu.Lock()
	if a.httpServer == srv {
		a.httpServer = nil
	}
	a.mu.Unlock()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
