package reply

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"thundr/internal/store"
)

// API Chat mode: an OpenAI-compatible model writes every line. The editable
// data lives in <data>/api_chat/ (seeded on first run):
//
//	persona.txt            system prompt; placeholders {age} {country} {name}
//	                       {major} {year} {tonight} {show} {snack} {role}
//	persona_roles.txt      rotating characters ([section] voice/speech/examples)
//	persona_countries.txt  one country per line (a random one is spoken)
//	sharesnap.txt          snap share deck; %username% filled by the app
//	sharetg.txt            telegram share deck, same rules
//	fallback.txt           lines used when the API fails
//
// The model never writes a username: after api_share_turn partner messages
// (or when the stranger asks for the handle) Go appends one share line from
// the platform's own deck with the chat's assigned %username%.

//go:embed all:api_assets
var apiAssets embed.FS

var (
	loreNames = []string{
		"emma", "mia", "ava", "chloe", "lily", "zoe", "ella", "madison", "hailey", "kayla",
		"brooke", "sophie", "riley", "paige", "lexi", "jenna", "sierra", "kenzie", "tori", "lauren",
		"sadie", "maddie", "kylie", "abby", "josie", "harper", "quinn", "nina", "tessa", "callie",
	}
	loreMajors = []string{
		"psych", "nursing", "business", "marketing", "bio", "comm", "graphic design",
		"english lit", "pre law", "sociology", "fashion", "education", "poli sci", "econ", "art history",
	}
	loreYears   = []string{"freshman", "sophomore", "junior", "senior"}
	loreTonight = []string{
		"in bed scrolling tiktok", "half watching netflix", "procrastinating an essay",
		"eating cereal at midnight", "lying on the floor bored", "doing a face mask",
		"cant sleep again", "listening to music in the dark", "home alone, roommate is out",
		"painting my nails badly",
	}
	loreShows = []string{
		"euphoria", "the office", "greys anatomy", "friends", "outer banks",
		"stranger things", "love island", "gilmore girls", "new girl",
	}
	loreSnacks = []string{"takis", "cold pizza", "cereal", "oreos", "hot cheetos", "ice cream", "ramen", "goldfish"}
)

type apiData struct {
	persona   string
	roles     []string // rendered role blocks
	countries []string
	fallback  []string
	shareSnap []string
	shareTG   []string
}

// parseRoles renders every [section] of persona_roles.txt into one block.
func parseRoles(raw string) []string {
	var roles []string
	var cur strings.Builder
	flush := func() {
		if block := strings.TrimSpace(cur.String()); block != "" {
			roles = append(roles, block)
		}
		cur.Reset()
	}
	for _, line := range strings.Split(raw, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "#") {
			continue
		}
		if strings.HasPrefix(trim, "[") && strings.HasSuffix(trim, "]") {
			flush()
			cur.WriteString("character: " + strings.TrimSuffix(strings.TrimPrefix(trim, "["), "]") + "\n")
			continue
		}
		if cur.Len() > 0 && trim != "" {
			cur.WriteString(trim + "\n")
		}
	}
	flush()
	return roles
}

func loadAPIData(st *store.Store) *apiData {
	root := st.DirPath("api_chat")
	d := &apiData{
		persona:   strings.Join(readPoolFileRaw(filepath.Join(root, "persona.txt")), "\n"),
		countries: readPoolFile(filepath.Join(root, "persona_countries.txt")),
		fallback:  readPoolFile(filepath.Join(root, "fallback.txt")),
		shareSnap: readPoolFile(filepath.Join(root, "sharesnap.txt")),
		shareTG:   readPoolFile(filepath.Join(root, "sharetg.txt")),
	}
	if raw := readPoolFileRaw(filepath.Join(root, "persona_roles.txt")); len(raw) > 0 {
		d.roles = parseRoles(strings.Join(raw, "\n"))
	}
	return d
}

// readPoolFileRaw keeps the file's raw lines (comments included) for prompts.
func readPoolFileRaw(path string) []string {
	data, err := readFileString(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(data, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, strings.TrimRight(l, "\r"))
		}
	}
	return out
}

func readFileString(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}

// ---------------------------------------------------------------------------
// OpenAI-compatible client
// ---------------------------------------------------------------------------

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

var thinkRe = regexp.MustCompile(`(?s)<think>.*?</think>`)

// cleanReply enforces the output guards: no think tags, no quotes, no
// usernames/@, no URLs, at most two short lines.
func cleanReply(text string) string {
	text = thinkRe.ReplaceAllString(text, "")
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		l = strings.Trim(l, `"'`+"«»“”")
		if l == "" || strings.Contains(l, "@") || strings.Contains(l, "http") {
			continue
		}
		lines = append(lines, l)
		if len(lines) == 2 {
			break
		}
	}
	return strings.Join(lines, " ")
}

// callAPI performs one /chat/completions round-trip (25s timeout).
func callAPI(baseURL, apiKey, model string, temp float64, msgs []chatMessage) (string, error) {
	body, _ := json.Marshal(chatRequest{Model: model, Messages: msgs, Temperature: temp, MaxTokens: 140})
	url := strings.TrimRight(baseURL, "/") + "/chat/completions"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/json")
	if apiKey != "" {
		req.Header.Set("authorization", "Bearer "+apiKey)
	}
	client := &http.Client{Timeout: 25 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("API %d: unreadable response", resp.StatusCode)
	}
	if out.Error != nil {
		return "", fmt.Errorf("API %d: %s", resp.StatusCode, out.Error.Message)
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("API %d", resp.StatusCode)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("empty reply")
	}
	return cleanReply(out.Choices[0].Message.Content), nil
}

// TestAPI is the dashboard "Test API" button: one tiny completion.
func TestAPI(baseURL, apiKey, model string, temp float64) (string, error) {
	msgs := []chatMessage{
		{Role: "system", Content: "you are a 21 y/o college girl texting a stranger on an anonymous chat app. one short lowercase line."},
		{Role: "user", Content: "hey, how are you?"},
	}
	reply, err := callAPI(baseURL, apiKey, model, temp, msgs)
	if err != nil {
		return "", err
	}
	if reply == "" {
		return "", fmt.Errorf("empty reply (thinking model spent all tokens?)")
	}
	return reply, nil
}

// ---------------------------------------------------------------------------
// session
// ---------------------------------------------------------------------------

var handleAskRe = regexp.MustCompile(`\b(snap|snapchat|sc|telegram|tg|insta|ig|username|add you|add me)\b`)

type apiSession struct {
	baseSess
	data        *apiData
	system      string
	history     []chatMessage
	partnerMsgs int
	shared      bool
	fbIdx       int
}

func newAPISession(b baseSess, d *apiData) Session {
	s := &apiSession{baseSess: b, data: d}
	s.system = s.renderPersona()
	return s
}

func (s *apiSession) renderPersona() string {
	tpl := s.data.persona
	if strings.TrimSpace(tpl) == "" {
		tpl = "you are roleplaying a real {age} year old college girl from {country} in an anonymous text chat. stay in character. one short lowercase line. never say you are an ai."
	}
	country := ""
	if len(s.data.countries) > 0 {
		country = s.data.countries[rand.Intn(len(s.data.countries))]
	}
	role := ""
	if len(s.data.roles) > 0 {
		role = s.data.roles[rand.Intn(len(s.data.roles))]
	}
	r := strings.NewReplacer(
		"{age}", fmt.Sprint(20+rand.Intn(6)),
		"{country}", country,
		"{name}", loreNames[rand.Intn(len(loreNames))],
		"{major}", loreMajors[rand.Intn(len(loreMajors))],
		"{year}", loreYears[rand.Intn(len(loreYears))],
		"{tonight}", loreTonight[rand.Intn(len(loreTonight))],
		"{show}", loreShows[rand.Intn(len(loreShows))],
		"{snack}", loreSnacks[rand.Intn(len(loreSnacks))],
		"{role}", role,
	)
	out := r.Replace(tpl)
	if role != "" && !strings.Contains(tpl, "{role}") {
		out = strings.TrimRight(out, "\n") + "\n\n# CHARACTER OF THE DAY\n" + role
	}
	return out
}

// askModel calls the API with one retry; "" means total failure.
func (s *apiSession) askModel() string {
	cfg := s.mgr.Settings
	msgs := make([]chatMessage, 0, len(s.history)+1)
	msgs = append(msgs, chatMessage{Role: "system", Content: s.system})
	msgs = append(msgs, s.history...)
	text, err := callAPI(cfg.AIBaseURL, cfg.AIAPIKey, cfg.AIModel, cfg.APITemperature, msgs)
	if err == nil && text != "" {
		return text
	}
	// one retry
	text, err = callAPI(cfg.AIBaseURL, cfg.AIAPIKey, cfg.AIModel, cfg.APITemperature, msgs)
	if err == nil {
		return text
	}
	return ""
}

func (s *apiSession) fallbackLine() string {
	if len(s.data.fallback) == 0 {
		return "lol my wifi is acting up, give me a sec"
	}
	line := s.data.fallback[s.fbIdx%len(s.data.fallback)]
	s.fbIdx++
	return line
}

// shareDeckLine picks an unused share line for this chat's platform.
func (s *apiSession) shareDeckLine() string {
	pool := s.data.shareSnap
	if s.platform == "tg" && len(s.data.shareTG) > 0 {
		pool = s.data.shareTG
	}
	line := s.pickLine(pool)
	return s.mgr.render(&s.baseSess, line)
}

func (s *apiSession) respond(incoming string) string {
	if incoming != "" {
		s.history = append(s.history, chatMessage{Role: "user", Content: incoming})
		s.partnerMsgs++
	}
	needShare := !s.shared && s.handle != "" && incoming != "" &&
		(s.partnerMsgs >= s.mgr.Settings.APIShareTurn || handleAskRe.MatchString(strings.ToLower(incoming)))

	text := s.askModel()
	if text == "" {
		text = s.fallbackLine()
	} else {
		s.history = append(s.history, chatMessage{Role: "assistant", Content: text})
	}
	// keep memory bounded
	if len(s.history) > 24 {
		s.history = s.history[len(s.history)-24:]
	}
	if needShare {
		if share := s.shareDeckLine(); share != "" {
			s.shared = true
			text = text + "\n" + share
		}
	}
	s.sent++
	return text
}

func (s *apiSession) Open() string { return s.respond("") }

func (s *apiSession) Next(incoming string) (string, bool) {
	return s.respond(incoming), false // API Chat never runs out of lines
}

func (s *apiSession) Progress() (int, int) { return s.sent, -1 }
