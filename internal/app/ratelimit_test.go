package app

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// The rate limits on everything that checks something an attacker might
// guess or flood: sign-in, the setup code (TestSetupEndToEnd), recovery
// codes and API tokens. Per-token MCP tool limits are in TestMCP.

func TestSignInIsRateLimited(t *testing.T) {
	hs := newHarness(t)
	hs.completeSetup()
	b := hs.browser("203.0.113.5")
	// 20 sign-in starts or callbacks per client address, then one every 3 s.
	for i := range 20 {
		if r := b.do("GET", "/api/auth/google/start", nil); r.Code != http.StatusFound || !strings.HasPrefix(r.location(), hs.idp.Issuer) {
			t.Fatalf("start %d: %d %q", i+1, r.Code, r.location())
		}
	}
	if r := b.do("GET", "/api/auth/google/start", nil); r.location() != "/signin?error=rate_limited" {
		t.Fatalf("21st start: %d %q", r.Code, r.location())
	}
	// Callbacks share the allowance, so flooding them does not help either.
	if r := b.do("GET", "/api/auth/google/callback?code=x&state=y", nil); r.location() != "/signin?error=rate_limited" {
		t.Fatalf("callback while limited: %d %q", r.Code, r.location())
	}
	// Other addresses are unaffected, and the allowance refills.
	if r := hs.browser("203.0.113.6").do("GET", "/api/auth/google/start", nil); !strings.HasPrefix(r.location(), hs.idp.Issuer) {
		t.Fatalf("other address: %q", r.location())
	}
	hs.clock.Advance(4 * time.Second)
	if r := b.do("GET", "/api/auth/google/start", nil); !strings.HasPrefix(r.location(), hs.idp.Issuer) {
		t.Fatalf("after refill: %q", r.location())
	}
}

func TestRecoveryCodeIsRateLimited(t *testing.T) {
	hs := newHarness(t)
	hs.completeSetup()
	url, _, err := hs.app.setup.IssueRecovery(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	code := url[strings.Index(url, "#code=")+len("#code="):]

	b := hs.browser("203.0.113.7")
	for i := range 5 {
		if r := b.do("POST", "/api/setup/recover", map[string]string{"code": "wrong"}); r.Code != http.StatusUnauthorized {
			t.Fatalf("wrong code %d: %d %s", i+1, r.Code, r.Body)
		}
	}
	// Even the right code is refused while the address is limited.
	r := b.do("POST", "/api/setup/recover", map[string]string{"code": code})
	if r.Code != http.StatusTooManyRequests || r.errCode() != "rate_limited" || r.Header().Get("Retry-After") == "" {
		t.Fatalf("6th attempt: %d %s", r.Code, r.Body)
	}
	// After the allowance refills, one more failure starts a growing delay.
	hs.clock.Advance(13 * time.Second)
	if r := b.do("POST", "/api/setup/recover", map[string]string{"code": "wrong"}); r.Code != http.StatusUnauthorized {
		t.Fatalf("after refill: %d", r.Code)
	}
	hs.clock.Advance(15 * time.Second)
	if r := b.do("POST", "/api/setup/recover", map[string]string{"code": code}); r.Code != http.StatusTooManyRequests {
		t.Fatalf("during backoff: %d", r.Code)
	}
	if !hs.hasAudit("setup.recovery_code_rejected") {
		t.Fatal("rejected recovery codes are not audited")
	}
	// The code is still good from another address.
	if r := hs.browser("203.0.113.8").do("POST", "/api/setup/recover", map[string]string{"code": code}); r.Code != http.StatusNoContent {
		t.Fatalf("other address: %d %s", r.Code, r.Body)
	}
}

func TestBadAPITokensAreRateLimited(t *testing.T) {
	hs := newHarness(t)
	admin := hs.completeSetup()
	r := admin.do("POST", "/api/tokens", map[string]any{"name": "agent"})
	secret, _ := r.body["secret"].(string)
	if r.Code != http.StatusCreated || secret == "" {
		t.Fatalf("create token: %d %s", r.Code, r.Body)
	}
	bad := "dbal_pat_" + strings.Repeat("A", len(secret)-len("dbal_pat_"))

	agent := hs.browser("203.0.113.9")
	agent.header.Set("Authorization", "Bearer "+bad)
	// Ten free failures, then a lock-out that doubles from one second.
	for i := range 11 {
		if r := agent.do("GET", "/mcp", nil); r.Code != http.StatusUnauthorized {
			t.Fatalf("bad token %d: %d %s", i+1, r.Code, r.Body)
		}
	}
	// While locked out, even a good token from this address waits.
	agent.header.Set("Authorization", "Bearer "+secret)
	r = agent.do("GET", "/mcp", nil)
	if r.Code != http.StatusTooManyRequests || r.errCode() != "rate_limited" || r.Header().Get("Retry-After") == "" {
		t.Fatalf("locked out: %d %s", r.Code, r.Body)
	}
	// Other addresses are unaffected.
	other := hs.browser("203.0.113.10")
	other.header.Set("Authorization", "Bearer "+secret)
	if r := other.do("GET", "/mcp", nil); r.Code == http.StatusUnauthorized || r.Code == http.StatusTooManyRequests {
		t.Fatalf("other address: %d %s", r.Code, r.Body)
	}
	// After the lock-out a good token works and clears the failures.
	hs.clock.Advance(2 * time.Second)
	if r := agent.do("GET", "/mcp", nil); r.Code == http.StatusUnauthorized || r.Code == http.StatusTooManyRequests {
		t.Fatalf("after lock-out: %d %s", r.Code, r.Body)
	}
	agent.header.Set("Authorization", "Bearer "+bad)
	if r := agent.do("GET", "/mcp", nil); r.Code != http.StatusUnauthorized {
		t.Fatalf("failures not cleared: %d", r.Code)
	}
}
