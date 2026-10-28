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
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Store struct {
	Dir string
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
	data, err := os.ReadFile(s.path(name))
	if err != nil {
		return false
	}
	return json.Unmarshal(data, v) == nil
}

// WriteJSON marshals v into a JSON file in the data directory.
func (s *Store) WriteJSON(name string, v any) bool {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return false
	}
	return os.WriteFile(s.path(name), data, 0o644) == nil
}

// ---- settings ---------------------------------------------------------------

func (s *Store) Settings() map[string]string {
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

func (s *Store) SaveSettings(updates map[string]string) {
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
	_ = os.WriteFile(s.path("settings.txt"), []byte(strings.Join(out, "\n")), 0o644)
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
	f, err := os.OpenFile(s.path("registered_accounts.txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(uid + "|" + email + "|" + token + "\n")
}

func (s *Store) AppendCreds(username, password, uid, token string) {
	f, err := os.OpenFile(s.path("registered_creds.txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(username + "|" + password + "|" + uid + "|" + token + "\n")
}

func (s *Store) DeleteAccount(uid string) {
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
	_ = os.WriteFile(s.path("registered_accounts.txt"), []byte(strings.Join(kept, "\n")), 0o644)
	_ = os.Remove(s.path("sessions/" + uid + ".json"))
}

// ---- sessions ----------------------------------------------------------------

func (s *Store) SaveSession(uid, rawUserJSON string) {
	_ = os.WriteFile(s.path("sessions/"+uid+".json"), []byte(rawUserJSON), 0o644)
}

func (s *Store) Session(uid string) (string, bool) {
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
	data, _ := os.ReadFile(s.path(rel))
	return string(data)
}

func (s *Store) WriteFile(name, content string) bool {
	rel, ok := EditableFiles[name]
	if !ok {
		return false
	}
	return os.WriteFile(s.path(rel), []byte(content), 0o644) == nil
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
