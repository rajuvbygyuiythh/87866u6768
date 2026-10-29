package store

// Per-account browser fingerprint, ported from the reference project's
// model.TransportFingerprint: captured from the real chrome at registration
// time, then replayed everywhere (WS/API headers) so each account always
// presents one consistent device identity.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// Fingerprint mirrors the reference project's model.TransportFingerprint.
type Fingerprint struct {
	UserAgent           string `json:"userAgent"`
	Language            string `json:"language"`
	ColorDepth          string `json:"colorDepth"`
	ScreenResolution    string `json:"screenResolution"`
	TimeZone            string `json:"timeZone"`
	AddBehavior         string `json:"addBehavior"`
	OpenDatabase        string `json:"openDatabase"`
	Plugins             string `json:"plugins"`
	Canvas              string `json:"canvas"`
	WebGL               string `json:"webgl"`
	TouchSupport        string `json:"touchSupport"`
	HardwareConcurrency string `json:"hardwareConcurrency"`
}

// SaveFingerprint persists an account fingerprint under data/fingerprints/.
func (s *Store) SaveFingerprint(uid string, fp Fingerprint) bool {
	_ = os.MkdirAll(s.path("fingerprints"), 0o755)
	return s.WriteJSON(filepath.Join("fingerprints", uid+".json"), fp)
}

// LoadFingerprint returns the stored fingerprint (ok=false if none).
func (s *Store) LoadFingerprint(uid string) (Fingerprint, bool) {
	var fp Fingerprint
	if !s.ReadJSON(filepath.Join("fingerprints", uid+".json"), &fp) {
		return Fingerprint{}, false
	}
	return fp, true
}

// UserAgentFor returns the account's stored UA, or the deterministic pool UA
// when unique UA mode is on, or the default UA.
func (s *Store) UserAgentFor(uid string) string {
	if fp, ok := s.LoadFingerprint(uid); ok && fp.UserAgent != "" {
		return fp.UserAgent
	}
	if s.Bool("unique_ua_per_browser", false) {
		return UAForUID(uid)
	}
	return DefaultUA
}

var chromeMajorRe = regexp.MustCompile(`Chrome/(\d+)\.`)

// ChromeMajor extracts the Chrome major version from a UA string.
func ChromeMajor(userAgent string) string {
	m := chromeMajorRe.FindStringSubmatch(userAgent)
	if len(m) == 2 {
		return m[1]
	}
	return "131"
}

// SecCHUA builds the sec-ch-ua header value matching the UA's Chrome major.
func SecCHUA(userAgent string) string {
	maj := ChromeMajor(userAgent)
	return fmt.Sprintf(`"Chromium";v="%s", "Not-A.Brand";v="24", "Google Chrome";v="%s"`, maj, maj)
}

// Summary is a one-line human description for logs/UI.
func (f Fingerprint) Summary() string {
	return fmt.Sprintf("Chrome/%s · %s · %s · %s · hw=%s",
		ChromeMajor(f.UserAgent), f.Language, f.ScreenResolution, f.TimeZone, f.HardwareConcurrency)
}

// DefaultUA when nothing is stored yet.
const DefaultUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// UAPool is the stable pool used when unique_ua_per_browser is enabled:
// each account gets one entry (deterministic by id) so every browser is a
// different "device" while staying consistent across restarts.
var UAPool = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/127.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
}

// UAForUID deterministically picks a pool UA for an account id.
func UAForUID(uid string) string {
	h := 0
	for _, c := range uid {
		h = h*31 + int(c)
	}
	if h < 0 {
		h = -h
	}
	return UAPool[h%len(UAPool)]
}
