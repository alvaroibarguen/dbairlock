package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/identity"
	"github.com/alvaroibarguen/dbairlock/internal/store"
)

// OAuth sign-in for MCP clients: dbairlock as the authorization server of
// its own /mcp endpoint, driven over real HTTP.

const testRedirect = "http://127.0.0.1:33418/callback"

// oauthEnv is a set-up instance served over HTTP, whose public URL is the
// test server's address, with ada (admin) and bob (member) signed in.
type oauthEnv struct {
	t        *testing.T
	hs       *harness
	srv      *httptest.Server
	ada, bob *browser
	http     *http.Client
}

func newOAuthEnv(t *testing.T) *oauthEnv {
	t.Helper()
	hs := newHarness(t)
	ada := hs.completeSetup()
	bob := hs.browser("198.51.100.2")
	if r := bob.signIn(bobMember); r.Code != http.StatusFound {
		t.Fatalf("bob sign-in: %d %s", r.Code, r.Body)
	}
	srv := httptest.NewServer(hs.h)
	t.Cleanup(srv.Close)
	// Every OAuth state change wrote its audit event into an intact chain.
	t.Cleanup(func() {
		res, err := audit.NewVerifier(hs.app.store.Reader()).Verify(context.Background())
		if err != nil || !res.OK() {
			t.Errorf("audit verify: %v %+v", err, res.Problem)
		}
	})
	e := &oauthEnv{t: t, hs: hs, srv: srv, ada: ada, bob: bob, http: &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	e.setting("instance.public_url", srv.URL)
	return e
}

func (e *oauthEnv) setting(key string, value any) {
	e.t.Helper()
	if r := e.ada.do("PATCH", "/api/settings", map[string]any{"values": map[string]any{key: value}}); r.Code != http.StatusOK {
		e.t.Fatalf("set %s: %d %s", key, r.Code, r.Body)
	}
}

type reply struct {
	status int
	header http.Header
	body   map[string]any
	raw    string
}

func (r reply) str(k string) string { s, _ := r.body[k].(string); return s }

// send makes one request to the test server.
func (e *oauthEnv) send(method, path, contentType, body string, header ...string) reply {
	e.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, e.srv.URL+path, strings.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := e.http.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	out := reply{status: resp.StatusCode, header: resp.Header, raw: string(raw)}
	_ = json.Unmarshal(raw, &out.body)
	return out
}

func (e *oauthEnv) register(redirects ...string) reply {
	e.t.Helper()
	body, _ := json.Marshal(map[string]any{
		"client_name": "Claude Code (dbairlock)", "redirect_uris": redirects,
		"grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"},
		"token_endpoint_auth_method": "none",
	})
	return e.send("POST", "/oauth/register", "application/json", string(body))
}

func (e *oauthEnv) client() string {
	e.t.Helper()
	r := e.register(testRedirect)
	if r.status != http.StatusCreated || r.str("client_id") == "" {
		e.t.Fatalf("register: %d %s", r.status, r.raw)
	}
	return r.str("client_id")
}

// authorizeParams are a valid authorization request for client.
func (e *oauthEnv) authorizeParams(client, verifier string) url.Values {
	return url.Values{
		"response_type": {"code"}, "client_id": {client}, "redirect_uri": {testRedirect},
		"code_challenge": {oauth2.S256ChallengeFromVerifier(verifier)}, "code_challenge_method": {"S256"},
		"state": {"st-123"}, "scope": {"mcp offline_access"}, "resource": {e.srv.URL + "/mcp"},
	}
}

func (e *oauthEnv) authorize(q url.Values) *url.URL {
	e.t.Helper()
	r := e.send("GET", "/oauth/authorize?"+q.Encode(), "", "")
	if r.status != http.StatusFound {
		e.t.Fatalf("authorize: %d %s", r.status, r.raw)
	}
	u, err := url.Parse(r.header.Get("Location"))
	if err != nil {
		e.t.Fatal(err)
	}
	return u
}

// consent follows an authorization request to the consent page and decides
// it as who, returning where the browser is sent.
func (e *oauthEnv) consent(who *browser, q url.Values, approve bool) *url.URL {
	e.t.Helper()
	page := e.authorize(q)
	id := page.Query().Get("request")
	if page.Path != "/oauth/consent" || id == "" {
		e.t.Fatalf("authorize sent the browser to %s", page)
	}
	if r := who.do("GET", "/api/oauth/requests/"+id, nil); r.Code != http.StatusOK {
		e.t.Fatalf("consent request: %d %s", r.Code, r.Body)
	}
	r := who.do("POST", "/api/oauth/requests/"+id+"/decision", map[string]any{"approve": approve})
	if r.Code != http.StatusOK {
		e.t.Fatalf("decision: %d %s", r.Code, r.Body)
	}
	to, err := url.Parse(r.body["redirect_url"].(string))
	if err != nil {
		e.t.Fatal(err)
	}
	return to
}

func (e *oauthEnv) code(who *browser, client, verifier string) string {
	e.t.Helper()
	to := e.consent(who, e.authorizeParams(client, verifier), true)
	q := to.Query()
	if to.Scheme+"://"+to.Host+to.Path != testRedirect || q.Get("code") == "" || q.Get("state") != "st-123" || q.Get("iss") != e.srv.URL {
		e.t.Fatalf("approval redirect: %s", to)
	}
	return q.Get("code")
}

func (e *oauthEnv) token(form url.Values) reply {
	e.t.Helper()
	return e.send("POST", "/oauth/token", "application/x-www-form-urlencoded", form.Encode())
}

func (e *oauthEnv) exchange(client, code, verifier string) reply {
	e.t.Helper()
	return e.token(url.Values{
		"grant_type": {"authorization_code"}, "client_id": {client}, "code": {code},
		"redirect_uri": {testRedirect}, "code_verifier": {verifier}, "resource": {e.srv.URL + "/mcp"},
	})
}

// tokens runs the whole flow for who and returns the token response.
func (e *oauthEnv) tokens(who *browser) (client string, r reply) {
	e.t.Helper()
	client = e.client()
	verifier := oauth2.GenerateVerifier()
	r = e.exchange(client, e.code(who, client, verifier), verifier)
	if r.status != http.StatusOK || r.str("access_token") == "" || r.str("refresh_token") == "" {
		e.t.Fatalf("token: %d %s", r.status, r.raw)
	}
	return client, r
}

func (e *oauthEnv) refresh(client, rt string) reply {
	e.t.Helper()
	return e.token(url.Values{"grant_type": {"refresh_token"}, "client_id": {client}, "refresh_token": {rt}})
}

func (e *oauthEnv) mcpStatus(token string) int {
	e.t.Helper()
	return rawMCP(e.t, e.srv.URL, "Bearer "+token, initializeBody).StatusCode
}

func (e *oauthEnv) grants(who *browser, all bool) []any {
	e.t.Helper()
	path := "/api/oauth/grants"
	if all {
		path += "?all=true"
	}
	r := who.do("GET", path, nil)
	if r.Code != http.StatusOK {
		e.t.Fatalf("grants: %d %s", r.Code, r.Body)
	}
	return list(r.body["grants"])
}

// oauthAccess runs the OAuth flow for p through the identity module (no
// HTTP) and returns an access token. ctx carries p as the actor; the
// instance's public URL must be set.
func oauthAccess(ctx context.Context, t *testing.T, a *App, p identity.Principal) string {
	t.Helper()
	meta := identity.Meta{IP: "192.0.2.50"}
	c, err := a.identity.RegisterOAuthClient(context.Background(), identity.ClientRegistration{
		Name: "Claude Code (dbairlock)", RedirectURIs: []string{testRedirect},
	}, meta)
	if err != nil {
		t.Fatal(err)
	}
	v := oauth2.GenerateVerifier()
	res, err := a.identity.BeginAuthorization(context.Background(), identity.AuthorizeParams{
		ResponseType: "code", ClientID: c.ID, RedirectURI: testRedirect,
		CodeChallenge: oauth2.S256ChallengeFromVerifier(v), CodeChallengeMethod: "S256",
	}, meta)
	if err != nil || res.RequestID == "" {
		t.Fatalf("authorize: %v %+v", err, res)
	}
	to, err := a.identity.DecideAuthorization(ctx, p, res.RequestID, true)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(to)
	tr, err := a.identity.ExchangeOAuthToken(context.Background(), identity.TokenRequest{
		GrantType: "authorization_code", ClientID: c.ID, Code: u.Query().Get("code"),
		RedirectURI: testRedirect, CodeVerifier: v,
	}, meta)
	if err != nil {
		t.Fatal(err)
	}
	return tr.AccessToken
}

// TestOAuthEndToEnd drives the whole flow with the MCP go-sdk's own OAuth
// client: discovery from the 401, dynamic registration, authorization with
// a signed-in session and consent, the token exchange, tool calls, refresh,
// refresh-token reuse, revocation.
func TestOAuthEndToEnd(t *testing.T) {
	e := newOAuthEnv(t)

	// An unauthenticated request names the protected resource metadata.
	r := rawMCP(t, e.srv.URL, "", initializeBody)
	wantChallenge := `Bearer realm="dbairlock", resource_metadata="` + e.srv.URL + `/.well-known/oauth-protected-resource/mcp", scope="mcp"`
	if r.StatusCode != http.StatusUnauthorized || r.Header.Get("WWW-Authenticate") != wantChallenge {
		t.Fatalf("401: %d %q", r.StatusCode, r.Header.Get("WWW-Authenticate"))
	}

	var issued *oauth2.Token
	var consentSeen bool
	handler, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
		DynamicClientRegistrationConfig: &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{
			ClientName: "Claude Code (dbairlock)", RedirectURIs: []string{testRedirect},
			GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"},
			TokenEndpointAuthMethod: "none",
		}},
		RedirectURL:         testRedirect,
		RequestRefreshToken: true,
		AuthorizationCodeFetcher: func(_ context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			// The person's browser: the authorization URL, then the consent
			// page (here its API) while signed in as ada, then Allow.
			u, err := url.Parse(args.URL)
			if err != nil || !strings.HasPrefix(args.URL, e.srv.URL+"/oauth/authorize?") {
				return nil, errors.New("unexpected authorization URL " + args.URL)
			}
			q := u.Query()
			if q.Get("resource") != e.srv.URL+"/mcp" || q.Get("code_challenge_method") != "S256" ||
				!strings.Contains(q.Get("scope"), "mcp") {
				return nil, errors.New("authorization request lacks resource, PKCE or scope: " + args.URL)
			}
			page := e.authorize(q)
			id := page.Query().Get("request")
			got := e.ada.do("GET", "/api/oauth/requests/"+id, nil)
			if got.Code != http.StatusOK || got.body["client_name"] != "Claude Code (dbairlock)" ||
				got.body["redirect_host"] != "127.0.0.1:33418" {
				return nil, errors.New("consent request: " + got.Body.String())
			}
			consentSeen = true
			d := e.ada.do("POST", "/api/oauth/requests/"+id+"/decision", map[string]any{"approve": true})
			to, err := url.Parse(obj(d.body)["redirect_url"].(string))
			if err != nil {
				return nil, err
			}
			return &auth.AuthorizationResult{Code: to.Query().Get("code"), State: to.Query().Get("state"), Iss: to.Query().Get("iss")}, nil
		},
		NewTokenSource: func(ctx context.Context, cfg *oauth2.Config, tok *oauth2.Token) (oauth2.TokenSource, error) {
			issued = tok
			return cfg.TokenSource(ctx, tok), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "dbairlock-oauth-test", Version: "1"}, nil)
	sess, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: e.srv.URL + "/mcp", OAuthHandler: handler, DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatalf("connect with OAuth: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	if !consentSeen || issued == nil || !strings.HasPrefix(issued.AccessToken, identity.OAuthAccessPrefix) ||
		!strings.HasPrefix(issued.RefreshToken, identity.OAuthRefreshPrefix) {
		t.Fatalf("flow incomplete: consent %v, token %+v", consentSeen, issued != nil)
	}
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_connections", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("list_connections with an OAuth token: %v %+v", err, res)
	}

	// The tool call is audited as ada's agent, with the client's name.
	var called string
	for _, a := range e.hs.auditActions() {
		if strings.HasPrefix(a, "mcp.tool_called") {
			called = a
		}
	}
	if !strings.Contains(called, " mcp ") || !strings.Contains(called, `"agent":"Claude Code (dbairlock)"`) {
		t.Errorf("tool audit event: %q", called)
	}
	row := e.hs.app.store.Reader().QueryRowContext(context.Background(),
		`SELECT actor_email, subject_type FROM audit_events WHERE action = 'mcp.tool_called' ORDER BY seq DESC LIMIT 1`)
	var email, subject string
	if err := row.Scan(&email, &subject); err != nil || email != "ada@example.com" || subject != "oauth_grant" {
		t.Errorf("tool call actor %q subject %q: %v", email, subject, err)
	}

	// The grant is listed as a connected app, and acts like ada's API token.
	grants := e.grants(e.ada, false)
	if len(grants) != 1 || obj(grants[0])["client_name"] != "Claude Code (dbairlock)" || obj(grants[0])["owner_email"] != "ada@example.com" {
		t.Fatalf("grants = %v", grants)
	}
	p, err := e.hs.app.identity.AuthenticateOAuthToken(context.Background(), issued.AccessToken)
	if err != nil || p.Email != "ada@example.com" || p.APITokenID != obj(grants[0])["id"] || p.Via != "mcp" || !p.OAuth {
		t.Fatalf("principal %+v %v", p, err)
	}
	if _, err := e.hs.app.identity.CreateToken(context.Background(), p, "minted", 0); err == nil {
		t.Error("an OAuth principal created an API token")
	}

	// An hour later the access token is refused; the refresh token gets a
	// new pair (rotation), and the new access token works.
	e.hs.clock.Advance(identity.OAuthAccessLifetime + time.Second)
	if s := e.mcpStatus(issued.AccessToken); s != http.StatusUnauthorized {
		t.Errorf("expired access token: %d", s)
	}
	cfg := &oauth2.Config{ClientID: obj(grants[0])["client_id"].(string), Endpoint: oauth2.Endpoint{
		TokenURL: e.srv.URL + "/oauth/token", AuthStyle: oauth2.AuthStyleInParams,
	}}
	refreshed, err := cfg.TokenSource(context.Background(), &oauth2.Token{RefreshToken: issued.RefreshToken}).Token()
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if refreshed.RefreshToken == issued.RefreshToken || refreshed.AccessToken == issued.AccessToken {
		t.Fatal("refresh did not rotate")
	}
	if s := e.mcpStatus(refreshed.AccessToken); s != http.StatusOK {
		t.Fatalf("refreshed access token: %d", s)
	}

	// The old refresh token again: someone else holds it. The whole grant
	// is revoked, so the new tokens stop working too.
	if r := e.refresh(cfg.ClientID, issued.RefreshToken); r.status != http.StatusBadRequest || r.str("error") != "invalid_grant" {
		t.Fatalf("reused refresh token: %d %s", r.status, r.raw)
	}
	if s := e.mcpStatus(refreshed.AccessToken); s != http.StatusUnauthorized {
		t.Errorf("access token after refresh reuse: %d", s)
	}
	if r := e.refresh(cfg.ClientID, refreshed.RefreshToken); r.status != http.StatusBadRequest {
		t.Errorf("newest refresh token after reuse: %d %s", r.status, r.raw)
	}
	if !e.hs.hasAudit("oauth.grant_revoked mcp") || len(e.grants(e.ada, false)) != 0 {
		t.Error("reuse did not revoke the grant")
	}

	// Revocation (RFC 7009) of a refresh token ends its access token too.
	client2, tr := e.tokens(e.ada)
	if s := e.mcpStatus(tr.str("access_token")); s != http.StatusOK {
		t.Fatalf("second grant: %d", s)
	}
	rv := e.send("POST", "/oauth/revoke", "application/x-www-form-urlencoded",
		url.Values{"token": {tr.str("refresh_token")}, "client_id": {client2}}.Encode())
	if rv.status != http.StatusOK {
		t.Fatalf("revoke: %d %s", rv.status, rv.raw)
	}
	if s := e.mcpStatus(tr.str("access_token")); s != http.StatusUnauthorized {
		t.Errorf("access token after revocation: %d", s)
	}
	// Revoking an unknown token succeeds without saying anything.
	if rv := e.send("POST", "/oauth/revoke", "application/x-www-form-urlencoded", "token=nope"); rv.status != http.StatusOK {
		t.Errorf("unknown token revocation: %d", rv.status)
	}
}

func TestOAuthDiscovery(t *testing.T) {
	e := newOAuthEnv(t)
	base := e.srv.URL

	prm := e.send("GET", "/.well-known/oauth-protected-resource/mcp", "", "")
	if prm.status != http.StatusOK || prm.str("resource") != base+"/mcp" ||
		!slices.Equal(stringsOf(prm.body["authorization_servers"]), []string{base}) ||
		!slices.Equal(stringsOf(prm.body["scopes_supported"]), []string{"mcp"}) {
		t.Fatalf("resource metadata: %d %s", prm.status, prm.raw)
	}
	root := e.send("GET", "/.well-known/oauth-protected-resource", "", "")
	if root.status != http.StatusOK || root.str("resource") != base {
		t.Fatalf("root resource metadata: %d %s", root.status, root.raw)
	}
	if r := e.send("GET", "/.well-known/oauth-protected-resource/other", "", ""); r.status != http.StatusNotFound {
		t.Errorf("metadata of another resource: %d", r.status)
	}
	as := e.send("GET", "/.well-known/oauth-authorization-server", "", "")
	if as.status != http.StatusOK || as.str("issuer") != base || as.str("token_endpoint") != base+"/oauth/token" ||
		as.str("registration_endpoint") != base+"/oauth/register" || as.str("revocation_endpoint") != base+"/oauth/revoke" ||
		!slices.Equal(stringsOf(as.body["code_challenge_methods_supported"]), []string{"S256"}) ||
		!slices.Equal(stringsOf(as.body["token_endpoint_auth_methods_supported"]), []string{"none"}) ||
		as.body["authorization_response_iss_parameter_supported"] != true {
		t.Fatalf("server metadata: %d %s", as.status, as.raw)
	}
	if _, ok := as.body["jwks_uri"]; ok {
		t.Error("server metadata names a jwks_uri")
	}
	// The SDK's own discovery accepts the documents.
	if m, err := auth.GetAuthServerMetadata(context.Background(), base, http.DefaultClient); err != nil || m == nil || m.Issuer != base {
		t.Fatalf("SDK discovery: %v %+v", err, m)
	}
	if _, err := oauthex.GetProtectedResourceMetadata(context.Background(), base+"/.well-known/oauth-protected-resource/mcp", base+"/mcp", http.DefaultClient); err != nil {
		t.Fatalf("SDK resource metadata: %v", err)
	}

	// Turned off, or without a public URL: no metadata, no challenge.
	for _, c := range []struct {
		key   string
		value any
	}{{"mcp.oauth_enabled", false}, {"instance.public_url", ""}} {
		e.setting(c.key, c.value)
		for _, p := range []string{"/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-authorization-server"} {
			if r := e.send("GET", p, "", ""); r.status != http.StatusNotFound {
				t.Errorf("%s=%v: %s answered %d", c.key, c.value, p, r.status)
			}
		}
		if h := rawMCP(t, base, "", initializeBody).Header.Get("WWW-Authenticate"); h != `Bearer realm="dbairlock"` {
			t.Errorf("%s=%v: challenge %q", c.key, c.value, h)
		}
		if r := e.register(testRedirect); r.status != http.StatusForbidden || r.str("error") != "access_denied" {
			t.Errorf("%s=%v: registration %d %s", c.key, c.value, r.status, r.raw)
		}
		if r := e.send("GET", "/oauth/authorize?client_id=x", "", ""); r.header.Get("Location") != "/oauth/consent?error=unavailable" {
			t.Errorf("%s=%v: authorize sent to %q", c.key, c.value, r.header.Get("Location"))
		}
		e.setting("mcp.oauth_enabled", true)
		e.setting("instance.public_url", base)
	}
}

func stringsOf(v any) []string {
	var out []string
	for _, x := range list(v) {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

func TestOAuthRegistration(t *testing.T) {
	e := newOAuthEnv(t)
	r := e.register(testRedirect, "http://localhost/cb")
	if r.status != http.StatusCreated || !strings.HasPrefix(r.str("client_id"), "dbal_client_") ||
		r.str("token_endpoint_auth_method") != "none" || r.str("client_secret") != "" || r.str("client_name") != "Claude Code (dbairlock)" {
		t.Fatalf("register: %d %s", r.status, r.raw)
	}
	if !e.hs.hasAudit("oauth.client_registered system") {
		t.Error("registration not audited")
	}
	refused := []string{
		`{"redirect_uris":["https://example.com/callback"]}`,
		`{"redirect_uris":["http://localhost.evil.com/callback"]}`,
		`{"redirect_uris":["http://localhost@evil.com/callback"]}`,
		`{"redirect_uris":["http://localhost:8080/callback#x"]}`,
		`{"redirect_uris":["http://localhost:8080/%2e%2e/callback"]}`,
		`{"redirect_uris":["https://127.0.0.1/callback"]}`,
		`{"redirect_uris":[]}`,
		`{"client_name":"x"}`,
		`{"redirect_uris":["http://localhost/a","http://localhost/b","http://localhost/c","http://localhost/d","http://localhost/e","http://localhost/f"]}`,
		`{"redirect_uris":["http://localhost/cb"],"token_endpoint_auth_method":"client_secret_basic"}`,
		`{"redirect_uris":["http://localhost/cb"],"grant_types":["client_credentials"]}`,
		`{"redirect_uris":["http://localhost/cb"],"response_types":["token"]}`,
		`{"redirect_uris":["http://localhost/cb"],"client_name":"` + strings.Repeat("x", 101) + `"}`,
		`{"redirect_uris":["http://localhost/cb"],"client_name":"bad\u0007name"}`,
		`not json`,
	}
	for _, body := range refused {
		e.hs.clock.Advance(time.Minute) // past the registration limit
		r := e.send("POST", "/oauth/register", "application/json", body)
		if r.status != http.StatusBadRequest || (r.str("error") != "invalid_redirect_uri" && r.str("error") != "invalid_client_metadata") {
			t.Errorf("%s: %d %s", body, r.status, r.raw)
		}
	}
	if r := e.send("POST", "/oauth/register", "text/plain", `{"redirect_uris":["http://localhost/cb"]}`); r.status != http.StatusBadRequest {
		t.Errorf("text/plain registration: %d", r.status)
	}

	t.Run("rate limited", func(t *testing.T) {
		var last reply
		for range 12 {
			last = e.register(testRedirect)
		}
		if last.status != http.StatusTooManyRequests || last.header.Get("Retry-After") == "" {
			t.Errorf("12 registrations at once: %d %s", last.status, last.raw)
		}
	})

	t.Run("unused clients are pruned and capped", func(t *testing.T) {
		e.hs.clock.Advance(identity.OAuthUnusedClientLifetime + time.Minute)
		e.hs.clock.Advance(time.Hour)
		_ = e.client()
		if !e.hs.hasAudit("oauth.clients_pruned") {
			t.Fatal("no prune event")
		}
		var n int
		if err := e.hs.app.store.Reader().QueryRowContext(context.Background(), `SELECT count(*) FROM oauth_clients`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("clients after prune: %d %v", n, err)
		}
		// Fill the cap directly, then one more registration is refused.
		if err := e.hs.app.store.Tx(context.Background(), func(tx *store.Tx) error {
			for i := range identity.MaxUnusedOAuthClients {
				if _, err := tx.ExecContext(context.Background(),
					`INSERT INTO oauth_clients (id, name, redirect_uris, created_at) VALUES (?, 'x', '[]', ?)`,
					"filler"+itoa(i), e.hs.clock.Now().UnixMilli()); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		e.hs.clock.Advance(time.Minute)
		if r := e.register(testRedirect); r.status != http.StatusServiceUnavailable || r.str("error") != "temporarily_unavailable" {
			t.Errorf("over the cap: %d %s", r.status, r.raw)
		}
	})
}

func TestOAuthAuthorization(t *testing.T) {
	e := newOAuthEnv(t)
	client := e.client()
	verifier := oauth2.GenerateVerifier()

	t.Run("untrusted client or redirect: shown on the consent page, not redirected", func(t *testing.T) {
		for name, edit := range map[string]func(url.Values){
			"unknown client":      func(q url.Values) { q.Set("client_id", "dbal_client_nope") },
			"no client":           func(q url.Values) { q.Del("client_id") },
			"other redirect path": func(q url.Values) { q.Set("redirect_uri", "http://127.0.0.1:33418/other") },
			"other redirect host": func(q url.Values) { q.Set("redirect_uri", "http://localhost:33418/callback") },
			"remote redirect":     func(q url.Values) { q.Set("redirect_uri", "https://evil.example/callback") },
			"no redirect":         func(q url.Values) { q.Del("redirect_uri") },
			"repeated parameter":  func(q url.Values) { q.Add("client_id", client) },
		} {
			q := e.authorizeParams(client, verifier)
			edit(q)
			to := e.authorize(q)
			if to.Path != "/oauth/consent" || to.Query().Get("error") == "" || to.Host != "" {
				t.Errorf("%s: sent to %s", name, to)
			}
		}
	})

	t.Run("other errors go back to the client with state and iss", func(t *testing.T) {
		for name, c := range map[string]struct {
			edit func(url.Values)
			code string
		}{
			"no PKCE":          {func(q url.Values) { q.Del("code_challenge") }, "invalid_request"},
			"plain PKCE":       {func(q url.Values) { q.Set("code_challenge_method", "plain") }, "invalid_request"},
			"bad challenge":    {func(q url.Values) { q.Set("code_challenge", "short") }, "invalid_request"},
			"token flow":       {func(q url.Values) { q.Set("response_type", "token") }, "unsupported_response_type"},
			"other resource":   {func(q url.Values) { q.Set("resource", "https://evil.example/mcp") }, "invalid_target"},
			"two resources":    {func(q url.Values) { q.Add("resource", e.srv.URL) }, "invalid_target"},
			"resource+query":   {func(q url.Values) { q.Set("resource", e.srv.URL+"/mcp?x=1") }, "invalid_target"},
			"long state":       {func(q url.Values) { q.Set("state", strings.Repeat("s", 1025)) }, "invalid_request"},
			"another port, ok": {func(q url.Values) { q.Set("redirect_uri", "http://127.0.0.1:40001/callback") }, ""},
		} {
			q := e.authorizeParams(client, verifier)
			c.edit(q)
			to := e.authorize(q)
			if c.code == "" {
				if to.Path != "/oauth/consent" || to.Query().Get("request") == "" {
					t.Errorf("%s: sent to %s", name, to)
				}
				continue
			}
			got := to.Query()
			if to.Scheme+"://"+to.Host+to.Path != testRedirect || got.Get("error") != c.code ||
				got.Get("iss") != e.srv.URL || (len(q.Get("state")) <= 1024 && got.Get("state") != "st-123") {
				t.Errorf("%s: sent to %s", name, to)
			}
		}
	})

	t.Run("the consent page needs a session", func(t *testing.T) {
		id := e.authorize(e.authorizeParams(client, verifier)).Query().Get("request")
		anon := e.hs.browser("198.51.100.9")
		if r := anon.do("GET", "/api/oauth/requests/"+id, nil); r.Code != http.StatusUnauthorized {
			t.Errorf("anonymous consent request: %d", r.Code)
		}
		if r := anon.do("POST", "/api/oauth/requests/"+id+"/decision", map[string]any{"approve": true}); r.Code != http.StatusUnauthorized {
			t.Errorf("anonymous decision: %d", r.Code)
		}
		// Cross-origin decisions are refused (a site cannot approve for you).
		e.ada.header.Set("Sec-Fetch-Site", "cross-site")
		r := e.ada.do("POST", "/api/oauth/requests/"+id+"/decision", map[string]any{"approve": true})
		e.ada.header.Del("Sec-Fetch-Site")
		if r.Code != http.StatusForbidden || r.errCode() != "cross_origin" {
			t.Errorf("cross-site decision: %d %s", r.Code, r.Body)
		}
	})

	t.Run("deny", func(t *testing.T) {
		to := e.consent(e.ada, e.authorizeParams(client, verifier), false)
		if to.Query().Get("error") != "access_denied" || to.Query().Get("state") != "st-123" || to.Query().Get("code") != "" {
			t.Fatalf("denied: %s", to)
		}
	})

	t.Run("a request is decided once and expires", func(t *testing.T) {
		id := e.authorize(e.authorizeParams(client, verifier)).Query().Get("request")
		if r := e.ada.do("POST", "/api/oauth/requests/"+id+"/decision", map[string]any{"approve": false}); r.Code != http.StatusOK {
			t.Fatalf("decision: %d", r.Code)
		}
		if r := e.ada.do("POST", "/api/oauth/requests/"+id+"/decision", map[string]any{"approve": true}); r.Code != http.StatusNotFound {
			t.Errorf("second decision: %d", r.Code)
		}
		id = e.authorize(e.authorizeParams(client, verifier)).Query().Get("request")
		e.hs.clock.Advance(identity.OAuthRequestLifetime)
		if r := e.ada.do("GET", "/api/oauth/requests/"+id, nil); r.Code != http.StatusNotFound {
			t.Errorf("expired request: %d", r.Code)
		}
	})
}

func TestOAuthCodes(t *testing.T) {
	e := newOAuthEnv(t)
	client := e.client()
	other := e.client()

	t.Run("single use: a second redemption revokes the grant", func(t *testing.T) {
		v := oauth2.GenerateVerifier()
		code := e.code(e.ada, client, v)
		first := e.exchange(client, code, v)
		if first.status != http.StatusOK || first.str("token_type") != "Bearer" || first.body["expires_in"] != float64(3600) ||
			first.str("scope") != "mcp" || first.header.Get("Cache-Control") != "no-store" {
			t.Fatalf("exchange: %d %s", first.status, first.raw)
		}
		if r := e.exchange(client, code, v); r.status != http.StatusBadRequest || r.str("error") != "invalid_grant" {
			t.Fatalf("second exchange: %d %s", r.status, r.raw)
		}
		if s := e.mcpStatus(first.str("access_token")); s != http.StatusUnauthorized {
			t.Errorf("token issued from a reused code still works: %d", s)
		}
		if !e.hs.hasAudit(`oauth.grant_revoked mcp {"client_id":"` + client) {
			t.Error("code reuse not audited")
		}
	})

	t.Run("bound to the verifier, redirect URI, client and resource", func(t *testing.T) {
		v := oauth2.GenerateVerifier()
		code := e.code(e.ada, client, v)
		for name, form := range map[string]url.Values{
			"wrong verifier":   {"code_verifier": {oauth2.GenerateVerifier()}},
			"no verifier":      {"code_verifier": {""}},
			"other redirect":   {"redirect_uri": {"http://127.0.0.1:40000/callback"}},
			"other client":     {"client_id": {other}},
			"other resource":   {"resource": {"https://evil.example/mcp"}},
			"secret sent":      {"client_secret": {"x"}},
			"unknown grant":    {"grant_type": {"password"}},
			"malformed code":   {"code": {"dbal_oac_short"}},
			"two codes":        {"code": {code, code}},
			"no client":        {"client_id": {""}},
			"refresh as code":  {"code": {strings.Replace(code, identity.OAuthCodePrefix, identity.OAuthRefreshPrefix, 1)}},
			"implicit grant":   {"grant_type": {"implicit"}},
			"client creds":     {"grant_type": {"client_credentials"}},
			"empty grant type": {"grant_type": {""}},
		} {
			f := url.Values{
				"grant_type": {"authorization_code"}, "client_id": {client}, "code": {code},
				"redirect_uri": {testRedirect}, "code_verifier": {v}, "resource": {e.srv.URL + "/mcp"},
			}
			for k, vals := range form {
				f[k] = vals
			}
			if r := e.token(f); r.status < 400 || r.status >= 500 || r.str("access_token") != "" || r.str("error") == "" {
				t.Errorf("%s: %d %s", name, r.status, r.raw)
			}
		}
		// None of those consumed the code.
		if r := e.exchange(client, code, v); r.status != http.StatusOK {
			t.Fatalf("valid exchange after the failures: %d %s", r.status, r.raw)
		}
	})

	t.Run("expires after a minute", func(t *testing.T) {
		v := oauth2.GenerateVerifier()
		code := e.code(e.ada, client, v)
		e.hs.clock.Advance(identity.OAuthCodeLifetime)
		if r := e.exchange(client, code, v); r.status != http.StatusBadRequest || r.str("error") != "invalid_grant" {
			t.Errorf("expired code: %d %s", r.status, r.raw)
		}
	})

	t.Run("the instance URL as resource is the same resource", func(t *testing.T) {
		v := oauth2.GenerateVerifier()
		q := e.authorizeParams(client, v)
		q.Set("resource", e.srv.URL)
		to := e.consent(e.ada, q, true)
		r := e.token(url.Values{
			"grant_type": {"authorization_code"}, "client_id": {client}, "code": {to.Query().Get("code")},
			"redirect_uri": {testRedirect}, "code_verifier": {v}, "resource": {e.srv.URL},
		})
		if r.status != http.StatusOK || e.mcpStatus(r.str("access_token")) != http.StatusOK {
			t.Errorf("origin resource: %d %s", r.status, r.raw)
		}
	})

	t.Run("approving again replaces the earlier grant", func(t *testing.T) {
		v := oauth2.GenerateVerifier()
		r1 := e.exchange(client, e.code(e.ada, client, v), v)
		v2 := oauth2.GenerateVerifier()
		r2 := e.exchange(client, e.code(e.ada, client, v2), v2)
		if r1.status != http.StatusOK || r2.status != http.StatusOK {
			t.Fatal("exchanges failed")
		}
		if e.mcpStatus(r1.str("access_token")) != http.StatusUnauthorized || e.mcpStatus(r2.str("access_token")) != http.StatusOK {
			t.Error("a second approval of the same client did not replace the first grant")
		}
	})
}

func TestOAuthResourceServer(t *testing.T) {
	e := newOAuthEnv(t)
	_, ada := e.tokens(e.ada)
	bobClient, bob := e.tokens(e.bob)
	access := ada.str("access_token")

	t.Run("a refresh token is not an access token", func(t *testing.T) {
		if s := e.mcpStatus(ada.str("refresh_token")); s != http.StatusUnauthorized {
			t.Errorf("refresh token as bearer: %d", s)
		}
		if s := e.mcpStatus(access[:len(access)-1] + "x"); s != http.StatusUnauthorized {
			t.Errorf("altered token: %d", s)
		}
	})

	t.Run("turning OAuth off refuses OAuth tokens, not API tokens", func(t *testing.T) {
		pat := e.ada.do("POST", "/api/tokens", map[string]any{"name": "ci"})
		secret, _ := pat.body["secret"].(string)
		e.setting("mcp.oauth_enabled", false)
		if s := e.mcpStatus(access); s != http.StatusUnauthorized {
			t.Errorf("OAuth token while disabled: %d", s)
		}
		if r := e.refresh(bobClient, bob.str("refresh_token")); r.status != http.StatusForbidden {
			t.Errorf("refresh while disabled: %d %s", r.status, r.raw)
		}
		if s := e.mcpStatus(secret); s != http.StatusOK {
			t.Errorf("API token while OAuth is off: %d", s)
		}
		e.setting("mcp.oauth_enabled", true)
		if s := e.mcpStatus(access); s != http.StatusOK {
			t.Errorf("OAuth token after re-enabling: %d", s)
		}
	})

	t.Run("a token is bound to the public URL it was issued for", func(t *testing.T) {
		e.setting("instance.public_url", "https://moved.example.com")
		if s := e.mcpStatus(access); s != http.StatusUnauthorized {
			t.Errorf("token for the old URL: %d", s)
		}
		e.setting("instance.public_url", e.srv.URL)
		if s := e.mcpStatus(access); s != http.StatusOK {
			t.Errorf("token after restoring the URL: %d", s)
		}
	})

	t.Run("connected apps: own, all for admins, revoke", func(t *testing.T) {
		if g := e.grants(e.bob, false); len(g) != 1 || obj(g[0])["owner_email"] != "bob@example.com" {
			t.Fatalf("bob's apps: %v", g)
		}
		if r := e.bob.do("GET", "/api/oauth/grants?all=true", nil); r.Code != http.StatusForbidden {
			t.Errorf("member listing everyone's apps: %d", r.Code)
		}
		all := e.grants(e.ada, true)
		if len(all) != 2 {
			t.Fatalf("all apps: %v", all)
		}
		var adaGrant, bobGrant string
		for _, g := range all {
			if obj(g)["owner_email"] == "ada@example.com" {
				adaGrant = obj(g)["id"].(string)
			} else {
				bobGrant = obj(g)["id"].(string)
			}
		}
		if r := e.bob.do("DELETE", "/api/oauth/grants/"+adaGrant, nil); r.Code != http.StatusNotFound {
			t.Errorf("member revoking another's app: %d", r.Code)
		}
		if r := e.ada.do("DELETE", "/api/oauth/grants/"+bobGrant, nil); r.Code != http.StatusNoContent {
			t.Fatalf("admin revoking bob's app: %d %s", r.Code, r.Body)
		}
		if s := e.mcpStatus(bob.str("access_token")); s != http.StatusUnauthorized {
			t.Errorf("revoked app's token: %d", s)
		}
		if r := e.refresh(bobClient, bob.str("refresh_token")); r.status != http.StatusBadRequest {
			t.Errorf("revoked app's refresh token: %d", r.status)
		}
		if r := e.ada.do("DELETE", "/api/oauth/grants/"+bobGrant, nil); r.Code != http.StatusNotFound {
			t.Errorf("second revocation: %d", r.Code)
		}
	})

	t.Run("removing a user revokes their apps", func(t *testing.T) {
		_, b := e.tokens(e.bob)
		me := e.bob.do("GET", "/api/me", nil)
		bobID := obj(me.body["user"])["id"].(string)
		if r := e.ada.do("POST", "/api/users/"+bobID+"/remove", nil); r.Code != http.StatusOK {
			t.Fatalf("remove: %d %s", r.Code, r.Body)
		}
		if s := e.mcpStatus(b.str("access_token")); s != http.StatusUnauthorized {
			t.Errorf("removed user's OAuth token: %d", s)
		}
		if !e.hs.hasAudit(`oauth.grant_revoked web`) {
			t.Error("removal did not revoke the grant")
		}
		var reason string
		if err := e.hs.app.store.Reader().QueryRowContext(context.Background(),
			`SELECT revoked_reason FROM oauth_grants WHERE user_id = ? ORDER BY created_at DESC LIMIT 1`, bobID).Scan(&reason); err != nil || reason != identity.RevokedUserRemoved {
			t.Errorf("revoked reason %q %v", reason, err)
		}
	})
}
