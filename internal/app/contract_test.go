package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// settingsValues returns the settings values as canonical JSON.
func settingsValues(t *testing.T, b *browser) string {
	t.Helper()
	r := b.do("GET", "/api/settings", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("get settings: %d %s", r.Code, r.Body)
	}
	raw, err := json.Marshal(r.body["values"])
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// A setting sent without the "values" wrapper used to answer 200 and change
// nothing, because the body decoder ignored unknown members. It must be
// refused, and nothing may change.
func TestSettingsPatchRejectsUnknownKey(t *testing.T) {
	hs := newHarness(t)
	b := hs.completeSetup()
	before := settingsValues(t, b)
	audits := len(hs.auditActions())

	for _, body := range []string{
		`{"instance.public_url":"https://elsewhere.test"}`,
		`{"values":{"instance.name":"renamed"},"instance.public_url":"https://elsewhere.test"}`,
		`{"values":{"instance.name":"renamed","instance.nope":1}}`,
		`{"values":{"instance.name":"renamed"},"secrets":{"auth.google.client_secret":"s3cret","extra":null}}`,
	} {
		r := b.do("PATCH", "/api/settings", body)
		if r.Code != http.StatusBadRequest || r.errCode() != "bad_request" {
			t.Errorf("%s: %d %s, want 400 bad_request", body, r.Code, r.Body)
		}
		if strings.Contains(r.Body.String(), "s3cret") {
			t.Errorf("%s: the error echoes a secret: %s", body, r.Body)
		}
	}
	if after := settingsValues(t, b); after != before {
		t.Fatalf("a refused update changed settings:\nbefore %s\nafter  %s", before, after)
	}
	if n := len(hs.auditActions()); n != audits {
		t.Fatalf("a refused update was audited: %v", hs.auditActions()[audits:])
	}

	// The same update, well formed, goes through.
	if r := b.do("PATCH", "/api/settings", `{"values":{"instance.name":"renamed"}}`); r.Code != http.StatusOK {
		t.Fatalf("valid update: %d %s", r.Code, r.Body)
	}
	if settingsValues(t, b) == before {
		t.Fatal("valid update changed nothing")
	}

	// A value rule (a range) is left to the settings module, whose 422 names
	// the field for the dashboard.
	r := b.do("PATCH", "/api/settings", `{"values":{"auth.session_hours":0}}`)
	if r.Code != http.StatusUnprocessableEntity || r.errCode() != "validation_failed" {
		t.Fatalf("out-of-range value: %d %s, want 422 validation_failed", r.Code, r.Body)
	}
}

// Every JSON request body is checked against api/openapi.yaml before any
// handler runs: an unknown member, a wrong type, a missing required member,
// a body that is not JSON or a body on an operation without one is a 400,
// whatever the resource.
func TestRequestBodiesMatchTheContract(t *testing.T) {
	hs := newHarness(t)
	b := hs.completeSetup()
	me := b.do("GET", "/api/me", nil)
	user, _ := me.body["user"].(map[string]any)
	userID, _ := user["id"].(string)
	tokensBefore := b.do("GET", "/api/tokens", nil).Body.String()
	audits := len(hs.auditActions())

	for _, tc := range []struct{ method, path, body string }{
		// Unknown members, at the top level and nested.
		{"POST", "/api/tokens", `{"name":"agent","admin":true}`},
		{"PATCH", "/api/users/" + userID, `{"role":"member","note":"x"}`},
		{"POST", "/api/changes", `{"connection_id":"c","title":"t","statements":[{"sql":"DELETE FROM t WHERE id = 1","rows":1}]}`},
		{"POST", "/api/changes/preview", `{"connection_id":"c","statements":[],"dry":true}`},
		{"POST", "/api/changes/x/reviews", `{"decision":"approve","force":true}`},
		{"POST", "/api/sequences", `{"title":"t","steps":[{"connection_id":"c","statements":["DELETE FROM t WHERE id = 1"],"run":"now"},{"connection_id":"c","statements":["x"]}]}`},
		{"POST", "/api/sequences", `{"title":"t","steps":[{"connection_id":"c","statements":["x"]}],"atomic":true}`},
		{"POST", "/api/sequences", `{"title":"t","steps":[{"statements":["x"]},{"connection_id":"c","statements":["x"]}]}`},
		{"POST", "/api/sequences", `{"title":"t","steps":"one"}`},
		{"POST", "/api/sequences/x/cancel", `{"reason":"r","force":true}`},
		{"POST", "/api/connections", `{"name":"n","connector":"postgres","config":{},"password":"p","ssl":true}`},
		{"PUT", "/api/setup/signin", `{"public_url":"https://x.test","clientId":"y"}`},
		// Wrong types and missing required members.
		{"POST", "/api/tokens", `{"name":42}`},
		{"POST", "/api/tokens", `{}`},
		{"PATCH", "/api/users/" + userID, `{"role":["admin"]}`},
		// Not JSON, or not an object.
		{"POST", "/api/tokens", `{"name":`},
		{"POST", "/api/tokens", `[{"name":"agent"}]`},
		// A body on an operation that takes none.
		{"POST", "/api/users/" + userID + "/restore", `{"role":"admin"}`},
		{"POST", "/api/auth/signout-everywhere", `{"keep":"this"}`},
	} {
		r := b.do(tc.method, tc.path, tc.body)
		if r.Code != http.StatusBadRequest || r.errCode() != "bad_request" {
			t.Errorf("%s %s %s: %d %s, want 400 bad_request", tc.method, tc.path, tc.body, r.Code, r.Body)
		}
	}
	if after := b.do("GET", "/api/tokens", nil).Body.String(); after != tokensBefore {
		t.Fatalf("a refused request created a token: %s", after)
	}
	if got := b.do("GET", "/api/me", nil); got.Code != http.StatusOK || got.body["user"].(map[string]any)["role"] != "admin" {
		t.Fatalf("a refused request changed the user or ended the session: %d %s", got.Code, got.Body)
	}
	if n := len(hs.auditActions()); n != audits {
		t.Fatalf("a refused request was audited: %v", hs.auditActions()[audits:])
	}

	// A body without a JSON content type is refused too.
	b.header.Set("Content-Type", "text/plain")
	if r := b.do("POST", "/api/tokens", `{"name":"agent"}`); r.Code != http.StatusBadRequest {
		t.Fatalf("text/plain body: %d %s", r.Code, r.Body)
	}
	b.header.Del("Content-Type")

	// Access comes first: without a session the answer is 401, whatever
	// the body.
	anon := hs.browser("192.0.2.20")
	if r := anon.do("PATCH", "/api/settings", `{"nope":1}`); r.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous bad body: %d %s, want 401", r.Code, r.Body)
	}

	// Well-formed requests still work.
	if r := b.do("POST", "/api/tokens", `{"name":"agent","expires_in_days":30}`); r.Code != http.StatusCreated {
		t.Fatalf("valid token: %d %s", r.Code, r.Body)
	}
}
