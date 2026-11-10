package app

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/alvaroibarguen/dbairlock/internal/platform/httpx"
)

var nonceSource = regexp.MustCompile(` 'nonce-[A-Za-z0-9+/=]{22,}'`)

// checkSecurityHeaders fails unless rec carries every security header, with
// HSTS exactly when hsts is true.
func checkSecurityHeaders(t *testing.T, name string, h http.Header, hsts bool) {
	t.Helper()
	// The dashboard page adds a per-response style nonce; nothing else differs.
	csp := nonceSource.ReplaceAllString(h.Get("Content-Security-Policy"), "")
	if csp != httpx.ContentSecurityPolicy {
		t.Errorf("%s: Content-Security-Policy = %q, want %q", name, h.Get("Content-Security-Policy"), httpx.ContentSecurityPolicy)
	}
	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
		"Permissions-Policy":     httpx.PermissionsPolicy,
	}
	for k, v := range want {
		if got := h.Get(k); got != v {
			t.Errorf("%s: %s = %q, want %q", name, k, got, v)
		}
	}
	got := h.Get("Strict-Transport-Security")
	if hsts && got != httpx.HSTS {
		t.Errorf("%s: HSTS = %q, want %q", name, got, httpx.HSTS)
	}
	if !hsts && got != "" {
		t.Errorf("%s: HSTS sent without an https public URL: %q", name, got)
	}
}

type headerCase struct {
	name, method, path, body string
	header                   map[string]string
	status                   int
}

// Every kind of response carries the security headers: the dashboard, its
// assets, the API, MCP, health probes, and errors raised by middleware
// before any handler runs.
func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	big := strings.Repeat("x", int(httpx.DefaultMaxBodyBytes)+1)
	common := []headerCase{
		{"dashboard", "GET", "/", "", nil, 200},
		{"client route", "GET", "/changes/1", "", nil, 200},
		{"missing asset", "GET", "/assets/missing.js", "", nil, 404},
		{"method not allowed", "POST", "/", "", nil, 405},
		{"health", "GET", "/healthz", "", nil, 200},
		{"ready", "GET", "/readyz", "", nil, 200},
		{"meta", "GET", "/api/meta", "", nil, 200},
		{"unknown api", "GET", "/api/setup/unknown", "", nil, 404},
		{"cross-origin", "POST", "/api/auth/signout", "{}", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"body too large", "POST", "/api/setup/verify", big, nil, 413},
	}

	t.Run("setup mode, no public URL", func(t *testing.T) {
		h := newTestApp(t).Handler()
		cases := append(common, headerCase{"mcp in setup", "POST", "/mcp", "{}", nil, 503})
		for _, c := range cases {
			req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
			req.Header.Set("Content-Type", "application/json")
			for k, v := range c.header {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != c.status {
				t.Errorf("%s: status %d, want %d", c.name, rec.Code, c.status)
			}
			checkSecurityHeaders(t, c.name, rec.Header(), false)
		}
	})

	t.Run("https public URL", func(t *testing.T) {
		hs := newHarness(t)
		hs.completeSetup() // sets instance.public_url to https://dbairlock.test
		b := hs.browser("198.51.100.9")
		cases := append(common,
			headerCase{"not signed in", "GET", "/api/me", "", nil, 401},
			headerCase{"mcp without token", "POST", "/mcp", "{}", nil, 401},
		)
		for _, c := range cases {
			for k, v := range c.header {
				b.header.Set(k, v)
			}
			var body any
			if c.body != "" {
				body = c.body
			}
			r := b.do(c.method, c.path, body)
			for k := range c.header {
				b.header.Del(k)
			}
			if r.Code != c.status {
				t.Errorf("%s: status %d, want %d", c.name, r.Code, c.status)
			}
			checkSecurityHeaders(t, c.name, r.Header(), true)
		}
	})
}
