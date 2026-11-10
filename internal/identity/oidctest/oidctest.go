// Package oidctest is a fake OpenID Connect provider for tests. It speaks
// enough of Google's protocol (discovery, authorization code with PKCE,
// RS256 ID tokens, JWKS) for identity to sign people in against it, and it
// can be told to misbehave so tests can prove each check.
//
// It is imported by tests only.
package oidctest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/alvaroibarguen/dbairlock/internal/platform/clock"
)

// Account is who the next authorization signs in as, and how the resulting
// ID token should be broken, if at all.
type Account struct {
	Sub           string
	Email         string
	Name          string
	HD            string
	EmailVerified bool

	// Faults.
	WrongNonce    bool // the ID token carries a different nonce
	WrongAudience bool // the ID token is for another client
	Expired       bool // the ID token expired an hour ago
	BadSignature  bool // the ID token is signed with a key not in the JWKS
	NoIDToken     bool // the token response has no id_token
}

// Provider is a running fake provider.
type Provider struct {
	Server       *httptest.Server
	Issuer       string
	ClientID     string
	ClientSecret string
	// CodeLifetime bounds how long an authorization code can be exchanged.
	CodeLifetime time.Duration
	Clock        clock.Clock

	key, otherKey *rsa.PrivateKey

	mu    sync.Mutex
	next  Account
	codes map[string]*grant
}

type grant struct {
	account   Account
	nonce     string
	challenge string
	redirect  string
	issued    time.Time
	used      bool
}

// New starts a provider. Close it with Close.
func New() *Provider {
	p := &Provider{
		ClientID: "test-client.apps.googleusercontent.com", ClientSecret: "test-client-secret",
		CodeLifetime: time.Minute, Clock: clock.Real{}, codes: map[string]*grant{},
		next: Account{Sub: "1001", Email: "ada@example.com", Name: "Ada", HD: "example.com", EmailVerified: true},
	}
	var err error
	if p.key, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		panic(err)
	}
	if p.otherKey, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("GET /authorize", p.authorize)
	mux.HandleFunc("POST /token", p.token)
	mux.HandleFunc("GET /jwks", p.jwks)
	p.Server = httptest.NewServer(mux)
	p.Issuer = p.Server.URL
	return p
}

// Close stops the provider.
func (p *Provider) Close() { p.Server.Close() }

// SignInAs sets the account the next authorizations sign in as.
func (p *Provider) SignInAs(a Account) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.next = a
}

func (p *Provider) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                p.Issuer,
		"authorization_endpoint":                p.Issuer + "/authorize",
		"token_endpoint":                        p.Issuer + "/token",
		"jwks_uri":                              p.Issuer + "/jwks",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
	})
}

// authorize approves at once as the configured account and redirects back
// with a code.
func (p *Provider) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != p.ClientID || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" {
		http.Error(w, "bad authorization request", http.StatusBadRequest)
		return
	}
	code := randomString()
	p.mu.Lock()
	p.codes[code] = &grant{
		account: p.next, nonce: q.Get("nonce"), challenge: q.Get("code_challenge"),
		redirect: q.Get("redirect_uri"), issued: p.Clock.Now(),
	}
	p.mu.Unlock()
	back, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	v := back.Query()
	v.Set("code", code)
	v.Set("state", q.Get("state"))
	back.RawQuery = v.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound) //nolint:gosec // a test provider redirects wherever the test asks
}

func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != p.ClientID || secret != p.ClientSecret {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
		return
	}
	p.mu.Lock()
	g := p.codes[r.PostForm.Get("code")]
	valid := g != nil && !g.used && p.Clock.Now().Sub(g.issued) < p.CodeLifetime &&
		g.redirect == r.PostForm.Get("redirect_uri") && challenge(r.PostForm.Get("code_verifier")) == g.challenge
	if g != nil {
		g.used = true
	}
	p.mu.Unlock()
	if !valid {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	resp := map[string]any{"access_token": randomString(), "token_type": "Bearer", "expires_in": 3600}
	if !g.account.NoIDToken {
		resp["id_token"] = p.idToken(g)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (p *Provider) idToken(g *grant) string {
	a := g.account
	key := p.key
	if a.BadSignature {
		key = p.otherKey
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		panic(err)
	}
	now := p.Clock.Now()
	aud, nonce, exp := p.ClientID, g.nonce, now.Add(time.Hour)
	if a.WrongAudience {
		aud = "someone-else.apps.googleusercontent.com"
	}
	if a.WrongNonce {
		nonce = "not-the-nonce"
	}
	if a.Expired {
		exp = now.Add(-time.Hour)
	}
	claims := map[string]any{
		"iss": p.Issuer, "sub": a.Sub, "aud": aud, "iat": now.Add(-time.Minute).Unix(), "exp": exp.Unix(),
		"nonce": nonce, "email": a.Email, "email_verified": a.EmailVerified, "name": a.Name,
	}
	if a.HD != "" {
		claims["hd"] = a.HD
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		panic(err)
	}
	return raw
}

func (p *Provider) jwks(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
		{Key: &p.key.PublicKey, KeyID: "k1", Algorithm: string(jose.RS256), Use: "sig"},
	}})
}

func challenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func randomString() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
