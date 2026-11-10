// Package app is the composition root. It builds every module in dependency
// order and wires them together. Later phases add modules here and nowhere
// else.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/changes"
	"github.com/alvaroibarguen/dbairlock/internal/connector"
	"github.com/alvaroibarguen/dbairlock/internal/connector/mysql"
	"github.com/alvaroibarguen/dbairlock/internal/connector/postgres"
	"github.com/alvaroibarguen/dbairlock/internal/executor"
	"github.com/alvaroibarguen/dbairlock/internal/httpapi"
	"github.com/alvaroibarguen/dbairlock/internal/identity"
	"github.com/alvaroibarguen/dbairlock/internal/mcpserver"
	"github.com/alvaroibarguen/dbairlock/internal/notify"
	"github.com/alvaroibarguen/dbairlock/internal/platform/clock"
	"github.com/alvaroibarguen/dbairlock/internal/platform/httpx"
	"github.com/alvaroibarguen/dbairlock/internal/platform/ids"
	"github.com/alvaroibarguen/dbairlock/internal/secrets"
	"github.com/alvaroibarguen/dbairlock/internal/settings"
	"github.com/alvaroibarguen/dbairlock/internal/setup"
	"github.com/alvaroibarguen/dbairlock/internal/store"
	"github.com/alvaroibarguen/dbairlock/internal/targets"
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
	Commit  string // VCS revision, shown in Settings > System; empty means unknown
	Logger  *slog.Logger

	// Banner receives the setup-code block printed in setup mode (stderr).
	Banner io.Writer

	// Optional overrides for tests.
	Clock      clock.Clock
	IDs        ids.Generator
	WebUI      fs.FS
	HTTPClient *http.Client // talks to the OpenID provider
}

// App is a fully wired dbairlock instance.
type App struct {
	opts     Options
	log      *slog.Logger
	store    *store.Store
	secrets  *secrets.Service
	settings *settings.Service
	identity *identity.Service
	setup    *setup.Service
	targets  *targets.Service
	changes  *changes.Service
	executor *executor.Executor
	notify   *notify.Service
	handler  http.Handler
	routes   []registeredRoute
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
	mods, err := buildModules(ctx, opts, st, recorder, sec)
	if err != nil {
		return nil, err
	}
	// Each start in setup mode prints a new setup code.
	if err := mods.setup.Start(ctx); err != nil {
		return nil, err
	}

	api := httpapi.New(httpapi.Deps{
		Version:    opts.Version,
		Dev:        opts.Dev,
		Setup:      mods.setup,
		Readiness:  st,
		Identity:   mods.identity,
		OAuth:      mods.identity,
		Settings:   mods.settings,
		Targets:    mods.targets,
		Changes:    mods.changes,
		AuditLog:   audit.NewReader(st.Reader()),
		AuditCheck: audit.NewChecker(st.Reader(), st, recorder, opts.Clock, opts.Logger),
		System: systemInfo{
			version: opts.Version, commit: opts.Commit, dataDir: opts.DataDir, dev: opts.Dev, store: st,
			identity: mods.identity, targets: mods.targets, changes: mods.changes,
		},
		Notify: mods.notify,
	})

	mux := &routeMux{mux: http.NewServeMux()}
	api.Register(mux)
	// Agents reach the MCP server with API tokens (PLAN 4.1).
	mcpSrv, err := mcpserver.New(mcpserver.Options{
		Version: opts.Version, Changes: mods.changes, Targets: mods.targets, Settings: mods.settings,
		Audit: func(ctx context.Context, ev audit.Event) error {
			return st.Tx(ctx, func(tx *store.Tx) error { return recorder.Record(ctx, tx, ev) })
		},
		Clock: opts.Clock, Logger: opts.Logger,
	})
	if err != nil {
		return nil, err
	}
	// Routes outside api/openapi.yaml declare their access level here.
	mux.handle("/mcp", httpapi.Access{Level: httpapi.LevelToken},
		httpx.BodyLimit(mcpserver.MaxBodyBytes)(mods.identity.RequireBearer(mcpSrv.Handler())))
	// OAuth sign-in for MCP clients: discovery documents and the protocol
	// endpoints of the authorization server (identity/oauth.go).
	for _, rt := range api.OAuthRoutes() {
		mux.handle(rt.Pattern, rt.Access, httpx.BodyLimit(httpapi.MaxOAuthBodyBytes)(rt.Handler))
	}
	// The dashboard's static files; every piece of data comes from the API.
	mux.handle("/", httpapi.Access{Level: httpapi.LevelPublic}, httpx.SPA(opts.WebUI))
	set := mods.settings

	// Mutating requests from another origin are rejected (PLAN 5.8).
	csrf := http.NewCrossOriginProtection()
	csrf.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, r, httpx.NewError(http.StatusForbidden, "cross_origin", "Cross-origin requests are not allowed."))
	}))

	trusted := func() []netip.Prefix {
		return settings.ParsePrefixes(settings.Get(set, settings.TrustedProxies))
	}
	https := func() bool {
		return strings.HasPrefix(strings.ToLower(settings.Get(set, settings.InstancePublicURL)), "https://")
	}
	// Security headers come first so every response carries them, including
	// panics, oversized bodies and cross-origin rejections (phase 11).
	handler := httpx.Chain(csrf.Handler(httpapi.SetupGate(mods.setup)(mux)),
		httpx.SecurityHeaders(https),
		httpx.RequestID(opts.IDs, opts.Logger),
		httpx.ClientIP(trusted),
		httpx.AccessLog(opts.Clock),
		httpx.Recover(),
		httpx.BodyLimit(httpx.DefaultMaxBodyBytes),
	)

	return &App{
		opts: opts, log: opts.Logger, store: st, secrets: sec, settings: set,
		identity: mods.identity, setup: mods.setup, targets: mods.targets,
		changes: mods.changes, executor: mods.executor, notify: mods.notify, handler: handler,
		routes: mux.routes,
	}, nil
}

// routeMux is the root mux. It records every pattern registered on it, so
// the route access test can walk the complete route list: the generated API
// routes (whose access is declared in httpapi's operationAccess) and the
// routes registered here with handle, which declare theirs in place.
type routeMux struct {
	mux    *http.ServeMux
	routes []registeredRoute
}

type registeredRoute struct {
	pattern string
	access  *httpapi.Access // nil for generated API routes
}

// HandleFunc is how the generated API code registers its routes.
func (m *routeMux) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	m.routes = append(m.routes, registeredRoute{pattern: pattern})
	m.mux.HandleFunc(pattern, h)
}

// handle registers a route outside the OpenAPI contract with its access level.
func (m *routeMux) handle(pattern string, access httpapi.Access, h http.Handler) {
	m.routes = append(m.routes, registeredRoute{pattern: pattern, access: &access})
	m.mux.Handle(pattern, h)
}

func (m *routeMux) ServeHTTP(w http.ResponseWriter, r *http.Request) { m.mux.ServeHTTP(w, r) }

type modules struct {
	settings *settings.Service
	identity *identity.Service
	setup    *setup.Service
	targets  *targets.Service
	changes  *changes.Service
	executor *executor.Executor
	notify   *notify.Service
}

// Connectors returns the registry of every database connector this binary
// supports. This is the only place concrete connectors are named: adding a
// connector is one line here (PLAN 4.3).
func Connectors() (*connector.Registry, error) {
	reg := connector.NewRegistry()
	for _, c := range []connector.Connector{postgres.New(), mysql.New()} {
		if err := reg.Register(c); err != nil {
			return nil, err
		}
	}
	return reg, nil
}

// buildModules wires settings, identity, setup and targets. Settings get the
// write-only side of the secret store; identity (for the Google client
// secret) and targets (for connection passwords) are two of the three
// modules allowed a secrets.Opener.
func buildModules(ctx context.Context, opts Options, st *store.Store, recorder *audit.Recorder, sec *secrets.Service) (modules, error) {
	var secretWriter secrets.Writer = sec
	var secretOpener secrets.Opener = sec

	set, err := settings.New(ctx, settings.Options{
		Store: st, Audit: recorder, Secrets: secretWriter,
		Clock: opts.Clock, Logger: opts.Logger,
	})
	if err != nil {
		return modules{}, err
	}
	idn, err := identity.New(identity.Options{
		Store: st, Audit: recorder, Secrets: secretOpener, Settings: set,
		Clock: opts.Clock, IDs: opts.IDs, Logger: opts.Logger,
		HTTPClient: opts.HTTPClient, Dev: opts.Dev,
	})
	if err != nil {
		return modules{}, err
	}
	// Rebuild the OpenID provider when the sign-in settings change.
	set.OnChange(idn.SettingsChanged)
	su, err := setup.New(setup.Options{
		Store: st, Audit: recorder, Settings: set, Identity: idn,
		Clock: opts.Clock, Logger: opts.Logger, Banner: opts.Banner,
		URLHint: urlHint(opts.Listen), Dev: opts.Dev,
	})
	if err != nil {
		return modules{}, err
	}
	reg, err := Connectors()
	if err != nil {
		return modules{}, err
	}
	tg, err := targets.New(targets.Options{
		Store: st, Audit: recorder, Secrets: sec, Settings: set, Connectors: reg,
		Clock: opts.Clock, IDs: opts.IDs, Logger: opts.Logger,
	})
	if err != nil {
		return modules{}, err
	}
	// notify is the third module allowed a secrets.Opener (the webhook URL).
	// Loopback receivers are allowed only in --dev.
	nt, err := notify.New(notify.Options{
		Store: st, Audit: recorder, Secrets: secretOpener, Settings: set,
		Clock: opts.Clock, IDs: opts.IDs, Logger: opts.Logger, AllowLoopback: opts.Dev,
	})
	if err != nil {
		return modules{}, err
	}
	chg, err := changes.New(changes.Options{
		Store: st, Audit: recorder, AuditLog: audit.NewReader(st.Reader()), Targets: tg, Directory: idn,
		Settings: set, Clock: opts.Clock, IDs: opts.IDs, Logger: opts.Logger,
		Notifier: changeNotifier{n: nt},
	})
	if err != nil {
		return modules{}, err
	}
	// Removing a user cancels their open changes and voids their approvals,
	// in the same transaction as the removal.
	idn.OnUserRemoved(chg.OnUserRemoved)
	ex, err := executor.New(executor.Options{
		Changes: chg, Targets: tg, Clock: opts.Clock, Logger: opts.Logger,
	})
	if err != nil {
		return modules{}, err
	}
	chg.SetWaker(ex.Wake)
	return modules{settings: set, identity: idn, setup: su, targets: tg, changes: chg, executor: ex, notify: nt}, nil
}

// urlHint is the address to open while no public URL is set.
func urlHint(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "http://localhost:8080"
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// Close closes the connection pools to target databases and releases the
// store. Call it once the server has stopped.
func (a *App) Close() error {
	a.targets.Close()
	return a.store.Close()
}

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

// Run runs background work until ctx is cancelled: the executor, which
// first marks changes left executing by a previous process as failed, and
// the notification worker.
func (a *App) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	notified := make(chan error, 1)
	go func() { notified <- a.notify.Run(ctx) }()
	err := a.executor.Run(ctx)
	cancel()
	return errors.Join(err, <-notified)
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

// RecoverResult is the outcome of Recover.
type RecoverResult struct {
	URL     string
	Expires time.Time
	// PublicURLSet is false when no public URL is configured; URL then
	// starts with a guessed local address.
	PublicURLSet bool
}

// Recover writes a one-time recovery code (hash only, valid 15 minutes) to
// the store of the instance in dataDir and returns the URL that uses it. The
// link reopens only the sign-in settings. It works while the server runs.
func Recover(ctx context.Context, dataDir, listen string, logger *slog.Logger) (RecoverResult, error) {
	st, err := store.OpenExisting(ctx, dataDir)
	if err != nil {
		return RecoverResult{}, err
	}
	defer func() { _ = st.Close() }()
	c, g := clock.Real{}, ids.UUIDv7{}
	recorder := audit.NewRecorder(c, g)
	opts := Options{DataDir: dataDir, Listen: listen, Logger: logger, Clock: c, IDs: g}
	sec, err := secrets.Load(ctx, secrets.Options{
		DataDir: dataDir, Store: st, Audit: recorder, Clock: c, IDs: g, Logger: logger,
	})
	if err != nil {
		return RecoverResult{}, err
	}
	mods, err := buildModules(ctx, opts, st, recorder, sec)
	if err != nil {
		return RecoverResult{}, err
	}
	url, exp, err := mods.setup.IssueRecovery(ctx)
	if err != nil {
		return RecoverResult{}, err
	}
	return RecoverResult{
		URL: url, Expires: exp,
		PublicURLSet: settings.Get(mods.settings, settings.InstancePublicURL) != "",
	}, nil
}
