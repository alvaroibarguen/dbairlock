// Package httpapi is the REST transport. It implements the strict server
// interface generated from api/openapi.yaml into package gen. Handlers decode,
// call one module method, and encode; business rules live in the modules.
package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/alvaroibarguen/dbairlock/internal/httpapi/gen"
	"github.com/alvaroibarguen/dbairlock/internal/platform/httpx"
)

// SetupStatus reports whether the instance still needs its first-run setup.
type SetupStatus interface {
	SetupRequired(ctx context.Context) (bool, error)
}

// Readiness reports whether the instance can serve traffic. A nil error means ready.
type Readiness interface {
	Ready(ctx context.Context) error
}

// Access authorizes a request. Until the identity module arrives (Phase 4)
// the composition root supplies a stub.
type Access interface {
	// RequireAdmin returns a context carrying the admin's audit.Actor, or an
	// *httpx.Error (unauthorized or forbidden).
	RequireAdmin(ctx context.Context) (context.Context, error)
}

// Deps are the modules the REST API calls into.
type Deps struct {
	Version   string
	Setup     SetupStatus
	Readiness Readiness
	Access    Access
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
	strict := gen.NewStrictHandlerWithOptions(s, nil, gen.StrictHTTPServerOptions{
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
	return gen.GetMeta200JSONResponse{Version: s.deps.Version, SetupState: state}, nil
}

// errorTable maps module sentinel errors to HTTP errors. It is the only place
// where a domain error becomes a status code. Later phases add rows here.
var errorTable = []struct {
	err  error
	http *httpx.Error
}{}

// mapError converts an error returned by a module into an *httpx.Error, or
// returns it unchanged (and so rendered as a generic 500) if it is unknown.
func mapError(err error) error {
	var he *httpx.Error
	if errors.As(err, &he) {
		return he
	}
	for _, row := range errorTable {
		if errors.Is(err, row.err) {
			return row.http
		}
	}
	return err
}

// responseError handles errors returned by handlers.
func responseError(w http.ResponseWriter, r *http.Request, err error) {
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
