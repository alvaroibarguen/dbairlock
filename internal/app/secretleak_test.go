package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/alvaroibarguen/dbairlock/internal/identity/oidctest"
	"github.com/alvaroibarguen/dbairlock/internal/platform/clock"
	"github.com/alvaroibarguen/dbairlock/internal/secrets"
)

// The secret leak test (Phase 11 check). It sets known values for every
// kind of secret (target passwords, the OpenID client secret, the webhook
// URL, API tokens after creation) and then looks for them in every
// response of every API route, error responses, every MCP tool's output,
// the logs at debug level, the audit log and its export, the database
// files, and the output of forced panics.

const (
	leakOIDC     = "SENTINEL-oidc-client-secret-4f1d"
	leakWebhook  = "SENTINEL-webhook-path-8a2e"
	leakTarget   = "SENTINEL-target-password-1c9b"
	leakTarget2  = "SENTINEL-target-password-replaced-77e0"
	leakRejected = "SENTINEL-rejected-password-3b5a"
	leakPanic    = "SENTINEL-panic-password-90ff"
)

var leakWebhookURL = "https://hooks.invalid/services/" + leakWebhook

// panicValue is what the forced panics carry: a secret value inside a
// struct, as a careless panic(x) in a handler might.
type panicValue struct {
	Note     string
	Password secrets.Value
}

// panicClock panics once, when armed and called from a function whose
// name contains the armed fragment (for example "internal/targets.").
type panicClock struct {
	clock.Clock
	armed atomic.Pointer[string]
}

func (c *panicClock) Now() time.Time {
	if frag := c.armed.Load(); frag != nil && calledFrom(*frag) && c.armed.CompareAndSwap(frag, nil) {
		panic(panicValue{Note: "forced by the secret leak test", Password: secrets.NewValue(leakPanic)})
	}
	return c.Clock.Now()
}

func calledFrom(fragment string) bool {
	pcs := make([]uintptr, 64)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs)])
	for {
		f, more := frames.Next()
		if strings.Contains(f.Function, fragment) {
			return true
		}
		if !more {
			return false
		}
	}
}

// leakCollector keeps every output the test must check.
type leakCollector struct {
	mu      sync.Mutex
	outputs []string // "where: text"
	// pats are API token values; allowed only in their creation response.
	pats []string
}

func (l *leakCollector) add(where, text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.outputs = append(l.outputs, where+"\x00"+text)
}

func (l *leakCollector) check(t *testing.T) {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	sentinels := append([]string{leakOIDC, leakWebhook, leakTarget, leakTarget2, leakRejected, leakPanic}, l.pats...)
	for _, o := range l.outputs {
		where, text, _ := strings.Cut(o, "\x00")
		for _, s := range sentinels {
			if strings.Contains(text, s) {
				t.Errorf("%s contains the secret %q:\n%s", where, s, excerpt(text, s))
			}
		}
	}
}

func excerpt(text, s string) string {
	i := strings.Index(text, s)
	lo, hi := max(0, i-200), min(len(text), i+len(s)+200)
	return text[lo:hi]
}

func TestSecretLeak(t *testing.T) {
	var logs syncBuffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	idp := oidctest.New()
	t.Cleanup(idp.Close)
	idp.ClientSecret = leakOIDC // the sign-in below works only if this secret reaches the provider
	fake := clock.NewFake(time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC))
	idp.Clock = fake
	pc := &panicClock{Clock: fake}
	hs := &harness{t: t, dir: filepath.Join(t.TempDir(), "data"), idp: idp, clock: fake, banner: &bytes.Buffer{}}
	a, err := New(Options{
		DataDir: hs.dir, Listen: "127.0.0.1:0", Version: "test", Logger: logger,
		Banner: hs.banner, Clock: pc, HTTPClient: idp.Server.Client(),
		WebUI: fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>shell</title>")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	hs.app, hs.h = a, a.Handler()
	lc := &leakCollector{}

	// do sends a request and keeps the response (body and cookies).
	do := func(b *browser, method, path string, body any) response {
		t.Helper()
		r := b.do(method, path, body)
		lc.add(method+" "+path+" ("+strconv.Itoa(r.Code)+")", r.Body.String()+strings.Join(r.Header().Values("Set-Cookie"), "\n"))
		return r
	}
	ok := func(r response, codes ...int) response {
		t.Helper()
		for _, c := range codes {
			if r.Code == c {
				return r
			}
		}
		t.Fatalf("%d %s", r.Code, r.Body)
		return r
	}

	// Sign-in uses the OpenID client secret.
	ada := hs.completeSetup()
	bob := hs.browser("198.51.100.2")
	ok(bob.signIn(bobMember), http.StatusFound)
	bobID := ok(do(bob, "GET", "/api/me", nil), 200).body["user"].(map[string]any)["id"].(string)

	// Notifications to a webhook URL that cannot be reached.
	ok(do(ada, "PATCH", "/api/settings", map[string]any{
		"values":  map[string]any{"notify.enabled": true, "notify.events": []string{"change.submitted", "change.approved", "change.executed"}},
		"secrets": map[string]any{"notify.webhook_url": leakWebhookURL},
	}), 200)
	ok(do(ada, "PATCH", "/api/settings", map[string]any{
		"values":  map[string]any{"auth.session_hours": 0},
		"secrets": map[string]any{"notify.webhook_url": "https://hooks.invalid/" + leakRejected},
	}), http.StatusUnprocessableEntity)
	ok(do(ada, "POST", "/api/notifications/test", nil), 200)

	// A target connection. With Docker it is a real Postgres database
	// whose user has the replaced password, so the first password fails to
	// authenticate and the second works; without Docker it is unreachable.
	host, port, dbName, user := "127.0.0.1", 1, "app", "app"
	var live targetDB
	t.Run("start postgres", func(t *testing.T) { live = postgresTargets(t).newDB(t) })
	if live != nil {
		h, p, err := net.SplitHostPort(live.backend())
		if err != nil {
			t.Fatal(err)
		}
		host, port = h, mustAtoi(t, p)
		dbName = live.config("", 0)["database"].(string)
		user = "leak_" + dbName
		live.exec(t, fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s'", user, leakTarget2))
		live.exec(t, "GRANT USAGE ON SCHEMA public TO "+user)
		live.exec(t, "GRANT SELECT, INSERT, UPDATE, DELETE ON users, audit_notes TO "+user)
	} else {
		t.Log("Docker is not available: the target connection is unreachable and the MCP schema tools are checked for errors only")
	}
	conn := ok(do(ada, "POST", "/api/connections", pgBody("leaky", host, port, dbName, user, leakTarget)), http.StatusCreated)
	connID := conn.body["id"].(string)
	// Wrong password (or unreachable): the error is reported, the password is not.
	if r := ok(do(ada, "POST", "/api/connections/"+connID+"/test", nil), 200); r.body["ok"] != false {
		t.Fatalf("test with the wrong password: %s", r.Body)
	}
	ok(do(ada, "PATCH", "/api/connections/"+connID, map[string]any{"password": leakTarget2}), 200)
	if r := ok(do(ada, "POST", "/api/connections/"+connID+"/test", nil), 200); live != nil && r.body["ok"] != true {
		t.Fatalf("test with the right password: %s", r.Body)
	}
	ok(do(ada, "POST", "/api/connections", pgBody("bad-host", "169.254.169.254", 5432, "app", "app", leakRejected)), http.StatusUnprocessableEntity)
	do(ada, "POST", "/api/connections", `{"name":"x","connector":"postgres","password":`+leakRejected+`}`)
	ok(do(ada, "POST", "/api/connections/test", map[string]any{
		"connector": "postgres", "password": leakRejected,
		"config": map[string]any{"host": "127.0.0.1", "port": 2, "database": "app", "username": "app", "tls_mode": "disable"},
	}), 200)
	ok(do(ada, "POST", "/api/connections", pgBody("unreachable", "127.0.0.1", 2, "app", "app", leakRejected)), http.StatusCreated)

	// API tokens: shown once, at creation.
	for _, b := range []*browser{bob, bob, ada} {
		r := b.do("POST", "/api/tokens", map[string]any{"name": "agent", "expires_in_days": 30})
		ok(r, http.StatusCreated)
		secret := r.body["secret"].(string)
		lc.mu.Lock()
		lc.pats = append(lc.pats, secret)
		lc.mu.Unlock()
		// The creation response may hold the token, nothing else.
		lc.add("POST /api/tokens", strings.ReplaceAll(r.Body.String(), secret, "[the new token]"))
	}

	// A change, reviewed and executed (with a live target).
	changeRef := "x"
	if live != nil {
		r := ok(do(bob, "POST", "/api/changes", map[string]any{
			"connection_id": connID, "title": "Rename Ann", "reason": "Ticket 1",
			"statements": []string{"UPDATE users SET name = 'Zed' WHERE id = 1"}, "submit": true,
		}), http.StatusCreated)
		changeRef = r.body["id"].(string)
		r = ok(do(ada, "GET", "/api/changes/"+changeRef, nil), 200)
		ok(do(ada, "POST", "/api/changes/"+changeRef+"/reviews", map[string]any{
			"decision": "approve", "sql_hash": r.body["sql_hash"], "preview_hash": r.body["preview_hash"],
		}), 200)
		a.executor.RunOnce(context.Background())
		if r := ok(do(ada, "GET", "/api/changes/"+changeRef, nil), 200); r.body["status"] != "executed" {
			t.Fatalf("change: %s", r.Body)
		}
		ok(do(ada, "POST", "/api/changes/"+changeRef+"/refresh-preview", nil), http.StatusConflict)
	}
	a.notify.RunOnce(context.Background())
	if r := ok(do(ada, "GET", "/api/notifications", nil), 200); !strings.Contains(r.Body.String(), "change.submitted") && live != nil {
		t.Errorf("no notification was attempted: %s", r.Body)
	}

	// MCP: every tool, with the first token.
	srv := httptest.NewServer(hs.h)
	t.Cleanup(srv.Close)
	mc := connectMCP(t, srv.URL, lc.pats[0])
	tools := []struct {
		name string
		args map[string]any
	}{
		{"list_connections", nil},
		{"list_tables", map[string]any{"connection": "leaky"}},
		{"get_schema", map[string]any{"connection": "leaky"}},
		{"get_schema", map[string]any{"connection": "leaky", "table": "users"}},
		{"find_tables", map[string]any{"query": "users"}},
		{"preview_change", map[string]any{"connection": "leaky", "statements": []string{"UPDATE users SET name = 'Q' WHERE id = 2"}}},
		{"preview_change", map[string]any{"connection": "unreachable", "statements": []string{"UPDATE users SET name = 'Q' WHERE id = 2"}}},
		{"submit_change", map[string]any{"connection": "leaky", "title": "Agent", "reason": "r", "statements": []string{"UPDATE users SET name = 'Q' WHERE id = 3"}}},
		{"list_changes", nil},
		{"get_change", map[string]any{"number": 1}},
		{"get_change", map[string]any{"number": 2}},
		{"cancel_change", map[string]any{"number": 2, "reason": "done"}},
		{"list_tables", map[string]any{"connection": "nope"}},
	}
	for _, tool := range tools {
		r := mc.call(tool.name, tool.args)
		lc.add("MCP "+tool.name, r.text)
		if live != nil && r.isErr && tool.args["connection"] != "nope" && tool.args["connection"] != "unreachable" && tool.name != "get_change" && tool.name != "cancel_change" {
			t.Errorf("MCP %s(%v) failed: %s", tool.name, tool.args, r.text)
		}
	}
	lc.add("MCP response bodies", mc.rec.all())
	for _, auth := range []string{"Bearer dbal_pat_" + strings.Repeat("A", 43), "Bearer " + leakTarget2, "Basic " + leakOIDC} {
		req, _ := http.NewRequestWithContext(context.Background(), "POST", srv.URL+"/mcp", strings.NewReader(initializeBody))
		req.Header.Set("Authorization", auth)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		// A presented credential echoed back would be a leak too; these
		// ones are sentinels only when they are ours.
		lc.add("MCP with a bad credential", string(raw)+fmt.Sprint(resp.Header))
	}

	// Forced panics while a secret is being handled.
	for _, p := range []struct {
		fragment, method, path string
		body                   any
	}{
		{"internal/targets.", "POST", "/api/connections", pgBody("panicky", "127.0.0.1", 3, "app", "app", leakPanic)},
		{"internal/settings.", "PATCH", "/api/settings", map[string]any{"secrets": map[string]any{"notify.webhook_url": "https://hooks.invalid/" + leakPanic}}},
	} {
		frag := p.fragment
		pc.armed.Store(&frag)
		r := do(ada, p.method, p.path, p.body)
		if pc.armed.Load() != nil {
			pc.armed.Store(nil)
			t.Fatalf("%s %s: the forced panic did not happen (%d)", p.method, p.path, r.Code)
		}
		if r.Code != http.StatusInternalServerError {
			t.Errorf("%s %s after a panic: %d %s", p.method, p.path, r.Code, r.Body)
		}
	}
	if !strings.Contains(logs.String(), "panic in handler") {
		t.Error("the forced panics were not logged")
	}
	// Nothing of the panicked requests was kept.
	if r := ok(do(ada, "GET", "/api/connections", nil), 200); strings.Contains(r.Body.String(), "panicky") {
		t.Error("the panicked connection was created")
	}

	// Walk every operation of the contract: GETs with real ids as both
	// users, and every operation with unknown ids and empty bodies for
	// the error responses.
	doc, err := openapi3.NewLoader().LoadFromFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{"users": bobID, "connections": connID, "changes": changeRef, "tokens": "x"}
	param := regexp.MustCompile(`\{[^}]+\}`)
	walked := 0
	for path, item := range doc.Paths.Map() {
		if strings.HasPrefix(path, "/api/auth/signout") {
			continue
		}
		for method, op := range item.Operations() {
			if method == "GET" {
				withIDs := param.ReplaceAllStringFunc(path, func(string) string {
					parts := strings.Split(path, "/")
					return ids[parts[2]]
				})
				for _, b := range []*browser{ada, bob} {
					do(b, method, withIDs, nil)
					walked++
				}
				if strings.HasPrefix(path, "/api/audit") {
					do(ada, method, withIDs+"?from=2000-01-01T00:00:00Z", nil)
				}
			}
			var body any
			if op.RequestBody != nil {
				body = "{}"
			}
			do(ada, method, param.ReplaceAllString(path, "x"), body)
			walked++
		}
	}
	if walked < 60 {
		t.Fatalf("walked only %d requests", walked)
	}
	r := ok(do(ada, "GET", "/api/audit/export", nil), 200)
	if !strings.Contains(r.Body.String(), "api_token.created") || !strings.Contains(r.Body.String(), "secret.replaced") {
		t.Errorf("the audit export lacks expected events")
	}
	for _, p := range []string{"/api/auth/signout", "/api/auth/signout-everywhere"} {
		do(bob, "POST", p, nil)
	}

	// Logs (debug level), the audit table, and the database files.
	if !strings.Contains(logs.String(), "secret opened") || !strings.Contains(logs.String(), `"level":"DEBUG"`) {
		t.Fatal("debug logs were not captured")
	}
	lc.add("logs", logs.String())
	lc.add("audit_events", strings.Join(hs.auditActions(), "\n"))
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(hs.dir, "*"))
	for _, f := range files {
		raw, err := os.ReadFile(f) //nolint:gosec // test data directory
		if err != nil {
			t.Fatal(err)
		}
		lc.add("file "+filepath.Base(f), string(raw))
	}
	lc.check(t)
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// syncBuffer is a bytes.Buffer safe for the concurrent writes of a logger.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
