package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/alvaroibarguen/dbairlock/internal/httpapi/gen"
	"github.com/alvaroibarguen/dbairlock/internal/identity"
	"github.com/alvaroibarguen/dbairlock/internal/platform/httpx"
	"github.com/alvaroibarguen/dbairlock/internal/platform/logx"
	"github.com/alvaroibarguen/dbairlock/internal/settings"
)

// OAuth is the authorization server side of the identity module: OAuth
// sign-in of MCP clients (identity/oauth.go).
type OAuth interface {
	OAuthServer() (identity.OAuthServer, error)
	RegisterOAuthClient(ctx context.Context, reg identity.ClientRegistration, meta identity.Meta) (identity.OAuthClient, error)
	BeginAuthorization(ctx context.Context, p identity.AuthorizeParams, meta identity.Meta) (identity.AuthorizeResult, error)
	AuthorizationRequest(ctx context.Context, p identity.Principal, id string) (identity.AuthorizationRequest, error)
	DecideAuthorization(ctx context.Context, p identity.Principal, id string, approve bool) (string, error)
	ExchangeOAuthToken(ctx context.Context, req identity.TokenRequest, meta identity.Meta) (identity.TokenResponse, error)
	RevokeOAuthToken(ctx context.Context, token, clientID string, meta identity.Meta) error
	ListOAuthGrants(ctx context.Context, p identity.Principal, all bool) ([]identity.OAuthGrant, error)
	RevokeOAuthGrant(ctx context.Context, p identity.Principal, id string) error
}

// MaxOAuthBodyBytes bounds the body of the OAuth protocol endpoints: a
// registration or a token form is a few hundred bytes.
const MaxOAuthBodyBytes int64 = 16 << 10

// ConsentPath is the dashboard page where a person allows or denies an
// MCP client. The authorization endpoint sends the browser there.
const ConsentPath = "/oauth/consent"

// Route is a route outside the OpenAPI contract, with its access level.
type Route struct {
	Pattern string
	Access  Access
	Handler http.Handler
}

// OAuthRoutes are the OAuth protocol endpoints: the discovery documents
// (RFC 9728, RFC 8414), dynamic client registration (RFC 7591), the
// authorization, token and revocation endpoints (RFC 6749, RFC 7009). They
// speak OAuth's own formats rather than the REST API's, so they are not in
// api/openapi.yaml. All are public: each authenticates what it needs (the
// consent page needs a session; codes and tokens authenticate themselves).
func (s *Server) OAuthRoutes() []Route {
	return []Route{
		{"GET /.well-known/oauth-protected-resource", public, http.HandlerFunc(s.resourceMetadata)},
		{"GET /.well-known/oauth-protected-resource/{path...}", public, http.HandlerFunc(s.resourceMetadata)},
		{"GET /.well-known/oauth-authorization-server", public, http.HandlerFunc(s.serverMetadata)},
		{"GET /.well-known/oauth-authorization-server/{path...}", public, http.HandlerFunc(s.serverMetadata)},
		{"POST /oauth/register", public, http.HandlerFunc(s.oauthRegister)},
		{"GET /oauth/authorize", public, http.HandlerFunc(s.oauthAuthorize)},
		{"POST /oauth/token", public, http.HandlerFunc(s.oauthToken)},
		{"POST /oauth/revoke", public, http.HandlerFunc(s.oauthRevoke)},
	}
}

// ---- Discovery ----

type resourceMetadataDoc struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	ScopesSupported        []string `json:"scopes_supported"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
	ResourceName           string   `json:"resource_name,omitempty"`
}

type serverMetadataDoc struct {
	Issuer                                     string   `json:"issuer"`
	AuthorizationEndpoint                      string   `json:"authorization_endpoint"`
	TokenEndpoint                              string   `json:"token_endpoint"`
	RegistrationEndpoint                       string   `json:"registration_endpoint"`
	RevocationEndpoint                         string   `json:"revocation_endpoint"`
	ScopesSupported                            []string `json:"scopes_supported"`
	ResponseTypesSupported                     []string `json:"response_types_supported"`
	ResponseModesSupported                     []string `json:"response_modes_supported"`
	GrantTypesSupported                        []string `json:"grant_types_supported"`
	TokenEndpointAuthMethodsSupported          []string `json:"token_endpoint_auth_methods_supported"`
	RevocationEndpointAuthMethodsSupported     []string `json:"revocation_endpoint_auth_methods_supported"`
	CodeChallengeMethodsSupported              []string `json:"code_challenge_methods_supported"`
	AuthorizationResponseIssParameterSupported bool     `json:"authorization_response_iss_parameter_supported"`
}

// pathOf is the path of an absolute URL, without a trailing slash.
func pathOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimRight(u.Path, "/")
}

// wellKnownSuffix is the part of the request path after the well-known
// name, with a leading slash ("" for the root document).
func wellKnownSuffix(r *http.Request) string {
	if rest := r.PathValue("path"); rest != "" {
		return "/" + strings.TrimRight(rest, "/")
	}
	return ""
}

func writeMetadata(w http.ResponseWriter, v any) {
	w.Header().Set("Cache-Control", "max-age=300")
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// resourceMetadata serves RFC 9728 protected resource metadata. The
// document at the MCP endpoint's path describes the MCP endpoint; the one
// at the root describes the instance as a whole (RFC 9728 section 3.1),
// which clients probing the root accept for the MCP endpoint below it.
func (s *Server) resourceMetadata(w http.ResponseWriter, r *http.Request) {
	srv, err := s.deps.OAuth.OAuthServer()
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	resource := ""
	switch suffix := wellKnownSuffix(r); suffix {
	case pathOf(srv.Resource):
		resource = srv.Resource
	case pathOf(srv.Issuer):
		resource = srv.Issuer
	default:
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	writeMetadata(w, resourceMetadataDoc{
		Resource: resource, AuthorizationServers: []string{srv.Issuer},
		ScopesSupported: []string{identity.OAuthScope}, BearerMethodsSupported: []string{"header"},
		ResourceName: settings.Get(s.deps.Settings, settings.InstanceName),
	})
}

// serverMetadata serves RFC 8414 authorization server metadata.
func (s *Server) serverMetadata(w http.ResponseWriter, r *http.Request) {
	srv, err := s.deps.OAuth.OAuthServer()
	if err != nil || wellKnownSuffix(r) != pathOf(srv.Issuer) {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	writeMetadata(w, serverMetadataDoc{
		Issuer:                                     srv.Issuer,
		AuthorizationEndpoint:                      srv.Issuer + "/oauth/authorize",
		TokenEndpoint:                              srv.Issuer + "/oauth/token",
		RegistrationEndpoint:                       srv.Issuer + "/oauth/register",
		RevocationEndpoint:                         srv.Issuer + "/oauth/revoke",
		ScopesSupported:                            []string{identity.OAuthScope, identity.OAuthScopeOffline},
		ResponseTypesSupported:                     []string{"code"},
		ResponseModesSupported:                     []string{"query"},
		GrantTypesSupported:                        []string{"authorization_code", "refresh_token"},
		TokenEndpointAuthMethodsSupported:          []string{"none"},
		RevocationEndpointAuthMethodsSupported:     []string{"none"},
		CodeChallengeMethodsSupported:              []string{"S256"},
		AuthorizationResponseIssParameterSupported: true,
	})
}

// ---- Protocol errors ----

// writeOAuthError writes an RFC 6749 section 5.2 error.
func writeOAuthError(w http.ResponseWriter, status int, code, desc string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	httpx.WriteJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

const unavailableDesc = "OAuth sign-in is turned off on this dbairlock instance, or its public URL is not set. Use an API token, or ask an admin."

// oauthFailure maps an error from the OAuth module to a protocol error.
func oauthFailure(w http.ResponseWriter, r *http.Request, err error) {
	var oe *identity.OAuthError
	var rl *identity.RateLimitedError
	switch {
	case errors.As(err, &oe):
		status := http.StatusBadRequest
		if oe.Code == "invalid_client" {
			status = http.StatusUnauthorized
		}
		if oe.Code == "temporarily_unavailable" {
			status = http.StatusServiceUnavailable
		}
		writeOAuthError(w, status, oe.Code, oe.Description)
	case errors.As(err, &rl):
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(rl.RetryAfter.Seconds()))))
		writeOAuthError(w, http.StatusTooManyRequests, "rate_limited", "Too many requests. Wait a moment and try again.")
	case errors.Is(err, identity.ErrOAuthUnavailable):
		writeOAuthError(w, http.StatusForbidden, "access_denied", unavailableDesc)
	default:
		logx.FromContext(r.Context()).ErrorContext(r.Context(), "oauth endpoint failed",
			slog.String("path", r.URL.Path), slog.Any("error", err))
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "An internal error occurred.")
	}
}

func oauthMeta(r *http.Request) identity.Meta {
	return identity.Meta{IP: httpx.ClientIPFrom(r.Context()), UserAgent: r.UserAgent()}
}

// singleValues reports whether no parameter in v other than resource
// appears more than once (RFC 6749 section 3.1: parameters MUST NOT repeat).
func singleValues(v url.Values) bool {
	for k, vals := range v {
		if k != "resource" && len(vals) > 1 {
			return false
		}
	}
	return true
}

// ---- Registration (RFC 7591) ----

type registrationRequest struct {
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

type registrationResponse struct {
	ClientID                string   `json:"client_id"`
	ClientIDIssuedAt        int64    `json:"client_id_issued_at"`
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

func (s *Server) oauthRegister(w http.ResponseWriter, r *http.Request) {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "Send the client metadata as application/json.")
		return
	}
	var req registrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "The body is not valid client metadata JSON.")
		return
	}
	c, err := s.deps.OAuth.RegisterOAuthClient(r.Context(), identity.ClientRegistration{
		Name: req.ClientName, RedirectURIs: req.RedirectURIs, GrantTypes: req.GrantTypes,
		ResponseTypes: req.ResponseTypes, TokenEndpointAuthMethod: req.TokenEndpointAuthMethod,
	}, oauthMeta(r))
	if err != nil {
		oauthFailure(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusCreated, registrationResponse{
		ClientID: c.ID, ClientIDIssuedAt: c.CreatedAt.Unix(), ClientName: c.Name, RedirectURIs: c.RedirectURIs,
		GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"},
		TokenEndpointAuthMethod: "none",
	})
}

// ---- Authorization endpoint ----

// consentError sends the browser to the consent page with an error, for
// requests that cannot safely be redirected back to the client.
func consentError(w http.ResponseWriter, r *http.Request, code string) {
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, ConsentPath+"?error="+url.QueryEscape(code), http.StatusFound)
}

func (s *Server) oauthAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if !singleValues(q) {
		consentError(w, r, "invalid_request")
		return
	}
	res, err := s.deps.OAuth.BeginAuthorization(r.Context(), identity.AuthorizeParams{
		ResponseType: q.Get("response_type"), ClientID: q.Get("client_id"), RedirectURI: q.Get("redirect_uri"),
		CodeChallenge: q.Get("code_challenge"), CodeChallengeMethod: q.Get("code_challenge_method"),
		State: q.Get("state"), Scope: q.Get("scope"), Resources: q["resource"],
	}, oauthMeta(r))
	var oe *identity.OAuthError
	var rl *identity.RateLimitedError
	switch {
	case err == nil && res.Redirect != "":
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, res.Redirect, http.StatusFound)
	case err == nil:
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, ConsentPath+"?request="+url.QueryEscape(res.RequestID), http.StatusFound)
	case errors.Is(err, identity.ErrOAuthUnavailable):
		consentError(w, r, "unavailable")
	case errors.As(err, &rl):
		consentError(w, r, "rate_limited")
	case errors.As(err, &oe):
		consentError(w, r, oe.Code)
	default:
		logx.FromContext(r.Context()).ErrorContext(r.Context(), "oauth authorize failed", slog.Any("error", err))
		consentError(w, r, "server_error")
	}
}

// ---- Token and revocation endpoints ----

// clientForm parses a form-encoded POST body and resolves the client id,
// from the form or from HTTP Basic with an empty password (public clients
// have no secret). ok is false after an error was written.
func clientForm(w http.ResponseWriter, r *http.Request) (url.Values, string, bool) {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/x-www-form-urlencoded" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "Send the request as application/x-www-form-urlencoded.")
		return nil, "", false
	}
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "The form could not be read.")
		return nil, "", false
	}
	form := r.PostForm
	if !singleValues(form) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "A parameter appears more than once.")
		return nil, "", false
	}
	clientID := form.Get("client_id")
	if user, pass, ok := r.BasicAuth(); ok {
		id, err := url.QueryUnescape(user)
		if err != nil || pass != "" || (clientID != "" && clientID != id) {
			w.Header().Set("WWW-Authenticate", `Basic realm="dbairlock"`)
			writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "Clients are public: send client_id without a secret.")
			return nil, "", false
		}
		clientID = id
	} else if form.Has("client_secret") {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "Clients are public: send client_id without a secret.")
		return nil, "", false
	}
	return form, clientID, true
}

func (s *Server) oauthToken(w http.ResponseWriter, r *http.Request) {
	form, clientID, ok := clientForm(w, r)
	if !ok {
		return
	}
	resp, err := s.deps.OAuth.ExchangeOAuthToken(r.Context(), identity.TokenRequest{
		GrantType: form.Get("grant_type"), ClientID: clientID, Code: form.Get("code"),
		RedirectURI: form.Get("redirect_uri"), CodeVerifier: form.Get("code_verifier"),
		RefreshToken: form.Get("refresh_token"), Resources: form["resource"],
	}, oauthMeta(r))
	if err != nil {
		oauthFailure(w, r, err)
		return
	}
	w.Header().Set("Pragma", "no-cache")
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"access_token": resp.AccessToken, "token_type": "Bearer", "expires_in": resp.ExpiresIn,
		"refresh_token": resp.RefreshToken, "scope": resp.Scope,
	})
}

func (s *Server) oauthRevoke(w http.ResponseWriter, r *http.Request) {
	form, clientID, ok := clientForm(w, r)
	if !ok {
		return
	}
	if form.Get("token") == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "token is required.")
		return
	}
	if err := s.deps.OAuth.RevokeOAuthToken(r.Context(), form.Get("token"), clientID, oauthMeta(r)); err != nil {
		oauthFailure(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
}

// ---- Dashboard API: consent page and connected apps ----

// GetOAuthRequest returns a pending authorization request for the consent page.
func (s *Server) GetOAuthRequest(ctx context.Context, req gen.GetOAuthRequestRequestObject) (gen.GetOAuthRequestResponseObject, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	ar, err := s.deps.OAuth.AuthorizationRequest(ctx, p, req.Id)
	if err != nil {
		return nil, err
	}
	return gen.GetOAuthRequest200JSONResponse{
		Id: ar.ID, ClientId: ar.ClientID, ClientName: ar.ClientName, ClientRegisteredAt: ar.ClientCreatedAt,
		RedirectUri: ar.RedirectURI, RedirectHost: ar.RedirectHost, ExpiresAt: ar.ExpiresAt,
	}, nil
}

// DecideOAuthRequest allows or denies a pending authorization request.
func (s *Server) DecideOAuthRequest(ctx context.Context, req gen.DecideOAuthRequestRequestObject) (gen.DecideOAuthRequestResponseObject, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	to, err := s.deps.OAuth.DecideAuthorization(ctx, p, req.Id, req.Body.Approve)
	if err != nil {
		return nil, err
	}
	if hp := pairFrom(ctx); hp.w != nil {
		hp.w.Header().Set("Cache-Control", "no-store")
	}
	return gen.DecideOAuthRequest200JSONResponse{RedirectUrl: to}, nil
}

func grantBody(g identity.OAuthGrant) gen.OAuthGrant {
	return gen.OAuthGrant{
		Id: g.ID, ClientId: g.ClientID, ClientName: g.ClientName, OwnerId: g.UserID, OwnerEmail: g.OwnerEmail,
		RedirectUri: g.RedirectURI, CreatedAt: g.CreatedAt, LastUsedAt: g.LastUsedAt,
	}
}

// ListOAuthGrants lists connected apps.
func (s *Server) ListOAuthGrants(ctx context.Context, req gen.ListOAuthGrantsRequestObject) (gen.ListOAuthGrantsResponseObject, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	all := req.Params.All != nil && *req.Params.All
	grants, err := s.deps.OAuth.ListOAuthGrants(ctx, p, all)
	if err != nil {
		return nil, err
	}
	out := make([]gen.OAuthGrant, len(grants))
	for i, g := range grants {
		out[i] = grantBody(g)
	}
	return gen.ListOAuthGrants200JSONResponse{Grants: out}, nil
}

// RevokeOAuthGrant disconnects an app.
func (s *Server) RevokeOAuthGrant(ctx context.Context, req gen.RevokeOAuthGrantRequestObject) (gen.RevokeOAuthGrantResponseObject, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.deps.OAuth.RevokeOAuthGrant(ctx, p, req.Id); err != nil {
		return nil, err
	}
	return gen.RevokeOAuthGrant204Response{}, nil
}
