package identity

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/identity/identitydb"
	"github.com/alvaroibarguen/dbairlock/internal/platform/ratelimit"
	"github.com/alvaroibarguen/dbairlock/internal/settings"
	"github.com/alvaroibarguen/dbairlock/internal/store"
)

// OAuth sign-in for MCP clients. dbairlock is the authorization server of
// its own MCP endpoint: a client registers itself (RFC 7591), sends the
// person's browser to the authorization endpoint, the person approves it on
// the consent page while signed in with Google, and the client exchanges the
// code (PKCE S256, RFC 7636) for an access token bound to the MCP endpoint
// (RFC 8707) and a rotating refresh token. An access token then acts exactly
// like an API token of that person.
//
// Codes, access tokens and refresh tokens are 256-bit random values, stored
// as SHA-256 hashes only and never logged or audited.

// Prefixes of the OAuth secrets, so secret scanners can find leaked ones
// and the bearer middleware knows which kind of token it was given.
const (
	OAuthAccessPrefix  = "dbal_oat_" //nolint:gosec // a public prefix, not a credential
	OAuthRefreshPrefix = "dbal_ort_" //nolint:gosec // a public prefix, not a credential
	OAuthCodePrefix    = "dbal_oac_" //nolint:gosec // a public prefix, not a credential
	oauthClientPrefix  = "dbal_client_"
)

// OAuthScope is the one scope: the MCP tools, exactly what an API token
// allows. offline_access is accepted and advertised so clients ask for a
// refresh token, which is always issued.
const (
	OAuthScope        = "mcp"
	OAuthScopeOffline = "offline_access"
)

// Lifetimes and bounds. Constants, not settings: they are the protocol's
// safety margins (docs/configuration.md lists them).
const (
	OAuthCodeLifetime    = time.Minute
	OAuthAccessLifetime  = time.Hour
	OAuthRefreshLifetime = 30 * 24 * time.Hour
	// OAuthRequestLifetime is how long a person has to decide on the
	// consent page.
	OAuthRequestLifetime = 10 * time.Minute
	// OAuthUnusedClientLifetime: a registered client that never led to an
	// approval is deleted after this long.
	OAuthUnusedClientLifetime = 24 * time.Hour
	// MaxUnusedOAuthClients caps registered clients that have not been
	// approved by anyone yet.
	MaxUnusedOAuthClients = 200
	maxPendingRequests    = 1000
	maxRedirectURIs       = 5
	maxClientName         = 100
	maxOAuthState         = 1024
	oauthTouchEvery       = time.Minute
)

// Revocation reasons recorded on a grant and in its audit event.
const (
	RevokedByUser       = "revoked"
	RevokedByClient     = "client_revoked"
	RevokedSuperseded   = "superseded"
	RevokedRefreshReuse = "refresh_token_reuse"
	RevokedCodeReuse    = "code_reuse"
	RevokedUserRemoved  = "user_removed"
)

// ErrOAuthUnavailable means OAuth sign-in is turned off (mcp.oauth_enabled)
// or cannot work because instance.public_url is not set.
var ErrOAuthUnavailable = errors.New("identity: OAuth sign-in is unavailable")

// OAuthError is a protocol error with an RFC 6749 error code and a
// description safe to send to the client.
type OAuthError struct {
	Code        string
	Description string
}

func (e *OAuthError) Error() string { return "identity: oauth " + e.Code + ": " + e.Description }

func oauthErr(code, desc string) *OAuthError { return &OAuthError{Code: code, Description: desc} }

// OAuthServer is where the authorization server lives, derived from
// instance.public_url.
type OAuthServer struct {
	// Issuer is the instance URL (RFC 8414 issuer identifier).
	Issuer string
	// Resource is the MCP endpoint, the canonical resource of every token.
	Resource string
}

// wellKnown is the RFC 8414 / RFC 9728 well-known URL of name for the
// identifier id: the well-known segment goes between the host and the path.
func wellKnown(id, name string) string {
	u, err := url.Parse(id)
	if err != nil {
		return ""
	}
	path := strings.TrimRight(u.Path, "/")
	u.Path = "/.well-known/" + name + path
	u.RawPath = ""
	return u.String()
}

// ResourceMetadataURL is where the protected resource metadata of the MCP
// endpoint is served (RFC 9728 section 3.1, with the resource's path).
func (o OAuthServer) ResourceMetadataURL() string {
	return wellKnown(o.Resource, "oauth-protected-resource")
}

// MetadataURL is where the authorization server metadata is served (RFC
// 8414 section 3.1).
func (o OAuthServer) MetadataURL() string {
	return wellKnown(o.Issuer, "oauth-authorization-server")
}

// OAuthServer returns the issuer and resource, or ErrOAuthUnavailable if
// OAuth is turned off or no public URL is set. Fail closed: nothing about
// OAuth works without both.
func (s *Service) OAuthServer() (OAuthServer, error) {
	if !settings.Get(s.opts.Settings, settings.MCPOAuthEnabled) {
		return OAuthServer{}, ErrOAuthUnavailable
	}
	issuer, ok := canonicalURL(settings.Get(s.opts.Settings, settings.InstancePublicURL))
	if !ok {
		return OAuthServer{}, ErrOAuthUnavailable
	}
	return OAuthServer{Issuer: issuer, Resource: issuer + "/mcp"}, nil
}

// oauthState is the in-memory part of the authorization server: rate
// limits and the authorization requests waiting for a decision. A restart
// forgets both; a person then starts again from their client.
type oauthState struct {
	mu      sync.Mutex
	pending map[string]*pendingAuthorization
}

type pendingAuthorization struct {
	id          string
	clientID    string
	clientName  string
	clientSince time.Time
	redirectURI string
	challenge   string
	state       string
	scope       string
	expires     time.Time
}

func randomSecret(prefix string) string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	body := new(big.Int).SetBytes(b).Text(62)
	return prefix + strings.Repeat("0", tokenBodyLen-len(body)) + body
}

func wellFormedSecret(raw, prefix string) bool {
	if len(raw) != len(prefix)+tokenBodyLen || !strings.HasPrefix(raw, prefix) {
		return false
	}
	for _, r := range raw[len(prefix):] {
		digit, lower, upper := '0' <= r && r <= '9', 'a' <= r && r <= 'z', 'A' <= r && r <= 'Z'
		if !digit && !lower && !upper {
			return false
		}
	}
	return true
}

func (s *Service) oauthAllow(l *ratelimit.Limiter, ip string) error {
	if ok, wait := l.Allow(ip); !ok {
		return &RateLimitedError{RetryAfter: wait}
	}
	return nil
}

// ---- Dynamic client registration (RFC 7591) ----

// ClientRegistration is the client metadata a registration request sends.
type ClientRegistration struct {
	Name                    string
	RedirectURIs            []string
	GrantTypes              []string
	ResponseTypes           []string
	TokenEndpointAuthMethod string
}

// OAuthClient is a registered client.
type OAuthClient struct {
	ID           string
	Name         string
	RedirectURIs []string
	CreatedAt    time.Time
}

// RegisterOAuthClient registers a public client. Only loopback redirect URIs
// are accepted; the client gets no secret. Clients that nobody approves are
// deleted after a day, and at most MaxUnusedOAuthClients may wait at once.
func (s *Service) RegisterOAuthClient(ctx context.Context, reg ClientRegistration, meta Meta) (OAuthClient, error) {
	if _, err := s.OAuthServer(); err != nil {
		return OAuthClient{}, err
	}
	if err := s.oauthAllow(s.oauthRegisterLimit, meta.IP); err != nil {
		return OAuthClient{}, err
	}
	if len(reg.RedirectURIs) == 0 || len(reg.RedirectURIs) > maxRedirectURIs {
		return OAuthClient{}, oauthErr("invalid_redirect_uri",
			fmt.Sprintf("Register 1 to %d redirect URIs.", maxRedirectURIs))
	}
	for _, u := range reg.RedirectURIs {
		if _, ok := parseLoopbackRedirect(u); !ok {
			return OAuthClient{}, oauthErr("invalid_redirect_uri",
				"Redirect URIs must be http://127.0.0.1, http://localhost or http://[::1] with an optional port and a plain path (RFC 8252).")
		}
	}
	if m := reg.TokenEndpointAuthMethod; m != "" && m != "none" {
		return OAuthClient{}, oauthErr("invalid_client_metadata",
			"Only public clients are supported: token_endpoint_auth_method must be none.")
	}
	for _, g := range reg.GrantTypes {
		if g != "authorization_code" && g != "refresh_token" {
			return OAuthClient{}, oauthErr("invalid_client_metadata",
				"Only the authorization_code and refresh_token grant types are supported.")
		}
	}
	for _, r := range reg.ResponseTypes {
		if r != "code" {
			return OAuthClient{}, oauthErr("invalid_client_metadata", "Only the code response type is supported.")
		}
	}
	name := strings.Join(strings.Fields(reg.Name), " ")
	if strings.ContainsFunc(name, unicode.IsControl) || len([]rune(name)) > maxClientName {
		return OAuthClient{}, oauthErr("invalid_client_metadata",
			fmt.Sprintf("client_name must be at most %d characters without control characters.", maxClientName))
	}
	if name == "" {
		name = "Unnamed MCP client"
	}
	uris, err := json.Marshal(reg.RedirectURIs)
	if err != nil {
		return OAuthClient{}, fmt.Errorf("identity: encode redirect URIs: %w", err)
	}
	now := s.now()
	c := OAuthClient{
		ID: oauthClientPrefix + RandomToken(), Name: name,
		RedirectURIs: slices.Clone(reg.RedirectURIs), CreatedAt: now.UTC(),
	}
	ctx = systemCtx(ctx, meta.IP)
	err = s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		q := identitydb.New(tx)
		if err := s.pruneOAuthClients(ctx, tx, q, now); err != nil {
			return err
		}
		n, err := q.CountUnusedOAuthClients(ctx)
		if err != nil {
			return fmt.Errorf("identity: count clients: %w", err)
		}
		if n >= MaxUnusedOAuthClients {
			return oauthErr("temporarily_unavailable",
				"Too many clients are waiting for approval. Try again later.")
		}
		if err := q.InsertOAuthClient(ctx, identitydb.InsertOAuthClientParams{
			ID: c.ID, Name: c.Name, RedirectUris: string(uris), CreatedAt: ms(now), CreatedIp: meta.IP,
		}); err != nil {
			return fmt.Errorf("identity: register client: %w", err)
		}
		return s.opts.Audit.Record(ctx, tx, audit.Event{
			Action: audit.OAuthClientRegistered, SubjectType: "oauth_client", SubjectID: c.ID,
			Payload: map[string]any{"client_name": c.Name, "redirect_uris": c.RedirectURIs},
		})
	})
	if err != nil {
		return OAuthClient{}, err
	}
	return c, nil
}

// pruneOAuthClients deletes clients that registered more than a day ago and
// were never approved, inside tx, with one audit event.
func (s *Service) pruneOAuthClients(ctx context.Context, tx *store.Tx, q *identitydb.Queries, now time.Time) error {
	n, err := q.DeleteUnusedOAuthClients(ctx, ms(now.Add(-OAuthUnusedClientLifetime)))
	if err != nil {
		return fmt.Errorf("identity: prune clients: %w", err)
	}
	if n == 0 {
		return nil
	}
	return s.opts.Audit.Record(ctx, tx, audit.Event{
		Action: audit.OAuthClientsPruned, SubjectType: "oauth_client",
		Payload: map[string]any{"deleted": n},
	})
}

func clientFromRow(r identitydb.OauthClient) (OAuthClient, error) {
	var uris []string
	if err := json.Unmarshal([]byte(r.RedirectUris), &uris); err != nil {
		return OAuthClient{}, fmt.Errorf("identity: decode redirect URIs: %w", err)
	}
	return OAuthClient{ID: r.ID, Name: r.Name, RedirectURIs: uris, CreatedAt: time.UnixMilli(r.CreatedAt).UTC()}, nil
}

// ---- Authorization endpoint ----

// AuthorizeParams are the query parameters of an authorization request.
type AuthorizeParams struct {
	ResponseType        string
	ClientID            string
	RedirectURI         string
	CodeChallenge       string
	CodeChallengeMethod string
	State               string
	Scope               string
	Resources           []string
}

// AuthorizeResult is what the authorization endpoint does next: show the
// consent page for RequestID, or send the browser back to the client with
// an error (Redirect).
type AuthorizeResult struct {
	RequestID string
	Redirect  string
}

// BeginAuthorization validates an authorization request. An error means
// the client or its redirect URI cannot be trusted, so the browser must not
// be sent back to it: the transport shows the error itself. Errors found
// after the redirect URI is known to be the client's go back to the client
// in Redirect, with state and iss.
func (s *Service) BeginAuthorization(ctx context.Context, p AuthorizeParams, meta Meta) (AuthorizeResult, error) {
	srv, err := s.OAuthServer()
	if err != nil {
		return AuthorizeResult{}, err
	}
	if err := s.oauthAllow(s.oauthAuthorizeLimit, meta.IP); err != nil {
		return AuthorizeResult{}, err
	}
	row, err := identitydb.New(s.opts.Store.Reader()).GetOAuthClient(ctx, p.ClientID)
	if errors.Is(err, sql.ErrNoRows) || p.ClientID == "" {
		return AuthorizeResult{}, oauthErr("invalid_client", "This client is not registered. Start again from your MCP client.")
	}
	if err != nil {
		return AuthorizeResult{}, fmt.Errorf("identity: load client: %w", err)
	}
	client, err := clientFromRow(row)
	if err != nil {
		return AuthorizeResult{}, err
	}
	if !redirectMatches(client.RedirectURIs, p.RedirectURI) {
		return AuthorizeResult{}, oauthErr("invalid_redirect_uri", "The redirect URI is not one this client registered.")
	}
	back := func(code, desc string) (AuthorizeResult, error) {
		return AuthorizeResult{Redirect: authorizationResponse(p.RedirectURI, srv.Issuer, p.State, url.Values{
			"error": {code}, "error_description": {desc},
		})}, nil
	}
	switch {
	case len(p.State) > maxOAuthState:
		return back("invalid_request", "state is too long.")
	case p.ResponseType != "code":
		return back("unsupported_response_type", "Only response_type=code is supported.")
	case p.CodeChallengeMethod != "S256" || !validChallenge(p.CodeChallenge):
		return back("invalid_request", "PKCE is required: send code_challenge with code_challenge_method=S256.")
	case len(p.Resources) > 1 || (len(p.Resources) == 1 && !resourceMatches(p.Resources[0], srv.Issuer, srv.Resource)):
		return back("invalid_target", "The resource must be this server's MCP endpoint, "+srv.Resource+".")
	}
	now := s.now()
	req := &pendingAuthorization{
		id: RandomToken(), clientID: client.ID, clientName: client.Name, clientSince: client.CreatedAt,
		redirectURI: p.RedirectURI, challenge: p.CodeChallenge, state: p.State, scope: grantedScope(p.Scope),
		expires: now.Add(OAuthRequestLifetime),
	}
	s.oauth.mu.Lock()
	defer s.oauth.mu.Unlock()
	for id, r := range s.oauth.pending {
		if !now.Before(r.expires) {
			delete(s.oauth.pending, id)
		}
	}
	if len(s.oauth.pending) >= maxPendingRequests {
		return back("temporarily_unavailable", "Too many sign-ins are in progress. Try again in a few minutes.")
	}
	s.oauth.pending[req.id] = req
	return AuthorizeResult{RequestID: req.id}, nil
}

// grantedScope is the scope a grant gets: always the MCP tools. Scopes the
// client asked for that dbairlock does not define grant nothing and are
// ignored (RFC 6749 section 3.3 lets the server narrow the request).
func grantedScope(string) string { return OAuthScope }

// authorizationResponse adds params, state and iss (RFC 9207) to a
// validated loopback redirect URI, which has no query of its own.
func authorizationResponse(redirectURI, issuer, state string, params url.Values) string {
	if state != "" {
		params.Set("state", state)
	}
	params.Set("iss", issuer)
	return redirectURI + "?" + params.Encode()
}

// AuthorizationRequest is what the consent page shows.
type AuthorizationRequest struct {
	ID              string
	ClientID        string
	ClientName      string
	ClientCreatedAt time.Time
	RedirectURI     string
	// RedirectHost is the host and port the code will be sent to.
	RedirectHost string
	ExpiresAt    time.Time
}

func (s *Service) pendingRequest(id string) (*pendingAuthorization, bool) {
	s.oauth.mu.Lock()
	defer s.oauth.mu.Unlock()
	r, ok := s.oauth.pending[id]
	if !ok || !s.now().Before(r.expires) {
		return nil, false
	}
	return r, true
}

// AuthorizationRequest returns a pending authorization request for the
// consent page. Any signed-in person may look at one; the one who decides
// is the person the client will act as.
func (s *Service) AuthorizationRequest(_ context.Context, p Principal, id string) (AuthorizationRequest, error) {
	if p.UserID == "" || p.APITokenID != "" {
		return AuthorizationRequest{}, ErrUnauthenticated
	}
	if _, err := s.OAuthServer(); err != nil {
		return AuthorizationRequest{}, err
	}
	r, ok := s.pendingRequest(id)
	if !ok {
		return AuthorizationRequest{}, ErrNotFound
	}
	u, _ := url.Parse(r.redirectURI)
	return AuthorizationRequest{
		ID: r.id, ClientID: r.clientID, ClientName: r.clientName, ClientCreatedAt: r.clientSince,
		RedirectURI: r.redirectURI, RedirectHost: u.Host, ExpiresAt: r.expires.UTC(),
	}, nil
}

// DecideAuthorization records the signed-in person's decision on a pending
// request and returns where to send the browser: the client's redirect URI
// with a code, or with error=access_denied. Approving creates the grant
// (replacing the person's earlier grants to the same client) and a
// single-use code, in one transaction with the audit event. A request can
// be decided once.
func (s *Service) DecideAuthorization(ctx context.Context, p Principal, id string, approve bool) (string, error) {
	if p.UserID == "" || p.APITokenID != "" {
		return "", ErrUnauthenticated
	}
	srv, err := s.OAuthServer()
	if err != nil {
		return "", err
	}
	s.oauth.mu.Lock()
	r, ok := s.oauth.pending[id]
	if ok {
		delete(s.oauth.pending, id)
	}
	s.oauth.mu.Unlock()
	if !ok || !s.now().Before(r.expires) {
		return "", ErrNotFound
	}
	if !approve {
		return authorizationResponse(r.redirectURI, srv.Issuer, r.state, url.Values{
			"error": {"access_denied"}, "error_description": {"The person denied the request."},
		}), nil
	}
	code := randomSecret(OAuthCodePrefix)
	now := s.now()
	grantID := s.opts.IDs.NewID()
	err = s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		q := identitydb.New(tx)
		// The principal was loaded before this transaction; a removal that
		// landed since must win.
		u, err := q.GetUser(ctx, p.UserID)
		if err != nil || u.DisabledAt.Valid {
			return ErrUnauthenticated
		}
		if _, err := q.GetOAuthClient(ctx, r.clientID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound // pruned while the person was deciding
			}
			return fmt.Errorf("identity: load client: %w", err)
		}
		old, err := q.ListActiveOAuthGrantsOfClientUser(ctx, identitydb.ListActiveOAuthGrantsOfClientUserParams{
			ClientID: r.clientID, UserID: p.UserID,
		})
		if err != nil {
			return fmt.Errorf("identity: load grants: %w", err)
		}
		for _, g := range old {
			if err := s.revokeGrantTx(ctx, tx, q, g, RevokedSuperseded, now); err != nil {
				return err
			}
		}
		if err := q.InsertOAuthGrant(ctx, identitydb.InsertOAuthGrantParams{
			ID: grantID, ClientID: r.clientID, UserID: p.UserID, ClientName: r.clientName,
			RedirectUri: r.redirectURI, Resource: srv.Resource, Scope: r.scope, CreatedAt: ms(now),
		}); err != nil {
			return fmt.Errorf("identity: create grant: %w", err)
		}
		if err := q.InsertOAuthCode(ctx, identitydb.InsertOAuthCodeParams{
			CodeHash: HashToken(code), GrantID: grantID, RedirectUri: r.redirectURI,
			CodeChallenge: r.challenge, CreatedAt: ms(now), ExpiresAt: ms(now.Add(OAuthCodeLifetime)),
		}); err != nil {
			return fmt.Errorf("identity: create code: %w", err)
		}
		if err := q.TouchOAuthClient(ctx, identitydb.TouchOAuthClientParams{
			LastUsedAt: sql.NullInt64{Int64: ms(now), Valid: true}, ID: r.clientID,
		}); err != nil {
			return fmt.Errorf("identity: touch client: %w", err)
		}
		return s.opts.Audit.Record(ctx, tx, audit.Event{
			Action: audit.OAuthGrantApproved, SubjectType: "oauth_grant", SubjectID: grantID,
			Payload: map[string]any{
				"client_id": r.clientID, "client_name": r.clientName, "redirect_uri": r.redirectURI,
				"resource": srv.Resource, "scope": r.scope, "replaced": len(old),
			},
		})
	})
	if err != nil {
		return "", err
	}
	return authorizationResponse(r.redirectURI, srv.Issuer, r.state, url.Values{"code": {code}}), nil
}

// ---- Token endpoint ----

// TokenRequest is the form of a token request.
type TokenRequest struct {
	GrantType    string
	ClientID     string
	Code         string
	RedirectURI  string
	CodeVerifier string
	RefreshToken string
	Resources    []string
}

// TokenResponse is a successful token response.
type TokenResponse struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	Scope        string
}

// tokenOutcome lets a token transaction commit a revocation (code or refresh
// token reuse) and still answer with an error.
type tokenOutcome struct {
	resp TokenResponse
	fail *OAuthError
}

var errInvalidGrant = oauthErr("invalid_grant", "The code or refresh token is invalid, expired or already used.")

// ExchangeOAuthToken serves the token endpoint: authorization_code (with
// PKCE) and refresh_token. Refresh tokens rotate: each is used once, and
// presenting a used one revokes the whole grant, since one of the two
// parties holding it is not the client.
func (s *Service) ExchangeOAuthToken(ctx context.Context, req TokenRequest, meta Meta) (TokenResponse, error) {
	srv, err := s.OAuthServer()
	if err != nil {
		return TokenResponse{}, err
	}
	if err := s.oauthAllow(s.oauthTokenLimit, meta.IP); err != nil {
		return TokenResponse{}, err
	}
	if len(req.Resources) > 1 || (len(req.Resources) == 1 && !resourceMatches(req.Resources[0], srv.Issuer, srv.Resource)) {
		return TokenResponse{}, oauthErr("invalid_target", "The resource must be this server's MCP endpoint, "+srv.Resource+".")
	}
	if req.ClientID == "" {
		return TokenResponse{}, oauthErr("invalid_client", "client_id is required.")
	}
	var out tokenOutcome
	switch req.GrantType {
	case "authorization_code":
		if !wellFormedSecret(req.Code, OAuthCodePrefix) {
			return TokenResponse{}, errInvalidGrant
		}
		out, err = s.redeemCode(ctx, srv, req, meta)
	case "refresh_token":
		if !wellFormedSecret(req.RefreshToken, OAuthRefreshPrefix) {
			return TokenResponse{}, errInvalidGrant
		}
		out, err = s.refresh(ctx, srv, req, meta)
	default:
		return TokenResponse{}, oauthErr("unsupported_grant_type", "Use authorization_code or refresh_token.")
	}
	if err != nil {
		return TokenResponse{}, err
	}
	if out.fail != nil {
		return TokenResponse{}, out.fail
	}
	return out.resp, nil
}

// grantContext returns ctx with the grant owner as the audit actor, via
// mcp with the grant id, as every request made with the grant's tokens.
func grantContext(ctx context.Context, u identitydb.User, grantID, ip string) context.Context {
	return audit.WithActor(ctx, principalOf(u, audit.ViaMCP, grantID).Actor(ip))
}

// usableGrant loads a grant and its owner and checks both are still valid
// for resource. ok is false if the grant was revoked, its owner removed, or
// it was issued for another resource (instance.public_url changed).
func usableGrant(ctx context.Context, q *identitydb.Queries, id, resource string) (identitydb.OauthGrant, identitydb.User, bool, error) {
	g, err := q.GetOAuthGrant(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return g, identitydb.User{}, false, nil
	}
	if err != nil {
		return g, identitydb.User{}, false, fmt.Errorf("identity: load grant: %w", err)
	}
	if g.RevokedAt.Valid || g.Resource != resource {
		return g, identitydb.User{}, false, nil
	}
	u, err := q.GetUser(ctx, g.UserID)
	if err != nil || u.DisabledAt.Valid {
		return g, u, false, nil
	}
	return g, u, true, nil
}

func (s *Service) redeemCode(ctx context.Context, srv OAuthServer, req TokenRequest, meta Meta) (tokenOutcome, error) {
	var out tokenOutcome
	err := s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		q := identitydb.New(tx)
		now := s.now()
		hash := HashToken(req.Code)
		c, err := q.GetOAuthCode(ctx, hash)
		if errors.Is(err, sql.ErrNoRows) {
			out.fail = errInvalidGrant
			return nil
		}
		if err != nil {
			return fmt.Errorf("identity: load code: %w", err)
		}
		g, u, ok, err := usableGrant(ctx, q, c.GrantID, srv.Resource)
		if err != nil {
			return err
		}
		if !ok || subtle.ConstantTimeCompare(c.CodeHash, hash) != 1 || g.ClientID != req.ClientID {
			out.fail = errInvalidGrant
			return nil
		}
		ctx := grantContext(ctx, u, g.ID, meta.IP)
		if c.UsedAt.Valid {
			// A code used twice was intercepted: end everything issued from
			// it (OAuth 2.1 section 4.1.3).
			out.fail = errInvalidGrant
			return s.revokeGrantTx(ctx, tx, q, g, RevokedCodeReuse, now)
		}
		if ms(now) >= c.ExpiresAt {
			out.fail = errInvalidGrant
			return nil
		}
		if req.RedirectURI != c.RedirectUri {
			out.fail = oauthErr("invalid_grant", "redirect_uri does not match the authorization request.")
			return nil
		}
		if !verifyPKCE(req.CodeVerifier, c.CodeChallenge) {
			out.fail = oauthErr("invalid_grant", "The code_verifier does not match the code_challenge.")
			return nil
		}
		if n, err := q.UseOAuthCode(ctx, identitydb.UseOAuthCodeParams{
			UsedAt: sql.NullInt64{Int64: ms(now), Valid: true}, CodeHash: hash,
		}); err != nil || n != 1 {
			return fmt.Errorf("identity: use code: %w", errors.Join(err, errors.New("already used")))
		}
		out.resp, err = s.issueTokens(ctx, tx, q, g, now, "authorization_code")
		return err
	})
	return out, err
}

func (s *Service) refresh(ctx context.Context, srv OAuthServer, req TokenRequest, meta Meta) (tokenOutcome, error) {
	var out tokenOutcome
	err := s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		q := identitydb.New(tx)
		now := s.now()
		hash := HashToken(req.RefreshToken)
		t, err := q.GetOAuthToken(ctx, hash)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && t.Kind != "refresh") {
			out.fail = errInvalidGrant
			return nil
		}
		if err != nil {
			return fmt.Errorf("identity: load token: %w", err)
		}
		g, u, ok, err := usableGrant(ctx, q, t.GrantID, srv.Resource)
		if err != nil {
			return err
		}
		if !ok || subtle.ConstantTimeCompare(t.TokenHash, hash) != 1 || g.ClientID != req.ClientID {
			out.fail = errInvalidGrant
			return nil
		}
		ctx := grantContext(ctx, u, g.ID, meta.IP)
		if t.UsedAt.Valid {
			// A rotated refresh token came back: someone besides the client
			// holds it. Revoke the grant, so both lose access.
			out.fail = errInvalidGrant
			return s.revokeGrantTx(ctx, tx, q, g, RevokedRefreshReuse, now)
		}
		if ms(now) >= t.ExpiresAt {
			out.fail = errInvalidGrant
			return nil
		}
		if n, err := q.UseOAuthToken(ctx, identitydb.UseOAuthTokenParams{
			UsedAt: sql.NullInt64{Int64: ms(now), Valid: true}, TokenHash: hash,
		}); err != nil || n != 1 {
			return fmt.Errorf("identity: use refresh token: %w", errors.Join(err, errors.New("already used")))
		}
		out.resp, err = s.issueTokens(ctx, tx, q, g, now, "refresh_token")
		return err
	})
	return out, err
}

// issueTokens creates an access token and a refresh token for g inside tx,
// records oauth.token_issued and clears expired codes and tokens.
func (s *Service) issueTokens(ctx context.Context, tx *store.Tx, q *identitydb.Queries, g identitydb.OauthGrant, now time.Time, grantType string) (TokenResponse, error) {
	if err := q.DeleteExpiredOAuthTokens(ctx, ms(now)); err != nil {
		return TokenResponse{}, fmt.Errorf("identity: clear tokens: %w", err)
	}
	if err := q.DeleteExpiredOAuthCodes(ctx, ms(now)); err != nil {
		return TokenResponse{}, fmt.Errorf("identity: clear codes: %w", err)
	}
	access, refresh := randomSecret(OAuthAccessPrefix), randomSecret(OAuthRefreshPrefix)
	for _, t := range []struct {
		secret, kind string
		life         time.Duration
	}{{access, "access", OAuthAccessLifetime}, {refresh, "refresh", OAuthRefreshLifetime}} {
		if err := q.InsertOAuthToken(ctx, identitydb.InsertOAuthTokenParams{
			TokenHash: HashToken(t.secret), GrantID: g.ID, Kind: t.kind,
			CreatedAt: ms(now), ExpiresAt: ms(now.Add(t.life)),
		}); err != nil {
			return TokenResponse{}, fmt.Errorf("identity: issue token: %w", err)
		}
	}
	if err := q.TouchOAuthGrant(ctx, identitydb.TouchOAuthGrantParams{
		LastUsedAt: sql.NullInt64{Int64: ms(now), Valid: true}, ID: g.ID,
	}); err != nil {
		return TokenResponse{}, fmt.Errorf("identity: touch grant: %w", err)
	}
	if err := s.opts.Audit.Record(ctx, tx, audit.Event{
		Action: audit.OAuthTokenIssued, SubjectType: "oauth_grant", SubjectID: g.ID,
		Payload: map[string]any{"grant_type": grantType, "client_name": g.ClientName},
	}); err != nil {
		return TokenResponse{}, err
	}
	return TokenResponse{
		AccessToken: access, RefreshToken: refresh,
		ExpiresIn: int(OAuthAccessLifetime.Seconds()), Scope: g.Scope,
	}, nil
}

// revokeGrantTx revokes g and deletes its codes and tokens, inside tx, with
// the audit event. The actor comes from ctx.
func (s *Service) revokeGrantTx(ctx context.Context, tx *store.Tx, q *identitydb.Queries, g identitydb.OauthGrant, reason string, now time.Time) error {
	n, err := q.RevokeOAuthGrant(ctx, identitydb.RevokeOAuthGrantParams{
		RevokedAt: sql.NullInt64{Int64: ms(now), Valid: true}, RevokedReason: reason, ID: g.ID,
	})
	if err != nil {
		return fmt.Errorf("identity: revoke grant: %w", err)
	}
	if err := q.DeleteOAuthGrantTokens(ctx, g.ID); err != nil {
		return fmt.Errorf("identity: delete tokens: %w", err)
	}
	if err := q.DeleteOAuthGrantCodes(ctx, g.ID); err != nil {
		return fmt.Errorf("identity: delete codes: %w", err)
	}
	if n == 0 {
		return nil // already revoked
	}
	return s.opts.Audit.Record(ctx, tx, audit.Event{
		Action: audit.OAuthGrantRevoked, SubjectType: "oauth_grant", SubjectID: g.ID,
		Payload: map[string]any{
			"client_id": g.ClientID, "client_name": g.ClientName, "owner_id": g.UserID, "reason": reason,
		},
	})
}

// RevokeOAuthToken serves the revocation endpoint (RFC 7009). Revoking an
// access or a refresh token ends the whole grant. Unknown tokens, and tokens
// of another client, are ignored without saying so.
func (s *Service) RevokeOAuthToken(ctx context.Context, token, clientID string, meta Meta) error {
	if _, err := s.OAuthServer(); err != nil {
		return err
	}
	if err := s.oauthAllow(s.oauthTokenLimit, meta.IP); err != nil {
		return err
	}
	if !wellFormedSecret(token, OAuthAccessPrefix) && !wellFormedSecret(token, OAuthRefreshPrefix) {
		return nil
	}
	hash := HashToken(token)
	return s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		q := identitydb.New(tx)
		t, err := q.GetOAuthToken(ctx, hash)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("identity: load token: %w", err)
		}
		g, err := q.GetOAuthGrant(ctx, t.GrantID)
		if err != nil {
			return fmt.Errorf("identity: load grant: %w", err)
		}
		if g.RevokedAt.Valid || (clientID != "" && clientID != g.ClientID) {
			return nil
		}
		u, err := q.GetUser(ctx, g.UserID)
		if err != nil {
			return fmt.Errorf("identity: load user: %w", err)
		}
		return s.revokeGrantTx(grantContext(ctx, u, g.ID, meta.IP), tx, q, g, RevokedByClient, s.now())
	})
}

// ---- Resource server ----

// AuthenticateOAuthToken returns the principal of an OAuth access token:
// the person who approved the grant, via mcp, with the grant id in place of
// an API token id, so rate limits, audit events and the agent's
// restrictions are exactly those of an API token. It fails with
// ErrUnauthenticated if OAuth is unavailable, or the token is unknown,
// expired, not an access token, issued for another resource, or its grant
// was revoked or its owner removed.
func (s *Service) AuthenticateOAuthToken(ctx context.Context, raw string) (Principal, error) {
	if !wellFormedSecret(raw, OAuthAccessPrefix) {
		return Principal{}, ErrUnauthenticated
	}
	srv, err := s.OAuthServer()
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	hash := HashToken(raw)
	q := identitydb.New(s.opts.Store.Reader())
	t, err := q.GetOAuthToken(ctx, hash)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, fmt.Errorf("identity: load token: %w", err)
	}
	now := s.now()
	if subtle.ConstantTimeCompare(t.TokenHash, hash) != 1 || t.Kind != "access" || ms(now) >= t.ExpiresAt {
		return Principal{}, ErrUnauthenticated
	}
	g, u, ok, err := usableGrant(ctx, q, t.GrantID, srv.Resource)
	if err != nil {
		return Principal{}, err
	}
	if !ok {
		return Principal{}, ErrUnauthenticated
	}
	if !g.LastUsedAt.Valid || now.Sub(time.UnixMilli(g.LastUsedAt.Int64)) >= oauthTouchEvery {
		// Not audited, like API token and session activity.
		if err := s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
			return identitydb.New(tx).TouchOAuthGrant(ctx, identitydb.TouchOAuthGrantParams{
				LastUsedAt: sql.NullInt64{Int64: ms(now), Valid: true}, ID: g.ID,
			})
		}); err != nil {
			return Principal{}, fmt.Errorf("identity: record grant use: %w", err)
		}
	}
	p := principalOf(u, audit.ViaMCP, g.ID)
	p.Agent = g.ClientName
	p.OAuth = true
	return p, nil
}

// ---- Connected apps ----

// OAuthGrant is a connected app: one person's approval of one client.
type OAuthGrant struct {
	ID          string
	ClientID    string
	ClientName  string
	UserID      string
	OwnerEmail  string
	RedirectURI string
	CreatedAt   time.Time
	LastUsedAt  *time.Time
}

func grantFromRow(r identitydb.ListOAuthGrantsOfUserRow) OAuthGrant {
	return OAuthGrant{
		ID: r.ID, ClientID: r.ClientID, ClientName: r.ClientName, UserID: r.UserID, OwnerEmail: r.OwnerEmail,
		RedirectURI: r.RedirectUri, CreatedAt: time.UnixMilli(r.CreatedAt).UTC(), LastUsedAt: optTime(r.LastUsedAt),
	}
}

// ListOAuthGrants returns the principal's active grants, or with all every
// user's (which requires user.manage), newest first.
func (s *Service) ListOAuthGrants(ctx context.Context, p Principal, all bool) ([]OAuthGrant, error) {
	if p.UserID == "" {
		return nil, ErrUnauthenticated
	}
	q := identitydb.New(s.opts.Store.Reader())
	var rows []identitydb.ListOAuthGrantsOfUserRow
	if all {
		if err := Can(p, ActionUserManage, Instance); err != nil {
			return nil, err
		}
		list, err := q.ListAllOAuthGrants(ctx)
		if err != nil {
			return nil, fmt.Errorf("identity: list grants: %w", err)
		}
		for _, r := range list {
			rows = append(rows, identitydb.ListOAuthGrantsOfUserRow(r))
		}
	} else {
		var err error
		if rows, err = q.ListOAuthGrantsOfUser(ctx, p.UserID); err != nil {
			return nil, fmt.Errorf("identity: list grants: %w", err)
		}
	}
	out := make([]OAuthGrant, 0, len(rows))
	for _, r := range rows {
		out = append(out, grantFromRow(r))
	}
	return out, nil
}

// RevokeOAuthGrant disconnects an app. Its owner may, and so may anyone
// allowed user.manage; another user's grant reads as not found to anyone
// else.
func (s *Service) RevokeOAuthGrant(ctx context.Context, p Principal, id string) error {
	if p.UserID == "" {
		return ErrUnauthenticated
	}
	return s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		q := identitydb.New(tx)
		g, err := q.GetOAuthGrant(ctx, id)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && g.RevokedAt.Valid) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("identity: load grant: %w", err)
		}
		if g.UserID != p.UserID && Can(p, ActionUserManage, Instance) != nil {
			return ErrNotFound
		}
		return s.revokeGrantTx(ctx, tx, q, g, RevokedByUser, s.now())
	})
}

// revokeUserGrants revokes every active grant of userID inside the removal
// transaction.
func (s *Service) revokeUserGrants(ctx context.Context, tx *store.Tx, q *identitydb.Queries, userID string, now time.Time) (int, error) {
	grants, err := q.ListActiveOAuthGrantsOfUser(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("identity: load grants: %w", err)
	}
	for _, g := range grants {
		if err := s.revokeGrantTx(ctx, tx, q, g, RevokedUserRemoved, now); err != nil {
			return 0, err
		}
	}
	return len(grants), nil
}
