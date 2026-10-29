package dash

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// maxRequestBodyBytes caps JSON payloads accepted by the local dashboard.
const maxRequestBodyBytes = 4 << 20 // 4 MiB

// handler returns the route mux wrapped with local-only request guards.
func (a *App) handler() http.Handler {
	return a.harden(a.routes())
}

func (a *App) harden(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !allowedOrigin(origin, r.Host) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch:
			if r.ContentLength > maxRequestBodyBytes {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodyBytes+1))
			if err != nil {
				http.Error(w, "cannot read request body", http.StatusBadRequest)
				return
			}
			if int64(len(body)) > maxRequestBodyBytes {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
		}
		next.ServeHTTP(w, r)
	})
}

// Shutdown gracefully stops the HTTP server, if one is running.
func (a *App) Shutdown(ctx context.Context) error {
	a.mu.Lock()
	srv := a.httpServer
	a.mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}

func isLoopbackHost(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	h = strings.ToLower(strings.Trim(h, "[]"))
	switch h {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

func allowedOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return strings.EqualFold(u.Host, host) && isLoopbackHost(u.Host)
}
