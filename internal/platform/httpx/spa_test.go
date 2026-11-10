package httpx

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><title>app</title>")},
		"assets/app-abc123.js": {Data: []byte("console.log(1)")},
		"favicon.svg":          {Data: []byte("<svg/>")},
		"manifest.webmanifest": {Data: []byte(`{"name":"app"}`)},
		".keep":                {Data: nil},
	}
}

func TestSPAManifestContentType(t *testing.T) {
	rec := get(t, SPA(testFS()), "GET", "/manifest.webmanifest")
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/manifest+json" {
		t.Errorf("Content-Type = %q, want application/manifest+json", got)
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
		{"root", "GET", "/", 200, cacheIndex, "<title>app</title>", false},
		{"index direct", "GET", "/index.html", 200, cacheIndex, "<title>app</title>", false},
		{"client route", "GET", "/changes/42", 200, cacheIndex, "<title>app</title>", false},
		{"hashed asset", "GET", "/assets/app-abc123.js", 200, cacheImmutable, "console.log", false},
		{"other file", "GET", "/favicon.svg", 200, cacheNone, "<svg/>", false},
		{"missing asset", "GET", "/assets/missing.js", 404, "", "", false},
		{"missing asset no ext", "GET", "/assets/missing", 404, "", "", false},
		{"missing file with ext", "GET", "/robots.txt", 404, "", "", false},
		{"dotfile", "GET", "/.keep", 404, "", "", false},
		{"traversal stays inside fs", "GET", "/../../etc/passwd", 200, cacheIndex, "<title>app</title>", false},
		{"traversal to dotfile", "GET", "/assets/../.keep", 404, "", "", false},
		{"api unknown", "GET", "/api/nope", 404, "", `"not_found"`, true},
		{"api root", "GET", "/api", 404, "", `"not_found"`, true},
		{"mcp", "POST", "/mcp", 404, "", `"not_found"`, true},
		{"post to app", "POST", "/changes", 405, "", `"method_not_allowed"`, true},
		{"head", "HEAD", "/", 200, cacheIndex, "", false},
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

// The page carries a fresh style nonce in a meta tag and in its CSP, and
// the two match.
func TestSPAIndexStyleNonce(t *testing.T) {
	h := SPA(fstest.MapFS{"index.html": {Data: []byte("<!doctype html><html><head><title>app</title></head></html>")}})
	nonces := map[string]bool{}
	for range 3 {
		rec := get(t, h, "GET", "/")
		body := rec.Body.String()
		const prefix = `<head><meta name="csp-nonce" content="`
		i := strings.Index(body, prefix)
		if i < 0 {
			t.Fatalf("no nonce meta in %q", body)
		}
		nonce, _, _ := strings.Cut(body[i+len(prefix):], `"`)
		if len(nonce) < 22 {
			t.Fatalf("nonce %q is too short", nonce)
		}
		nonces[nonce] = true
		csp := rec.Header().Get("Content-Security-Policy")
		if csp != ContentSecurityPolicyWithStyleNonce(nonce) || !strings.Contains(csp, "style-src 'self' ") ||
			!strings.Contains(csp, "'nonce-"+nonce+"'") || strings.Contains(csp, "script-src 'self' 'nonce") {
			t.Fatalf("CSP %q does not match nonce %q", csp, nonce)
		}
		if rec.Header().Get("ETag") != "" || rec.Header().Get("Last-Modified") != "" {
			t.Fatal("the page can be revalidated, which would reuse an old nonce")
		}
		if rec.Header().Get("Content-Length") != strconv.Itoa(len(body)) {
			t.Fatalf("Content-Length = %s, body %d bytes", rec.Header().Get("Content-Length"), len(body))
		}
	}
	if len(nonces) != 3 {
		t.Fatal("nonces repeat")
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
