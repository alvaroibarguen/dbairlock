package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// parseCSP splits a policy into directive name -> source list.
func parseCSP(t *testing.T, policy string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for d := range strings.SplitSeq(policy, ";") {
		f := strings.Fields(d)
		if len(f) == 0 {
			continue
		}
		if _, dup := out[f[0]]; dup {
			t.Fatalf("directive %s appears twice", f[0])
		}
		out[f[0]] = f[1:]
	}
	return out
}

func TestContentSecurityPolicy(t *testing.T) {
	csp := parseCSP(t, ContentSecurityPolicy)
	want := map[string]string{
		"default-src":     "'self'",
		"script-src":      "'self'",
		"img-src":         "'self'",
		"font-src":        "'self'",
		"connect-src":     "'self'",
		"object-src":      "'none'",
		"base-uri":        "'none'",
		"form-action":     "'self'",
		"frame-ancestors": "'none'",
	}
	for k, v := range want {
		if got := strings.Join(csp[k], " "); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	// style-src: self plus hashes only. 'unsafe-inline' would also void the hash.
	styles := csp["style-src"]
	if len(styles) == 0 || styles[0] != "'self'" {
		t.Errorf("style-src = %v, want 'self' first", styles)
	}
	for _, s := range styles[1:] {
		if !strings.HasPrefix(s, "'sha256-") {
			t.Errorf("style-src allows %s; only hashes may be added", s)
		}
	}
	for _, bad := range []string{"'unsafe-inline'", "'unsafe-eval'", "'unsafe-hashes'", "'wasm-unsafe-eval'", "data:", "blob:", "*", "http:", "https:"} {
		if strings.Contains(ContentSecurityPolicy, bad) {
			t.Errorf("policy contains %s: %s", bad, ContentSecurityPolicy)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	for _, https := range []bool{false, true} {
		rec := httptest.NewRecorder()
		SecurityHeaders(func() bool { return https })(http.NotFoundHandler()).
			ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		h := rec.Header()
		want := map[string]string{
			"Content-Security-Policy":      ContentSecurityPolicy,
			"X-Content-Type-Options":       "nosniff",
			"X-Frame-Options":              "DENY",
			"Referrer-Policy":              "no-referrer",
			"Cross-Origin-Opener-Policy":   "same-origin",
			"Cross-Origin-Resource-Policy": "same-origin",
			"Permissions-Policy":           PermissionsPolicy,
		}
		for k, v := range want {
			if got := h.Get(k); got != v {
				t.Errorf("https=%v: %s = %q, want %q", https, k, got, v)
			}
		}
		hsts := h.Get("Strict-Transport-Security")
		if https && hsts != HSTS {
			t.Errorf("HSTS = %q, want %q", hsts, HSTS)
		}
		if !https && hsts != "" {
			t.Errorf("HSTS sent without HTTPS: %q", hsts)
		}
	}
	for _, f := range []string{"camera=()", "microphone=()", "geolocation=()", "usb=()", "payment=()"} {
		if !strings.Contains(PermissionsPolicy, f) {
			t.Errorf("Permissions-Policy does not deny %s", f)
		}
	}
}
