// Package httpapi is the REST transport. It implements the strict server
// interface generated from api/openapi.yaml into package gen. Handlers decode,
// call one module method, and encode; business rules live in the modules.
package httpapi

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/httpapi/gen"
	"github.com/alvaroibarguen/dbairlock/internal/identity"
	"github.com/alvaroibarguen/dbairlock/internal/platform/httpx"
	"github.com/alvaroibarguen/dbairlock/internal/settings"
	"github.com/alvaroibarguen/dbairlock/internal/setup"
)

// Readiness reports whether the instance can serve traffic. A nil error means ready.
type Readiness interface {
	Ready(ctx context.Context) error
}

// Deps are the modules the REST API calls into.
type Deps struct {
	Version   string
	Dev       bool
	Setup     Setup
	Readiness Readiness
	Identity  Identity
	Settings  Settings
}

// Server implements gen.StrictServerInterface.
type Server struct {
	deps Deps
}

var _ gen.StrictServerInterface = (*Server)(nil)

// New returns a Server.
func New(deps Deps) *Server {
	return &Server{deps: deps}
}

// Register mounts every API route on mux.
func (s *Server) Register(mux *http.ServeMux) {
	strict := gen.NewStrictHandlerWithOptions(s, []gen.StrictMiddlewareFunc{s.authenticate}, gen.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  requestError,
		ResponseErrorHandlerFunc: responseError,
	})
	gen.HandlerWithOptions(strict, gen.StdHTTPServerOptions{
		BaseRouter:       mux,
		ErrorHandlerFunc: requestError,
	})
}

// GetHealthz reports liveness.
func (s *Server) GetHealthz(context.Context, gen.GetHealthzRequestObject) (gen.GetHealthzResponseObject, error) {
	return gen.GetHealthz200JSONResponse{Status: gen.Ok}, nil
}

// GetReadyz reports readiness.
func (s *Server) GetReadyz(ctx context.Context, _ gen.GetReadyzRequestObject) (gen.GetReadyzResponseObject, error) {
	if err := s.deps.Readiness.Ready(ctx); err != nil {
		return gen.GetReadyz503JSONResponse{ErrorJSONResponse: gen.ErrorJSONResponse{
			Error: gen.ErrorBody{Code: "not_ready", Message: "The instance is not ready."},
		}}, nil
	}
	return gen.GetReadyz200JSONResponse{Status: gen.Ok}, nil
}

// GetMeta returns public instance metadata.
func (s *Server) GetMeta(ctx context.Context, _ gen.GetMetaRequestObject) (gen.GetMetaResponseObject, error) {
	required, err := s.deps.Setup.SetupRequired(ctx)
	if err != nil {
		return nil, err
	}
	state := gen.Ready
	if required {
		state = gen.SetupRequired
	}
	name := "dbairlock"
	if s.deps.Settings != nil {
		name = settings.Get(s.deps.Settings, settings.InstanceName)
	}
	return gen.GetMeta200JSONResponse{
		Version: s.deps.Version, SetupState: state, InstanceName: name, DevLogin: s.deps.Dev,
	}, nil
}

// Errors specific to the transport.
var (
	errSetupRequired = httpx.NewError(http.StatusServiceUnavailable, "setup_required",
		"dbairlock is not set up yet. Open /setup and enter the setup code from the server log.")
	errRateLimited = httpx.NewError(http.StatusTooManyRequests, "rate_limited",
		"Too many attempts. Wait a moment and try again.")
)

// errorTable maps module sentinel errors to HTTP errors. It is the only place
// where a domain error becomes a status code.
var errorTable = []struct {
	err  error
	http *httpx.Error
}{
	{identity.ErrUnauthenticated, httpx.ErrUnauthorized},
	{identity.ErrForbidden, httpx.ErrForbidden},
	{identity.ErrNotFound, httpx.ErrNotFound},
	{identity.ErrDevOnly, httpx.ErrNotFound},
	{identity.ErrLastAdmin, httpx.NewError(http.StatusConflict, "last_admin",
		"There must always be at least one admin. Make someone else an admin first.")},
	{identity.ErrNotConfigured, httpx.NewError(http.StatusConflict, "sign_in_not_configured",
		"Google sign-in is not configured yet.")},
	{setup.ErrInvalidCode, httpx.NewError(http.StatusUnauthorized, "invalid_code",
		"That code is not valid. Check it and try again.")},
	{setup.ErrComplete, httpx.NewError(http.StatusConflict, "setup_complete",
		"Setup is already complete. Sign in instead.")},
	{setup.ErrNoGrant, httpx.NewError(http.StatusUnauthorized, "setup_session_required",
		"Your setup session has ended. Enter the code again.")},
}

// mapError converts an error returned by a module into an *httpx.Error, or
// returns it unchanged (and so rendered as a generic 500) if it is unknown.
func mapError(err error) error {
	var he *httpx.Error
	if errors.As(err, &he) {
		return he
	}
	var inv *identity.InvalidError
	if errors.As(err, &inv) {
		return httpx.NewError(http.StatusUnprocessableEntity, "invalid", inv.Message)
	}
	var ve *settings.ValidationError
	if errors.As(err, &ve) {
		fields := make([]httpx.FieldError, len(ve.Fields))
		for i, f := range ve.Fields {
			fields[i] = httpx.FieldError{Field: f.Field, Message: f.Message}
		}
		return httpx.NewValidationError("Some settings are invalid. Nothing was changed.", fields)
	}
	if _, ok := retryAfter(err); ok {
		return errRateLimited
	}
	for _, row := range errorTable {
		if errors.Is(err, row.err) {
			return row.http
		}
	}
	return err
}

// retryAfter extracts the wait of a rate-limit error from either module.
func retryAfter(err error) (time.Duration, bool) {
	var a *identity.RateLimitedError
	if errors.As(err, &a) {
		return a.RetryAfter, true
	}
	var b *setup.RateLimitedError
	if errors.As(err, &b) {
		return b.RetryAfter, true
	}
	return 0, false
}

// responseError handles errors returned by handlers.
func responseError(w http.ResponseWriter, r *http.Request, err error) {
	if d, ok := retryAfter(err); ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(d.Seconds()))))
	}
	httpx.WriteError(w, r, mapError(err))
}

// requestError handles errors decoding a request (parameters or body).
func requestError(w http.ResponseWriter, r *http.Request, err error) {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		httpx.WriteError(w, r, httpx.ErrRequestTooLarge)
		return
	}
	httpx.WriteError(w, r, httpx.NewError(http.StatusBadRequest, "bad_request", "The request is invalid: "+err.Error()))
}

// SetupGate answers 503 setup_required on every API route except /api/meta,
// /api/setup/* and the Google callback (which finishes setup), and on /mcp,
// while the instance is in setup mode. Health probes and the dashboard
// itself stay available.
func SetupGate(st SetupStatus) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := r.URL.Path
			gated := (strings.HasPrefix(p, "/api/") || p == "/mcp" || strings.HasPrefix(p, "/mcp/")) &&
				p != "/api/meta" && !strings.HasPrefix(p, "/api/setup/") && p != identity.CallbackPath
			if gated {
				required, err := st.SetupRequired(r.Context())
				if err != nil {
					httpx.WriteError(w, r, fmt.Errorf("setup gate: %w", err))
					return
				}
				if required {
					w.Header().Set("Retry-After", "60")
					httpx.WriteError(w, r, errSetupRequired)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
