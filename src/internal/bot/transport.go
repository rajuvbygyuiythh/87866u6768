package bot

// Chrome-fingerprinted HTTP transport for plain REST calls (persistTextMode
// etc). The websocket socket already handshakes like Chrome (dialChromeTLS);
// an API request next to it with Go's default TLS ClientHello + "Go-http-client"
// UA would give the whole bot away — every site-side call now rides the same
// proxy hop (SOCKS5/CONNECT) and the same Chrome ClientHello.

import (
	"context"
	"net"
	"net/http"
	"time"

	"thundr/internal/store"
)

// ChromeHTTPClient returns an http.Client whose TLS handshake mimics Chrome
// (uTLS) and whose TCP hop goes through proxyURL ("" = direct). Exported for
// the browser package (geo probe) and the ws bot's REST calls.
func ChromeHTTPClient(proxyURL string) *http.Client {
	return &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialChromeTLS(ctx, addr, proxyURL)
			},
		},
	}
}

// StampBrowserHeaders stamps one request with the account's browser identity
// (UA + sec-ch-ua family) — the same fingerprint wsHeaders replays on the
// socket, so REST and WS traffic look like one browser.
func StampBrowserHeaders(h http.Header, st *store.Store, uid string) http.Header {
	ua := st.UserAgentFor(uid)
	h.Set("User-Agent", ua)
	h.Set("sec-ch-ua", store.SecCHUA(ua))
	h.Set("sec-ch-ua-mobile", "?0")
	h.Set("sec-ch-ua-platform", `"Windows"`)
	h.Set("Accept-Language", "en-US,en;q=0.9")
	h.Set("Origin", "https://thundr.com")
	return h
}