package app

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// fuzzRoutes are the API routes FuzzAPIRequests drives, with {id} standing
// for a path parameter. Left out on purpose: sign-out (it would end the
// fuzzer's session), the Google redirects, and the routes that open network
// connections to an address taken from the request (connection tests and
// the notification test), so the fuzzer never dials arbitrary hosts.
var fuzzRoutes = []struct{ method, path string }{
	{"GET", "/api/meta"},
	{"GET", "/api/me"},
	{"POST", "/api/setup/verify"},
	{"POST", "/api/setup/recover"},
	{"PUT", "/api/setup/signin"},
	{"POST", "/api/dev/login"},
	{"GET", "/api/users"},
	{"PATCH", "/api/users/{id}"},
	{"POST", "/api/users/{id}/restore"},
	{"GET", "/api/tokens"},
	{"POST", "/api/tokens"},
	{"DELETE", "/api/tokens/{id}"},
	{"GET", "/api/settings"},
	{"PATCH", "/api/settings"},
	{"GET", "/api/connections"},
	{"GET", "/api/connections/{id}"},
	{"PATCH", "/api/connections/{id}"},
	{"POST", "/api/connections/{id}/archive"},
	{"GET", "/api/connections/{id}/schema"},
	{"GET", "/api/changes"},
	{"POST", "/api/changes"},
	{"POST", "/api/changes/preview"},
	{"GET", "/api/changes/{id}"},
	{"PATCH", "/api/changes/{id}"},
	{"POST", "/api/changes/{id}/submit"},
	{"POST", "/api/changes/{id}/cancel"},
	{"POST", "/api/changes/{id}/refresh-preview"},
	{"POST", "/api/changes/{id}/reviews"},
	{"POST", "/api/changes/{id}/execute"},
	{"POST", "/api/changes/{id}/retry"},
	{"GET", "/api/changes/{id}/events"},
	{"GET", "/api/audit"},
	{"GET", "/api/audit/verify"},
	{"GET", "/api/audit/export"},
	{"GET", "/api/system"},
	{"GET", "/api/notifications"},
}

// FuzzAPIRequests sends arbitrary bodies, path parameters and query strings
// to the API as a signed-in admin. Whatever the input, the server must
// answer with a client error or success and never a 5xx (a 5xx means a
// decoder or handler did not anticipate the input), and the session must
// survive.
func FuzzAPIRequests(f *testing.F) {
	hs := newHarness(f)
	b := hs.completeSetup()
	me := b.do("GET", "/api/me", nil)
	user, _ := me.body["user"].(map[string]any)
	userID, _ := user["id"].(string)
	if userID == "" {
		f.Fatalf("me: %s", me.Body)
	}

	seeds := []struct{ body, id, query string }{
		{`{}`, "", ""},
		{`{"values":{"instance.name":"x","policy.default_max_rows":250},"secrets":{"notify.webhook_url":null}}`, "", ""},
		{`{"values":{"auth.session_hours":1e400,"instance.trusted_proxies":["::/0"]}}`, "", ""},
		{`{"role":"admin"}`, userID, ""},
		{`{"name":"agent","expires_in_days":30}`, "", ""},
		{`{"connection_id":"x","title":"t","statements":[{"sql":"DELETE FROM t WHERE id = 1"}],"submit":true}`, "", ""},
		{`{"revision":1,"title":"t","statements":[]}`, "00000000-0000-0000-0000-000000000000", ""},
		{`{"decision":"approve","sql_hash":"","preview_hash":""}`, "1", ""},
		{`{"code":"AAAAA-BBBBB-CCCCC-DDDDD"}`, "", ""},
		{``, "", "view=all&status=executed&before=9223372036854775807&limit=200"},
		{`null`, "%00", "actor=a&action=change.&from=2026-01-01T00:00:00Z&to=0001-01-01T00:00:00Z&limit=500&before=-1"},
		{`[1,2,3]`, "../..", "from=2026-13-45T99:00:00Z&limit=0&before=0"},
		{`{"values":{"notify.events":["change.failed","change.failed"]}}`, "", "open=maybe&connection_id=" + strings.Repeat("a", 100)},
	}
	for i := range fuzzRoutes {
		for _, s := range seeds {
			f.Add(uint8(i), s.body, s.id, s.query)
		}
	}
	f.Fuzz(func(t *testing.T, which uint8, body, id, query string) {
		rt := fuzzRoutes[int(which)%len(fuzzRoutes)]
		path := strings.ReplaceAll(rt.path, "{id}", url.PathEscape(id))
		if query != "" {
			path += "?" + query
		}
		// Only what can arrive on a request line: httptest.NewRequest panics
		// on anything else, and so would the client sending it.
		if _, err := url.ParseRequestURI(path); err != nil || strings.ContainsFunc(path, func(r rune) bool { return r <= ' ' || r >= 0x7f }) {
			return
		}
		// The harness reports through this input's t: testing.F methods
		// must not be called inside the fuzz function.
		hst := *hs
		hst.t = t
		in := *b
		in.hs = &hst
		r := in.do(rt.method, path, body)
		if r.Code >= 500 {
			t.Fatalf("%s %s with body %q: %d %s", rt.method, path, body, r.Code, r.Body)
		}
		if r.Code == http.StatusUnauthorized && rt.path != "/api/setup/recover" && rt.path != "/api/setup/verify" {
			// The session must survive whatever the fuzzer sends.
			if me := in.do("GET", "/api/me", nil); me.Code != http.StatusOK {
				t.Fatalf("%s %s with body %q ended the session: %d", rt.method, path, body, me.Code)
			}
		}
	})
}
