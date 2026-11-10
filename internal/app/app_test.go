package app

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/alvaroibarguen/dbairlock/internal/platform/logx"
)

func newTestApp(t *testing.T) *App {
	t.Helper()
	a, err := New(Options{
		DataDir: filepath.Join(t.TempDir(), "data"),
		Listen:  "127.0.0.1:0",
		Version: "test",
		Logger:  logx.Discard(),
		WebUI:   fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>shell</title>")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

func TestRoutes(t *testing.T) {
	h := newTestApp(t).Handler()
	cases := []struct {
		path   string
		status int
		ctype  string
	}{
		{"/healthz", 200, "application/json"},
		{"/readyz", 200, "application/json"},
		{"/api/meta", 200, "application/json"},
		{"/api/unknown", 404, "application/json"},
		{"/mcp", 404, "application/json"},
		{"/", 200, "text/html; charset=utf-8"},
		{"/changes/1", 200, "text/html; charset=utf-8"},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != tc.status || rec.Header().Get("Content-Type") != tc.ctype {
			t.Errorf("%s: %d %q, want %d %q", tc.path, rec.Code, rec.Header().Get("Content-Type"), tc.status, tc.ctype)
		}
		if rec.Header().Get("X-Request-Id") == "" || rec.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("%s: middleware headers missing", tc.path)
		}
	}
}

func TestMetaBody(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestApp(t).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/meta", nil))
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["version"] != "test" || body["setupState"] != "ready" {
		t.Fatalf("body = %v", body)
	}
}

func TestServeShutsDownOnCancel(t *testing.T) {
	a := newTestApp(t)
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.serve(ctx, ln) }()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+ln.Addr().String()+"/healthz", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serve returned %v", err)
	}
}

func TestLoopbackOnly(t *testing.T) {
	cases := map[string]string{
		":8080":       "127.0.0.1:8080",
		"0.0.0.0:9":   "127.0.0.1:9",
		"10.1.2.3:9":  "127.0.0.1:9",
		"127.0.0.1:9": "127.0.0.1:9",
		"[::1]:9":     "[::1]:9",
		"localhost:9": "localhost:9",
	}
	for in, want := range cases {
		if got, _ := loopbackOnly(in); got != want {
			t.Errorf("loopbackOnly(%q) = %q, want %q", in, got, want)
		}
	}
}
