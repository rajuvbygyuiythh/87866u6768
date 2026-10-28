package reply

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"thundr/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	return store.Open(t.TempDir())
}

func writeSettings(t *testing.T, st *store.Store, s Settings) {
	t.Helper()
	if !st.WriteJSON("message_settings.json", s) {
		t.Fatal("write settings")
	}
}

func TestSingleMode(t *testing.T) {
	st := testStore(t)
	if !st.WriteFile("chat", "hey {there|you}\nwyd %username%") {
		t.Fatal("write chat")
	}
	if !st.WriteFile("snaps", "snap1\nsnap2") {
		t.Fatal("write snaps")
	}
	writeSettings(t, st, Settings{ChatMode: ModeSingle, SharePlatform: "snap"})

	m := NewManager(st)
	if !m.Ready() {
		t.Fatal("manager not ready")
	}
	s := m.NewSession()
	open := s.Open()
	if open == "" || strings.ContainsAny(open, "{}") {
		t.Fatalf("bad opener %q", open)
	}
	line, done := s.Next("hi")
	if done || !strings.HasPrefix(line, "wyd snap") {
		t.Fatalf("bad second line %q done=%v", line, done)
	}
	if _, done := s.Next("again"); !done {
		t.Fatal("sequence should be exhausted")
	}
}

func TestFolderMode(t *testing.T) {
	st := testStore(t)
	if !st.WriteJSON("loaded_sequences.json", [][]string{{"a1", "a2"}, {"b1"}}) {
		t.Fatal("write seqs")
	}
	writeSettings(t, st, Settings{ChatMode: ModeFolder, SharePlatform: "snap"})

	m := NewManager(st)
	if !m.Ready() || m.Settings.ChatMode != ModeFolder {
		t.Fatalf("folder mode not active: %+v warn=%q", m.Settings, m.Warning)
	}
	s := m.NewSession()
	if s.Open() == "" {
		t.Fatal("empty opener")
	}
	done := false
	for i := 0; i < 3 && !done; i++ {
		_, done = s.Next("x")
	}
	if !done {
		t.Fatal("folder sequence never exhausted")
	}
}

func TestFolderModeFallbackWhenEmpty(t *testing.T) {
	st := testStore(t)
	_ = st.WriteFile("chat", "hello")
	writeSettings(t, st, Settings{ChatMode: ModeFolder})
	m := NewManager(st)
	if m.Settings.ChatMode != ModeSingle || m.Warning == "" {
		t.Fatalf("expected fallback to single, got %q warn=%q", m.Settings.ChatMode, m.Warning)
	}
}

func TestEVAFlow(t *testing.T) {
	st := testStore(t)
	if !st.WriteFile("snaps", "snapqueen") {
		t.Fatal("write snaps")
	}
	writeSettings(t, st, Settings{ChatMode: ModeEVA, SharePlatform: "snap"})
	m := NewManager(st)
	if !m.Ready() {
		t.Fatalf("eva not ready: %s", m.Warning)
	}
	if len(m.eva.inputs) == 0 || len(m.eva.outputs) == 0 {
		t.Fatal("eva pools empty")
	}

	s := m.NewSession()
	if s.Open() == "" {
		t.Fatal("empty eva opener")
	}
	line, done := s.Next("m 21") // age_gender category
	if done || line == "" {
		t.Fatalf("age_gender reply failed: %q done=%v", line, done)
	}
	// unmatched messages; the 3rd must funnel into ask_snap
	var third string
	for i := 0; i < 3; i++ {
		third, done = s.Next("zxqw vlkj porg")
		if done {
			t.Fatalf("chat ended early on middle message %d", i)
		}
	}
	if third == "" {
		t.Fatal("empty middle reply")
	}
	// yes after ask_snap -> share line with the assigned handle, then end
	share, done := s.Next("yes")
	if !done {
		t.Fatal("share should end the chat")
	}
	if !strings.Contains(share, "snapqueen") || strings.Contains(share, "%username%") {
		t.Fatalf("bad share line %q", share)
	}
}

func TestEVADirectShare(t *testing.T) {
	st := testStore(t)
	_ = st.WriteFile("snaps", "snapqueen")
	writeSettings(t, st, Settings{ChatMode: ModeEVA, SharePlatform: "snap"})
	m := NewManager(st)
	s := m.NewSession()
	_ = s.Open()
	line, done := s.Next("yeah") // share_snap trigger word
	if !done || !strings.Contains(line, "snapqueen") {
		t.Fatalf("direct share failed: %q done=%v", line, done)
	}
}

func TestAPIChatWithMockServer(t *testing.T) {
	st := testStore(t)
	_ = st.WriteFile("snaps", "snapqueen")

	srv := newMockLLM(t, "heyy wyd")
	writeSettings(t, st, Settings{
		ChatMode: ModeAPI, SharePlatform: "snap",
		AIBaseURL: srv.URL + "/v1", AIAPIKey: "k", AIModel: "m",
		APITemperature: 0.5, APIShareTurn: 2,
	})
	m := NewManager(st)
	if !m.Ready() || m.Settings.ChatMode != ModeAPI {
		t.Fatalf("api mode not active: warn=%q", m.Warning)
	}
	s := m.NewSession()
	if open := s.Open(); open != "heyy wyd" {
		t.Fatalf("bad opener %q", open)
	}
	if line, _ := s.Next("hi"); line != "heyy wyd" {
		t.Fatalf("bad first reply %q", line)
	}
	// second partner message crosses the share turn -> share line appended
	line, _ := s.Next("tell me more")
	if !strings.Contains(line, "snapqueen") || strings.Contains(line, "%username%") {
		t.Fatalf("share line missing handle: %q", line)
	}
	if !strings.Contains(line, "\n") {
		t.Fatalf("expected lead-in + share line, got %q", line)
	}
}

func TestAPIChatFallbackOnError(t *testing.T) {
	st := testStore(t)
	srv := newMockLLMStatus(t, 500, `{"error":{"message":"gated model"}}`)
	writeSettings(t, st, Settings{
		ChatMode: ModeAPI, AIBaseURL: srv.URL + "/v1", AIAPIKey: "bad", AIModel: "m",
	})
	m := NewManager(st)
	s := m.NewSession()
	line := s.Open()
	if line == "" || strings.Contains(line, "gated") {
		t.Fatalf("expected fallback line, got %q", line)
	}
}

func TestAPIChatRequiresKey(t *testing.T) {
	st := testStore(t)
	_ = st.WriteFile("chat", "hello")
	writeSettings(t, st, Settings{ChatMode: ModeAPI})
	m := NewManager(st)
	if m.Settings.ChatMode != ModeSingle || !strings.Contains(m.Warning, "API key") {
		t.Fatalf("expected key warning + single fallback, got %q warn=%q", m.Settings.ChatMode, m.Warning)
	}
}

func TestTestAPIHelper(t *testing.T) {
	srv := newMockLLM(t, "working fine")
	reply, err := TestAPI(srv.URL+"/v1", "k", "m", 0.9)
	if err != nil || reply != "working fine" {
		t.Fatalf("TestAPI: %q %v", reply, err)
	}
}

func TestSpintaxAndHandles(t *testing.T) {
	st := testStore(t)
	_ = st.WriteFile("snaps", "s1")
	_ = st.WriteFile("telegram", "t1")
	writeSettings(t, st, Settings{ChatMode: ModeSingle, SharePlatform: "both"})
	m := NewManager(st)
	for i := 0; i < 10; i++ {
		platform, handle := m.assignHandle()
		if platform != "snap" && platform != "tg" {
			t.Fatalf("bad platform %q", platform)
		}
		if handle != "s1" && handle != "t1" {
			t.Fatalf("bad handle %q", handle)
		}
	}
	if got := spin("{a|b}"); got != "a" && got != "b" {
		t.Fatalf("spintax broke: %q", got)
	}
}

// ---------------------------------------------------------------------------
// mock OpenAI-compatible server
// ---------------------------------------------------------------------------

func newMockLLM(t *testing.T, content string) *httptest.Server {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{
			"message": map[string]string{"role": "assistant", "content": content},
		}},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newMockLLMStatus(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}
