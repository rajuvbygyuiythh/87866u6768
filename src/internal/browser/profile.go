package browser

// Profile persistence: everything needed for "the next run uses the SAME
// browser as the registration". A Chrome profile directory holds the cookies,
// localStorage, IndexedDB and history the site uses to recognise a returning
// user; ProfileMeta records the device identity that profile must keep using
// (UA/locale/timezone/screen) plus pointers to the session dumps.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"thundr/internal/store"
)

// ProfileMeta is written to data/profiles/<uid>.json after a successful
// registration (and refreshed on every run through TouchProfile).
type ProfileMeta struct {
	UserID       string   `json:"user_id"`
	Email        string   `json:"email"`
	Username     string   `json:"username"`
	ProfileDir   string   `json:"profile_dir"`
	UserAgent    string   `json:"user_agent"`
	ChromeMajor  string   `json:"chrome_major"`
	Language     string   `json:"language"`
	TimeZone     string   `json:"time_zone"`
	Screen       string   `json:"screen"`
	WebGL        string   `json:"webgl"`
	Canvas       string   `json:"canvas"`
	HardwareConc string   `json:"hardware_concurrency"`
	Proxy        string   `json:"proxy"`
	ProxyTZ      string   `json:"proxy_timezone"`
	StorageKeys  []string `json:"local_storage_keys"`
	StorageBytes int      `json:"local_storage_bytes"`
	AccountFile  string   `json:"account_file"`
	StorageFile  string   `json:"storage_file"`
	Fingerprint  string   `json:"fingerprint_file"`
	CreatedAt    string   `json:"created_at"`
	LastUsedAt   string   `json:"last_used_at"`
}

func profileMetaPath(st *store.Store, uid string) string {
	return st.DirPath(filepath.Join("profiles", uid+".json"))
}

// saveLocalStorage persists the full localStorage dump next to the user
// object, so a session can be rebuilt even if the profile dir is lost.
func saveLocalStorage(st *store.Store, uid string, storage map[string]string) string {
	if len(storage) == 0 {
		return ""
	}
	raw, err := json.MarshalIndent(storage, "", "  ")
	if err != nil {
		return ""
	}
	rel := filepath.Join("sessions", uid+".storage.json")
	_ = os.MkdirAll(st.DirPath("sessions"), 0o755)
	if err := os.WriteFile(st.DirPath(rel), raw, 0o644); err != nil {
		return ""
	}
	return rel
}

// credsFor looks the username up in registered_creds.txt (uid is field 3).
func credsFor(st *store.Store, uid string) string {
	data, err := os.ReadFile(st.DirPath("registered_creds.txt"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) >= 3 && strings.TrimSpace(parts[2]) == uid {
			return strings.TrimSpace(parts[0])
		}
	}
	return ""
}

// saveProfileMeta writes the profile record and logs a one-line summary.
func saveProfileMeta(st *store.Store, acc store.Account, dir, username, proxy, proxyTZ string, storage map[string]string, log func(string)) {
	fp, _ := st.LoadFingerprint(acc.UserID)
	if username == "" {
		username = credsFor(st, acc.UserID)
	}

	keys := make([]string, 0, len(storage))
	bytes := 0
	for k, v := range storage {
		keys = append(keys, k)
		bytes += len(k) + len(v)
	}
	sort.Strings(keys)

	storageRel := saveLocalStorage(st, acc.UserID, storage)
	now := time.Now().Format("2006-01-02 15:04:05")
	meta := ProfileMeta{
		UserID:       acc.UserID,
		Email:        acc.Email,
		Username:     username,
		ProfileDir:   dir,
		UserAgent:    fp.UserAgent,
		ChromeMajor:  store.ChromeMajor(fp.UserAgent),
		Language:     fp.Language,
		TimeZone:     fp.TimeZone,
		Screen:       fp.ScreenResolution,
		WebGL:        fp.WebGL,
		Canvas:       fp.Canvas,
		HardwareConc: fp.HardwareConcurrency,
		Proxy:        proxy,
		ProxyTZ:      proxyTZ,
		StorageKeys:  keys,
		StorageBytes: bytes,
		AccountFile:  filepath.Join("sessions", acc.UserID+".json"),
		StorageFile:  storageRel,
		Fingerprint:  filepath.Join("fingerprints", acc.UserID+".json"),
		CreatedAt:    now,
		LastUsedAt:   now,
	}
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(st.DirPath("profiles"), 0o755)
	if err := os.WriteFile(profileMetaPath(st, acc.UserID), raw, 0o644); err != nil {
		log("profile meta write failed: " + err.Error())
		return
	}
	log(fmt.Sprintf("profile record saved: %d localStorage key(s), %d bytes, ua=Chrome/%s tz=%s",
		len(keys), bytes, meta.ChromeMajor, meta.TimeZone))
}

// TouchProfile refreshes last_used_at when an account's profile is driven by a
// bot run (best effort, never fails the run).
func TouchProfile(st *store.Store, uid string) {
	p := profileMetaPath(st, uid)
	raw, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var meta ProfileMeta
	if json.Unmarshal(raw, &meta) != nil {
		return
	}
	meta.LastUsedAt = time.Now().Format("2006-01-02 15:04:05")
	if out, err := json.MarshalIndent(meta, "", "  "); err == nil {
		_ = os.WriteFile(p, out, 0o644)
	}
}

// LoadProfileMeta returns the stored profile record for an account.
func LoadProfileMeta(st *store.Store, uid string) (ProfileMeta, bool) {
	var meta ProfileMeta
	raw, err := os.ReadFile(profileMetaPath(st, uid))
	if err != nil {
		return meta, false
	}
	if json.Unmarshal(raw, &meta) != nil {
		return ProfileMeta{}, false
	}
	return meta, true
}

// waitPortDown blocks until the DevTools endpoint stops answering (the chrome
// process has exited) or the deadline passes.
func waitPortDown(attachURL string, d time.Duration) {
	if attachURL == "" {
		return
	}
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !DebugPortUpOn(attachURL) {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// moveProfile renames the registration profile dir onto browser-profiles/<uid>.
// Windows keeps a chrome profile locked for a moment after the process exits,
// so this retries instead of failing the whole registration.
func moveProfile(from, to string, log func(string)) bool {
	if from == to {
		return true
	}
	if _, err := os.Stat(from); err != nil {
		log("profile dir missing after registration: " + from)
		return false
	}
	_ = os.RemoveAll(to) // replace any stale profile for this uid
	for attempt := 1; attempt <= 15; attempt++ {
		if err := os.Rename(from, to); err == nil {
			log("profile saved -> browser-profiles/" + filepath.Base(to))
			return true
		} else if attempt == 1 {
			log("profile dir locked, retrying: " + err.Error())
		}
		time.Sleep(time.Second)
	}
	log("profile save FAILED (dir still locked) - this account will need a manual login")
	return false
}
