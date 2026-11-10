// Package app is the composition root. It builds every module in dependency
// order and wires them together. Later phases add modules here and nowhere
// else.
package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/httpapi"
	"github.com/alvaroibarguen/dbairlock/internal/platform/clock"
	"github.com/alvaroibarguen/dbairlock/internal/platform/httpx"
	"github.com/alvaroibarguen/dbairlock/internal/platform/ids"
	"github.com/alvaroibarguen/dbairlock/internal/secrets"
	"github.com/alvaroibarguen/dbairlock/internal/settings"
	"github.com/alvaroibarguen/dbairlock/internal/store"
	"github.com/alvaroibarguen/dbairlock/internal/webui"
)

// ShutdownTimeout is how long a stopping server waits for in-flight requests
// and background work. A compile-time constant: it must stay below the
// default grace period of container orchestrators (30 seconds).
const ShutdownTimeout = 20 * time.Second

// Options configure an App. They come from the process flags only.
type Options struct {
	DataDir string
	Listen  string
	Dev     bool
	Version string
	Logger  *slog.Logger

	// Optional overrides for tests.
	Clock clock.Clock
	IDs   ids.Generator
	WebUI fs.FS
}

// App is a fully wired dbairlock instance.
type App struct {
	opts     Options
	log      *slog.Logger
	store    *store.Store
	secrets  *secrets.Service
	settings *settings.Service
	handler  http.Handler
}

// New builds an App.
func New(opts Options) (*App, error) {
	if opts.Logger == nil {
		return nil, errors.New("app: logger is required")
	}
	if opts.Clock == nil {
		opts.Clock = clock.Real{}
	}
	if opts.IDs == nil {
		opts.IDs = ids.UUIDv7{}
	}
	if opts.WebUI == nil {
		opts.WebUI = webui.FS()
	}
	if opts.DataDir == "" {
		return nil, errors.New("app: data directory is required")
	}

	if opts.Dev {
		addr, changed := loopbackOnly(opts.Listen)
		if changed {
			opts.Logger.Warn("dev mode binds to loopback only", slog.String("requested", opts.Listen), slog.String("using", addr))
		}
		opts.Listen = addr
		opts.Logger.Warn("DEV MODE: not for production use")
	}

	ctx := context.Background()
	// Opening the store creates the data directory (0700), the database, and
	// applies migrations. It refuses a database newer than this binary.
	st, err := store.Open(ctx, opts.DataDir)
	if err != nil {
		return nil, fmt.Errorf("app: %w", err)
	}
	a, err := build(ctx, opts, st)
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("app: %w", err)
	}
	return a, nil
}

func build(ctx context.Context, opts Options, st *store.Store) (*App, error) {
	recorder := audit.NewRecorder(opts.Clock, opts.IDs)

	// Loading the secret store reads or creates master.key and refuses to
	// start if it does not belong to this database.
	sec, err := secrets.Load(ctx, secrets.Options{
		DataDir: opts.DataDir, Store: st, Audit: recorder,
		Clock: opts.Clock, IDs: opts.IDs, Logger: opts.Logger,
	})
	if err != nil {
		return nil, err
	}
	// Transports and settings get the write-only side of the secret store.
	// Only targets, identity and notify will be given a secrets.Opener.
	var secretWriter secrets.Writer = sec

	set, err := settings.New(ctx, settings.Options{
		Store: st, Audit: recorder, Secrets: secretWriter,
		Clock: opts.Clock, Logger: opts.Logger,
	})
	if err != nil {
		return nil, err
	}

	api := httpapi.New(httpapi.Deps{
		Version:   opts.Version,
		Setup:     setupStub{},
		Readiness: st,
		Access:    accessStub{dev: opts.Dev},
		Settings:  set,
	})

	mux := http.NewServeMux()
	api.Register(mux)
	mux.Handle("/", httpx.SPA(opts.WebUI))

	// Mutating requests from another origin are rejected (PLAN 5.8).
	csrf := http.NewCrossOriginProtection()
	csrf.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, r, httpx.NewError(http.StatusForbidden, "cross_origin", "Cross-origin requests are not allowed."))
	}))

	handler := httpx.Chain(csrf.Handler(mux),
		httpx.RequestID(opts.IDs, opts.Logger),
		httpx.AccessLog(opts.Clock),
		httpx.Recover(),
		httpx.SecurityHeaders(),
		httpx.BodyLimit(httpx.DefaultMaxBodyBytes),
	)

	return &App{opts: opts, log: opts.Logger, store: st, secrets: sec, settings: set, handler: handler}, nil
}

// Close releases the store. Call it once the server has stopped.
func (a *App) Close() error { return a.store.Close() }

// AuditResult is the outcome of VerifyAudit.
type AuditResult = audit.Result

// ErrNoDatabase means VerifyAudit found no database in the data directory.
var ErrNoDatabase = store.ErrNotFound

// VerifyAudit opens the existing database in dataDir and walks the audit
// hash chain. It never creates a database. A broken chain is reported in the
// result, not as an error; an error means the check could not run.
func VerifyAudit(ctx context.Context, dataDir string) (AuditResult, error) {
	st, err := store.OpenExisting(ctx, dataDir)
	if err != nil {
		return AuditResult{}, err
	}
	defer func() { _ = st.Close() }()
	return audit.NewVerifier(st.Reader()).Verify(ctx)
}

// Handler returns the root HTTP handler.
func (a *App) Handler() http.Handler { return a.handler }

// Run runs background work until ctx is cancelled. There is none yet; the
// executor joins in a later phase.
func (a *App) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

// Serve listens on opts.Listen, serves HTTP and runs background work until ctx
// is cancelled, then shuts down gracefully within ShutdownTimeout.
func (a *App) Serve(ctx context.Context) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", a.opts.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", a.opts.Listen, err)
	}
	return a.serve(ctx, ln)
}

func (a *App) serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           a.handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(a.log.Handler(), slog.LevelWarn),
		BaseContext:       func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}

	runCtx, stopRun := context.WithCancel(ctx)
	defer stopRun()
	runDone := make(chan error, 1)
	go func() { runDone <- a.Run(runCtx) }()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	a.log.Info("dbairlock listening", slog.String("addr", ln.Addr().String()),
		slog.String("version", a.opts.Version), slog.Bool("dev", a.opts.Dev))

	select {
	case err := <-serveErr:
		stopRun()
		return errors.Join(fmt.Errorf("http server: %w", err), <-runDone)
	case <-ctx.Done():
	}

	a.log.Info("shutting down", slog.Duration("timeout", ShutdownTimeout))
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ShutdownTimeout)
	defer cancel()

	var errs []error
	if err := srv.Shutdown(shutdownCtx); err != nil {
		errs = append(errs, fmt.Errorf("http shutdown: %w", err))
	}
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		errs = append(errs, fmt.Errorf("http server: %w", err))
	}
	stopRun()
	select {
	case err := <-runDone:
		if err != nil {
			errs = append(errs, fmt.Errorf("background work: %w", err))
		}
	case <-shutdownCtx.Done():
		errs = append(errs, errors.New("background work did not stop in time"))
	}
	a.log.Info("stopped")
	return errors.Join(errs...)
}

// loopbackOnly rewrites a listen address so it binds to 127.0.0.1 unless it
// already names a loopback host. It reports whether the address changed.
func loopbackOnly(listen string) (string, bool) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen, false // Listen will report the error.
	}
	if host == "localhost" {
		return listen, false
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return listen, false
	}
	return net.JoinHostPort("127.0.0.1", port), true
}

// RotateResult is the outcome of RotateKey.
type RotateResult = secrets.RotateResult

// RotateKey replaces the master key of the instance in dataDir and
// re-encrypts every secret (see secrets.Rotate). The server must be stopped.
func RotateKey(ctx context.Context, dataDir string, logger *slog.Logger) (RotateResult, error) {
	st, err := store.OpenExisting(ctx, dataDir)
	if err != nil {
		return RotateResult{}, err
	}
	defer func() { _ = st.Close() }()
	c, g := clock.Real{}, ids.UUIDv7{}
	return secrets.Rotate(audit.WithActor(ctx, audit.Actor{Via: audit.ViaCLI}), secrets.Options{
		DataDir: dataDir, Store: st, Audit: audit.NewRecorder(c, g),
		Clock: c, IDs: g, Logger: logger,
	})
}

// setupStub stands in for the setup module until Phase 4.
type setupStub struct{}

func (setupStub) SetupRequired(context.Context) (bool, error) { return false, nil }

// devAdmin is the actor of every admin request in --dev mode until the
// identity module (Phase 4) provides real sign-in.
var devAdmin = audit.Actor{UserID: "dev-admin", Email: "admin@dev.localhost", Via: audit.ViaWeb}

// errNoSignIn is returned for admin routes outside --dev mode until Phase 4.
var errNoSignIn = httpx.NewError(http.StatusUnauthorized, "unauthorized",
	"Sign-in is not available yet. Start dbairlock with --dev to manage settings.")

// accessStub stands in for identity's RequireAdmin until Phase 4. In --dev
// mode (loopback only) every request acts as devAdmin; otherwise admin routes
// are refused.
type accessStub struct{ dev bool }

func (a accessStub) RequireAdmin(ctx context.Context) (context.Context, error) {
	if !a.dev {
		return ctx, errNoSignIn
	}
	return audit.WithActor(ctx, devAdmin), nil
}
