// Package proxyurl turns a proxies.txt line into a dialable proxy URL using the
// scheme the operator picked in the dashboard (http | https | socks5).
//
// Both dashboards write the scheme under different keys: the kit UI saves
// "proxy_scheme", the legacy dashboards saved "proxytype". Before this package
// existed the key was never read at all and every proxy line was forced to
// http://, so SOCKS5 proxies silently failed to connect.
package proxyurl

import (
	"net/url"
	"strings"

	"thundr/internal/store"
)

// Supported scheme names (values used in settings.txt and the UI selects).
const (
	HTTP   = "http"
	HTTPS  = "https"
	SOCKS5 = "socks5"
)

// NormalizeScheme maps free-form user input onto a supported scheme.
// Empty means "not one of ours" so callers can fall back.
func NormalizeScheme(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "http":
		return HTTP
	case "https":
		return HTTPS
	case "socks5", "socks5h", "socks":
		return SOCKS5
	}
	return ""
}

// Scheme returns the configured proxy scheme for this store.
// "proxy_scheme" (kit UI) wins over "proxytype" (legacy dashboard); default http.
func Scheme(st *store.Store) string {
	for _, key := range []string{"proxy_scheme", "proxytype"} {
		if s := NormalizeScheme(st.Get(key, "")); s != "" {
			return s
		}
	}
	return HTTP
}

// Normalize converts one proxies.txt line into a full proxy URL.
//
//	host:port                  -> scheme://host:port
//	host:port:user:pass        -> scheme://user:pass@host:port
//	user:pass@host:port        -> scheme://user:pass@host:port
//	http://... (already a URL) -> unchanged, its own scheme wins
func Normalize(line, scheme string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	if strings.Contains(line, "://") {
		return line
	}
	if s := NormalizeScheme(scheme); s != "" {
		scheme = s
	} else {
		scheme = HTTP
	}
	if parts := strings.SplitN(line, ":", 4); len(parts) == 4 {
		return scheme + "://" + parts[2] + ":" + parts[3] + "@" + parts[0] + ":" + parts[1]
	}
	return scheme + "://" + line
}

// IsSOCKS reports whether raw is a socks (4/5) proxy URL.
func IsSOCKS(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return strings.HasPrefix(strings.ToLower(u.Scheme), "socks")
}

// Label is the credential-free host:port shown next to a worker in the UI.
func Label(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return strings.TrimSpace(raw)
	}
	return u.Host
}

// Credentials returns the proxy user/pass, if the line carries them.
func Credentials(raw string) (user, pass string) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.User == nil {
		return "", ""
	}
	p, _ := u.User.Password()
	return u.User.Username(), p
}
