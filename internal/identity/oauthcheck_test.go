package identity

import (
	"strings"
	"testing"
)

func TestLoopbackRedirectURIs(t *testing.T) {
	accepted := []string{
		"http://127.0.0.1/callback",
		"http://127.0.0.1:33418/callback",
		"http://localhost:8080/callback",
		"http://localhost/",
		"http://localhost",
		"http://[::1]:9000/oauth/callback",
		"http://[::1]/cb",
		"http://127.0.0.1:65535/a/b-c_d.e~f",
	}
	for _, u := range accepted {
		if _, ok := parseLoopbackRedirect(u); !ok {
			t.Errorf("refused %q", u)
		}
	}
	hostile := []string{
		"",
		// Not plain http on a loopback host (RFC 8252).
		"https://127.0.0.1/callback",
		"https://example.com/callback",
		"http://example.com/callback",
		// Names and addresses that only look like loopback, or are loopback
		// written another way.
		"http://localhost.evil.com/callback",
		"http://localhost.:8080/callback",
		"http://127.0.0.1.nip.io/callback",
		"http://127.0.0.2/callback",
		"http://0.0.0.0:8080/callback",
		"http://2130706433/callback",
		"http://0x7f000001/callback",
		"http://0177.0.0.1/callback",
		"http://[::ffff:127.0.0.1]/callback",
		"http://[0:0:0:0:0:0:0:1]/callback",
		"http://LOCALHOST/callback",
		"HTTP://localhost/callback",
		"http://lоcalhost/callback", // Cyrillic о
		// User info tricks.
		"http://localhost@evil.com/callback",
		"http://evil.com@localhost/callback",
		"http://user:pass@127.0.0.1/callback",
		// Fragments and queries.
		"http://localhost:8080/callback#frag",
		"http://localhost:8080/callback#",
		"http://localhost:8080/callback?x=1",
		"http://localhost:8080/callback?",
		// Encoded characters, dot segments, odd characters.
		"http://localhost:8080/%63allback",
		"http://localhost%2eevil.com/callback",
		"http://127.0.0.1%00.evil.com/",
		"http://localhost:8080/../admin",
		"http://localhost:8080/a/./b",
		"http://localhost:8080\\@evil.com/",
		"http://local host/callback",
		"http://localhost/call back",
		"http://localhost/callback\n",
		"http://localhost/callbäck",
		// Ports.
		"http://localhost:0/callback",
		"http://localhost:65536/callback",
		"http://localhost:08080/callback",
		"http://localhost:-1/callback",
		"http://localhost:/callback",
		// Not an absolute http URL at all.
		"//localhost/callback",
		"localhost:8080/callback",
		"javascript://localhost/%0aalert(1)",
		"file://localhost/etc/passwd",
		"http:localhost/callback",
		"http:///callback",
		"http://[::1/callback",
	}
	hostile = append(hostile, "http://localhost/"+strings.Repeat("a", 600)) // too long
	for _, u := range hostile {
		if _, ok := parseLoopbackRedirect(u); ok {
			t.Errorf("accepted %q", u)
		}
	}
}

func TestRedirectMatching(t *testing.T) {
	reg := []string{"http://localhost:33418/callback", "http://127.0.0.1/cb"}
	for _, c := range []struct {
		uri  string
		want bool
	}{
		{"http://localhost:33418/callback", true},
		{"http://localhost:40000/callback", true}, // any port (RFC 8252 7.3)
		{"http://localhost/callback", true},
		{"http://127.0.0.1:1234/cb", true},
		{"http://127.0.0.1:33418/callback", false}, // another host than registered
		{"http://localhost:33418/callback/", false},
		{"http://localhost:33418/Callback", false},
		{"http://localhost:33418/callbackx", false},
		{"http://localhost:33418/cb", false},
		{"http://localhost:33418/callback?x=1", false},
		{"http://localhost:33418/callback#x", false},
		{"https://localhost:33418/callback", false},
		{"http://localhost.evil.com:33418/callback", false},
		{"", false},
	} {
		if got := redirectMatches(reg, c.uri); got != c.want {
			t.Errorf("%q: %v, want %v", c.uri, got, c.want)
		}
	}
	if redirectMatches([]string{"http://evil.com/callback"}, "http://evil.com/callback") {
		t.Error("a non-loopback registration matched")
	}
}

func TestPKCE(t *testing.T) {
	// RFC 7636 appendix B.
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	challenge := "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if pkceS256(verifier) != challenge {
		t.Fatalf("S256 = %s", pkceS256(verifier))
	}
	if !verifyPKCE(verifier, challenge) {
		t.Fatal("the RFC example does not verify")
	}
	for _, c := range []struct{ name, verifier, challenge string }{
		{"wrong verifier", "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXl", challenge},
		{"plain method: challenge equals verifier", verifier, verifier},
		{"short verifier", "abc", pkceS256("abc")},
		{"verifier of 42 characters", strings.Repeat("a", 42), pkceS256(strings.Repeat("a", 42))},
		{"verifier of 129 characters", strings.Repeat("a", 129), pkceS256(strings.Repeat("a", 129))},
		{"verifier with a forbidden character", strings.Repeat("a", 42) + "+", pkceS256(strings.Repeat("a", 42) + "+")},
		{"empty", "", ""},
		{"padded challenge", verifier, challenge + "="},
	} {
		if verifyPKCE(c.verifier, c.challenge) {
			t.Errorf("%s verified", c.name)
		}
	}
	if !verifyPKCE(strings.Repeat("~", 128), pkceS256(strings.Repeat("~", 128))) {
		t.Error("a 128-character verifier was refused")
	}
	for _, c := range []string{challenge, strings.Repeat("A", 43)} {
		if !validChallenge(c) {
			t.Errorf("challenge %q refused", c)
		}
	}
	for _, c := range []string{"", "short", challenge + "A", strings.Repeat("A", 42) + "=", strings.Repeat("A", 42) + "+"} {
		if validChallenge(c) {
			t.Errorf("challenge %q accepted", c)
		}
	}
}

func TestResourceIndicators(t *testing.T) {
	issuer, resource := "https://dbairlock.example.com", "https://dbairlock.example.com/mcp"
	for _, r := range []string{
		resource, resource + "/", "HTTPS://DBAIRLOCK.EXAMPLE.COM/mcp", issuer, issuer + "/",
	} {
		if !resourceMatches(r, issuer, resource) {
			t.Errorf("refused %q", r)
		}
	}
	for _, r := range []string{
		"", "dbairlock.example.com/mcp", "https://dbairlock.example.com/MCP", "https://dbairlock.example.com/mcp#x",
		"https://dbairlock.example.com/mcp?x=1", "https://dbairlock.example.com/other", "http://dbairlock.example.com/mcp",
		"https://dbairlock.example.com:8443/mcp", "https://evil.example.com/mcp", "https://user@dbairlock.example.com/mcp",
		"https://dbairlock.example.com.evil.com/mcp", "urn:dbairlock", "https://dbairlock.example.com/mcp/x",
	} {
		if resourceMatches(r, issuer, resource) {
			t.Errorf("accepted %q", r)
		}
	}
}

func TestWellKnownURLs(t *testing.T) {
	o := OAuthServer{Issuer: "https://db.example.com", Resource: "https://db.example.com/mcp"}
	if got := o.ResourceMetadataURL(); got != "https://db.example.com/.well-known/oauth-protected-resource/mcp" {
		t.Error(got)
	}
	if got := o.MetadataURL(); got != "https://db.example.com/.well-known/oauth-authorization-server" {
		t.Error(got)
	}
	p := OAuthServer{Issuer: "https://example.com/dbairlock", Resource: "https://example.com/dbairlock/mcp"}
	if got := p.ResourceMetadataURL(); got != "https://example.com/.well-known/oauth-protected-resource/dbairlock/mcp" {
		t.Error(got)
	}
	if got := p.MetadataURL(); got != "https://example.com/.well-known/oauth-authorization-server/dbairlock" {
		t.Error(got)
	}
}

func TestOAuthSecrets(t *testing.T) {
	seen := map[string]bool{}
	for _, prefix := range []string{OAuthAccessPrefix, OAuthRefreshPrefix, OAuthCodePrefix} {
		for range 50 {
			s := randomSecret(prefix)
			if !wellFormedSecret(s, prefix) || seen[s] {
				t.Fatalf("%q", s)
			}
			seen[s] = true
		}
	}
	a := randomSecret(OAuthAccessPrefix)
	for _, bad := range []string{a[:len(a)-1], a + "x", OAuthRefreshPrefix + a[len(OAuthAccessPrefix):], a[:len(a)-1] + "-", ""} {
		if wellFormedSecret(bad, OAuthAccessPrefix) {
			t.Errorf("accepted %q", bad)
		}
	}
}
