package store

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestWriteFileAtomicReplacesContentAndCleansTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.txt")
	if err := os.WriteFile(path, []byte("old=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(path, []byte("new=2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "new=2\n" {
		t.Fatalf("replacement failed: %q err=%v", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("temporary file left behind: %s", entry.Name())
		}
	}
}

func TestWriteFileAtomicPreservesExistingPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission bits")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("{}"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(path, []byte(`{"ok":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("permissions changed: %o", info.Mode().Perm())
	}
}

func TestWriteFileAtomicFailureKeepsOriginal(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(filepath.Join(blocked, "settings.txt"), []byte("new"), 0o644); err == nil {
		t.Fatal("expected failure when a parent path is a regular file")
	}
	data, err := os.ReadFile(blocked)
	if err != nil || string(data) != "original" {
		t.Fatalf("original file changed: %q err=%v", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "blocked" {
		t.Fatalf("unexpected leftover files: %v", entries)
	}
}

func TestStoreConcurrentSettingsKeepEveryUpdate(t *testing.T) {
	st := Open(t.TempDir())
	st.SaveSettings(map[string]string{"base": "keep"})

	const writers = 24
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			st.SaveSettings(map[string]string{fmt.Sprintf("key_%02d", i): fmt.Sprintf("value_%02d", i)})
		}(i)
	}
	wg.Wait()

	got := st.Settings()
	if len(got) != writers+1 || got["base"] != "keep" {
		t.Fatalf("lost base setting or wrong count: %d", len(got))
	}
	for i := 0; i < writers; i++ {
		key := fmt.Sprintf("key_%02d", i)
		if got[key] != fmt.Sprintf("value_%02d", i) {
			t.Fatalf("lost update for %s", key)
		}
	}
}

func TestStoreConcurrentReadersAndWriters(t *testing.T) {
	st := Open(t.TempDir())
	st.SaveSettings(map[string]string{"mode": "start"})
	done := make(chan struct{})
	var readers sync.WaitGroup
	for i := 0; i < 4; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-done:
					return
				default:
					_ = st.Settings()
					_ = st.Get("mode", "")
				}
			}
		}()
	}
	for i := 0; i < 20; i++ {
		st.SaveSettings(map[string]string{"mode": fmt.Sprintf("step_%02d", i)})
	}
	close(done)
	readers.Wait()
	if !strings.HasPrefix(st.Get("mode", ""), "step_") {
		t.Fatal("final settings value was not preserved")
	}
}

func TestStoreConcurrentAppendsKeepWholeRecords(t *testing.T) {
	st := Open(t.TempDir())
	const records = 20
	var wg sync.WaitGroup
	for i := 0; i < records; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			uid := fmt.Sprintf("u%02d", i)
			st.AppendAccount(uid, uid+"@example.invalid", "synthetic-token")
			st.AppendCreds(uid, "synthetic-password", uid, "synthetic-token")
		}(i)
	}
	wg.Wait()

	for _, name := range []string{"registered_accounts.txt", "registered_creds.txt"} {
		data, err := os.ReadFile(filepath.Join(st.Dir, name))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) != records {
			t.Fatalf("%s: want %d records, got %d", name, records, len(lines))
		}
		for i := 0; i < records; i++ {
			if !strings.Contains(string(data), fmt.Sprintf("u%02d|", i)) {
				t.Fatalf("%s: missing record %02d", name, i)
			}
		}
	}
}

func TestStoreSettingsMergePreservesCommentsAndUnrelatedKeys(t *testing.T) {
	st := Open(t.TempDir())
	initial := "# header\r\nkeep=yes\r\nchange=old\r\n\r\n"
	if err := os.WriteFile(filepath.Join(st.Dir, "settings.txt"), []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	st.SaveSettings(map[string]string{"change": "new"})
	data, err := os.ReadFile(filepath.Join(st.Dir, "settings.txt"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"# header\r\n", "keep=yes\r\n", "change=new"} {
		if !strings.Contains(text, want) {
			t.Fatalf("merged settings lost %q", want)
		}
	}
	if got := st.Settings(); got["keep"] != "yes" || got["change"] != "new" {
		t.Fatalf("parsed settings wrong: %v", got)
	}
}

func TestWriteJSONFailureKeepsOriginal(t *testing.T) {
	st := Open(t.TempDir())
	if !st.WriteJSON("message_settings.json", map[string]any{"mode": "old"}) {
		t.Fatal("initial WriteJSON failed")
	}
	if st.WriteJSON("message_settings.json", map[string]any{"bad": make(chan int)}) {
		t.Fatal("expected marshal failure for an unsupported value")
	}
	var got map[string]any
	if !st.ReadJSON("message_settings.json", &got) || got["mode"] != "old" {
		t.Fatalf("original JSON was damaged: %v", got)
	}
}

func TestStoreAppendRecordKeepsExistingLines(t *testing.T) {
	st := Open(t.TempDir())
	path := filepath.Join(st.Dir, "registered_accounts.txt")
	if err := os.WriteFile(path, []byte("u0|u0@example.invalid|token\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st.AppendAccount("u1", "u1@example.invalid", "token")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "u0|") || !strings.HasPrefix(lines[1], "u1|") {
		t.Fatalf("append lost existing records: %q", lines)
	}
}

func TestStoreJSONListAndSessionRoundTrip(t *testing.T) {
	st := Open(t.TempDir())
	if !st.WriteJSON("message_settings.json", map[string]any{"mode": "test"}) {
		t.Fatal("WriteJSON failed")
	}
	var settings map[string]any
	if !st.ReadJSON("message_settings.json", &settings) || settings["mode"] != "test" {
		t.Fatalf("JSON round trip failed: %v", settings)
	}
	if !st.WriteFile("chat", "one\ntwo") {
		t.Fatal("WriteFile failed")
	}
	if lines := st.ChatLines(); len(lines) != 2 || lines[0] != "one" || lines[1] != "two" {
		t.Fatalf("chat round trip failed: %v", lines)
	}
	st.SaveSession("u1", `{"user_id":"u1"}`)
	if raw, ok := st.Session("u1"); !ok || raw != `{"user_id":"u1"}` {
		t.Fatalf("session round trip failed: %q ok=%v", raw, ok)
	}
}

func TestSaveSettingsReturnsWriteError(t *testing.T) {
	st := Open(t.TempDir())
	if err := os.MkdirAll(filepath.Join(st.Dir, "settings.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSettings(map[string]string{"mode": "ws"}); err == nil {
		t.Fatal("expected SaveSettings to report a write error")
	}
}

func TestWriteFileCheckedReportsUnknownAndWriteErrors(t *testing.T) {
	st := Open(t.TempDir())
	if err := st.WriteFileChecked("missing", "x"); err == nil {
		t.Fatal("expected unknown editable file error")
	}
	if err := os.MkdirAll(filepath.Join(st.Dir, "msg", "chat.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteFileChecked("chat", "hello"); err == nil {
		t.Fatal("expected chat write error")
	}
}

func TestWriteJSONCheckedReportsWriteError(t *testing.T) {
	st := Open(t.TempDir())
	if err := os.MkdirAll(filepath.Join(st.Dir, "message_settings.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteJSONChecked("message_settings.json", map[string]any{"mode": "single"}); err == nil {
		t.Fatal("expected message settings write error")
	}
}
