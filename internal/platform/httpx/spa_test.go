package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><title>app</title>")},
		"assets/app-abc123.js": {Data: []byte("console.log(1)")},
		"favicon.svg":          {Data: []byte("<svg/>")},
		".keep":                {Data: nil},
	}
}

func get(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestSPA(t *testing.T) {
	h := SPA(testFS())
	cases := []struct {
		name, method, path string
		status             int
		cache              string
		bodyHas            string
		json               bool
	}{
		{"root", "GET", "/", 200, cacheNone, "<title>app</title>", false},
		{"index direct", "GET", "/index.html", 200, cacheNone, "<title>app</title>", false},
		{"client route", "GET", "/changes/42", 200, cacheNone, "<title>app</title>", false},
		{"hashed asset", "GET", "/assets/app-abc123.js", 200, cacheImmutable, "console.log", false},
		{"other file", "GET", "/favicon.svg", 200, cacheNone, "<svg/>", false},
		{"missing asset", "GET", "/assets/missing.js", 404, "", "", false},
		{"missing asset no ext", "GET", "/assets/missing", 404, "", "", false},
		{"missing file with ext", "GET", "/robots.txt", 404, "", "", false},
		{"dotfile", "GET", "/.keep", 404, "", "", false},
		{"traversal stays inside fs", "GET", "/../../etc/passwd", 200, cacheNone, "<title>app</title>", false},
		{"traversal to dotfile", "GET", "/assets/../.keep", 404, "", "", false},
		{"api unknown", "GET", "/api/nope", 404, "", `"not_found"`, true},
		{"api root", "GET", "/api", 404, "", `"not_found"`, true},
		{"mcp", "POST", "/mcp", 404, "", `"not_found"`, true},
		{"post to app", "POST", "/changes", 405, "", `"method_not_allowed"`, true},
		{"head", "HEAD", "/", 200, cacheNone, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := get(t, h, tc.method, tc.path)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tc.status, rec.Body.String())
			}
			if tc.cache != "" && rec.Header().Get("Cache-Control") != tc.cache {
				t.Errorf("Cache-Control = %q, want %q", rec.Header().Get("Cache-Control"), tc.cache)
			}
			if tc.bodyHas != "" && !strings.Contains(rec.Body.String(), tc.bodyHas) {
				t.Errorf("body %q does not contain %q", rec.Body.String(), tc.bodyHas)
			}
			if tc.json && rec.Header().Get("Content-Type") != "application/json" {
				t.Errorf("Content-Type = %q, want JSON", rec.Header().Get("Content-Type"))
			}
		})
	}
}

func TestSPAIndexContentType(t *testing.T) {
	rec := get(t, SPA(testFS()), "GET", "/settings")
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
}

func TestSPANotBuilt(t *testing.T) {
	h := SPA(fstest.MapFS{".keep": {}})
	rec := get(t, h, "GET", "/anything")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "make build") {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != cacheNone {
		t.Fatalf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
}
