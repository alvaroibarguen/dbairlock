package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestContractRoutesCoverEveryOperation(t *testing.T) {
	routes, err := contractRoutes()
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != len(operationAccess) {
		t.Fatalf("%d contract routes, %d operations with declared access", len(routes), len(operationAccess))
	}
	for pattern, cr := range routes {
		if !strings.HasPrefix(pattern, cr.route.Method+" /") {
			t.Errorf("pattern %q", pattern)
		}
		// See validationSpec: a keyword only JSON Schema 2020-12 knows
		// would make every validated request compile its schema.
		if !cr.route.Spec.IsOpenAPI30() {
			t.Fatalf("the contract uses a keyword that disables direct validation (%s)", pattern)
		}
	}
}

func TestUnknownSettingsKeyIsRefusedBeforeTheModule(t *testing.T) {
	fs := &fakeSettings{}
	rec, out := send(t, settingsMux(fakeAccess{}, fs), "PATCH", "/api/settings", `{"instance.public_url":"http://x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("%d %v", rec.Code, out)
	}
	e := out["error"].(map[string]any)
	fields, _ := e["fields"].([]any)
	if e["code"] != "bad_request" || !strings.Contains(e["message"].(string), `"instance.public_url"`) || len(fields) != 1 {
		t.Fatalf("error = %v", e)
	}
	if fs.got != nil {
		t.Fatalf("the module ran: %+v", fs.got)
	}
}

func TestContractErrorDoesNotEchoASecret(t *testing.T) {
	const plain = "s3cret-from-request"
	for _, body := range []string{
		`{"secrets":{"auth.google.client_secret":["` + plain + `"]}}`,
		`{"secrets":{"auth.google.client_secret":{"v":"` + plain + `"}}}`,
	} {
		rec, out := send(t, settingsMux(fakeAccess{}, &fakeSettings{}), "PATCH", "/api/settings", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %v", body, rec.Code, out)
		}
		if strings.Contains(rec.Body.String(), plain) {
			t.Fatalf("%s: error echoes the secret: %s", body, rec.Body)
		}
	}
}

func BenchmarkValidatePatchSettings(b *testing.B) {
	h := settingsMux(fakeAccess{}, &fakeSettings{})
	const body = `{"values":{"auth.session_hours":24,"auth.allowed_domains":["example.com"],"instance.name":"x"}}`
	for b.Loop() {
		req := httptest.NewRequest("PATCH", "/api/settings", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: "__Host-dbairlock_session", Value: "session-token"})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatal(rec.Code, rec.Body.String())
		}
	}
}
