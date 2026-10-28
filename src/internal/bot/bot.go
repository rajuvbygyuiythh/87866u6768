// Package bot is the Go port of chat.py's ThundrBot: a raw Phoenix-websocket
// client that joins match:<uid>, starts text-mode matches, joins rooms and
// replies with scripted chat lines. Identical payload shapes to the Python
// version (validated against the live site).
package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"thundr/internal/reply"
	"thundr/internal/store"
)

var (
	banKeywords   = []string{"ban", "suspend", "blocked", "kicked"}
	banCloseCodes = map[int]bool{4003: true, 4001: true, 4100: true}
)

// Event is one human-readable bot log line for the dashboard.
type Event struct {
	UID  string `json:"uid"`
	Text string `json:"text"`
	At   string `json:"at"`
}

// Status is the per-bot dashboard view.
type Status struct {
	State     string `json:"state"`
	Matches   int    `json:"matches"`
	Replies   int    `json:"replies"`
	Line      int    `json:"line"`
	Bots      int    `json:"bots"`
	Reals     int    `json:"reals"`
	Unknown   int    `json:"unknown"`
	LastEvent string `json:"last_event"`
	Updated   string `json:"updated"`
	Tag       string `json:"tag"`
	Proxy     string `json:"proxy,omitempty"` // worker's assigned proxy label
	UA        string `json:"ua,omitempty"`    // worker's Chrome UA summary
	Port      int    `json:"port,omitempty"`  // debug port (visible mode)

	SnapShares     int `json:"snap_shares"`     // share lines sent (snap platform)
	TelegramShares int `json:"telegram_shares"` // share lines sent (tg platform)
}

// Bot chats as one account.
type Bot struct {
	st       *store.Store
	acc      store.Account
	proxy    string
	settings map[string]string

	mgr    *reply.Manager
	tags   []string
	waitT  float64
	respT  float64 // legacy single value (kept for reference; ranges below win)
	retryT float64
	workT  float64

	respMin, respMax   float64 // stranger_response_time range (random per reply)
	noRespMin, noRespMax float64 // partner_no_response_secs range (random per match)
	noRespCur          float64   // this match's drawn silent-skip threshold
	rerollMin, rerollMax float64 // reroll_delay_secs range (random per re-roll)
	instSkip float64 // instant_skip_secs: partner leaving within this = instant re-roll
	sendsFirst bool  // bot_sends_first: send the opener ourselves (off = partner writes first)

	mu           sync.Mutex
	replyMu      sync.Mutex // serializes replies so lines go out one at a time
	sessMu       sync.Mutex
	sess         reply.Session // current match's reply engine
	ws           *websocket.Conn
	ref          int
	joinRefs     map[string]string
	matchID      string
	roomTop      string
	partner      string
	lastStart    time.Time
	lastActivity time.Time
	pauseUntil   time.Time // humans pause before re-rolling
	intel        *MatchIntel
	stop         bool
	banned       bool
	status       Status
	events       chan Event
}

func New(st *store.Store, acc store.Account, proxy string, events chan Event) *Bot {
	b := &Bot{
		st: st, acc: acc, proxy: proxy, events: events,
		joinRefs: map[string]string{},
		mgr:      reply.NewManager(st),
		tags:     st.LoadList("interests"),
		settings: st.Settings(),
	}
	b.waitT = st.Float("stranger_wait_time", 15)
	b.respT = st.Float("stranger_response_time", 3)
	b.retryT = st.Float("search_retry_time", 10)
	b.workT = st.Float("thread_work_time", 600)
	b.respMin, b.respMax = st.RangeSecs("stranger_response_time", 2, 5)
	b.noRespMin, b.noRespMax = st.RangeSecs("partner_no_response_secs", 10, 15)
	b.noRespCur = store.RandBetween(b.noRespMin, b.noRespMax)
	b.rerollMin, b.rerollMax = st.RangeSecs("reroll_delay_secs", 6, 12)
	b.instSkip = st.Float("instant_skip_secs", 5)
	b.sendsFirst = st.Bool("bot_sends_first", false)
	b.status = Status{
		State: "starting",
		Tag:   acc.UserID[:8],
		UA:    "Chrome/" + store.ChromeMajor(st.UserAgentFor(acc.UserID)),
	}
	if proxy != "" {
		if pu, err := url.Parse(proxy); err == nil && pu.Host != "" {
			b.status.Proxy = pu.Host
		} else {
			b.status.Proxy = proxy
		}
	} else {
		b.status.Proxy = "direct"
	}
	return b
}

func (b *Bot) log(state, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	b.mu.Lock()
	b.status.State = state
	b.status.LastEvent = msg
	b.status.Updated = time.Now().Format("15:04:05")
	b.mu.Unlock()
	select {
	case b.events <- Event{UID: b.acc.UserID, Text: "[" + b.status.Tag + "] " + msg, At: b.status.Updated}:
	default:
	}
}

func (b *Bot) Status() Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.status
}

func (b *Bot) Stop() { b.mu.Lock(); b.stop = true; b.mu.Unlock() }

// Banned reports whether the server banned this account mid-run — the caller
// auto-removes the account from the store.
func (b *Bot) Banned() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.banned }

func (b *Bot) stopped() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.stop }

func (b *Bot) nextRef() string {
	b.ref++
	return fmt.Sprint(b.ref)
}

// ---- payloads (shapes captured from the real client) -------------------------

// personaFor derives a deterministic persona per account, CONSISTENT with the
// chat lines ("hey f 21" opener) - a male-36 profile saying "f 21" is an
// instant bot tell that makes real users skip.
func personaFor(uid string) (sex, dob string) {
	h := 0
	for _, c := range uid {
		h = h*31 + int(c)
	}
	year := 2003 + (h%3+3)%3 // 2003-2005 => 21-23yo
	return "f", fmt.Sprintf("%d-0%d-1%d", year, 1+(h%9+9)%9, h%9)
}

func (b *Bot) userPayload() map[string]any {
	sex, dob := personaFor(b.acc.UserID)
	profile := map[string]any{"sex": sex, "looking_for": "a", "dob": dob}
	return map[string]any{"user": map[string]any{
		"created_at": fmt.Sprint(time.Now().UnixMilli()),
		"id":         b.acc.UserID,
		"email":      b.acc.Email,
		"onboarded":  true,
		"settings": map[string]any{
			"device":       nil,
			"userDevice":   nil,
			"platform":     "web",
			"volume":       map[string]any{"master": 0, "output": 100},
			"autoRoll":     map[string]any{"video": false, "text": false},
			"performance":  map[string]any{"lowPower": false, "facePopup": false},
			"interests":    map[string]any{"wait": 0, "tags": b.tags},
			"filters":      map[string]any{"countries": []any{}, "regions": []any{}, "sex": []any{}, "max_wait": 3},
			"autoMod":      true,
			"mobile":       map[string]any{"swipeSensitivity": 0, "swipeSkip": true, "haptics": true},
			"privacy":      map[string]any{"hidden": false},
			"event":        map[string]any{"looking_for": "a"},
			"reviewed":     nil,
			"notification": nil,
			"profile":      profile,
		},
		"verification": false,
		"profile":      profile,
		"hashed_token": b.acc.HashedToken,
		"boost":        false,
	}}
}

func (b *Bot) startPayload() map[string]any {
	user := b.userPayload()["user"].(map[string]any)
	profile := user["profile"]
	return map[string]any{
		"user": user,
		"config": map[string]any{
			"mode": "text",
			"settings": map[string]any{
				"device":    nil,
				"interests": map[string]any{"wait": 0, "tags": b.tags},
				"filters":   map[string]any{"countries": []any{}, "regions": []any{}, "sex": []any{}, "max_wait": 3},
				"profile":   profile,
				"event":     map[string]any{"looking_for": "a"},
				"privacy":   map[string]any{"hidden": false},
			},
		},
		"version": "20",
	}
}

// persistTextMode mirrors PUT /api/updateUser (accounts are born video-mode).
func (b *Bot) persistTextMode() {
	body, _ := json.Marshal(b.userPayload())
	req, err := http.NewRequest(http.MethodPut, "https://thundr.com/api/updateUser",
		strings.NewReader(string(body)))
	if err != nil {
		return
	}
	req.Header.Set("content-type", "application/json")
	StampBrowserHeaders(req.Header, b.st, b.acc.UserID) // UA matches the WS socket
	// Chrome ClientHello + proxy hop (uTLS) — never Go's default TLS stack.
	resp, err := ChromeHTTPClient(b.proxy).Do(req)
	if err != nil {
		b.log("starting", "updateUser failed (non-fatal): %v", err)
		return
	}
	defer resp.Body.Close()
	b.log("starting", "updateUser (text mode) -> %d", resp.StatusCode)
}

// ---- websocket ----------------------------------------------------------------

func (b *Bot) send(topic, event string, payload map[string]any) error {
	b.mu.Lock()
	ref := b.nextRef()
	if event == "phx_join" {
		b.joinRefs[topic] = ref
	}
	jr, ok := b.joinRefs[topic]
	if !ok {
		jr = ref
	}
	ws := b.ws
	b.mu.Unlock()
	if ws == nil {
		return fmt.Errorf("no socket")
	}
	frame, _ := json.Marshal([]any{jr, ref, topic, event, payload})
	if err := ws.WriteMessage(websocket.TextMessage, frame); err != nil {
		return err
	}
	if event == "message" {
		b.log("", "SEND message body=%.60q", payload["body"])
	} else if event != "typing" {
		b.log("", "SEND [%s] %s", strings.Split(topic, ":")[0], event)
	}
	return nil
}

func isBanClose(code int, reason string) bool {
	if banCloseCodes[code] && code != 1000 {
		return true
	}
	r := strings.ToLower(reason)
	for _, kw := range banKeywords {
		if strings.Contains(r, kw) {
			return true
		}
	}
	return false
}

// sessionNext asks the current match's reply engine for the next line.
// incoming="" means the bot's opener. done=true = engine exhausted -> skip.
func (b *Bot) sessionNext(incoming string) (line string, done bool, sent, total int) {
	b.sessMu.Lock()
	defer b.sessMu.Unlock()
	if b.sess == nil {
		return "", true, 0, 0
	}
	if incoming == "" {
		line = b.sess.Open()
	} else {
		line, done = b.sess.Next(incoming)
	}
	sent, total = b.sess.Progress()
	return line, done || line == "", sent, total
}

// replyWith sends one engine line to the current room (with typing
// simulation), or rolls to the next match when the engine is done.
func (b *Bot) replyWith(incoming string) {
	b.replyMu.Lock()
	defer b.replyMu.Unlock()
	line, done, sent, total := b.sessionNext(incoming)
	if done || b.roomTop == "" {
		if incoming != "" && b.roomTop != "" {
			go b.startNewMatch()
		}
		return
	}
	time.Sleep(time.Duration(store.RandBetween(b.respMin, b.respMax)*1000) * time.Millisecond)
	room := b.roomTop
	if room == "" {
		return
	}
	_ = b.send(room, "typing", map[string]any{"sender_id": b.acc.UserID})
	// typing duration proportional to message length (humans type ~60-90ms/char)
	typingMs := 300 + len(line)*75 + rand.Intn(400)
	if typingMs > 3500 {
		typingMs = 3500
	}
	time.Sleep(time.Duration(typingMs) * time.Millisecond)
	if b.roomTop != room { // match ended while we were "typing"
		return
	}
	if err := b.send(room, "message", map[string]any{
		"body": line, "sender_id": b.acc.UserID}); err != nil {
		return
	}
	b.mu.Lock()
	b.status.Replies++
	b.status.Line = sent
	b.lastActivity = time.Now()
	if b.intel != nil && b.intel.OurFirstReplyAt.IsZero() {
		b.intel.OurFirstReplyAt = time.Now()
	}
	if total > 0 && sent == total {
		// the final line of a sequence IS the share line
		switch b.mgr.Settings.SharePlatform {
		case "snap":
			b.status.SnapShares++
		case "both":
			b.status.SnapShares++
			b.status.TelegramShares++
		default: // "tg"
			b.status.TelegramShares++
		}
	}
	b.mu.Unlock()
	if total > 0 {
		b.log("matched", "REPLIED (%d/%d): %.80s", sent, total, line)
	} else {
		b.log("matched", "REPLIED (%d): %.80s", sent, line)
	}
}

// endMatchIntel classifies the finished match and counts bot vs real.
func (b *Bot) endMatchIntel(reason string) {
	b.mu.Lock()
	in := b.intel
	b.intel = nil
	b.mu.Unlock()
	if in == nil {
		return
	}
	cls, why := in.Classify()
	b.mu.Lock()
	switch cls {
	case "bot":
		b.status.Bots++
	case "real":
		b.status.Reals++
	default:
		b.status.Unknown++
	}
	b.mu.Unlock()
	b.log("", "match ended (%s) -> partner classified %s %v", reason, cls, why)
}

// startNewMatch mirrors _start_new_match: leave room, disconnect, wait, start.
func (b *Bot) startNewMatch() {
	b.endMatchIntel("script exhausted")
	if b.roomTop != "" {
		_ = b.send(b.roomTop, "phx_leave", map[string]any{})
	}
	if b.matchID != "" {
		_ = b.send("match:"+b.acc.UserID, "disconnect", map[string]any{
			"to": b.partner, "from": b.acc.UserID, "match_id": b.matchID,
			"mode": "text", "videoLoaded": false})
	}
	b.matchID, b.roomTop, b.partner = "", "", ""
	b.sessMu.Lock()
	b.sess = nil
	b.sessMu.Unlock()
	time.Sleep(time.Duration(b.waitT * float64(time.Second)))
	b.lastStart = time.Now()
	_ = b.send("match:"+b.acc.UserID, "start", b.startPayload())
	b.log("searching", "Searching for new match...")
}

func (b *Bot) handleEvent(topic, event string, payload map[string]any) {
	matchTopic := "match:" + b.acc.UserID
	switch event {
	case "phx_reply":
		if resp, ok := payload["response"].(map[string]any); ok {
			if code, _ := resp["code"].(string); code == "captchafail" {
				b.log("captcha-blocked", "CAPTCHA required - stopping cleanly (use visible-browser mode)")
				b.mu.Lock()
				b.stop = true
				b.mu.Unlock()
			}
		}
	case "matched":
		if topic != matchTopic {
			return
		}
		mid, _ := payload["match_id"].(string)
		partner, _ := payload["partner"].(map[string]any)
		pid, _ := partner["id"].(string)
		raw, _ := json.Marshal(payload)
		shared, _ := payload["shared_interests"].([]any)
		searchSecs := time.Since(b.lastStart).Seconds()
		b.mu.Lock()
		b.matchID = mid
		b.roomTop = "room:" + mid
		b.partner = pid
		b.status.Matches++
		b.lastActivity = time.Now()
		b.noRespCur = store.RandBetween(b.noRespMin, b.noRespMax) // fresh random threshold per match
		b.intel = &MatchIntel{
			MatchedAt: time.Now(), SearchSecs: searchSecs,
			SharedInterests: len(shared), PartnerProfileRaw: string(raw),
		}
		b.mu.Unlock()
		b.sessMu.Lock()
		b.sess = b.mgr.NewSession()
		b.sessMu.Unlock()
		b.log("matched", "MATCHED %.8s after %.1fs shared=%v", mid, searchSecs, payload["shared_interests"])
		b.log("", "matched payload: %.300s", string(raw))
		_ = b.send(b.roomTop, "phx_join", b.userPayload())
		// real client kicks off the conversation after the wait window — but only
		// when the bot is configured to send the first message itself
		go func() {
			time.Sleep(time.Duration(b.waitT * float64(time.Second)))
			if b.roomTop == "room:"+mid && !b.stopped() && b.sendsFirst {
				b.replyWith("")
			}
		}()
	case "message":
		if topic == b.roomTop {
			if sid, _ := payload["sender_id"].(string); sid != b.acc.UserID {
				body, _ := payload["body"].(string)
				b.log("matched", "PARTNER: %.120s", body)
				b.mu.Lock()
				b.lastActivity = time.Now()
				if b.intel != nil {
					b.intel.PartnerMsgCount++
					if b.intel.FirstPartnerMsgAt.IsZero() {
						b.intel.FirstPartnerMsgAt = time.Now()
						b.intel.FirstPartnerMsg = body
					}
				}
				b.mu.Unlock()
				go b.replyWith(body)
			}
		}
	case "disconnect":
		if topic == matchTopic && b.matchID != "" {
			b.mu.Lock()
			dur := time.Minute
			if b.intel != nil {
				dur = time.Since(b.intel.MatchedAt)
			}
			b.mu.Unlock()
			b.log("searching", "partner disconnected (after %.1fs)", dur.Seconds())
			b.mu.Lock()
			if b.intel != nil {
				b.intel.PartnerLeftAt = time.Now()
			}
			b.mu.Unlock()
			b.endMatchIntel("partner left")
			b.sessMu.Lock()
			b.sess = nil
			b.sessMu.Unlock()
			b.mu.Lock()
			b.matchID, b.roomTop, b.partner = "", "", ""
			if dur < time.Duration(b.instSkip*float64(time.Second)) {
				// instant-skipper: re-roll immediately
				b.pauseUntil = time.Now()
				b.lastStart = time.Now().Add(-time.Duration(b.retryT) * time.Second)
				b.mu.Unlock()
				b.log("searching", "partner skipped <%.0fs - starting next match now", b.instSkip)
			} else {
				// humans pause before re-rolling (reroll_delay_secs range, random each time)
				b.pauseUntil = time.Now().Add(time.Duration(store.RandBetween(b.rerollMin, b.rerollMax)) * time.Second)
				b.mu.Unlock()
			}
		}
	}
}

// Run connects and serves until stopped/banned/work_time. Blocking.
func (b *Bot) Run(ctx context.Context) {
	if b.mgr.Warning != "" {
		b.log("", "message: %s", b.mgr.Warning)
	}
	if !b.mgr.Ready() {
		b.log("error", "no chat lines configured (Message panel)")
		return
	}
	b.log("starting", "chat mode: %s", b.mgr.Settings.ChatMode)
	b.persistTextMode()
	matchTopic := "match:" + b.acc.UserID
	deadline := time.Now().Add(time.Duration(b.workT * float64(time.Second)))
	attempts := 0

	for !b.stopped() && time.Now().Before(deadline) {
		attempts++
		u := "wss://api.thundr.com/socket/websocket?" + url.Values{
			"user_id":      {b.acc.UserID},
			"email":        {b.acc.Email},
			"hashed_token": {b.acc.HashedToken},
			"vsn":          {"2.0.0"},
		}.Encode()
		d := websocket.Dialer{
			HandshakeTimeout: 30 * time.Second,
			// Chrome TLS fingerprint (uTLS) + account UA headers — the same
			// anti-detection transport the reference project uses.
			NetDialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialChromeTLS(ctx, addr, b.proxy)
			},
		}
		b.log("connecting", "Connecting (attempt %d, Chrome TLS, UA Chrome/%s)...", attempts, store.ChromeMajor(b.st.UserAgentFor(b.acc.UserID)))
		conn, _, err := d.Dial(u, wsHeaders(b.st, b.acc.UserID))
		if err != nil {
			b.log("connecting", "dial failed: %v", err)
			time.Sleep(time.Duration(b.retryT) * time.Second)
			continue
		}
		b.mu.Lock()
		b.ws = conn
		b.mu.Unlock()
		b.log("connected", "connected - joining match channel")

		// heartbeat goroutine
		hbDone := make(chan struct{})
		go func(c *websocket.Conn) {
			t := time.NewTicker(30 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-hbDone:
					return
				case <-t.C:
					b.mu.Lock()
					ref := b.nextRef()
					b.mu.Unlock()
					f, _ := json.Marshal([]any{nil, ref, "phoenix", "heartbeat", map[string]any{}})
					if c.WriteMessage(websocket.TextMessage, f) != nil {
						return
					}
				}
			}
		}(conn)

		_ = b.send(matchTopic, "phx_join", b.userPayload())
		_ = b.send(matchTopic, "rematch_restore", map[string]any{})
		time.Sleep(500 * time.Millisecond)
		b.lastStart = time.Now()
		_ = b.send(matchTopic, "start", b.startPayload())
		b.log("searching", "Searching for match...")

		// search watcher (port of chat.py's _search_watcher): if no match
		// within search_retry_time, fire start again; if a partner goes
		// silent for 10s, skip them.
		watchDone := make(chan struct{})
		go func() {
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for {
				select {
				case <-watchDone:
					return
				case <-t.C:
					if b.stopped() {
						return
					}
					b.mu.Lock()
					idle := b.matchID == "" && b.ws != nil &&
						time.Now().After(b.pauseUntil) &&
						time.Since(b.lastStart) > time.Duration(b.retryT)*time.Second
					silent := b.matchID != "" && b.ws != nil &&
						time.Since(b.lastActivity) > time.Duration(b.noRespCur*float64(time.Second))
					b.mu.Unlock()
					if silent {
						b.log("searching", "partner no response in %ds - skipping", int(b.noRespCur))
						go b.startNewMatch()
						continue
					}
					if idle {
						b.lastStart = time.Now()
						_ = b.send(matchTopic, "start", b.startPayload())
						b.log("searching", "no match yet - re-sending start")
					}
				}
			}
		}()

		// read loop
		for !b.stopped() {
			_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
			mt, raw, err := conn.ReadMessage()
			if err != nil {
				if ce, ok := err.(*websocket.CloseError); ok && isBanClose(ce.Code, ce.Text) {
					b.log("banned", "socket closed with ban signature: %d %s", ce.Code, ce.Text)
					b.mu.Lock()
					b.banned, b.stop = true, true
					b.mu.Unlock()
				}
				break
			}
			if mt != websocket.TextMessage {
				continue
			}
			var frame []any
			if json.Unmarshal(raw, &frame) != nil || len(frame) < 5 {
				continue
			}
			topic, _ := frame[2].(string)
			event, _ := frame[3].(string)
			payload, _ := frame[4].(map[string]any)
			rawLower := strings.ToLower(string(raw))
			bad := false
			for _, kw := range banKeywords {
				if strings.Contains(rawLower, kw) {
					b.log("banned", "ban keyword in frame: %s", event)
					b.mu.Lock()
					b.banned, b.stop = true, true
					b.mu.Unlock()
					bad = true
					break
				}
			}
			if bad {
				break
			}
			b.handleEvent(topic, event, payload)
		}
		close(hbDone)
		close(watchDone)
		b.mu.Lock()
		b.ws = nil
		b.mu.Unlock()
		_ = conn.Close()
		if !b.stopped() {
			b.log("connecting", "socket closed - reconnecting in %ds", int(b.retryT))
			time.Sleep(time.Duration(b.retryT) * time.Second)
		}
	}
	if b.banned {
		b.log("banned", "bot finished (banned)")
	} else {
		b.log("finished", "bot finished")
	}
}
