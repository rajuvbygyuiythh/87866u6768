package browser

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"thundr/internal/store"
)

// The pure helpers around session injection (SavedSessionEntries + the JS
// builder) run without a browser: a store fixture is enough.
func TestSavedSessionEntriesFromStorageDump(t *testing.T) {
	st := store.Open(t.TempDir())
	dump := map[string]string{
		"user":  `{"id":"u1","hashed_token":"tok"}`,
		"flags": `{"v":1}`,
	}
	raw, _ := json.Marshal(dump)
	if !st.WriteJSON(filepath.Join("sessions", "u1.storage.json"), json.RawMessage(raw)) {
		t.Fatal("write storage dump")
	}
	entries := SavedSessionEntries(st, "u1")
	if len(entries) != 2 || entries["user"] == "" {
		t.Fatalf("entries = %v", entries)
	}
}

func TestSavedSessionEntriesFallsBackToUserObject(t *testing.T) {
	st := store.Open(t.TempDir())
	sess := filepath.Join("sessions", "u2.json")
	if !st.WriteJSON(sess, map[string]any{"id": "u2", "hashed_token": "tok2"}) {
		t.Fatal("write session")
	}
	entries := SavedSessionEntries(st, "u2")
	if len(entries) != 1 || entries["user"] == "" {
		t.Fatalf("entries = %v", entries)
	}
	var u map[string]any
	if json.Unmarshal([]byte(entries["user"]), &u) != nil || u["hashed_token"] != "tok2" {
		t.Fatalf("user payload = %q", entries["user"])
	}
}

func TestSavedSessionEntriesEmptyWhenNothingValid(t *testing.T) {
	st := store.Open(t.TempDir())
	// a session WITHOUT a token cannot log in -> must not be offered
	if !st.WriteJSON(filepath.Join("sessions", "u3.json"), map[string]any{"id": "u3"}) {
		t.Fatal("write session")
	}
	if entries := SavedSessionEntries(st, "u3"); len(entries) != 0 {
		t.Fatalf("entries = %v", entries)
	}
}

func TestInjectSessionJSIsRunnable(t *testing.T) {
	js, err := InjectSessionJS(map[string]string{"user": `{"a":"b"}`})
	if err != nil {
		t.Fatal(err)
	}
	// the payload is embedded as a JS object literal between "const m = " and "; let n"
	if !strings.HasPrefix(js, "(() => { try { const m = {") {
		t.Fatalf("bad js prefix: %.60s", js)
	}
	if !strings.Contains(js, `localStorage.setItem(k, typeof v === "string" ? v : JSON.stringify(v))`) {
		t.Fatalf("value passthrough missing: %.120s", js)
	}
	if !strings.HasSuffix(js, "return -1; } })()") {
		t.Fatalf("bad js suffix: %.60s", js)
	}
}

// Guard the store side too: SessionStorage/SavedSessionEntries read the exact
// file saveLocalStorage writes (sessions/<uid>.storage.json).
func TestStoreSessionStorageRoundTrip(t *testing.T) {
	st := store.Open(t.TempDir())
	_ = os.MkdirAll(filepath.Join(st.Dir, "sessions"), 0o755)
	path := filepath.Join(st.Dir, "sessions", "u4.storage.json")
	if os.WriteFile(path, []byte(`{"user":"{\"id\":\"u4\",\"hashed_token\":\"t\"}"}`), 0o644) != nil {
		t.Fatal("write")
	}
	if raw, ok := st.SessionStorage("u4"); !ok || !strings.Contains(raw, "hashed_token") {
		t.Fatalf("SessionStorage = %q %v", raw, ok)
	}
}