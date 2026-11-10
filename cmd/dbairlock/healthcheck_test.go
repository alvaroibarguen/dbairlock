package main

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthcheckURL(t *testing.T) {
	for listen, want := range map[string]string{
		":8080":          "http://127.0.0.1:8080/readyz",
		"0.0.0.0:8080":   "http://127.0.0.1:8080/readyz",
		"[::]:8080":      "http://[::1]:8080/readyz",
		"127.0.0.1:9000": "http://127.0.0.1:9000/readyz",
		"10.0.0.5:8080":  "http://10.0.0.5:8080/readyz",
	} {
		got, err := healthcheckURL(listen)
		if err != nil || got != want {
			t.Errorf("healthcheckURL(%q) = %q, %v; want %q", listen, got, err, want)
		}
	}
	for _, bad := range []string{"8080", "", "host:"} {
		if _, err := healthcheckURL(bad); err == nil {
			t.Errorf("healthcheckURL(%q) accepted", bad)
		}
	}
}

func TestHealthcheckExitCodes(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())

	if code := run([]string{"healthcheck", "--listen", ":" + port}, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatalf("ready server: exit %d, want 0", code)
	}
	status = http.StatusServiceUnavailable
	if code := run([]string{"healthcheck", "--listen", ":" + port}, &bytes.Buffer{}, &bytes.Buffer{}); code != 1 {
		t.Fatalf("store not ready: exit %d, want 1", code)
	}
	srv.Close()
	if code := run([]string{"healthcheck", "--listen", ":" + port}, &bytes.Buffer{}, &bytes.Buffer{}); code != 1 {
		t.Fatalf("no server: exit %d, want 1", code)
	}
}
