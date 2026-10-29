package dash

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHardenedHandlerRejectsForeignHost(t *testing.T) {
	a := Open(t.TempDir())
	req := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	req.Host = "evil.example:8787"
	rec := httptest.NewRecorder()
	a.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("foreign host status = %d, want 403", rec.Code)
	}
}

func TestHardenedHandlerAllowsLoopbackHostWithoutOrigin(t *testing.T) {
	a := Open(t.TempDir())
	for _, host := range []string{"127.0.0.1:8787", "localhost:8787", "[::1]:8787"} {
		req := httptest.NewRequest(http.MethodGet, "/api/state", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		a.handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("host %s status = %d, want 200", host, rec.Code)
		}
	}
}

func TestHardenedHandlerRejectsForeignOrigin(t *testing.T) {
	a := Open(t.TempDir())
	req := httptest.NewRequest(http.MethodPost, "/api/ClearLogs", strings.NewReader("{}"))
	req.Host = "127.0.0.1:8787"
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	a.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("foreign origin status = %d, want 403", rec.Code)
	}
}

func TestHardenedHandlerAllowsMatchingLoopbackOrigin(t *testing.T) {
	a := Open(t.TempDir())
	req := httptest.NewRequest(http.MethodPost, "/api/ClearLogs", strings.NewReader("{}"))
	req.Host = "127.0.0.1:8787"
	req.Header.Set("Origin", "http://127.0.0.1:8787")
	rec := httptest.NewRecorder()
	a.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("matching origin status = %d, want 200", rec.Code)
	}
}

func TestHardenedHandlerRejectsOversizedContentLength(t *testing.T) {
	a := Open(t.TempDir())
	req := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader("{}"))
	req.Host = "127.0.0.1:8787"
	req.ContentLength = maxRequestBodyBytes + 1
	rec := httptest.NewRecorder()
	a.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body status = %d, want 413", rec.Code)
	}
}

func TestHardenedHandlerRejectsOversizedChunkedBody(t *testing.T) {
	a := Open(t.TempDir())
	payload := bytes.Repeat([]byte("x"), maxRequestBodyBytes+1)
	req := httptest.NewRequest(http.MethodPost, "/api/settings", io.NopCloser(bytes.NewReader(payload)))
	req.Host = "127.0.0.1:8787"
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	a.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized chunked body status = %d, want 413", rec.Code)
	}
}

func TestServeShutdownStopsListener(t *testing.T) {
	a := Open(t.TempDir())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.serve(ln) }()

	client := &http.Client{Timeout: time.Second}
	url := "http://" + ln.Addr().String() + "/api/state"
	var lastErr error
	for i := 0; i < 100; i++ {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			lastErr = nil
			break
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("server did not become ready: %v", lastErr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := a.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned error after shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not stop after shutdown")
	}
}
