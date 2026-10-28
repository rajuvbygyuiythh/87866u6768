package bot

// Chrome-fingerprinted transport for the Phoenix websocket, ported from the
// reference project (internal/bot/realtime.go):
//   - TLS ClientHello mimics Chrome (uTLS), not Go's default stack
//   - request headers replay the account's stored fingerprint UA with a
//     matching sec-ch-ua (Chrome major consistency)

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	utls "github.com/refraction-networking/utls"
	netproxy "golang.org/x/net/proxy"

	"thundr/internal/store"
)

// wsHeaders builds the dial headers for the account (fingerprint replay).
func wsHeaders(st *store.Store, uid string) http.Header {
	ua := st.UserAgentFor(uid)
	h := http.Header{}
	h.Set("Origin", "https://thundr.com")
	h.Set("User-Agent", ua)
	h.Set("Accept-Language", "en-US,en;q=0.9")
	h.Set("sec-ch-ua", store.SecCHUA(ua))
	h.Set("sec-ch-ua-mobile", "?0")
	h.Set("sec-ch-ua-platform", `"Windows"`)
	h.Set("Cache-Control", "no-cache")
	h.Set("Pragma", "no-cache")
	return h
}

// dialChromeTLS dials addr (host:port) and wraps it in a uTLS connection with
// a Chrome ClientHello. When proxyURL is non-empty the TCP hop goes through
// that proxy first: SOCKS5 (socks5://) via x/net/proxy, or HTTP/HTTPS CONNECT
// (with Proxy-Authorization when the line carries credentials) — same as the
// reference's dialWithChromeUTLS, extended for the dashboard's scheme picker.
func dialChromeTLS(ctx context.Context, addr, proxyURL string) (net.Conn, error) {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}

	rawConn, err := dialThroughProxy(ctx, addr, proxyURL)
	if err != nil {
		return nil, err
	}

	uConn := utls.UClient(rawConn, &utls.Config{
		ServerName: host,
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
	}, utls.HelloChrome_120)

	// Advertise h1 ONLY (the Phoenix socket is http/1.1; Cloudflare would
	// otherwise negotiate h2 and the ws upgrade reads binary garbage).
	// IMPORTANT: BuildHandshakeState() first — ApplyPreset runs inside it and
	// (re)builds uConn.Extensions from the spec; mutating before it is a no-op.
	if err := uConn.BuildHandshakeState(); err == nil {
		for _, ext := range uConn.Extensions {
			if alpn, ok := ext.(*utls.ALPNExtension); ok {
				alpn.AlpnProtocols = []string{"http/1.1"}
			}
		}
	}

	if err := uConn.HandshakeContext(ctx); err != nil {
		rawConn.Close()
		return nil, fmt.Errorf("uTLS handshake: %w", err)
	}
	return uConn, nil
}

// dialThroughProxy opens the raw TCP tunnel to addr: direct when proxyURL is
// empty, SOCKS5 when the scheme says so, HTTP/HTTPS CONNECT otherwise.
func dialThroughProxy(ctx context.Context, addr, proxyURL string) (net.Conn, error) {
	d := &net.Dialer{Timeout: 30 * time.Second}
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return d.DialContext(ctx, "tcp", addr)
	}
	pu, err := url.Parse(proxyURL)
	if err != nil || pu.Host == "" {
		return nil, fmt.Errorf("bad proxy %q: %v", proxyURL, err)
	}

	if strings.HasPrefix(strings.ToLower(pu.Scheme), "socks") {
		var auth *netproxy.Auth
		if pu.User != nil {
			if user := pu.User.Username(); user != "" {
				pass, _ := pu.User.Password()
				auth = &netproxy.Auth{User: user, Password: pass}
			}
		}
		pd, err := netproxy.SOCKS5("tcp", pu.Host, auth, d)
		if err != nil {
			return nil, fmt.Errorf("socks5 proxy %s: %w", pu.Host, err)
		}
		if cd, ok := pd.(netproxy.ContextDialer); ok {
			return cd.DialContext(ctx, "tcp", addr)
		}
		return pd.Dial("tcp", addr)
	}

	// HTTP / HTTPS proxy: TCP to the proxy, then a CONNECT tunnel.
	target := pu.Host
	if _, _, err := net.SplitHostPort(target); err != nil {
		target = net.JoinHostPort(target, "80")
	}
	rawConn, err := d.DialContext(ctx, "tcp", target)
	if err != nil {
		return nil, err
	}
	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n", addr, addr)
	if pu.User != nil {
		user := pu.User.Username()
		pass, _ := pu.User.Password()
		token := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
		req += "Proxy-Authorization: Basic " + token + "\r\n"
	}
	req += "\r\n"
	if _, err := rawConn.Write([]byte(req)); err != nil {
		rawConn.Close()
		return nil, fmt.Errorf("proxy CONNECT write: %w", err)
	}
	br := bufio.NewReader(rawConn)
	resp, err := http.ReadResponse(br, &http.Request{Method: "CONNECT"})
	if err != nil {
		rawConn.Close()
		return nil, fmt.Errorf("proxy CONNECT read: %w", err)
	}
	if resp.StatusCode != 200 {
		rawConn.Close()
		return nil, fmt.Errorf("proxy CONNECT: %s", resp.Status)
	}
	return &bufferedConn{Conn: rawConn, r: br}, nil
}

// bufferedConn keeps any bytes the proxy already pushed into the bufio.Reader
// readable by the TLS layer.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }
