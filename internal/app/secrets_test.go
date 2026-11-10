package app

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/secrets"
	"github.com/alvaroibarguen/dbairlock/internal/store"
)

// Plaintexts that must never come back out of the server.
const (
	googleSecret   = "PLAINTEXT-google-client-secret-7f3a"
	webhookURL     = "https://hooks.example.com/services/PLAINTEXT-webhook-91c2"
	targetPassword = "PLAINTEXT-target-password-55d0"
	rejectedSecret = "PLAINTEXT-rejected-in-a-bad-update-0b8e"
)

var plaintexts = []string{googleSecret, webhookURL, targetPassword, rejectedSecret}

func newDevApp(t *testing.T, dir string, logs *bytes.Buffer) *App {
	t.Helper()
	a, err := New(Options{
		DataDir: dir,
		Listen:  "127.0.0.1:0",
		Dev:     true,
		Version: "test",
		Logger:  slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		WebUI:   fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>shell</title>")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

func call(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func assertNoPlaintext(t *testing.T, where, s string) {
	t.Helper()
	for _, p := range plaintexts {
		if strings.Contains(s, p) {
			t.Errorf("%s contains the secret %q", where, p)
		}
	}
}

func TestNoInterfaceRevealsASecret(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "data")
	var logs bytes.Buffer
	a := newDevApp(t, dir, &logs)
	h := a.Handler()

	// Set secrets through the API and directly through the secret store.
	body, _ := json.Marshal(map[string]any{
		"values":  map[string]any{"auth.google.client_id": "123.apps.googleusercontent.com"},
		"secrets": map[string]any{"auth.google.client_secret": googleSecret, "notify.webhook_url": webhookURL},
	})
	if rec := call(t, h, "PATCH", "/api/settings", string(body)); rec.Code != 200 {
		t.Fatalf("set secrets: %d %s", rec.Code, rec.Body)
	}
	actx := audit.WithActor(ctx, devAdmin)
	ref := secrets.Ref{Kind: secrets.KindTargetPassword, OwnerType: "connection", OwnerID: "c1"}
	for range 2 { // set, then replace
		if err := a.store.Tx(actx, func(tx *store.Tx) error {
			return a.secrets.Set(actx, tx, ref, secrets.NewValue(targetPassword))
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.secrets.Open(ctx, ref); err != nil {
		t.Fatal(err)
	}

	// A rejected update and a malformed one must not echo the secret either.
	bad, _ := json.Marshal(map[string]any{
		"values":  map[string]any{"auth.session_hours": 0},
		"secrets": map[string]any{"auth.google.client_secret": rejectedSecret},
	})
	if rec := call(t, h, "PATCH", "/api/settings", string(bad)); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad update: %d %s", rec.Code, rec.Body)
	} else {
		assertNoPlaintext(t, "422 response", rec.Body.String())
	}
	malformed := `{"secrets":{"auth.google.client_secret":"` + rejectedSecret + `"},"values":{"auth.session_hours":"x"}}`
	if rec := call(t, h, "PATCH", "/api/settings", malformed); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed update: %d %s", rec.Code, rec.Body)
	} else {
		assertNoPlaintext(t, "400 response", rec.Body.String())
	}

	// Walk every operation in the contract as the admin.
	doc, err := openapi3.NewLoader().LoadFromFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	param := regexp.MustCompile(`\{[^}]+\}`)
	walked := 0
	sawStatus := false
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			reqBody := ""
			if op.RequestBody != nil {
				reqBody = "{}"
			}
			rec := call(t, h, method, param.ReplaceAllString(path, "x"), reqBody)
			walked++
			assertNoPlaintext(t, method+" "+path, rec.Body.String()+strings.Join(rec.Header().Values("Set-Cookie"), ""))
			if path == "/api/settings" && method == "GET" {
				if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"is_set":true`) {
					t.Fatalf("GET /api/settings: %d %s", rec.Code, rec.Body)
				}
				sawStatus = true
			}
		}
	}
	if walked < 5 || !sawStatus {
		t.Fatalf("walked %d operations", walked)
	}

	// Logs (including debug) and every audit payload.
	assertNoPlaintext(t, "logs", logs.String())
	if !strings.Contains(logs.String(), "secret opened") {
		t.Fatal("debug logs were not captured")
	}
	rows, err := a.store.Reader().QueryContext(ctx, `SELECT action || ' ' || payload FROM audit_events ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var events []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		events = append(events, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(events, "\n")
	assertNoPlaintext(t, "audit log", all)
	for _, action := range []string{"secret.set", "secret.replaced", "setting.changed"} {
		if !strings.Contains(all, action) {
			t.Errorf("no %s event in:\n%s", action, all)
		}
	}
}

func TestRotateKeyThenRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	var logs bytes.Buffer
	a := newDevApp(t, dir, &logs)
	body := `{"secrets":{"auth.google.client_secret":"` + googleSecret + `"}}`
	if rec := call(t, a.Handler(), "PATCH", "/api/settings", body); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}

	res, err := RotateKey(context.Background(), dir, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil || res.Secrets != 1 || res.KeyVersion != 2 {
		t.Fatalf("rotate: %+v %v", res, err)
	}

	b := newDevApp(t, dir, &logs)
	if b.secrets.KeyVersion() != 2 {
		t.Fatalf("key version %d", b.secrets.KeyVersion())
	}
	v, err := b.secrets.Open(context.Background(), secrets.Ref{Kind: secrets.KindOAuthClientSecret, OwnerType: "instance", OwnerID: "google_oauth"})
	if err != nil || v.IsEmpty() {
		t.Fatalf("open after rotation: %v", err)
	}
	rec := call(t, b.Handler(), "GET", "/api/settings", "")
	if !strings.Contains(rec.Body.String(), `"is_set":true`) {
		t.Fatalf("%s", rec.Body)
	}
	assertNoPlaintext(t, "logs", logs.String())
}

func TestSettingsNeedDevModeUntilSignIn(t *testing.T) {
	rec := call(t, newTestApp(t).Handler(), "GET", "/api/settings", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestCrossOriginPatchIsRejected(t *testing.T) {
	var logs bytes.Buffer
	h := newDevApp(t, filepath.Join(t.TempDir(), "data"), &logs).Handler()
	req := httptest.NewRequest("PATCH", "/api/settings", strings.NewReader(`{"values":{"instance.name":"x"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "cross_origin") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
