// Package store owns the project's on-disk data files. Formats are identical
// to the Python project so both can run side by side:
//
//	settings.txt              key=value (+ # comments)
//	registered_accounts.txt   user_id|email|hashed_token (+ # comments)
//	registered_creds.txt      username|password|user_id|hashed_token
//	sessions/<uid>.json       exact localStorage "user" object
//	msg/chat.txt              bot chat lines (one per line)
//	snaps.txt / telegram.txt / interests.txt / proxies.txt
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Store owns the on-disk state files. It must not be copied after first use;
// the unexported mutex serializes read-modify-write operations and protects
// readers from observing a replacement mid-write.
type Store struct {
	Dir string
	mu  sync.RWMutex
}

func Open(dir string) *Store {
	_ = os.MkdirAll(filepath.Join(dir, "sessions"), 0o755)
	_ = os.MkdirAll(filepath.Join(dir, "msg"), 0o755)
	return &Store{Dir: dir}
}

func (s *Store) path(name string) string { return filepath.Join(s.Dir, name) }

// DirPath returns an absolute path inside the data directory.
func (s *Store) DirPath(rel string) string { return filepath.Join(s.Dir, rel) }

// ---- JSON state files (message settings, loaded sequences) -------------------

// ReadJSON unmarshals a JSON file from the data directory into v.
func (s *Store) ReadJSON(name string, v any) bool {
	s.mu.RLock()
	data, err := os.ReadFile(s.path(name))
	s.mu.RUnlock()
	if err != nil {
		return false
	}
	return json.Unmarshal(data, v) == nil
}

// WriteJSON marshals v into a JSON file in the data directory.
func (s *Store) WriteJSON(name string, v any) bool {
	return s.WriteJSONChecked(name, v) == nil
}

// WriteJSONChecked is WriteJSON with the underlying error preserved for UI paths.
func (s *Store) WriteJSONChecked(name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", name, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := writeFileAtomic(s.path(name), data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

// ---- settings ---------------------------------------------------------------

func (s *Store) Settings() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settingsLocked()
}

// settingsLocked parses settings.txt. Callers must hold at least a read lock.
func (s *Store) settingsLocked() map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile(s.path("settings.txt"))
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		kv := strings.SplitN(line, "=", 2)
		out[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
	}
	return out
}

func (s *Store) SaveSettings(updates map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var lines []string
	if data, err := os.ReadFile(s.path("settings.txt")); err == nil {
		lines = strings.Split(string(data), "\n")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(lines)+len(updates))
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if trim != "" && !strings.HasPrefix(trim, "#") && strings.Contains(trim, "=") {
			k := strings.TrimSpace(strings.SplitN(trim, "=", 2)[0])
			if v, ok := updates[k]; ok {
				out = append(out, k+"="+v)
				seen[k] = true
				continue
			}
		}
		out = append(out, line)
	}
	for k, v := range updates {
		if !seen[k] {
			out = append(out, k+"="+v)
		}
	}
	if err := writeFileAtomic(s.path("settings.txt"), []byte(strings.Join(out, "\n")), 0o644); err != nil {
		return fmt.Errorf("write settings.txt: %w", err)
	}
	return nil
}

func (s *Store) Get(key, def string) string {
	if v, ok := s.Settings()[key]; ok && v != "" {
		return v
	}
	return def
}

func (s *Store) Float(key string, def float64) float64 {
	f, err := strconv.ParseFloat(s.Get(key, ""), 64)
	if err != nil {
		return def
	}
	return f
}

func (s *Store) Bool(key string, def bool) bool {
	switch strings.ToLower(s.Get(key, "")) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

func (s *Store) Int(key string, def int) int {
	n, err := strconv.Atoi(s.Get(key, ""))
	if err != nil {
		return def
	}
	return n
}

// ---- accounts ---------------------------------------------------------------

type Account struct {
	UserID      string `json:"user_id"`
	Email       string `json:"email"`
	HashedToken string `json:"-"`
	HasSession  bool   `json:"has_session"`
}

func (s *Store) Accounts() []Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Account
	data, err := os.ReadFile(s.path("registered_accounts.txt"))
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 3 {
			continue
		}
		_, sessErr := os.Stat(s.path("sessions/" + parts[0] + ".json"))
		out = append(out, Account{
			UserID: parts[0], Email: parts[1], HashedToken: parts[2],
			HasSession: sessErr == nil || s.HasProfile(parts[0]),
		})
	}
	return out
}

func (s *Store) AppendAccount(uid, email, token string) {
	s.appendRecord("registered_accounts.txt", uid+"|"+email+"|"+token)
}

func (s *Store) AppendCreds(username, password, uid, token string) {
	s.appendRecord("registered_creds.txt", username+"|"+password+"|"+uid+"|"+token)
}

// appendRecord adds one line to an append-only record file. O_APPEND keeps
// separate processes sharing the data directory from overwriting each other;
// the mutex serializes goroutines inside this process.
func (s *Store) appendRecord(name, record string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.path(name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(record + "\n")
}

func (s *Store) DeleteAccount(uid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path("registered_accounts.txt"))
	if err != nil {
		return
	}
	var kept []string
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, uid+"|") {
			kept = append(kept, line)
		}
	}
	if err := writeFileAtomic(s.path("registered_accounts.txt"), []byte(strings.Join(kept, "\n")), 0o644); err != nil {
		return
	}
	_ = os.Remove(s.path("sessions/" + uid + ".json"))
}

// ---- sessions ----------------------------------------------------------------

func (s *Store) SaveSession(uid, rawUserJSON string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = writeFileAtomic(s.path("sessions/"+uid+".json"), []byte(rawUserJSON), 0o644)
}

func (s *Store) Session(uid string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, err := os.ReadFile(s.path("sessions/" + uid + ".json"))
	if err != nil {
		return "", false
	}
	return string(data), true
}

// SessionStorage returns the full localStorage dump captured at registration
// (sessions/<uid>.storage.json) — the payload the browser replays when a
// profile dir was lost or rejected (session injection).
func (s *Store) SessionStorage(uid string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, err := os.ReadFile(s.path("sessions/" + uid + ".storage.json"))
	if err != nil {
		return "", false
	}
	return string(data), true
}

// ProfileDir is the persistent Chrome profile for an account (cookies +
// localStorage + app state - required for the app to accept a returning user).
// Profiles live at <root>/browser-profiles (next to data/), keeping the bulky
// chrome state out of the small data folder.
func (s *Store) ProfileDir(uid string) string {
	root := s.Dir
	if filepath.Base(root) == "data" {
		root = filepath.Dir(root)
	}
	return filepath.Join(root, "browser-profiles", uid)
}

func (s *Store) HasProfile(uid string) bool {
	st, err := os.Stat(s.ProfileDir(uid))
	return err == nil && st.IsDir()
}

// RemoveProfile deletes an account's chrome profile directory (best effort;
// used when the account is banned + removed).
func (s *Store) RemoveProfile(uid string) error {
	dir := s.ProfileDir(uid)
	if _, err := os.Stat(dir); err != nil {
		return nil // nothing to remove
	}
	return os.RemoveAll(dir)
}

// ---- list/message files -------------------------------------------------------

var EditableFiles = map[string]string{
	"chat":      "msg/chat.txt",
	"snaps":     "snaps.txt",
	"telegram":  "telegram.txt",
	"interests": "interests.txt",
	"proxies":   "proxies.txt",
}

func (s *Store) ReadFile(name string) string {
	rel, ok := EditableFiles[name]
	if !ok {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, _ := os.ReadFile(s.path(rel))
	return string(data)
}

func (s *Store) WriteFile(name, content string) bool {
	return s.WriteFileChecked(name, content) == nil
}

// WriteFileChecked is WriteFile with the underlying error preserved for UI paths.
func (s *Store) WriteFileChecked(name, content string) error {
	rel, ok := EditableFiles[name]
	if !ok {
		return fmt.Errorf("unknown editable file %q", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := writeFileAtomic(s.path(rel), []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", rel, err)
	}
	return nil
}

// LoadList reads a # -commented list file (used for snaps/telegram/interests).
func (s *Store) LoadList(name string) []string {
	var out []string
	for _, line := range strings.Split(s.ReadFile(name), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return out
}

func (s *Store) ChatLines() []string {
	var out []string
	for _, line := range strings.Split(s.ReadFile("chat"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// Tail returns the last n lines of a log file.
func (s *Store) Tail(name string, n int) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, err := os.ReadFile(s.path(name))
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
