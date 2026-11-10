package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/httpapi/gen"
	"github.com/alvaroibarguen/dbairlock/internal/identity"
	"github.com/alvaroibarguen/dbairlock/internal/platform/httpx"
	"github.com/alvaroibarguen/dbairlock/internal/platform/logx"
	"github.com/alvaroibarguen/dbairlock/internal/settings"
)

// Identity is the identity module as the REST API uses it.
type Identity interface {
	AuthenticateSession(ctx context.Context, token string) (identity.Principal, error)
	BeginLogin(ctx context.Context, purpose identity.Purpose, next string, meta identity.Meta) (string, string, error)
	FinishLogin(ctx context.Context, flowCookie string, p identity.CallbackParams, meta identity.Meta) (identity.Claims, identity.Flow, error)
	SignIn(ctx context.Context, c identity.Claims, meta identity.Meta) (identity.User, identity.NewSession, error)
	SignOut(ctx context.Context, p identity.Principal, token string) error
	SignOutEverywhere(ctx context.Context, p identity.Principal) error
	DevLogin(ctx context.Context, email string, role identity.Role, meta identity.Meta) (identity.User, identity.NewSession, error)
	Me(ctx context.Context, p identity.Principal) (identity.User, error)
	ListUsers(ctx context.Context, p identity.Principal) ([]identity.User, error)
	SetRole(ctx context.Context, p identity.Principal, id string, role identity.Role) (identity.User, error)
	RemoveUser(ctx context.Context, p identity.Principal, id string) (identity.User, error)
	RestoreUser(ctx context.Context, p identity.Principal, id string) (identity.User, error)
	CreateToken(ctx context.Context, p identity.Principal, name string, expiresInDays int) (identity.CreatedToken, error)
	ListTokens(ctx context.Context, p identity.Principal, all bool) ([]identity.Token, error)
	RevokeToken(ctx context.Context, p identity.Principal, id string) error
}

// Cookie names. With TLS (or anywhere outside --dev on loopback) they carry
// the __Host- prefix, which browsers accept only with Secure, Path=/ and no
// Domain, so no other host or path can set or shadow them.
const (
	cookieSession = "session"
	cookieFlow    = "oidc"
	cookieSetup   = "setup"
)

type httpKey struct{}

type httpPair struct {
	w http.ResponseWriter
	r *http.Request
}

func pairFrom(ctx context.Context) httpPair {
	p, _ := ctx.Value(httpKey{}).(httpPair)
	return p
}

// authenticate runs before every operation. It makes the request and
// response available to handlers that set cookies, and, if the request
// carries a valid session cookie, puts the principal and its audit actor in
// the context. It then enforces the operation's declared access level
// (access.go), refusing operations that declare none; handlers authorize
// again with requireUser or authorize, against the specific resource.
func (s *Server) authenticate(f gen.StrictHandlerFunc, operationID string) gen.StrictHandlerFunc {
	access, declared := OperationAccess(operationID)
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, req any) (any, error) {
		if !declared || access.Level == LevelToken {
			logx.FromContext(ctx).ErrorContext(ctx, "operation has no declared access level", slog.String("operation", operationID))
			return nil, identity.ErrForbidden
		}
		if access.Level == LevelDev && !s.deps.Dev {
			// The handler refuses too; refusing here answers 404 before
			// the request is validated against the contract.
			return nil, identity.ErrDevOnly
		}
		ctx = context.WithValue(ctx, httpKey{}, httpPair{w: w, r: r})
		if s.deps.Identity != nil {
			if token := s.readCookie(r, cookieSession); token != "" {
				p, err := s.deps.Identity.AuthenticateSession(ctx, token)
				switch {
				case err == nil:
					ctx = identity.WithPrincipal(ctx, p, httpx.ClientIPFrom(ctx))
				case errors.Is(err, identity.ErrUnauthenticated):
					s.clearCookie(w, r, cookieSession)
				default:
					return nil, err
				}
			}
		}
		if access.Level == LevelUser {
			p, err := requireUser(ctx)
			if err == nil && access.Action != "" {
				err = identity.Can(p, access.Action, identity.Instance)
			}
			if err != nil {
				return nil, err
			}
		}
		return f(ctx, w, r, req)
	}
}

// requireUser returns the signed-in principal, or ErrUnauthenticated.
func requireUser(ctx context.Context) (identity.Principal, error) {
	p, ok := identity.PrincipalFrom(ctx)
	if !ok {
		return identity.Principal{}, identity.ErrUnauthenticated
	}
	return p, nil
}

// authorize returns the principal if identity.Can allows the action.
func authorize(ctx context.Context, a identity.Action, r identity.Resource) (identity.Principal, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return p, err
	}
	return p, identity.Can(p, a, r)
}

func meta(ctx context.Context) identity.Meta {
	m := identity.Meta{IP: httpx.ClientIPFrom(ctx)}
	if p := pairFrom(ctx); p.r != nil {
		m.UserAgent = p.r.UserAgent()
	}
	return m
}

// secureCookies is false only in --dev mode over plain HTTP on a loopback
// host, where browsers would refuse a __Host- cookie on some setups.
func (s *Server) secureCookies(r *http.Request) bool {
	if !s.deps.Dev || r.TLS != nil {
		return true
	}
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	return !settings.IsLoopbackHost(host)
}

func (s *Server) cookieName(r *http.Request, base string) string {
	if s.secureCookies(r) {
		return "__Host-dbairlock_" + base
	}
	return "dbairlock_" + base
}

func (s *Server) readCookie(r *http.Request, base string) string {
	c, err := r.Cookie(s.cookieName(r, base))
	if err != nil {
		return ""
	}
	return c.Value
}

func (s *Server) setCookie(w http.ResponseWriter, r *http.Request, base, value string, maxAge time.Duration) {
	// Secure is dropped only in --dev mode over plain HTTP on loopback.
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // see secureCookies
		Name: s.cookieName(r, base), Value: value, Path: "/",
		MaxAge: int(maxAge.Seconds()), HttpOnly: true, Secure: s.secureCookies(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearCookie(w http.ResponseWriter, r *http.Request, base string) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // see secureCookies
		Name: s.cookieName(r, base), Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.secureCookies(r), SameSite: http.SameSiteLaxMode,
	})
}

func redirect(to string) gen.RedirectResponse {
	return gen.RedirectResponse{Headers: gen.RedirectResponseHeaders{Location: to}}
}

// withError appends an error code to a page path.
func withError(page, code string) string {
	return page + "?error=" + url.QueryEscape(code)
}

// loginErrorCode turns a failure to start or finish a sign-in into a code
// for the sign-in or setup page. Unexpected errors are logged.
func loginErrorCode(ctx context.Context, err error) string {
	var de *identity.DeniedError
	switch {
	case errors.As(err, &de):
		return de.Reason
	case errors.Is(err, identity.ErrNotConfigured):
		return identity.DenyNotConfigured
	case errors.Is(err, identity.ErrRateLimited):
		return "rate_limited"
	}
	logx.FromContext(ctx).ErrorContext(ctx, "sign-in failed", slog.Any("error", err))
	return "provider_unreachable"
}

// StartGoogleSignIn redirects to Google.
func (s *Server) StartGoogleSignIn(ctx context.Context, req gen.StartGoogleSignInRequestObject) (gen.StartGoogleSignInResponseObject, error) {
	next := ""
	if req.Params.Next != nil {
		next = *req.Params.Next
	}
	to, flow, err := s.deps.Identity.BeginLogin(ctx, identity.PurposeSignIn, next, meta(ctx))
	if err != nil {
		return gen.StartGoogleSignIn302Response(redirect(withError("/signin", loginErrorCode(ctx, err)))), nil
	}
	hp := pairFrom(ctx)
	s.setCookie(hp.w, hp.r, cookieFlow, flow, identity.FlowLifetime)
	return gen.StartGoogleSignIn302Response(redirect(to)), nil
}

// GoogleCallback finishes a sign-in started for signing in, setup or
// recovery, and redirects to the right page.
func (s *Server) GoogleCallback(ctx context.Context, req gen.GoogleCallbackRequestObject) (gen.GoogleCallbackResponseObject, error) {
	hp := pairFrom(ctx)
	flowCookie := s.readCookie(hp.r, cookieFlow)
	s.clearCookie(hp.w, hp.r, cookieFlow)
	params := identity.CallbackParams{}
	if req.Params.Code != nil {
		params.Code = *req.Params.Code
	}
	if req.Params.State != nil {
		params.State = *req.Params.State
	}
	if req.Params.Error != nil {
		params.Error = *req.Params.Error
	}
	m := meta(ctx)
	claims, flow, err := s.deps.Identity.FinishLogin(ctx, flowCookie, params, m)
	page := "/signin"
	if flow.Purpose == identity.PurposeSetup || flow.Purpose == identity.PurposeRecovery {
		page = "/setup"
	}
	if err != nil {
		return gen.GoogleCallback302Response(redirect(withError(page, loginErrorCode(ctx, err)))), nil
	}

	switch flow.Purpose {
	case identity.PurposeSetup, identity.PurposeRecovery:
		grant := s.readCookie(hp.r, cookieSetup)
		if flow.Purpose == identity.PurposeRecovery {
			if err := s.deps.Setup.ConfirmRecovery(ctx, grant, claims, m); err != nil {
				return gen.GoogleCallback302Response(redirect(withError(page, setupErrorCode(ctx, err)))), nil
			}
			s.clearCookie(hp.w, hp.r, cookieSetup)
			return gen.GoogleCallback302Response(redirect("/signin?recovered=1")), nil
		}
		_, sess, err := s.deps.Setup.Complete(ctx, grant, claims, m)
		if err != nil {
			return gen.GoogleCallback302Response(redirect(withError(page, setupErrorCode(ctx, err)))), nil
		}
		s.clearCookie(hp.w, hp.r, cookieSetup)
		s.setCookie(hp.w, hp.r, cookieSession, sess.Token, identity.SessionAbsoluteMax)
		return gen.GoogleCallback302Response(redirect("/")), nil
	default:
		_, sess, err := s.deps.Identity.SignIn(ctx, claims, m)
		if err != nil {
			return gen.GoogleCallback302Response(redirect(withError(page, loginErrorCode(ctx, err)))), nil
		}
		s.setCookie(hp.w, hp.r, cookieSession, sess.Token, identity.SessionAbsoluteMax)
		return gen.GoogleCallback302Response(redirect(identity.SafeNext(flow.Next))), nil
	}
}

// SignOut ends this browser's session.
func (s *Server) SignOut(ctx context.Context, _ gen.SignOutRequestObject) (gen.SignOutResponseObject, error) {
	hp := pairFrom(ctx)
	p, _ := identity.PrincipalFrom(ctx)
	if token := s.readCookie(hp.r, cookieSession); token != "" {
		if err := s.deps.Identity.SignOut(ctx, p, token); err != nil {
			return nil, err
		}
	}
	s.clearCookie(hp.w, hp.r, cookieSession)
	return gen.SignOut204Response{}, nil
}

// SignOutEverywhere ends all of the caller's sessions.
func (s *Server) SignOutEverywhere(ctx context.Context, _ gen.SignOutEverywhereRequestObject) (gen.SignOutEverywhereResponseObject, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.deps.Identity.SignOutEverywhere(ctx, p); err != nil {
		return nil, err
	}
	hp := pairFrom(ctx)
	s.clearCookie(hp.w, hp.r, cookieSession)
	return gen.SignOutEverywhere204Response{}, nil
}

// DevLogin signs in as anyone, in --dev mode only.
func (s *Server) DevLogin(ctx context.Context, req gen.DevLoginRequestObject) (gen.DevLoginResponseObject, error) {
	if !s.deps.Dev {
		return nil, httpx.ErrNotFound
	}
	_, sess, err := s.deps.Identity.DevLogin(ctx, req.Body.Email, identity.Role(req.Body.Role), meta(ctx))
	if err != nil {
		return nil, err
	}
	hp := pairFrom(ctx)
	s.setCookie(hp.w, hp.r, cookieSession, sess.Token, identity.SessionAbsoluteMax)
	return gen.DevLogin204Response{}, nil
}

// GetMe returns the signed-in user and what they may do.
func (s *Server) GetMe(ctx context.Context, _ gen.GetMeRequestObject) (gen.GetMeResponseObject, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	u, err := s.deps.Identity.Me(ctx, p)
	if err != nil {
		return nil, err
	}
	perms := []gen.Permission{}
	for _, a := range identity.Permissions(p) {
		perms = append(perms, gen.Permission(a))
	}
	return gen.GetMe200JSONResponse{User: userBody(u), Permissions: perms}, nil
}
