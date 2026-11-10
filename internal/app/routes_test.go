package app

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/alvaroibarguen/dbairlock/internal/httpapi"
	"github.com/alvaroibarguen/dbairlock/internal/identity"
)

// memberActions are what a member may do (identity.Can); the rest needs an admin.
var memberActions = []identity.Action{identity.ActionChangeSubmit, identity.ActionChangeReview, identity.ActionAuditRead}

// TestRouteAccess walks every route registered on the root mux (the
// generated API routes and the ones app.go adds: MCP and the dashboard) and
// checks that each declares an access level, that the level holds against
// real requests, and that every mutating request from another origin is
// refused before it reaches a handler.
func TestRouteAccess(t *testing.T) {
	hs := newHarness(t)
	admin := hs.completeSetup()
	member := hs.browser("198.51.100.2")
	if r := member.signIn(bobMember); r.Code != http.StatusFound {
		t.Fatalf("member sign-in: %d %s", r.Code, r.Body)
	}
	anon := hs.browser("198.51.100.3")

	doc, err := openapi3.NewLoader().LoadFromFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	contract := map[string]string{} // "GET /api/me" -> GetMe
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			contract[method+" "+path] = strings.ToUpper(op.OperationID[:1]) + op.OperationID[1:]
		}
	}

	registered := map[string]bool{}
	for _, rt := range hs.app.routes {
		registered[rt.pattern] = true
		var access httpapi.Access
		if rt.access != nil {
			access = *rt.access
		} else {
			op, ok := contract[rt.pattern]
			if !ok {
				t.Errorf("%s is registered by the generated code but not in api/openapi.yaml", rt.pattern)
				continue
			}
			a, ok := httpapi.OperationAccess(op)
			if !ok {
				t.Errorf("%s (%s) has no declared access level", rt.pattern, op)
				continue
			}
			access = a
		}

		methods := []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}
		path := rt.pattern
		if m, p, ok := strings.Cut(rt.pattern, " "); ok {
			methods, path = []string{m}, p
		}
		path = strings.ReplaceAll(path, "{id}", "no-such-id")

		for _, method := range methods {
			name := method + " " + path
			unsafe := method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions

			// Cross-origin protection: a signed-in admin's browser, lured to
			// another site, must not be able to change anything.
			if unsafe {
				for _, h := range [][2]string{{"Sec-Fetch-Site", "cross-site"}, {"Origin", "https://evil.example"}} {
					admin.header.Set(h[0], h[1])
					r := admin.do(method, path, "{}")
					admin.header.Del(h[0])
					if r.Code != http.StatusForbidden || r.errCode() != "cross_origin" {
						t.Errorf("%s with %s: %d %s, want 403 cross_origin", name, h[0], r.Code, r.Body)
					}
				}
			}

			var body any
			if unsafe {
				body = "{}"
			}
			r := anon.do(method, path, body)
			switch access.Level {
			case httpapi.LevelUser:
				if r.Code != http.StatusUnauthorized || r.errCode() != "unauthorized" {
					t.Errorf("anonymous %s: %d %s, want 401", name, r.Code, r.Body)
				}
				if access.Action != "" && !slices.Contains(memberActions, access.Action) {
					if r := member.do(method, path, body); r.Code != http.StatusForbidden || r.errCode() != "forbidden" {
						t.Errorf("member %s: %d %s, want 403", name, r.Code, r.Body)
					}
				}
			case httpapi.LevelToken:
				if r.Code != http.StatusUnauthorized {
					t.Errorf("anonymous %s: %d %s, want 401", name, r.Code, r.Body)
				}
				// A session cookie is not an API token.
				if r := admin.do(method, path, body); r.Code != http.StatusUnauthorized {
					t.Errorf("%s with a session cookie: %d %s, want 401", name, r.Code, r.Body)
				}
			case httpapi.LevelDev:
				if r.Code != http.StatusNotFound {
					t.Errorf("%s outside --dev: %d %s, want 404", name, r.Code, r.Body)
				}
			case httpapi.LevelSetup:
				// Setup is complete and no code was given: nothing is granted.
				// Browser navigations come back to the setup page with an
				// error instead of going on to Google.
				if r.Code == http.StatusFound && strings.HasPrefix(r.location(), "/setup?error=") {
					break
				}
				if r.Code < 400 || r.Code >= 500 {
					t.Errorf("anonymous %s after setup: %d %s, want a 4xx", name, r.Code, r.Body)
				}
			case httpapi.LevelPublic:
				if r.Code >= 500 {
					t.Errorf("anonymous %s: %d %s", name, r.Code, r.Body)
				}
			default:
				t.Errorf("%s: unknown access level %q", name, access.Level)
			}
		}
	}

	for pattern := range contract {
		if !registered[pattern] {
			t.Errorf("%s is in api/openapi.yaml but not registered", pattern)
		}
	}
	for _, p := range []string{"/mcp", "/"} {
		if !registered[p] {
			t.Errorf("%s is not registered", p)
		}
	}
	// The admin was never signed out by any of the requests above.
	if r := admin.do("GET", "/api/me", nil); r.Code != http.StatusOK {
		t.Fatalf("admin session lost: %d", r.Code)
	}
}
