package identity

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/identity/identitydb"
	"github.com/alvaroibarguen/dbairlock/internal/settings"
	"github.com/alvaroibarguen/dbairlock/internal/store"
)

// CallbackPath is where Google redirects after sign-in. The redirect URI to
// register with Google is the public URL followed by this path.
const CallbackPath = "/api/auth/google/callback"

// FlowLifetime bounds the time between starting a sign-in and Google's
// redirect back. Fixed: a person completes the redirect in seconds, and a
// longer window only widens the time a stolen state cookie is useful.
const FlowLifetime = 10 * time.Minute

// Purpose is why a sign-in was started.
type Purpose string

// Purposes.
const (
	PurposeSignIn   Purpose = "signin"
	PurposeSetup    Purpose = "setup"
	PurposeRecovery Purpose = "recovery"
)

// Flow is the state of one sign-in between the redirect to Google and the
// callback. It travels in a short-lived, signed, HttpOnly cookie.
type Flow struct {
	State    string  `json:"s"`
	Nonce    string  `json:"n"`
	Verifier string  `json:"v"`
	Purpose  Purpose `json:"p"`
	Next     string  `json:"x"`
	Expires  int64   `json:"e"`
}

// Claims are the verified facts about a Google account.
type Claims struct {
	Subject      string
	Email        string
	Name         string
	Picture      string
	HostedDomain string
}

// Reasons a sign-in is denied. The sign-in page shows a message per reason.
const (
	DenyNotConfigured  = "not_configured"
	DenyFlowExpired    = "flow_expired"
	DenyProviderError  = "provider_error"
	DenyBadState       = "bad_state"
	DenyCodeExchange   = "code_exchange_failed"
	DenyNoIDToken      = "no_id_token"
	DenyInvalidIDToken = "invalid_id_token"
	DenyBadNonce       = "bad_nonce"
	DenyEmailUnverfied = "email_unverified"
	DenyDomain         = "domain_not_allowed"
	DenyRemoved        = "account_removed"
)

// DeniedError is a refused sign-in. A login.denied audit event was recorded.
type DeniedError struct {
	Reason  string
	Purpose Purpose
}

func (e *DeniedError) Error() string { return "identity: sign-in denied: " + e.Reason }

type oidcClient struct {
	issuer   string
	provider *oidc.Provider
}

// RedirectURI returns the URI to register with Google for a public URL, or
// "" if the public URL is not set.
func RedirectURI(publicURL string) string {
	if publicURL == "" {
		return ""
	}
	return strings.TrimRight(publicURL, "/") + CallbackPath
}

func (s *Service) clientCtx(ctx context.Context) context.Context {
	return oidc.ClientContext(ctx, s.opts.HTTPClient)
}

// provider returns the OpenID provider for the configured issuer, running
// discovery the first time and again after the sign-in settings change.
func (s *Service) provider(ctx context.Context) (*oidc.Provider, error) {
	issuer := settings.Get(s.opts.Settings, settings.OIDCIssuer)
	s.oidcMu.Lock()
	defer s.oidcMu.Unlock()
	if s.oidc != nil && s.oidc.issuer == issuer {
		return s.oidc.provider, nil
	}
	p, err := oidc.NewProvider(s.clientCtx(ctx), issuer)
	if err != nil {
		return nil, fmt.Errorf("identity: discover %s: %w", issuer, err)
	}
	s.oidc = &oidcClient{issuer: issuer, provider: p}
	return p, nil
}

// oauthConfig builds the OAuth client from the settings. withSecret opens
// the client secret, which only the token exchange needs.
func (s *Service) oauthConfig(ctx context.Context, withSecret bool) (oauth2.Config, *oidc.Provider, error) {
	clientID := settings.Get(s.opts.Settings, settings.GoogleClientID)
	redirect := RedirectURI(settings.Get(s.opts.Settings, settings.InstancePublicURL))
	if clientID == "" || redirect == "" {
		return oauth2.Config{}, nil, ErrNotConfigured
	}
	p, err := s.provider(ctx)
	if err != nil {
		return oauth2.Config{}, nil, err
	}
	cfg := oauth2.Config{
		ClientID: clientID, RedirectURL: redirect, Endpoint: p.Endpoint(),
		Scopes: []string{oidc.ScopeOpenID, "email", "profile"},
	}
	if withSecret {
		v, err := s.opts.Secrets.Open(ctx, settings.GoogleClientSecret)
		if err != nil {
			return oauth2.Config{}, nil, fmt.Errorf("%w: client secret: %w", ErrNotConfigured, err)
		}
		cfg.ClientSecret = string(v.Reveal())
	}
	return cfg, p, nil
}

// SafeNext returns next if it is a path on this origin, and "/" otherwise,
// so the post-sign-in redirect can never leave the site.
func SafeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") ||
		strings.ContainsAny(next, "\\\r\n\t") {
		return "/"
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return "/"
	}
	return u.RequestURI()
}

// BeginLogin starts a Google sign-in. It returns the URL to redirect the
// browser to and the value of the signed flow cookie that binds state,
// nonce and the PKCE verifier to this browser.
func (s *Service) BeginLogin(ctx context.Context, purpose Purpose, next string, meta Meta) (string, string, error) {
	if err := s.allow(meta.IP); err != nil {
		return "", "", err
	}
	cfg, _, err := s.oauthConfig(ctx, false)
	if err != nil {
		return "", "", err
	}
	f := Flow{
		State: RandomToken(), Nonce: RandomToken(), Verifier: oauth2.GenerateVerifier(),
		Purpose: purpose, Next: SafeNext(next), Expires: ms(s.now().Add(FlowLifetime)),
	}
	opts := []oauth2.AuthCodeOption{
		oidc.Nonce(f.Nonce), oauth2.S256ChallengeOption(f.Verifier),
		oauth2.SetAuthURLParam("prompt", "select_account"),
	}
	if d := settings.Get(s.opts.Settings, settings.AllowedDomains); len(d) == 1 {
		// A hint for Google's account chooser; the callback checks hd itself.
		opts = append(opts, oauth2.SetAuthURLParam("hd", d[0]))
	}
	return cfg.AuthCodeURL(f.State, opts...), s.signFlow(f), nil
}

// CallbackParams are the query parameters of the callback.
type CallbackParams struct {
	Code  string
	State string
	Error string
}

// FinishLogin verifies a sign-in: the flow cookie (signature and expiry),
// state, the code exchange with PKCE, the ID token's signature, issuer,
// audience and expiry, the nonce, email_verified and the hd domain. It
// returns the verified claims and the flow, or a *DeniedError after
// recording login.denied.
func (s *Service) FinishLogin(ctx context.Context, flowCookie string, p CallbackParams, meta Meta) (Claims, Flow, error) {
	if err := s.allow(meta.IP); err != nil {
		return Claims{}, Flow{}, err
	}
	f, ok := s.openFlow(flowCookie)
	if !ok {
		return Claims{}, f, s.deny(ctx, meta, PurposeSignIn, DenyFlowExpired, nil)
	}
	if p.Error != "" {
		return Claims{}, f, s.deny(ctx, meta, f.Purpose, DenyProviderError, map[string]any{"provider_error": p.Error})
	}
	if p.State == "" || subtle.ConstantTimeCompare([]byte(p.State), []byte(f.State)) != 1 {
		return Claims{}, f, s.deny(ctx, meta, f.Purpose, DenyBadState, nil)
	}
	cfg, provider, err := s.oauthConfig(ctx, true)
	if errors.Is(err, ErrNotConfigured) {
		return Claims{}, f, s.deny(ctx, meta, f.Purpose, DenyNotConfigured, nil)
	}
	if err != nil {
		return Claims{}, f, err
	}
	tok, err := cfg.Exchange(s.clientCtx(ctx), p.Code, oauth2.VerifierOption(f.Verifier))
	if err != nil {
		s.opts.Logger.WarnContext(ctx, "sign-in code exchange failed", slog.Any("error", err))
		return Claims{}, f, s.deny(ctx, meta, f.Purpose, DenyCodeExchange, nil)
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return Claims{}, f, s.deny(ctx, meta, f.Purpose, DenyNoIDToken, nil)
	}
	idt, err := provider.Verifier(&oidc.Config{ClientID: cfg.ClientID, Now: s.now}).Verify(ctx, raw)
	if err != nil {
		s.opts.Logger.WarnContext(ctx, "ID token rejected", slog.Any("error", err))
		return Claims{}, f, s.deny(ctx, meta, f.Purpose, DenyInvalidIDToken, nil)
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(f.Nonce)) != 1 {
		return Claims{}, f, s.deny(ctx, meta, f.Purpose, DenyBadNonce, nil)
	}
	var c struct {
		Email         string          `json:"email"`
		EmailVerified json.RawMessage `json:"email_verified"`
		Name          string          `json:"name"`
		Picture       string          `json:"picture"`
		HD            string          `json:"hd"`
	}
	if err := idt.Claims(&c); err != nil || idt.Subject == "" || c.Email == "" {
		return Claims{}, f, s.deny(ctx, meta, f.Purpose, DenyInvalidIDToken, nil)
	}
	claims := Claims{
		Subject: idt.Subject, Email: strings.ToLower(c.Email), Name: c.Name, Picture: c.Picture,
		HostedDomain: strings.ToLower(c.HD),
	}
	who := map[string]any{"email": claims.Email, "hd": claims.HostedDomain}
	// Some providers send the flag as a string; only an explicit true passes.
	if v := string(c.EmailVerified); v != "true" && v != `"true"` {
		return Claims{}, f, s.deny(ctx, meta, f.Purpose, DenyEmailUnverfied, who)
	}
	allowed := settings.Get(s.opts.Settings, settings.AllowedDomains)
	if claims.HostedDomain == "" || !slices.Contains(allowed, claims.HostedDomain) {
		return Claims{}, f, s.deny(ctx, meta, f.Purpose, DenyDomain, who)
	}
	return claims, f, nil
}

// deny records login.denied and returns the *DeniedError.
func (s *Service) deny(ctx context.Context, meta Meta, purpose Purpose, reason string, extra map[string]any) error {
	payload := map[string]any{"reason": reason, "purpose": string(purpose)}
	for k, v := range extra {
		payload[k] = v
	}
	actx := systemCtx(ctx, meta.IP)
	if err := s.opts.Store.Tx(actx, func(tx *store.Tx) error {
		return s.opts.Audit.Record(actx, tx, audit.Event{Action: audit.LoginDenied, SubjectType: "login", Payload: payload})
	}); err != nil {
		return err
	}
	return &DeniedError{Reason: reason, Purpose: purpose}
}

// SignIn signs a verified Google account in: it creates the user as a
// member on first sign-in, updates their name and picture otherwise, and
// issues a session. A removed user is refused.
func (s *Service) SignIn(ctx context.Context, c Claims, meta Meta) (User, NewSession, error) {
	var (
		row     identitydb.User
		sess    NewSession
		removed bool
	)
	err := s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		var err error
		row, err = s.upsert(ctx, tx, c, RoleMember, meta)
		if err != nil {
			return err
		}
		if row.DisabledAt.Valid {
			removed = true
			return nil
		}
		uctx := audit.WithActor(ctx, principalOf(row, audit.ViaWeb, "").Actor(meta.IP))
		if sess, err = s.IssueSession(uctx, tx, row.ID, meta); err != nil {
			return err
		}
		return s.opts.Audit.Record(uctx, tx, audit.Event{Action: audit.LoginSucceeded, SubjectType: "user", SubjectID: row.ID})
	})
	if err != nil {
		return User{}, NewSession{}, err
	}
	if removed {
		return User{}, NewSession{}, s.deny(ctx, meta, PurposeSignIn, DenyRemoved, map[string]any{"email": c.Email, "user_id": row.ID})
	}
	return userFromRow(row), sess, nil
}

// upsert creates the account (recording user.created with the new user as
// the actor) or refreshes its profile. A removed account is returned
// unchanged.
func (s *Service) upsert(ctx context.Context, tx *store.Tx, c Claims, role Role, meta Meta) (identitydb.User, error) {
	q := identitydb.New(tx)
	now := sql.NullInt64{Int64: ms(s.now()), Valid: true}
	u, err := q.GetUserBySub(ctx, c.Subject)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		u = identitydb.User{
			ID: s.opts.IDs.NewID(), GoogleSub: c.Subject, Email: c.Email, Name: c.Name,
			PictureUrl: c.Picture, Role: string(role), CreatedAt: now.Int64, LastSignInAt: now,
		}
		if err := q.InsertUser(ctx, identitydb.InsertUserParams{
			ID: u.ID, GoogleSub: u.GoogleSub, Email: u.Email, Name: u.Name, PictureUrl: u.PictureUrl,
			Role: u.Role, CreatedAt: u.CreatedAt, LastSignInAt: u.LastSignInAt,
		}); err != nil {
			return u, fmt.Errorf("identity: create user: %w", err)
		}
		uctx := audit.WithActor(ctx, principalOf(u, audit.ViaWeb, "").Actor(meta.IP))
		return u, s.opts.Audit.Record(uctx, tx, audit.Event{
			Action: audit.UserCreated, SubjectType: "user", SubjectID: u.ID,
			Payload: map[string]any{"email": u.Email, "role": u.Role},
		})
	case err != nil:
		return u, fmt.Errorf("identity: load user: %w", err)
	case u.DisabledAt.Valid:
		return u, nil
	}
	if err := q.UpdateProfile(ctx, identitydb.UpdateProfileParams{
		Email: c.Email, Name: c.Name, PictureUrl: c.Picture, LastSignInAt: now, ID: u.ID,
	}); err != nil {
		return u, fmt.Errorf("identity: update user: %w", err)
	}
	return q.GetUser(ctx, u.ID)
}

// CreateFirstAdmin makes the account that finished setup the first admin,
// inside the setup transaction.
func (s *Service) CreateFirstAdmin(ctx context.Context, tx *store.Tx, c Claims, meta Meta) (User, error) {
	u, err := s.upsert(ctx, tx, c, RoleAdmin, meta)
	if err != nil {
		return User{}, err
	}
	q := identitydb.New(tx)
	if u.DisabledAt.Valid {
		if err := q.EnableUser(ctx, u.ID); err != nil {
			return User{}, fmt.Errorf("identity: enable user: %w", err)
		}
	}
	if Role(u.Role) != RoleAdmin {
		if err := q.SetRole(ctx, identitydb.SetRoleParams{Role: string(RoleAdmin), ID: u.ID}); err != nil {
			return User{}, fmt.Errorf("identity: set role: %w", err)
		}
	}
	row, err := q.GetUser(ctx, u.ID)
	if err != nil {
		return User{}, fmt.Errorf("identity: load user: %w", err)
	}
	return userFromRow(row), nil
}

// PrincipalOf returns the principal of a user, for actions taken right
// after setup created them.
func PrincipalOf(u User) Principal {
	return Principal{UserID: u.ID, Email: u.Email, Name: u.Name, Via: audit.ViaWeb, role: u.Role}
}

// DevLogin signs in as any email with the chosen role. Only in --dev mode.
func (s *Service) DevLogin(ctx context.Context, email string, role Role, meta Meta) (User, NewSession, error) {
	if !s.opts.Dev {
		return User{}, NewSession{}, ErrDevOnly
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if !strings.Contains(email, "@") || !role.valid() {
		return User{}, NewSession{}, &InvalidError{Message: "Enter an email address and a role."}
	}
	var (
		row  identitydb.User
		sess NewSession
	)
	err := s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		c := Claims{Subject: "dev:" + email, Email: email, Name: strings.Split(email, "@")[0]}
		var err error
		if row, err = s.upsert(ctx, tx, c, role, meta); err != nil {
			return err
		}
		q := identitydb.New(tx)
		if err := q.EnableUser(ctx, row.ID); err != nil {
			return err
		}
		if err := q.SetRole(ctx, identitydb.SetRoleParams{Role: string(role), ID: row.ID}); err != nil {
			return err
		}
		if row, err = q.GetUser(ctx, row.ID); err != nil {
			return err
		}
		uctx := audit.WithActor(ctx, principalOf(row, audit.ViaWeb, "").Actor(meta.IP))
		if sess, err = s.IssueSession(uctx, tx, row.ID, meta); err != nil {
			return err
		}
		return s.opts.Audit.Record(uctx, tx, audit.Event{Action: audit.DevLogin, SubjectType: "user", SubjectID: row.ID,
			Payload: map[string]any{"role": string(role)}})
	})
	if err != nil {
		return User{}, NewSession{}, err
	}
	return userFromRow(row), sess, nil
}

func (s *Service) mac(b []byte) []byte {
	m := hmac.New(sha256.New, s.flowKey)
	m.Write(b)
	return m.Sum(nil)
}

func (s *Service) signFlow(f Flow) string {
	b, _ := json.Marshal(f)
	enc := base64.RawURLEncoding
	return enc.EncodeToString(b) + "." + enc.EncodeToString(s.mac(b))
}

func (s *Service) openFlow(v string) (Flow, bool) {
	var f Flow
	body, sig, ok := strings.Cut(v, ".")
	if !ok {
		return f, false
	}
	enc := base64.RawURLEncoding
	b, err1 := enc.DecodeString(body)
	m, err2 := enc.DecodeString(sig)
	if err1 != nil || err2 != nil || !hmac.Equal(m, s.mac(b)) {
		return f, false
	}
	if json.Unmarshal(b, &f) != nil || ms(s.now()) >= f.Expires {
		return Flow{Purpose: f.Purpose}, false
	}
	return f, true
}
