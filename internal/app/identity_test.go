package app

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/identity/oidctest"
)

var regexpGroups = regexp.MustCompile(`^[A-Z2-7]{5}(-[A-Z2-7]{5}){3}$`)

func TestSetupEndToEnd(t *testing.T) {
	hs := newHarness(t)
	code := hs.setupCode()
	if !regexpGroups.MatchString(code) {
		t.Fatalf("setup code %q is not 4 groups of 5 base32 characters", code)
	}
	if !strings.Contains(hs.banner.String(), "http://127.0.0.1:0/setup") {
		t.Fatalf("banner has no URL: %s", hs.banner)
	}
	anon := hs.browser("192.0.2.1")

	// Setup mode: every API route but meta and setup answers 503; so does /mcp.
	for _, p := range []string{"/api/users", "/api/settings", "/api/me", "/mcp", "/api/tokens"} {
		if r := anon.do("GET", p, nil); r.Code != http.StatusServiceUnavailable || r.errCode() != "setup_required" {
			t.Fatalf("%s in setup mode: %d %s", p, r.Code, r.Body)
		}
	}
	if r := anon.do("GET", "/api/meta", nil); r.body["setupState"] != "setup_required" {
		t.Fatalf("meta: %s", r.Body)
	}
	if r := anon.do("GET", "/healthz", nil); r.Code != http.StatusOK {
		t.Fatalf("healthz: %d", r.Code)
	}
	// Nothing in the wizard works without the code.
	if r := anon.do("GET", "/api/setup/signin", nil); r.Code != http.StatusUnauthorized {
		t.Fatalf("signin settings without a session: %d", r.Code)
	}
	if r := anon.do("GET", "/api/setup/google/start", nil); r.Code != http.StatusFound || !strings.HasPrefix(r.location(), "/setup?error=") {
		t.Fatalf("google start without a session: %d %q", r.Code, r.location())
	}

	// A wrong code is rejected, and repeated attempts are rate limited.
	for i := range 5 {
		if r := anon.do("POST", "/api/setup/verify", map[string]string{"code": "AAAAA-BBBBB-CCCCC-DDDDD"}); r.Code != http.StatusUnauthorized || r.errCode() != "invalid_code" {
			t.Fatalf("wrong code %d: %d %s", i, r.Code, r.Body)
		}
	}
	r := anon.do("POST", "/api/setup/verify", map[string]string{"code": code})
	if r.Code != http.StatusTooManyRequests || r.Header().Get("Retry-After") == "" {
		t.Fatalf("6th attempt in a minute: %d %s", r.Code, r.Body)
	}
	hs.clock.Advance(13 * time.Second)
	if r := anon.do("POST", "/api/setup/verify", map[string]string{"code": "wrong"}); r.Code != http.StatusUnauthorized {
		t.Fatalf("after refill: %d", r.Code)
	}
	// Six failures in a row: a growing delay (30 seconds now) applies even
	// though the per-minute allowance has refilled.
	hs.clock.Advance(15 * time.Second)
	if r := anon.do("POST", "/api/setup/verify", map[string]string{"code": code}); r.Code != http.StatusTooManyRequests {
		t.Fatalf("during backoff: %d", r.Code)
	}
	if !hs.hasAudit("setup.code_rejected") {
		t.Fatal("rejected codes are not audited")
	}

	// The correct code (typed in lower case, without dashes) from another
	// address opens a setup session.
	admin := hs.browser("192.0.2.10")
	typed := strings.ToLower(strings.ReplaceAll(code, "-", ""))
	if r := admin.do("POST", "/api/setup/verify", map[string]string{"code": typed}); r.Code != http.StatusNoContent {
		t.Fatalf("verify: %d %s", r.Code, r.Body)
	}
	if admin.cookies["__Host-dbairlock_setup"] == "" {
		t.Fatalf("no setup cookie: %v", admin.cookies)
	}
	if r := admin.do("GET", "/api/setup/session", nil); r.body["kind"] != "setup" {
		t.Fatalf("session: %s", r.Body)
	}
	r = admin.do("GET", "/api/setup/signin", nil)
	if r.Code != http.StatusOK || r.body["suggested_public_url"] != "http://dbairlock.test" || r.body["redirect_uri"] != "" {
		t.Fatalf("signin view: %d %s", r.Code, r.Body)
	}
	// Every field is required.
	r = admin.do("PUT", "/api/setup/signin", map[string]any{"public_url": " ", "client_id": "x", "allowed_domains": []string{"example.com"}})
	if r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), "auth.google.client_secret") {
		t.Fatalf("incomplete settings: %d %s", r.Code, r.Body)
	}
	// A remote issuer other than Google is refused.
	r = admin.do("PUT", "/api/setup/signin", map[string]any{
		"public_url": publicURL, "client_id": "x", "client_secret": "y",
		"allowed_domains": []string{"example.com"}, "oidc_issuer": "https://evil.example.com",
	})
	if r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), "auth.oidc_issuer") {
		t.Fatalf("remote issuer: %d %s", r.Code, r.Body)
	}

	r = admin.do("PUT", "/api/setup/signin", map[string]any{
		"public_url": publicURL, "client_id": hs.idp.ClientID, "client_secret": hs.idp.ClientSecret,
		"allowed_domains": []string{"example.com"}, "oidc_issuer": hs.idp.Issuer,
	})
	if r.Code != http.StatusOK || r.body["redirect_uri"] != publicURL+"/api/auth/google/callback" {
		t.Fatalf("save: %d %s", r.Code, r.Body)
	}
	if strings.Contains(r.Body.String(), hs.idp.ClientSecret) {
		t.Fatal("the client secret came back")
	}

	// Still in setup mode until a real sign-in works.
	if r := anon.do("GET", "/api/users", nil); r.Code != http.StatusServiceUnavailable {
		t.Fatalf("before sign-in: %d", r.Code)
	}
	hs.idp.SignInAs(adaAdmin)
	r = admin.google(admin.do("GET", "/api/setup/google/start", nil), nil)
	if r.Code != http.StatusFound || r.location() != "/" || admin.cookies[sessionCookie] == "" {
		t.Fatalf("finish: %d %q %v", r.Code, r.location(), admin.cookies)
	}
	if _, ok := admin.cookies["__Host-dbairlock_setup"]; ok {
		t.Fatal("setup cookie survived")
	}
	setCookie := strings.Join(r.Header().Values("Set-Cookie"), "\n")
	for _, attr := range []string{"HttpOnly", "Secure", "SameSite=Lax", "Path=/"} {
		if !strings.Contains(setCookie, attr) {
			t.Errorf("session cookie lacks %s: %s", attr, setCookie)
		}
	}

	// The first user is an admin; setup mode is over.
	r = admin.do("GET", "/api/me", nil)
	user, _ := r.body["user"].(map[string]any)
	if r.Code != http.StatusOK || user["role"] != "admin" || user["email"] != "ada@example.com" ||
		!strings.Contains(r.Body.String(), "user.manage") {
		t.Fatalf("me: %d %s", r.Code, r.Body)
	}
	if r := anon.do("GET", "/api/meta", nil); r.body["setupState"] != "ready" {
		t.Fatalf("meta after setup: %s", r.Body)
	}
	if r := admin.do("GET", "/api/users", nil); r.Code != http.StatusOK {
		t.Fatalf("users after setup: %d", r.Code)
	}
	if r := anon.do("GET", "/api/users", nil); r.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous after setup: %d", r.Code)
	}
	// The setup code and the setup session no longer work.
	other := hs.browser("192.0.2.20")
	if r := other.do("POST", "/api/setup/verify", map[string]string{"code": code}); r.Code != http.StatusConflict || r.errCode() != "setup_complete" {
		t.Fatalf("code after setup: %d %s", r.Code, r.Body)
	}
	for _, a := range []string{"setup.code_issued", "setup.code_accepted", "user.created", "setup.completed", "setting.changed", "secret.set"} {
		if !hs.hasAudit(a) {
			t.Errorf("no %s audit event", a)
		}
	}
	if res, err := VerifyAudit(context.Background(), hs.dir); err != nil || !res.OK() {
		t.Fatalf("audit chain: %+v %v", res, err)
	}

	// A restart does not return to setup mode or print a new code.
	n := len(codePattern.FindAllString(hs.banner.String(), -1))
	_ = hs.app.Close()
	hs.start()
	if got := len(codePattern.FindAllString(hs.banner.String(), -1)); got != n {
		t.Fatal("a completed instance printed a setup code")
	}
	if r := anon.do("GET", "/api/meta", nil); r.body["setupState"] != "ready" {
		t.Fatalf("meta after restart: %s", r.Body)
	}
}

func TestSetupCodeChangesOnRestart(t *testing.T) {
	hs := newHarness(t)
	first := hs.setupCode()
	_ = hs.app.Close()
	hs.start()
	if second := hs.setupCode(); second == first {
		t.Fatal("restart reused the setup code")
	}
	b := hs.browser("192.0.2.1")
	if r := b.do("POST", "/api/setup/verify", map[string]string{"code": first}); r.Code != http.StatusUnauthorized {
		t.Fatalf("old code: %d", r.Code)
	}
}

func TestSignInChecks(t *testing.T) {
	hs := newHarness(t)
	hs.completeSetup()

	cases := []struct {
		name    string
		account oidctest.Account
		between func(b *browser, cb *url.URL)
		want    string
	}{
		{"wrong domain", oidctest.Account{Sub: "2", Email: "eve@other.com", HD: "other.com", EmailVerified: true}, nil, "domain_not_allowed"},
		{"personal account", oidctest.Account{Sub: "3", Email: "eve@gmail.com", EmailVerified: true}, nil, "domain_not_allowed"},
		{"unverified email", oidctest.Account{Sub: "4", Email: "eve@example.com", HD: "example.com"}, nil, "email_unverified"},
		{"bad nonce", withFault(bobMember, func(a *oidctest.Account) { a.WrongNonce = true }), nil, "bad_nonce"},
		{"wrong audience", withFault(bobMember, func(a *oidctest.Account) { a.WrongAudience = true }), nil, "invalid_id_token"},
		{"expired ID token", withFault(bobMember, func(a *oidctest.Account) { a.Expired = true }), nil, "invalid_id_token"},
		{"bad signature", withFault(bobMember, func(a *oidctest.Account) { a.BadSignature = true }), nil, "invalid_id_token"},
		{"no ID token", withFault(bobMember, func(a *oidctest.Account) { a.NoIDToken = true }), nil, "no_id_token"},
		{"bad state", bobMember, func(_ *browser, cb *url.URL) {
			q := cb.Query()
			q.Set("state", "forged")
			cb.RawQuery = q.Encode()
		}, "bad_state"},
		{"expired code", bobMember, func(b *browser, _ *url.URL) { b.hs.clock.Advance(2 * time.Minute) }, "code_exchange_failed"},
		{"no flow cookie", bobMember, func(b *browser, _ *url.URL) { clear(b.cookies) }, "flow_expired"},
		{"flow too old", bobMember, func(b *browser, _ *url.URL) { b.hs.clock.Advance(11 * time.Minute) }, "flow_expired"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := hs.browser("198.51.100." + string(rune('1'+i%9)))
			hs.idp.SignInAs(tc.account)
			r := b.google(b.do("GET", "/api/auth/google/start", nil), func(cb *url.URL) {
				if tc.between != nil {
					tc.between(b, cb)
				}
			})
			if r.Code != http.StatusFound || r.location() != "/signin?error="+tc.want {
				t.Fatalf("got %d %q, want error %s", r.Code, r.location(), tc.want)
			}
			if b.cookies[sessionCookie] != "" {
				t.Fatal("a denied sign-in got a session")
			}
		})
	}
	if !hs.hasAudit("login.denied") {
		t.Fatal("denied sign-ins are not audited")
	}
	// The provider's own error is reported when the flow is intact.
	b := hs.browser("198.51.100.50")
	r := b.google(b.do("GET", "/api/auth/google/start", nil), func(cb *url.URL) { cb.RawQuery = "error=access_denied&state=x" })
	if r.location() != "/signin?error=provider_error" {
		t.Fatalf("provider error: %q", r.location())
	}

	// First sign-in creates a member; the redirect stays on this site.
	b = hs.browser("198.51.100.60")
	hs.idp.SignInAs(bobMember)
	r = b.google(b.do("GET", "/api/auth/google/start?next="+url.QueryEscape("//evil.example.com/x"), nil), nil)
	if r.Code != http.StatusFound || r.location() != "/" {
		t.Fatalf("open redirect: %q", r.location())
	}
	r = b.signIn(bobMember)
	if r.location() != "/changes" {
		t.Fatalf("next: %q", r.location())
	}
	me := b.do("GET", "/api/me", nil)
	if u, _ := me.body["user"].(map[string]any); u["role"] != "member" || strings.Contains(me.Body.String(), "user.manage") {
		t.Fatalf("new user: %s", me.Body)
	}
}

func withFault(a oidctest.Account, f func(*oidctest.Account)) oidctest.Account {
	f(&a)
	return a
}

func TestRemovedUser(t *testing.T) {
	hs := newHarness(t)
	admin := hs.completeSetup()
	bob := hs.browser("198.51.100.2")
	bob.signIn(bobMember)
	bob2 := hs.browser("198.51.100.3")
	bob2.signIn(bobMember)
	created := bob.do("POST", "/api/tokens", map[string]any{"name": "laptop agent"})
	secret, _ := created.body["secret"].(string)
	if created.Code != http.StatusCreated || secret == "" {
		t.Fatalf("token: %d %s", created.Code, created.Body)
	}
	agent := hs.browser("198.51.100.4")
	agent.header.Set("Authorization", "Bearer "+secret)
	if r := agent.do("GET", "/mcp", nil); r.Code != http.StatusNotFound {
		t.Fatalf("token before removal: %d %s", r.Code, r.Body)
	}

	users := admin.do("GET", "/api/users", nil).body["users"].([]any)
	var bobID string
	for _, u := range users {
		if m := u.(map[string]any); m["email"] == "bob@example.com" {
			bobID = m["id"].(string)
		}
	}
	r := admin.do("POST", "/api/users/"+bobID+"/remove", nil)
	if r.Code != http.StatusOK || r.body["removed_at"] == nil || r.body["removed_by"] != "ada@example.com" {
		t.Fatalf("remove: %d %s", r.Code, r.Body)
	}
	// Every session and the token stop working on the very next request.
	for _, b := range []*browser{bob, bob2} {
		if r := b.do("GET", "/api/me", nil); r.Code != http.StatusUnauthorized {
			t.Fatalf("removed user's session: %d", r.Code)
		}
	}
	if r := agent.do("GET", "/mcp", nil); r.Code != http.StatusUnauthorized {
		t.Fatalf("removed user's token: %d", r.Code)
	}
	// Signing in again is refused with a plain reason.
	if r := hs.browser("198.51.100.5").signIn(bobMember); r.location() != "/signin?error=account_removed" {
		t.Fatalf("removed user's sign-in: %q", r.location())
	}
	if !hs.hasAudit("user.removed") {
		t.Fatal("removal not audited")
	}

	// Restoring lets them sign in again as a member; the token stays revoked.
	if r := admin.do("POST", "/api/users/"+bobID+"/restore", nil); r.Code != http.StatusOK || r.body["role"] != "member" || r.body["removed_at"] != nil {
		t.Fatalf("restore: %d %s", r.Code, r.Body)
	}
	if r := hs.browser("198.51.100.6").signIn(bobMember); r.location() != "/changes" {
		t.Fatalf("restored sign-in: %q", r.location())
	}
	if r := agent.do("GET", "/mcp", nil); r.Code != http.StatusUnauthorized {
		t.Fatalf("token after restore: %d", r.Code)
	}
}

func TestSessions(t *testing.T) {
	hs := newHarness(t)
	hs.completeSetup()
	b := hs.browser("198.51.100.2")
	b.signIn(bobMember)

	// Activity slides the expiry (12 hours by default).
	for range 3 {
		hs.clock.Advance(11 * time.Hour)
		if r := b.do("GET", "/api/me", nil); r.Code != http.StatusOK {
			t.Fatalf("active session expired: %d", r.Code)
		}
	}
	hs.clock.Advance(12*time.Hour + time.Second)
	if r := b.do("GET", "/api/me", nil); r.Code != http.StatusUnauthorized {
		t.Fatalf("idle session: %d", r.Code)
	}
	if _, ok := b.cookies[sessionCookie]; ok {
		t.Fatal("an expired session cookie is not cleared")
	}

	// The absolute cap of 30 days holds however active the session is.
	b.signIn(bobMember)
	for range 70 {
		hs.clock.Advance(11 * time.Hour)
		if r := b.do("GET", "/api/me", nil); r.Code != http.StatusOK {
			break
		}
	}
	if r := b.do("GET", "/api/me", nil); r.Code != http.StatusUnauthorized {
		t.Fatal("session outlived the 30-day cap")
	}

	// Sign out ends this session only.
	one, two := hs.browser("198.51.100.3"), hs.browser("198.51.100.4")
	one.signIn(bobMember)
	two.signIn(bobMember)
	if r := one.do("POST", "/api/auth/signout", nil); r.Code != http.StatusNoContent {
		t.Fatalf("sign out: %d", r.Code)
	}
	if r := one.do("GET", "/api/me", nil); r.Code != http.StatusUnauthorized {
		t.Fatal("signed-out session still works")
	}
	if r := two.do("GET", "/api/me", nil); r.Code != http.StatusOK {
		t.Fatal("sign out ended another session")
	}
	// Sign out everywhere ends all of them.
	one.signIn(bobMember)
	if r := two.do("POST", "/api/auth/signout-everywhere", nil); r.Code != http.StatusNoContent {
		t.Fatalf("sign out everywhere: %d", r.Code)
	}
	for _, b := range []*browser{one, two} {
		if r := b.do("GET", "/api/me", nil); r.Code != http.StatusUnauthorized {
			t.Fatal("a session survived sign out everywhere")
		}
	}
	// A forged cookie is nobody.
	forged := hs.browser("198.51.100.7")
	forged.cookies[sessionCookie] = "forged"
	if r := forged.do("GET", "/api/me", nil); r.Code != http.StatusUnauthorized {
		t.Fatal("forged cookie accepted")
	}
}

func TestCrossSitePostIsRejected(t *testing.T) {
	hs := newHarness(t)
	admin := hs.completeSetup()
	admin.header.Set("Sec-Fetch-Site", "cross-site")
	for _, p := range []string{"/api/auth/signout", "/api/tokens"} {
		if r := admin.do("POST", p, map[string]any{"name": "x"}); r.Code != http.StatusForbidden || r.errCode() != "cross_origin" {
			t.Fatalf("%s: %d %s", p, r.Code, r.Body)
		}
	}
	admin.header.Del("Sec-Fetch-Site")
	if r := admin.do("GET", "/api/me", nil); r.Code != http.StatusOK {
		t.Fatal("the cross-site sign-out went through")
	}
}

func TestMemberIsRefusedOnAdminEndpoints(t *testing.T) {
	hs := newHarness(t)
	hs.completeSetup()
	m := hs.browser("198.51.100.2")
	m.signIn(bobMember)
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/settings", ""},
		{"PATCH", "/api/settings", `{"values":{"instance.name":"x"}}`},
		{"GET", "/api/users", ""},
		{"PATCH", "/api/users/x", `{"role":"admin"}`},
		{"POST", "/api/users/x/remove", ""},
		{"POST", "/api/users/x/restore", ""},
		{"GET", "/api/tokens?all=true", ""},
	} {
		var body any
		if c.body != "" {
			body = c.body
		}
		if r := m.do(c.method, c.path, body); r.Code != http.StatusForbidden || r.errCode() != "forbidden" {
			t.Errorf("member %s %s: %d %s", c.method, c.path, r.Code, r.Body)
		}
	}
}

func TestLastAdminInvariant(t *testing.T) {
	hs := newHarness(t)
	ada := hs.completeSetup()
	adaID := ada.do("GET", "/api/me", nil).body["user"].(map[string]any)["id"].(string)

	// Alone, the admin can neither demote nor remove themselves.
	if r := ada.do("PATCH", "/api/users/"+adaID, map[string]string{"role": "member"}); r.Code != http.StatusConflict || r.errCode() != "last_admin" {
		t.Fatalf("demote last admin: %d %s", r.Code, r.Body)
	}
	if r := ada.do("POST", "/api/users/"+adaID+"/remove", nil); r.Code != http.StatusConflict {
		t.Fatalf("remove last admin: %d %s", r.Code, r.Body)
	}

	// Two admins try to remove or demote each other at the same moment.
	// Whatever the interleaving, one enabled admin remains.
	bob := hs.browser("198.51.100.2")
	bob.signIn(bobMember)
	bobID := bob.do("GET", "/api/me", nil).body["user"].(map[string]any)["id"].(string)
	enabledAdmins := func() int {
		var n int
		if err := hs.app.store.Reader().QueryRowContext(context.Background(),
			`SELECT count(*) FROM users WHERE role = 'admin' AND disabled_at IS NULL`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for round := range 6 {
		// Both are enabled admins with a fresh session.
		for _, step := range []struct {
			by   *browser
			path string
			body any
		}{
			{ada, "/api/users/" + bobID + "/restore", nil},
			{ada, "/api/users/" + bobID, map[string]string{"role": "admin"}},
		} {
			method := "POST"
			if step.body != nil {
				method = "PATCH"
			}
			if r := step.by.do(method, step.path, step.body); r.Code != http.StatusOK {
				t.Fatalf("round %d: %s %s: %d %s", round, method, step.path, r.Code, r.Body)
			}
		}
		bob.signIn(bobMember)

		var wg sync.WaitGroup
		codes := make([]int, 2)
		wg.Go(func() {
			if round%2 == 0 {
				codes[0] = ada.do("POST", "/api/users/"+bobID+"/remove", nil).Code
			} else {
				codes[0] = ada.do("PATCH", "/api/users/"+bobID, map[string]string{"role": "member"}).Code
			}
		})
		wg.Go(func() { codes[1] = bob.do("POST", "/api/users/"+adaID+"/remove", nil).Code })
		wg.Wait()
		if n := enabledAdmins(); n != 1 {
			t.Fatalf("round %d: %d enabled admins after %v", round, n, codes)
		}
		if (codes[0] == http.StatusOK) == (codes[1] == http.StatusOK) {
			t.Fatalf("round %d: exactly one change must win, got %v", round, codes)
		}
		if codes[1] == http.StatusOK {
			// Bob won: he hands the admin role back to Ada and steps down.
			for _, req := range []struct {
				method, path string
				body         any
			}{
				{"POST", "/api/users/" + adaID + "/restore", nil},
				{"PATCH", "/api/users/" + adaID, map[string]string{"role": "admin"}},
			} {
				if r := bob.do(req.method, req.path, req.body); r.Code != http.StatusOK {
					t.Fatalf("hand back: %d %s", r.Code, r.Body)
				}
			}
			ada.signIn(adaAdmin)
		}
		if r := ada.do("POST", "/api/users/"+bobID+"/remove", nil); r.Code != http.StatusOK {
			t.Fatalf("round %d reset: %d %s", round, r.Code, r.Body)
		}
	}
}

func TestAPITokens(t *testing.T) {
	hs := newHarness(t)
	admin := hs.completeSetup()
	bob := hs.browser("198.51.100.2")
	bob.signIn(bobMember)

	r := bob.do("POST", "/api/tokens", map[string]any{"name": "claude", "expires_in_days": 30})
	secret, _ := r.body["secret"].(string)
	tok, _ := r.body["token"].(map[string]any)
	if r.Code != http.StatusCreated || !strings.HasPrefix(secret, "dbal_pat_") || len(secret) != 52 ||
		r.body["mcp_url"] != publicURL+"/mcp" || !strings.HasPrefix(secret, tok["prefix"].(string)) {
		t.Fatalf("create: %d %s", r.Code, r.Body)
	}
	if r := bob.do("POST", "/api/tokens", map[string]any{"name": "x", "expires_in_days": 7}); r.Code != http.StatusBadRequest && r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad expiry: %d", r.Code)
	}
	// Shown once: no listing contains it.
	list := bob.do("GET", "/api/tokens", nil)
	if strings.Contains(list.Body.String(), secret) || len(list.body["tokens"].([]any)) != 1 {
		t.Fatalf("list: %s", list.Body)
	}
	if all := admin.do("GET", "/api/tokens?all=true", nil); strings.Contains(all.Body.String(), secret) || len(all.body["tokens"].([]any)) != 1 {
		t.Fatalf("admin list: %s", all.Body)
	}
	if mine := admin.do("GET", "/api/tokens", nil); len(mine.body["tokens"].([]any)) != 0 {
		t.Fatalf("admin's own list: %s", mine.Body)
	}

	agent := hs.browser("198.51.100.9")
	if r := agent.do("GET", "/mcp", nil); r.Code != http.StatusUnauthorized || r.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("no token: %d", r.Code)
	}
	for _, bad := range []string{"dbal_pat_" + strings.Repeat("0", 43), secret[:20], tok["prefix"].(string)} {
		agent.header.Set("Authorization", "Bearer "+bad)
		if r := agent.do("GET", "/mcp", nil); r.Code != http.StatusUnauthorized {
			t.Fatalf("token %q: %d", bad, r.Code)
		}
	}
	agent.header.Set("Authorization", "Bearer "+secret)
	if r := agent.do("GET", "/mcp", nil); r.Code != http.StatusNotFound {
		t.Fatalf("valid token: %d %s", r.Code, r.Body)
	}
	// A token is not a session: it cannot use the dashboard API.
	if r := agent.do("GET", "/api/me", nil); r.Code != http.StatusUnauthorized {
		t.Fatalf("token on /api: %d", r.Code)
	}
	used := bob.do("GET", "/api/tokens", nil).body["tokens"].([]any)[0].(map[string]any)
	if used["last_used_at"] == nil {
		t.Fatal("last_used_at not recorded")
	}

	// Expired tokens stop working.
	hs.clock.Advance(31 * 24 * time.Hour)
	bob.signIn(bobMember)
	if r := agent.do("GET", "/mcp", nil); r.Code != http.StatusUnauthorized {
		t.Fatalf("expired token: %d", r.Code)
	}

	// Revoked tokens stop working; another member cannot revoke yours.
	r = bob.do("POST", "/api/tokens", map[string]any{"name": "no expiry"})
	secret2 := r.body["secret"].(string)
	id2 := r.body["token"].(map[string]any)["id"].(string)
	carol := hs.browser("198.51.100.3")
	carol.signIn(oidctest.Account{Sub: "1003", Email: "carol@example.com", HD: "example.com", EmailVerified: true})
	if r := carol.do("DELETE", "/api/tokens/"+id2, nil); r.Code != http.StatusNotFound {
		t.Fatalf("someone else's token: %d", r.Code)
	}
	agent.header.Set("Authorization", "Bearer "+secret2)
	if r := agent.do("GET", "/mcp", nil); r.Code != http.StatusNotFound {
		t.Fatalf("second token: %d", r.Code)
	}
	if r := bob.do("DELETE", "/api/tokens/"+id2, nil); r.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d", r.Code)
	}
	if r := agent.do("GET", "/mcp", nil); r.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token: %d", r.Code)
	}
	if r := bob.do("DELETE", "/api/tokens/"+id2, nil); r.Code != http.StatusNotFound {
		t.Fatalf("revoke twice: %d", r.Code)
	}
	for _, a := range []string{"api_token.created", "api_token.revoked"} {
		if !hs.hasAudit(a) {
			t.Errorf("no %s event", a)
		}
	}
	// The token never reaches the audit log.
	if all := strings.Join(hs.auditActions(), "\n"); strings.Contains(all, secret) || strings.Contains(all, secret2) {
		t.Fatal("a token is in the audit log")
	}
}

func TestRecovery(t *testing.T) {
	hs := newHarness(t)
	hs.completeSetup()
	ctx := context.Background()

	// The CLI path writes the code straight to the store.
	res, err := Recover(ctx, hs.dir, "127.0.0.1:8080", hs.app.log)
	if err != nil || !res.PublicURLSet || !strings.HasPrefix(res.URL, publicURL+"/recover#code=") {
		t.Fatalf("recover: %+v %v", res, err)
	}
	code := strings.TrimPrefix(res.URL, publicURL+"/recover#code=")

	b := hs.browser("198.51.100.2")
	if r := b.do("POST", "/api/setup/recover", map[string]string{"code": code}); r.Code != http.StatusNoContent {
		t.Fatalf("use code: %d %s", r.Code, r.Body)
	}
	// It works once.
	if r := hs.browser("198.51.100.3").do("POST", "/api/setup/recover", map[string]string{"code": code}); r.Code != http.StatusUnauthorized {
		t.Fatalf("second use: %d", r.Code)
	}
	if r := b.do("GET", "/api/setup/session", nil); r.body["kind"] != "recovery" {
		t.Fatalf("session: %s", r.Body)
	}
	// It reaches the sign-in settings and nothing else.
	for _, p := range []string{"/api/me", "/api/users", "/api/settings", "/api/tokens"} {
		if r := b.do("GET", p, nil); r.Code != http.StatusUnauthorized {
			t.Fatalf("recovery session on %s: %d", p, r.Code)
		}
	}
	if r := b.do("PATCH", "/api/settings", map[string]any{"values": map[string]any{"instance.name": "pwned"}}); r.Code != http.StatusUnauthorized {
		t.Fatalf("recovery session patching settings: %d", r.Code)
	}
	r := b.do("PUT", "/api/setup/signin", map[string]any{
		"public_url": publicURL, "client_id": hs.idp.ClientID, "allowed_domains": []string{"example.com", "example.org"},
	})
	if r.Code != http.StatusOK {
		t.Fatalf("save: %d %s", r.Code, r.Body)
	}
	// Confirming with a sign-in ends the recovery without creating a session.
	hs.idp.SignInAs(bobMember)
	r = b.google(b.do("GET", "/api/setup/google/start", nil), nil)
	if r.location() != "/signin?recovered=1" || b.cookies[sessionCookie] != "" {
		t.Fatalf("confirm: %q %v", r.location(), b.cookies)
	}
	if r := b.do("GET", "/api/setup/session", nil); r.Code != http.StatusUnauthorized {
		t.Fatal("recovery session survived confirmation")
	}
	var users int
	_ = hs.app.store.Reader().QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&users)
	if users != 1 {
		t.Fatalf("recovery created a user (%d users)", users)
	}
	var cli int
	_ = hs.app.store.Reader().QueryRowContext(ctx,
		`SELECT count(*) FROM audit_events WHERE via = 'cli' AND action IN ('setup.recovery_issued', 'setup.recovery_code_accepted', 'setting.changed', 'setup.recovery_completed')`).Scan(&cli)
	if cli < 4 {
		t.Fatalf("recovery audited with via=cli %d times", cli)
	}

	// Codes expire after 15 minutes.
	url, _, err := hs.app.setup.IssueRecovery(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hs.clock.Advance(16 * time.Minute)
	code = url[strings.Index(url, "#code=")+len("#code="):]
	if r := hs.browser("198.51.100.4").do("POST", "/api/setup/recover", map[string]string{"code": code}); r.Code != http.StatusUnauthorized {
		t.Fatalf("expired code: %d", r.Code)
	}
}
