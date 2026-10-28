package dash

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Profile section still saves name/backend/headless; the launch_mode
// option was removed — the exe itself is the only thing that opens chrome
// now, so the snapshot must NOT advertise a launch_mode field anymore.
func TestSaveSectionProfile(t *testing.T) {
	a := Open(t.TempDir())

	snap, err := a.SaveSection("profile", map[string]any{
		"name":     "default",
		"backend":  "chromedp",
		"headless": false,
	})
	if err != nil {
		t.Fatalf("SaveSection: %v", err)
	}
	settings, ok := snap["settings"].(map[string]any)
	if !ok {
		t.Fatalf("snapshot has no settings: %#v", snap)
	}
	profile, ok := settings["profile"].(map[string]any)
	if !ok {
		t.Fatalf("snapshot has no profile section: %#v", settings)
	}
	if _, exists := profile["launch_mode"]; exists {
		t.Fatalf("profile.launch_mode must not be advertised anymore")
	}
}

func setupSettings(t *testing.T, snap map[string]any) map[string]any {
	t.Helper()
	settings, ok := snap["settings"].(map[string]any)
	if !ok {
		t.Fatalf("snapshot has no settings: %#v", snap)
	}
	setup, ok := settings["setup"].(map[string]any)
	if !ok {
		t.Fatalf("snapshot has no setup section: %#v", settings)
	}
	return setup
}

// StartRun saves only {mode, concurrency}. Omitted setup keys must survive.
func TestSaveSectionSetupPartialPreservesOmittedKeys(t *testing.T) {
	a := Open(t.TempDir())
	a.st.SaveSettings(map[string]string{
		"workspace_name":  "keep-name",
		"bot_mode":        "visible",
		"concurrency":     "2",
		"timeout_seconds": "90",
		"headless":        "true",
		"notes":           "keep-notes",
	})

	snap, err := a.SaveSection("setup", map[string]any{
		"mode":        "ws",
		"concurrency": float64(7),
	})
	if err != nil {
		t.Fatalf("SaveSection: %v", err)
	}
	setup := setupSettings(t, snap)
	want := map[string]any{
		"name":            "keep-name",
		"mode":            "ws",
		"concurrency":     7,
		"timeout_seconds": 90,
		"headless":        true,
		"notes":           "keep-notes",
	}
	for key, expected := range want {
		if got := setup[key]; got != expected {
			t.Errorf("setup[%s] = %#v, want %#v", key, got, expected)
		}
	}
}

// An omitted mode must not reset a stored ws mode back to visible.
func TestSaveSectionSetupOmittedModePreservesStoredMode(t *testing.T) {
	a := Open(t.TempDir())
	a.st.SaveSettings(map[string]string{"bot_mode": "ws", "concurrency": "2"})

	snap, err := a.SaveSection("setup", map[string]any{"concurrency": float64(4)})
	if err != nil {
		t.Fatalf("SaveSection: %v", err)
	}
	setup := setupSettings(t, snap)
	if setup["mode"] != "ws" {
		t.Errorf("mode = %#v, want ws", setup["mode"])
	}
	if setup["concurrency"] != 4 {
		t.Errorf("concurrency = %#v, want 4", setup["concurrency"])
	}
}

// Explicit false/empty values are real updates, not "missing" values.
func TestSaveSectionSetupExplicitFalseAndEmptyUpdates(t *testing.T) {
	a := Open(t.TempDir())
	a.st.SaveSettings(map[string]string{
		"workspace_name":  "old-name",
		"timeout_seconds": "90",
		"headless":        "true",
		"notes":           "old-notes",
	})

	snap, err := a.SaveSection("setup", map[string]any{
		"name":            "",
		"timeout_seconds": "",
		"headless":        false,
		"notes":           "",
	})
	if err != nil {
		t.Fatalf("SaveSection: %v", err)
	}
	setup := setupSettings(t, snap)
	if setup["headless"] != false {
		t.Errorf("headless = %#v, want false", setup["headless"])
	}
	if setup["notes"] != "" {
		t.Errorf("notes = %#v, want empty", setup["notes"])
	}
	stored := a.st.Settings()
	for _, key := range []string{"workspace_name", "timeout_seconds", "notes"} {
		if stored[key] != "" {
			t.Errorf("stored %s = %q, want explicitly empty", key, stored[key])
		}
	}
}

// A full setup save must keep working exactly as before.
func TestSaveSectionSetupFullSaveStillWorks(t *testing.T) {
	a := Open(t.TempDir())
	snap, err := a.SaveSection("setup", map[string]any{
		"name":            "new-name",
		"mode":            "visible",
		"concurrency":     float64(3),
		"timeout_seconds": float64(45),
		"headless":        true,
		"notes":           "new-notes",
	})
	if err != nil {
		t.Fatalf("SaveSection: %v", err)
	}
	setup := setupSettings(t, snap)
	want := map[string]any{
		"name":            "new-name",
		"mode":            "visible",
		"concurrency":     3,
		"timeout_seconds": 45,
		"headless":        true,
		"notes":           "new-notes",
	}
	for key, expected := range want {
		if got := setup[key]; got != expected {
			t.Errorf("setup[%s] = %#v, want %#v", key, got, expected)
		}
	}
}

// Clear must truncate both the legacy flat buffer and the structured kit ring.
func TestClearLogTruncatesBothBuffersAndKeepsVersionsMoving(t *testing.T) {
	a := Open(t.TempDir())
	a.appendLog("12:00:00 [system] before clear")

	before := a.LogsAfter(0)
	beforeVersion := before["version"].(int)
	if got := len(before["entries"].([]LogEntry)); got != 1 {
		t.Fatalf("entries before clear = %d, want 1", got)
	}

	a.ClearLog()
	after := a.LogsAfter(0)
	if after["cleared"] != true {
		t.Fatalf("cleared flag after ClearLog = %#v, want true", after["cleared"])
	}
	if got := len(after["entries"].([]LogEntry)); got != 0 {
		t.Fatalf("entries after clear = %d, want 0", got)
	}
	if len(a.logBuf) != 0 {
		t.Fatalf("legacy log buffer after clear = %d lines, want 0", len(a.logBuf))
	}

	a.appendLog("12:00:01 [system] after clear")
	next := a.LogsAfter(beforeVersion)
	entries := next["entries"].([]LogEntry)
	if len(entries) != 1 || entries[0].ID <= beforeVersion {
		t.Fatalf("post-clear entries = %#v, want one entry newer than %d", entries, beforeVersion)
	}
}

// The legacy web Clear button and the desktop bridge must hit the same route.
func TestClearLogsHTTPRouteMatchesDesktopContract(t *testing.T) {
	a := Open(t.TempDir())
	a.appendLog("12:00:00 [system] before clear")
	mux := a.routes()

	clearReq := httptest.NewRequest(http.MethodPost, "/api/ClearLogs", strings.NewReader("{}"))
	clearRec := httptest.NewRecorder()
	mux.ServeHTTP(clearRec, clearReq)
	if clearRec.Code != http.StatusOK {
		t.Fatalf("POST /api/ClearLogs status = %d, want 200", clearRec.Code)
	}
	var clearBody map[string]any
	if err := json.Unmarshal(clearRec.Body.Bytes(), &clearBody); err != nil {
		t.Fatalf("decode ClearLogs response: %v", err)
	}
	if clearBody["ok"] != true {
		t.Fatalf("ClearLogs response = %#v, want ok=true", clearBody)
	}

	logsReq := httptest.NewRequest(http.MethodGet, "/api/logs", nil)
	logsRec := httptest.NewRecorder()
	mux.ServeHTTP(logsRec, logsReq)
	if logsRec.Code != http.StatusOK {
		t.Fatalf("GET /api/logs status = %d, want 200", logsRec.Code)
	}
	var logsBody map[string]string
	if err := json.Unmarshal(logsRec.Body.Bytes(), &logsBody); err != nil {
		t.Fatalf("decode logs response: %v", err)
	}
	if logsBody["content"] != "" {
		t.Fatalf("legacy logs after ClearLogs = %q, want empty", logsBody["content"])
	}
}
func TestSaveSectionSetupWriteFailureReturnsError(t *testing.T) {
	a := Open(t.TempDir())
	if err := os.MkdirAll(filepath.Join(a.st.Dir, "settings.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SaveSection("setup", map[string]any{"concurrency": float64(2)}); err == nil {
		t.Fatal("expected setup save to fail when settings.txt cannot be written")
	}
}

func TestSaveSectionConnectionsProxyWriteFailureReturnsError(t *testing.T) {
	a := Open(t.TempDir())
	if err := os.MkdirAll(filepath.Join(a.st.Dir, "proxies.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := a.SaveSection("connections", map[string]any{"enabled": true, "scheme": "http", "entries": "proxy.invalid:8080"})
	if err == nil {
		t.Fatal("expected connections save to fail when proxies.txt cannot be written")
	}
}

func TestSaveSectionActionsChatWriteFailureReturnsError(t *testing.T) {
	a := Open(t.TempDir())
	if err := os.MkdirAll(filepath.Join(a.st.Dir, "msg", "chat.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := a.SaveSection("actions", map[string]any{"strategy": "single", "sequence": "hello", "targets": ""})
	if err == nil {
		t.Fatal("expected actions save to fail when chat.txt cannot be written")
	}
}

func TestSaveSectionCredentialsMessageSettingsWriteFailureReturnsError(t *testing.T) {
	a := Open(t.TempDir())
	if err := os.MkdirAll(filepath.Join(a.st.Dir, "message_settings.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SaveSection("credentials", map[string]any{"model": "test-model"}); err == nil {
		t.Fatal("expected credentials save to fail when message_settings.json cannot be written")
	}
}

func TestLegacySettingsRouteReportsWriteFailure(t *testing.T) {
	a := Open(t.TempDir())
	if err := os.MkdirAll(filepath.Join(a.st.Dir, "settings.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(`{"concurrency":"2"}`))
	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, req)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["ok"] != false || body["error"] == nil || body["error"] == "" {
		t.Fatalf("legacy settings failure response = %#v, want ok=false and error", body)
	}
}

func TestLegacyFileSaveRouteReportsWriteFailure(t *testing.T) {
	a := Open(t.TempDir())
	if err := os.MkdirAll(filepath.Join(a.st.Dir, "msg", "chat.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/file/save", strings.NewReader(`{"name":"chat","content":"hello"}`))
	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, req)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["ok"] != false || body["error"] == nil || body["error"] == "" {
		t.Fatalf("legacy file save failure response = %#v, want ok=false and error", body)
	}
}

func TestLegacyMessageSaveRouteReportsWriteFailure(t *testing.T) {
	a := Open(t.TempDir())
	if err := os.MkdirAll(filepath.Join(a.st.Dir, "message_settings.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/message/save", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, req)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["ok"] != false || body["error"] == nil || body["error"] == "" {
		t.Fatalf("legacy message save failure response = %#v, want ok=false and error", body)
	}
}
