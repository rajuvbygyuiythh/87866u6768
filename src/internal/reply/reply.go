// Package reply decides every line the bot sends. It ports the ChitChat
// dashboard's four chat modes to thundr-go:
//
//	single         - one sequence from msg/chat.txt, in order (default)
//	folder         - many sequences loaded from a folder, one random per chat
//	eva_chat_flow  - TXT reply pools in <data>/eva_chat/ drive a stage funnel
//	api_chat       - an OpenAI-compatible model writes every line
//
// All modes share the same templating: {a|b} spintax, %username% (one handle
// assigned per chat from the configured share platform), plus the legacy
// %snap% / %tguser% placeholders. A line is never repeated inside one chat.
package reply

import (
	"math/rand"
	"regexp"
	"strings"
	"sync"

	"thundr/internal/store"
)

// Chat modes (same values as the ChitChat dashboard).
const (
	ModeSingle = "single"
	ModeFolder = "folder"
	ModeEVA    = "eva_chat_flow"
	ModeAPI    = "api_chat"
)

// Settings is the Message panel state, persisted in message_settings.json.
type Settings struct {
	ChatMode       string  `json:"chat_mode"`
	SharePlatform  string  `json:"share_platform"` // snap | tg | both
	FolderPath     string  `json:"folder_path"`
	AIBaseURL      string  `json:"ai_base_url"`
	AIAPIKey       string  `json:"ai_api_key"`
	AIModel        string  `json:"ai_model"`
	APITemperature float64 `json:"api_temperature"`
	APIShareTurn   int     `json:"api_share_turn"` // share snap/tg after this many partner messages
}

// DefaultSettings mirrors the ChitChat defaults.
func DefaultSettings() Settings {
	return Settings{
		ChatMode:       ModeSingle,
		SharePlatform:  "snap",
		AIBaseURL:      "https://api.featherless.ai/v1",
		AIModel:        "deepseek-ai/DeepSeek-V3-0324",
		APITemperature: 0.9,
		APIShareTurn:   4,
	}
}

// Session is one conversation's reply engine (created per match).
type Session interface {
	// Open is the bot's first line ("" = nothing to say).
	Open() string
	// Next answers one partner message. done=true ends the chat (bot skips).
	Next(incoming string) (line string, done bool)
	// Progress reports lines sent and total (-1 = open-ended mode).
	Progress() (sent, total int)
}

// Manager is built once per bot start and spawns a Session per match.
type Manager struct {
	st       *store.Store
	Settings Settings
	Warning  string // non-fatal problem worth logging at bot start

	lines []string   // msg/chat.txt
	seqs  [][]string // folder sequences

	mu      sync.Mutex
	snaps   []string
	tgs     []string
	snapOrd []int
	tgOrd   []int
	snapPos int
	tgPos   int

	eva *evaPools
	api *apiData
}

// NewManager loads settings + every pool the selected mode needs.
func NewManager(st *store.Store) *Manager {
	s := DefaultSettings()
	var stored Settings
	if st.ReadJSON("message_settings.json", &stored) {
		s = mergeSettings(s, stored)
	}
	m := &Manager{
		st:       st,
		Settings: s,
		lines:    st.ChatLines(),
		snaps:    st.LoadList("snaps"),
		tgs:      st.LoadList("telegram"),
	}

	switch s.ChatMode {
	case ModeFolder:
		m.seqs = FolderSequences(st)
		if len(m.seqs) == 0 {
			m.Warning = "folder sequences empty - falling back to msg/chat.txt"
			m.Settings.ChatMode = ModeSingle
		}
	case ModeEVA:
		if err := SeedEVA(st); err != nil {
			m.Warning = "eva_chat seed: " + err.Error()
		}
		m.eva = loadEVAPools(st)
		if len(m.eva.outputs) == 0 {
			m.Warning = "eva_chat data missing - falling back to msg/chat.txt"
			m.Settings.ChatMode = ModeSingle
		}
	case ModeAPI:
		if err := SeedAPI(st); err != nil {
			m.Warning = "api_chat seed: " + err.Error()
		}
		m.api = loadAPIData(st)
		if strings.TrimSpace(s.AIAPIKey) == "" {
			m.Warning = "API Chat needs an API key - falling back to msg/chat.txt"
			m.Settings.ChatMode = ModeSingle
		}
	}
	return m
}

func mergeSettings(base, o Settings) Settings {
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
	base.AIAPIKey = o.AIAPIKey // may legitimately be empty
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

// Ready reports whether the bot has anything to say at all.
func (m *Manager) Ready() bool {
	switch m.Settings.ChatMode {
	case ModeEVA:
		return m.eva != nil && len(m.eva.outputs) > 0
	case ModeAPI:
		return m.api != nil
	case ModeFolder:
		return len(m.seqs) > 0
	default:
		return len(m.lines) > 0
	}
}

// NewSession starts one conversation.
func (m *Manager) NewSession() Session {
	platform, handle := m.assignHandle()
	b := baseSess{mgr: m, platform: platform, handle: handle, used: map[string]bool{}}
	switch m.Settings.ChatMode {
	case ModeFolder:
		return &seqSession{baseSess: b, lines: m.pickSequence()}
	case ModeEVA:
		return newEVASession(b, m.eva)
	case ModeAPI:
		return newAPISession(b, m.api)
	default:
		return &seqSession{baseSess: b, lines: m.lines}
	}
}

// ---------------------------------------------------------------------------
// handle assignment (share platform)
// ---------------------------------------------------------------------------

func shuffled(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	rand.Shuffle(n, func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

func (m *Manager) nextSnap() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.snaps) == 0 {
		return ""
	}
	if m.snapPos >= len(m.snapOrd) {
		m.snapOrd = shuffled(len(m.snaps))
		m.snapPos = 0
	}
	h := m.snaps[m.snapOrd[m.snapPos]]
	m.snapPos++
	return h
}

func (m *Manager) nextTG() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.tgs) == 0 {
		return ""
	}
	if m.tgPos >= len(m.tgOrd) {
		m.tgOrd = shuffled(len(m.tgs))
		m.tgPos = 0
	}
	h := m.tgs[m.tgOrd[m.tgPos]]
	m.tgPos++
	return h
}

// assignHandle picks one platform + one handle for a whole conversation.
func (m *Manager) assignHandle() (platform, handle string) {
	p := m.Settings.SharePlatform
	if p == "both" || p == "" {
		switch {
		case len(m.snaps) > 0 && len(m.tgs) > 0:
			if rand.Intn(2) == 0 {
				p = "snap"
			} else {
				p = "tg"
			}
		case len(m.tgs) > 0:
			p = "tg"
		default:
			p = "snap"
		}
	}
	if p == "tg" {
		if h := m.nextTG(); h != "" {
			return "tg", h
		}
		if h := m.nextSnap(); h != "" {
			return "snap", h
		}
		return "tg", ""
	}
	if h := m.nextSnap(); h != "" {
		return "snap", h
	}
	if h := m.nextTG(); h != "" {
		return "tg", h
	}
	return "snap", ""
}

// ---------------------------------------------------------------------------
// templating
// ---------------------------------------------------------------------------

var spintaxRe = regexp.MustCompile(`\{([^{}]*\|[^{}]*)\}`)

// spin resolves {option|option} spintax, innermost first.
func spin(text string) string {
	for i := 0; i < 10 && spintaxRe.MatchString(text); i++ {
		text = spintaxRe.ReplaceAllStringFunc(text, func(match string) string {
			inner := match[1 : len(match)-1]
			parts := strings.Split(inner, "|")
			return parts[rand.Intn(len(parts))]
		})
	}
	return text
}

// render applies spintax + username placeholders for one session.
func (m *Manager) render(s *baseSess, text string) string {
	text = spin(text)
	if s.handle != "" {
		text = strings.ReplaceAll(text, "%username%", s.handle)
		text = strings.ReplaceAll(text, "{snap}", s.handle)
	}
	if strings.Contains(text, "%snap%") {
		if h := m.nextSnap(); h != "" {
			text = strings.ReplaceAll(text, "%snap%", h)
		}
	}
	if strings.Contains(text, "%tguser%") {
		if h := m.nextTG(); h != "" {
			text = strings.ReplaceAll(text, "%tguser%", h)
		}
	}
	return text
}

// ---------------------------------------------------------------------------
// shared session base
// ---------------------------------------------------------------------------

type baseSess struct {
	mgr      *Manager
	platform string // resolved share platform for this chat
	handle   string // resolved handle for this chat ("" = none configured)
	sent     int
	lastLine string
	used     map[string]bool // lines already sent in this chat
}

// pickLine returns an unused pool line (never the last line twice in a row);
// "" when the pool is exhausted or empty.
func (s *baseSess) pickLine(pool []string) string {
	if len(pool) == 0 {
		return ""
	}
	avail := make([]string, 0, len(pool))
	for _, l := range pool {
		if !s.used[l] && l != s.lastLine {
			avail = append(avail, l)
		}
	}
	if len(avail) == 0 {
		// pool exhausted: still avoid repeating the immediately-last line
		for _, l := range pool {
			if l != s.lastLine {
				avail = append(avail, l)
			}
		}
		if len(avail) == 0 {
			avail = pool
		}
	}
	l := avail[rand.Intn(len(avail))]
	s.used[l] = true
	s.lastLine = l
	return l
}

func (s *baseSess) emit(text string) string {
	s.sent++
	return text
}

// ---------------------------------------------------------------------------
// sequence sessions (single + folder)
// ---------------------------------------------------------------------------

type seqSession struct {
	baseSess
	lines []string
	idx   int
}

func (s *seqSession) Open() string {
	if s.idx >= len(s.lines) {
		return ""
	}
	line := s.mgr.render(&s.baseSess, s.lines[s.idx])
	s.idx++
	s.sent++
	s.lastLine = line
	return line
}

func (s *seqSession) Next(_ string) (string, bool) {
	if s.idx >= len(s.lines) {
		return "", true
	}
	return s.Open(), false
}

func (s *seqSession) Progress() (int, int) { return s.idx, len(s.lines) }

// pickSequence returns a random loaded sequence, reshuffling when exhausted.
func (m *Manager) pickSequence() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.seqs) == 0 {
		return m.lines
	}
	return m.seqs[rand.Intn(len(m.seqs))]
}

// HasShareTarget reports whether any username is configured for sharing.
func (m *Manager) HasShareTarget() bool { return len(m.snaps) > 0 || len(m.tgs) > 0 }
