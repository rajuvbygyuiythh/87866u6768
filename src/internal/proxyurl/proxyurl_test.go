package proxyurl

import (
	"os"
	"path/filepath"
	"testing"

	"thundr/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	return store.Open(t.TempDir())
}

func TestNormalize(t *testing.T) {
	cases := []struct {
		line, scheme, want string
	}{
		{"1.2.3.4:8080", HTTP, "http://1.2.3.4:8080"},
		{"1.2.3.4:8080", SOCKS5, "socks5://1.2.3.4:8080"},
		{"1.2.3.4:8080", "", "http://1.2.3.4:8080"},
		{"1.2.3.4:8080:user:pa:ss", SOCKS5, "socks5://user:pa:ss@1.2.3.4:8080"},
		{"user:pass@1.2.3.4:8080", HTTPS, "https://user:pass@1.2.3.4:8080"},
		{"socks5://1.2.3.4:1080", HTTP, "socks5://1.2.3.4:1080"}, // own scheme wins
		{"  ", HTTP, ""},
	}
	for _, c := range cases {
		if got := Normalize(c.line, c.scheme); got != c.want {
			t.Errorf("Normalize(%q, %q) = %q, want %q", c.line, c.scheme, got, c.want)
		}
	}
}

func TestSchemePrefersKitKeyThenLegacy(t *testing.T) {
	st := testStore(t)
	if got := Scheme(st); got != HTTP {
		t.Fatalf("default scheme = %q, want %q", got, HTTP)
	}

	st.SaveSettings(map[string]string{"proxytype": "socks5"})
	if got := Scheme(st); got != SOCKS5 {
		t.Fatalf("legacy proxytype scheme = %q, want %q", got, SOCKS5)
	}

	// the kit key must win when both exist
	st.SaveSettings(map[string]string{"proxy_scheme": "https"})
	if got := Scheme(st); got != HTTPS {
		t.Fatalf("proxy_scheme should win, got %q", got)
	}

	// junk falls back to the next key, then to http
	st.SaveSettings(map[string]string{"proxy_scheme": "banana"})
	if got := Scheme(st); got != SOCKS5 {
		t.Fatalf("junk proxy_scheme should fall back to proxytype, got %q", got)
	}
}

func TestNormalizeSchemeAliases(t *testing.T) {
	for _, in := range []string{"SOCKS5", "socks5h", " socks "} {
		if got := NormalizeScheme(in); got != SOCKS5 {
			t.Errorf("NormalizeScheme(%q) = %q, want %q", in, got, SOCKS5)
		}
	}
	if got := NormalizeScheme("banana"); got != "" {
		t.Errorf("NormalizeScheme(banana) = %q, want empty", got)
	}
}

func TestLabelAndCredentials(t *testing.T) {
	raw := Normalize("1.2.3.4:8080:user:pa:ss", SOCKS5)
	if got := Label(raw); got != "1.2.3.4:8080" {
		t.Errorf("Label = %q", got)
	}
	u, p := Credentials(raw)
	if u != "user" || p != "pa:ss" {
		t.Errorf("Credentials = %q/%q", u, p)
	}
	if !IsSOCKS(raw) {
		t.Errorf("IsSOCKS(%q) = false", raw)
	}
	if IsSOCKS(Normalize("1.2.3.4:8080", HTTP)) {
		t.Errorf("http proxy reported as socks")
	}
}

// The store is what the dashboards write to; make sure the file round-trips so
// a saved scheme survives a restart (this is the bug the UI had).
func TestSchemeSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	st := store.Open(dir)
	st.SaveSettings(map[string]string{"proxy_scheme": "socks5"})
	if _, err := os.Stat(filepath.Join(dir, "settings.txt")); err != nil {
		t.Fatalf("settings.txt not written: %v", err)
	}
	if got := Scheme(store.Open(dir)); got != SOCKS5 {
		t.Fatalf("reopened scheme = %q, want %q", got, SOCKS5)
	}
}
