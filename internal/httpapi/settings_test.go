package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/identity"
	"github.com/alvaroibarguen/dbairlock/internal/platform/logx"
	"github.com/alvaroibarguen/dbairlock/internal/secrets"
	"github.com/alvaroibarguen/dbairlock/internal/settings"
)

// fakeAccess authenticates every session cookie as the given user, or
// fails with err.
type fakeAccess struct {
	Identity
	err    error
	member bool
}

func (f fakeAccess) AuthenticateSession(context.Context, string) (identity.Principal, error) {
	if f.err != nil {
		return identity.Principal{}, f.err
	}
	role := identity.RoleAdmin
	if f.member {
		role = identity.RoleMember
	}
	return identity.PrincipalOf(identity.User{ID: "u1", Email: "admin@example.com", Role: role}), nil
}

type fakeSettings struct {
	got       *settings.Changes
	updateErr error
	status    map[string]secrets.Meta
}

func (f *fakeSettings) Values() map[string]any {
	out := map[string]any{}
	for _, d := range settings.Definitions() {
		out[d.Key] = d.Default
	}
	return out
}

func (f *fakeSettings) Value(key string) any { return f.Values()[key] }

func (f *fakeSettings) SecretStatus(context.Context) (map[string]secrets.Meta, error) {
	return f.status, nil
}

func (f *fakeSettings) Update(_ context.Context, ch settings.Changes) error {
	f.got = &ch
	return f.updateErr
}

func settingsMux(access Identity, s Settings) *http.ServeMux {
	return newMux(Deps{Setup: fakeSetup{}, Readiness: fakeReady{}, Identity: access, Settings: s})
}

func send(t *testing.T, h http.Handler, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "__Host-dbairlock_session", Value: "session-token"})
	req = req.WithContext(logx.WithLogger(req.Context(), logx.Discard()))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return rec, out
}

func TestSettingsRequireAdmin(t *testing.T) {
	fs := &fakeSettings{}
	for _, tc := range []struct {
		access fakeAccess
		want   int
	}{
		{fakeAccess{err: identity.ErrUnauthenticated}, http.StatusUnauthorized},
		{fakeAccess{member: true}, http.StatusForbidden},
	} {
		h := settingsMux(tc.access, fs)
		for _, m := range []string{"GET", "PATCH"} {
			rec, _ := send(t, h, m, "/api/settings", `{"values":{"auth.session_hours":2}}`)
			if rec.Code != tc.want {
				t.Errorf("%s: %d, want %d", m, rec.Code, tc.want)
			}
		}
	}
	if fs.got != nil {
		t.Fatal("update ran without access")
	}
}

func TestGetSettingsShape(t *testing.T) {
	at := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	fs := &fakeSettings{status: map[string]secrets.Meta{
		"auth.google.client_secret": {IsSet: true, SetBy: "admin@example.com", SetAt: at},
	}}
	rec, body := send(t, settingsMux(fakeAccess{}, fs), "GET", "/api/settings", "")
	if rec.Code != 200 {
		t.Fatalf("%d %v", rec.Code, body)
	}
	values := body["values"].(map[string]any)
	if values["auth.session_hours"] != float64(12) || values["instance.name"] != "dbairlock" {
		t.Fatalf("values = %v", values)
	}
	sec := body["secrets"].(map[string]any)
	g := sec["auth.google.client_secret"].(map[string]any)
	if g["is_set"] != true || g["set_by"] != "admin@example.com" || g["set_at"] != "2026-11-09T10:00:00Z" || len(g) != 3 {
		t.Fatalf("secret status = %v", g)
	}
	if w := sec["notify.webhook_url"].(map[string]any); w["is_set"] != false || len(w) != 1 {
		t.Fatalf("unset status = %v", w)
	}
}

func TestPatchSettingsSecretSemantics(t *testing.T) {
	fs := &fakeSettings{}
	const plain = "s3cret-from-request"
	body := fmt.Sprintf(`{"values":{"auth.session_hours":24,"auth.allowed_domains":["example.com"]},
		"secrets":{"auth.google.client_secret":%q,"notify.webhook_url":null}}`, plain)
	rec, out := send(t, settingsMux(fakeAccess{}, fs), "PATCH", "/api/settings", body)
	if rec.Code != 200 {
		t.Fatalf("%d %v", rec.Code, out)
	}
	if strings.Contains(rec.Body.String(), plain) {
		t.Fatal("response echoes the secret")
	}
	ch := fs.got
	if len(ch.Values) != 2 || string(ch.Values["auth.session_hours"].(json.RawMessage)) != "24" {
		t.Fatalf("values = %v", ch.Values)
	}
	g, ok := ch.Secrets["auth.google.client_secret"]
	if !ok || g.Clear || g.Value.IsEmpty() {
		t.Fatalf("replace = %+v", g)
	}
	if w := ch.Secrets["notify.webhook_url"]; !w.Clear {
		t.Fatalf("null did not clear: %+v", w)
	}

	// Omitted means unchanged.
	_, _ = send(t, settingsMux(fakeAccess{}, fs), "PATCH", "/api/settings", `{"secrets":{}}`)
	if len(fs.got.Secrets) != 0 || len(fs.got.Values) != 0 {
		t.Fatalf("omitted fields changed something: %+v", fs.got)
	}
}

func TestPatchSettingsValidationError(t *testing.T) {
	fs := &fakeSettings{updateErr: &settings.ValidationError{Fields: []settings.FieldError{
		{Field: "auth.session_hours", Message: "Must be between 1 and 720."},
	}}}
	rec, out := send(t, settingsMux(fakeAccess{}, fs), "PATCH", "/api/settings", `{"values":{"auth.session_hours":0}}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("%d %v", rec.Code, out)
	}
	e := out["error"].(map[string]any)
	fields := e["fields"].([]any)
	if e["code"] != "validation_failed" || len(fields) != 1 ||
		fields[0].(map[string]any)["field"] != "auth.session_hours" {
		t.Fatalf("error = %v", e)
	}
}
