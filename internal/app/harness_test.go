package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/identity/oidctest"
	"github.com/alvaroibarguen/dbairlock/internal/platform/clock"
	"github.com/alvaroibarguen/dbairlock/internal/platform/logx"
)

const publicURL = "https://dbairlock.test"

// harness is a non-dev instance wired to a fake OpenID provider, with a
// fake clock shared by both.
type harness struct {
	t      *testing.T
	dir    string
	app    *App
	h      http.Handler
	idp    *oidctest.Provider
	clock  *clock.Fake
	banner *bytes.Buffer
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	idp := oidctest.New()
	t.Cleanup(idp.Close)
	c := clock.NewFake(time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC))
	idp.Clock = c
	hs := &harness{t: t, dir: filepath.Join(t.TempDir(), "data"), idp: idp, clock: c, banner: &bytes.Buffer{}}
	hs.start()
	return hs
}

func (hs *harness) start() {
	hs.t.Helper()
	a, err := New(Options{
		DataDir: hs.dir, Listen: "127.0.0.1:0", Version: "test", Logger: logx.Discard(),
		Banner: hs.banner, Clock: hs.clock, HTTPClient: hs.idp.Server.Client(),
		WebUI: fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>shell</title>")}},
	})
	if err != nil {
		hs.t.Fatal(err)
	}
	hs.t.Cleanup(func() { _ = a.Close() })
	hs.app, hs.h = a, a.Handler()
}

var codePattern = regexp.MustCompile(`Code:\s+(\S+)`)

// setupCode returns the code from the most recent setup banner.
func (hs *harness) setupCode() string {
	hs.t.Helper()
	m := codePattern.FindAllStringSubmatch(hs.banner.String(), -1)
	if len(m) == 0 {
		hs.t.Fatalf("no setup code in %q", hs.banner.String())
	}
	return m[len(m)-1][1]
}

// browser keeps cookies like a browser would (by name; one host).
type browser struct {
	hs      *harness
	ip      string
	cookies map[string]string
	header  http.Header
}

func (hs *harness) browser(ip string) *browser {
	return &browser{hs: hs, ip: ip, cookies: map[string]string{}, header: http.Header{}}
}

type response struct {
	*httptest.ResponseRecorder
	body map[string]any
}

func (r response) errCode() string {
	e, _ := r.body["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

func (r response) location() string { return r.Header().Get("Location") }

func (b *browser) do(method, path string, body any) response {
	b.hs.t.Helper()
	var rd *strings.Reader
	switch v := body.(type) {
	case nil:
		rd = strings.NewReader("")
	case string:
		rd = strings.NewReader(v)
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			b.hs.t.Fatal(err)
		}
		rd = strings.NewReader(string(raw))
	}
	req := httptest.NewRequest(method, path, rd)
	req.Host = "dbairlock.test"
	req.RemoteAddr = b.ip + ":40000"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range b.header {
		req.Header[k] = v
	}
	for name, value := range b.cookies {
		req.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	rec := httptest.NewRecorder()
	b.hs.h.ServeHTTP(rec, req)
	for _, c := range (&http.Response{Header: rec.Header()}).Cookies() {
		if c.MaxAge < 0 || c.Value == "" {
			delete(b.cookies, c.Name)
		} else {
			b.cookies[c.Name] = c.Value
		}
	}
	out := response{ResponseRecorder: rec}
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
	}
	return out
}

// google follows a sign-in redirect through the fake provider and returns
// our callback's path and query. between runs after Google approved and
// before the browser comes back.
func (b *browser) google(start response, between func(callback *url.URL)) response {
	b.hs.t.Helper()
	if start.Code != http.StatusFound {
		b.hs.t.Fatalf("start: %d %s", start.Code, start.Body)
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, start.location(), nil)
	client := b.hs.idp.Server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		b.hs.t.Fatal(err)
	}
	_ = resp.Body.Close()
	cb, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || resp.StatusCode != http.StatusFound || !strings.HasPrefix(cb.String(), publicURL+"/api/auth/google/callback") {
		b.hs.t.Fatalf("provider answered %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if between != nil {
		between(cb)
	}
	return b.do("GET", cb.RequestURI(), nil)
}

// signIn runs a normal sign-in as account and returns the callback response.
func (b *browser) signIn(a oidctest.Account) response {
	b.hs.t.Helper()
	b.hs.idp.SignInAs(a)
	return b.google(b.do("GET", "/api/auth/google/start?next=/changes", nil), nil)
}

var (
	adaAdmin  = oidctest.Account{Sub: "1001", Email: "ada@example.com", Name: "Ada", HD: "example.com", EmailVerified: true}
	bobMember = oidctest.Account{Sub: "1002", Email: "bob@example.com", Name: "Bob", HD: "example.com", EmailVerified: true}
)

// completeSetup runs the whole wizard over HTTP and returns the first
// admin's browser.
func (hs *harness) completeSetup() *browser {
	hs.t.Helper()
	b := hs.browser("192.0.2.10")
	if r := b.do("POST", "/api/setup/verify", map[string]string{"code": hs.setupCode()}); r.Code != http.StatusNoContent {
		hs.t.Fatalf("verify: %d %s", r.Code, r.Body)
	}
	r := b.do("PUT", "/api/setup/signin", map[string]any{
		"public_url": publicURL, "client_id": hs.idp.ClientID, "client_secret": hs.idp.ClientSecret,
		"allowed_domains": []string{"example.com"}, "oidc_issuer": hs.idp.Issuer,
	})
	if r.Code != http.StatusOK || r.body["redirect_uri"] != publicURL+"/api/auth/google/callback" {
		hs.t.Fatalf("signin settings: %d %s", r.Code, r.Body)
	}
	hs.idp.SignInAs(adaAdmin)
	r = b.google(b.do("GET", "/api/setup/google/start", nil), nil)
	if r.Code != http.StatusFound || r.location() != "/" {
		hs.t.Fatalf("setup sign-in: %d %q %s", r.Code, r.location(), r.Body)
	}
	return b
}

const sessionCookie = "__Host-dbairlock_session"

// auditActions returns the actions recorded so far, in order.
func (hs *harness) auditActions() []string {
	hs.t.Helper()
	rows, err := hs.app.store.Reader().QueryContext(context.Background(), `SELECT action || ' ' || via || ' ' || payload FROM audit_events ORDER BY seq`)
	if err != nil {
		hs.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			hs.t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		hs.t.Fatal(err)
	}
	return out
}

func (hs *harness) hasAudit(prefix string) bool {
	for _, a := range hs.auditActions() {
		if strings.HasPrefix(a, prefix) {
			return true
		}
	}
	return false
}
