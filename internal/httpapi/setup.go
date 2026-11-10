package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/alvaroibarguen/dbairlock/internal/httpapi/gen"
	"github.com/alvaroibarguen/dbairlock/internal/identity"
	"github.com/alvaroibarguen/dbairlock/internal/platform/logx"
	"github.com/alvaroibarguen/dbairlock/internal/secrets"
	"github.com/alvaroibarguen/dbairlock/internal/setup"
)

// SetupStatus reports whether the instance still needs its first-run setup.
type SetupStatus interface {
	SetupRequired(ctx context.Context) (bool, error)
}

// Setup is the setup module as the REST API uses it.
type Setup interface {
	SetupStatus
	VerifyCode(ctx context.Context, code string, meta identity.Meta) (string, error)
	VerifyRecovery(ctx context.Context, code string, meta identity.Meta) (string, error)
	Grant(ctx context.Context, token string) (setup.Kind, error)
	SignInSettings(ctx context.Context, token string) (setup.SignIn, error)
	SaveSignIn(ctx context.Context, token string, u setup.SignInUpdate, meta identity.Meta) (setup.SignIn, error)
	Complete(ctx context.Context, token string, c identity.Claims, meta identity.Meta) (identity.User, identity.NewSession, error)
	ConfirmRecovery(ctx context.Context, token string, c identity.Claims, meta identity.Meta) error
}

func setupErrorCode(ctx context.Context, err error) string {
	switch {
	case errors.Is(err, setup.ErrNoGrant):
		return "setup_session_expired"
	case errors.Is(err, setup.ErrComplete):
		return "setup_complete"
	}
	logx.FromContext(ctx).ErrorContext(ctx, "setup sign-in failed", slog.Any("error", err))
	return "internal"
}

// VerifySetupCode exchanges the setup code for a setup session cookie.
func (s *Server) VerifySetupCode(ctx context.Context, req gen.VerifySetupCodeRequestObject) (gen.VerifySetupCodeResponseObject, error) {
	token, err := s.deps.Setup.VerifyCode(ctx, deref(req.Body.Code), meta(ctx))
	if err != nil {
		return nil, err
	}
	hp := pairFrom(ctx)
	s.setCookie(hp.w, hp.r, cookieSetup, token, setup.SessionLifetime)
	return gen.VerifySetupCode204Response{}, nil
}

// VerifyRecoveryCode exchanges a recovery code for a recovery session cookie.
func (s *Server) VerifyRecoveryCode(ctx context.Context, req gen.VerifyRecoveryCodeRequestObject) (gen.VerifyRecoveryCodeResponseObject, error) {
	token, err := s.deps.Setup.VerifyRecovery(ctx, deref(req.Body.Code), meta(ctx))
	if err != nil {
		return nil, err
	}
	hp := pairFrom(ctx)
	s.setCookie(hp.w, hp.r, cookieSetup, token, setup.SessionLifetime)
	return gen.VerifyRecoveryCode204Response{}, nil
}

// GetSetupSession reports which grant the browser holds.
func (s *Server) GetSetupSession(ctx context.Context, _ gen.GetSetupSessionRequestObject) (gen.GetSetupSessionResponseObject, error) {
	kind, err := s.deps.Setup.Grant(ctx, s.readCookie(pairFrom(ctx).r, cookieSetup))
	if err != nil {
		return nil, err
	}
	return gen.GetSetupSession200JSONResponse{Kind: gen.SetupSessionKind(kind)}, nil
}

// GetSetupSignIn returns the sign-in settings for the wizard.
func (s *Server) GetSetupSignIn(ctx context.Context, _ gen.GetSetupSignInRequestObject) (gen.GetSetupSignInResponseObject, error) {
	hp := pairFrom(ctx)
	v, err := s.deps.Setup.SignInSettings(ctx, s.readCookie(hp.r, cookieSetup))
	if err != nil {
		return nil, err
	}
	return gen.GetSetupSignIn200JSONResponse(signInBody(v, hp.r)), nil
}

// PutSetupSignIn stores the sign-in settings.
func (s *Server) PutSetupSignIn(ctx context.Context, req gen.PutSetupSignInRequestObject) (gen.PutSetupSignInResponseObject, error) {
	hp := pairFrom(ctx)
	b := req.Body
	u := setup.SignInUpdate{
		PublicURL: b.PublicUrl, ClientID: b.ClientId, AllowedDomains: b.AllowedDomains, Issuer: b.OidcIssuer,
	}
	if b.ClientSecret != nil {
		v := secrets.NewValue(*b.ClientSecret)
		u.ClientSecret = &v
	}
	v, err := s.deps.Setup.SaveSignIn(ctx, s.readCookie(hp.r, cookieSetup), u, meta(ctx))
	if err != nil {
		return nil, err
	}
	return gen.PutSetupSignIn200JSONResponse(signInBody(v, hp.r)), nil
}

// StartSetupGoogle starts the Google sign-in that finishes setup or confirms
// a recovery.
func (s *Server) StartSetupGoogle(ctx context.Context, _ gen.StartSetupGoogleRequestObject) (gen.StartSetupGoogleResponseObject, error) {
	hp := pairFrom(ctx)
	kind, err := s.deps.Setup.Grant(ctx, s.readCookie(hp.r, cookieSetup))
	if err != nil {
		return gen.StartSetupGoogle302Response(redirect(withError("/setup", setupErrorCode(ctx, err)))), nil
	}
	purpose := identity.PurposeSetup
	if kind == setup.KindRecovery {
		purpose = identity.PurposeRecovery
	}
	to, flow, err := s.deps.Identity.BeginLogin(ctx, purpose, "/", meta(ctx))
	if err != nil {
		return gen.StartSetupGoogle302Response(redirect(withError("/setup", loginErrorCode(ctx, err)))), nil
	}
	s.setCookie(hp.w, hp.r, cookieFlow, flow, identity.FlowLifetime)
	return gen.StartSetupGoogle302Response(redirect(to)), nil
}

func signInBody(v setup.SignIn, r *http.Request) gen.SetupSignIn {
	return gen.SetupSignIn{
		PublicUrl: v.PublicURL, SuggestedPublicUrl: requestOrigin(r), ClientId: v.ClientID,
		ClientSecret: secretStatus(v.ClientSecret), AllowedDomains: nonNil(v.AllowedDomains),
		RedirectUri: v.RedirectURI,
	}
}

// requestOrigin is the origin the request arrived on, offered as the public
// URL for the admin to confirm; it is never saved without that.
func requestOrigin(r *http.Request) string {
	if r == nil || r.Host == "" {
		return ""
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
