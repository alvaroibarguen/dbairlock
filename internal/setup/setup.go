// Package setup is the first-run wizard and sign-in recovery.
//
// Until setup completes, the instance is in setup mode: every API route but
// /api/meta and /api/setup/* answers 503. On each start in setup mode a
// one-time setup code is printed to the log; only someone who can read the
// log (that is, has access to the host or container) can claim the
// instance. Entering the code grants a short-lived setup session, which may
// store the Google sign-in settings and start a Google sign-in. The first
// successful sign-in becomes the first admin and ends setup mode, so setup
// is complete only once sign-in is proven to work.
//
// `dbairlock recover` prints a one-time link that reopens only the sign-in
// settings, for an instance whose sign-in is misconfigured.
package setup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/identity"
	"github.com/alvaroibarguen/dbairlock/internal/platform/clock"
	"github.com/alvaroibarguen/dbairlock/internal/platform/ratelimit"
	"github.com/alvaroibarguen/dbairlock/internal/secrets"
	"github.com/alvaroibarguen/dbairlock/internal/settings"
	"github.com/alvaroibarguen/dbairlock/internal/setup/setupdb"
	"github.com/alvaroibarguen/dbairlock/internal/store"
)

// Lifetimes.
const (
	SessionLifetime      = 30 * time.Minute
	RecoveryCodeLifetime = 15 * time.Minute
)

// Kind is the kind of grant a browser holds.
type Kind string

// Kinds of browser grant.
const (
	KindSetup    Kind = "setup"
	KindRecovery Kind = "recovery"
)

const (
	grantSetupSession    = "setup_session"
	grantRecoveryCode    = "recovery_code"
	grantRecoverySession = "recovery_session"
)

// Transactor is the store surface setup needs.
type Transactor interface {
	Tx(ctx context.Context, fn func(tx *store.Tx) error) error
	Reader() store.DBTX
}

// Auditor records audit events inside the caller's transaction.
type Auditor interface {
	Record(ctx context.Context, tx *store.Tx, ev audit.Event) error
}

// Settings is the part of the settings module setup uses.
type Settings interface {
	settings.Source
	Update(ctx context.Context, ch settings.Changes) error
	SecretStatus(ctx context.Context) (map[string]secrets.Meta, error)
}

// Identity is the part of the identity module setup uses, inside the
// transaction that completes setup.
type Identity interface {
	CreateFirstAdmin(ctx context.Context, tx *store.Tx, c identity.Claims, meta identity.Meta) (identity.User, error)
	IssueSession(ctx context.Context, tx *store.Tx, userID string, meta identity.Meta) (identity.NewSession, error)
}

// Options configure the setup module.
type Options struct {
	Store    Transactor
	Audit    Auditor
	Settings Settings
	Identity Identity
	Clock    clock.Clock
	Logger   *slog.Logger
	// Banner receives the clearly marked setup-code block (stderr).
	Banner io.Writer
	// URLHint is where to open the dashboard when no public URL is set yet,
	// for example "http://127.0.0.1:8080".
	URLHint string
	// Dev skips setup mode.
	Dev bool
}

var (
	// ErrInvalidCode means the setup or recovery code is wrong or expired.
	ErrInvalidCode = errors.New("setup: the code is not valid")
	// ErrComplete means setup has already been completed.
	ErrComplete = errors.New("setup: setup is already complete")
	// ErrNoGrant means the browser holds no valid setup or recovery session.
	ErrNoGrant = errors.New("setup: no setup or recovery session")
)

// RateLimitedError means too many code attempts from one address.
type RateLimitedError struct{ RetryAfter time.Duration }

func (e *RateLimitedError) Error() string { return "setup: too many attempts" }

// Service is the setup module.
type Service struct {
	opts     Options
	required atomic.Bool
	limit    *ratelimit.Limiter
	backoff  *ratelimit.Backoff
}

// New returns the setup module. Call Start before serving.
func New(opts Options) (*Service, error) {
	if opts.Store == nil || opts.Audit == nil || opts.Settings == nil || opts.Identity == nil ||
		opts.Clock == nil || opts.Logger == nil {
		return nil, errors.New("setup: store, audit, settings, identity, clock and logger are required")
	}
	if opts.Banner == nil {
		opts.Banner = io.Discard
	}
	return &Service{
		opts: opts,
		// 5 attempts per minute per address, then a growing delay:
		// 30 seconds after the fifth consecutive failure, doubling up to
		// 15 minutes. A correct code resets it.
		limit:   ratelimit.New(opts.Clock, 12*time.Second, 5),
		backoff: ratelimit.NewBackoff(opts.Clock, 5, 30*time.Second, 15*time.Minute),
	}, nil
}

// Start determines whether the instance is in setup mode. In setup mode it
// issues a new setup code (storing only its hash) and prints it with the
// URL to open. In dev mode setup is skipped.
func (s *Service) Start(ctx context.Context) error {
	st, err := setupdb.New(s.opts.Store.Reader()).GetState(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("setup: read state: %w", err)
	}
	if err == nil && st.CompletedAt.Valid {
		s.required.Store(false)
		return nil
	}
	if s.opts.Dev {
		s.opts.Logger.Warn("dev mode: setup is skipped; sign in with POST /api/dev/login")
		s.required.Store(false)
		return nil
	}
	s.required.Store(true)
	code := newCode()
	actx := audit.WithActor(ctx, audit.SystemActor)
	if err := s.opts.Store.Tx(actx, func(tx *store.Tx) error {
		q := setupdb.New(tx)
		if err := q.PutCode(actx, setupdb.PutCodeParams{CodeHash: hash(normalize(code)), CodeIssuedAt: s.now().UnixMilli()}); err != nil {
			return fmt.Errorf("setup: store code: %w", err)
		}
		// Sessions from an earlier run were granted by an older code.
		if err := q.DeleteGrantsOfKind(actx, grantSetupSession); err != nil {
			return fmt.Errorf("setup: clear sessions: %w", err)
		}
		return s.opts.Audit.Record(actx, tx, audit.Event{Action: audit.SetupCodeIssued, SubjectType: "setup"})
	}); err != nil {
		return err
	}
	url := s.baseURL() + "/setup"
	s.opts.Logger.Warn("setup required: open the URL and enter the setup code printed above", slog.String("url", url))
	_, _ = fmt.Fprintf(s.opts.Banner, "\n"+
		"==================== dbairlock setup ====================\n"+
		"  This instance is not set up yet.\n"+
		"  Open:  %s\n"+
		"  Code:  %s\n"+
		"  The code changes every time dbairlock restarts.\n"+
		"=========================================================\n\n", url, code)
	return nil
}

func (s *Service) baseURL() string {
	if u := settings.Get(s.opts.Settings, settings.InstancePublicURL); u != "" {
		return u
	}
	return strings.TrimRight(s.opts.URLHint, "/")
}

// SetupRequired reports whether the instance is in setup mode.
func (s *Service) SetupRequired(context.Context) (bool, error) { return s.required.Load(), nil }

func (s *Service) now() time.Time { return s.opts.Clock.Now() }

// codeEncoding is base32 without padding; 20 characters carry 100 bits.
var codeEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// newCode returns 20 base32 characters in groups of five, for typing.
func newCode() string {
	b := make([]byte, 13)
	_, _ = rand.Read(b)
	c := codeEncoding.EncodeToString(b)[:20]
	return c[0:5] + "-" + c[5:10] + "-" + c[10:15] + "-" + c[15:20]
}

// normalize removes separators and case so a typed code matches.
func normalize(code string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '-' || r == ' ' || r == '\t':
			return -1
		case r >= 'a' && r <= 'z':
			return r - 'a' + 'A'
		}
		return r
	}, code)
}

func hash(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

// checkRate applies the per-address limit and the failure backoff.
func (s *Service) checkRate(ip string) error {
	if d := s.backoff.Wait(ip); d > 0 {
		return &RateLimitedError{RetryAfter: d}
	}
	if ok, d := s.limit.Allow(ip); !ok {
		return &RateLimitedError{RetryAfter: d}
	}
	return nil
}

// VerifyCode checks the setup code. On success it returns the value of a
// new setup session cookie, valid for SessionLifetime.
func (s *Service) VerifyCode(ctx context.Context, code string, meta identity.Meta) (string, error) {
	if !s.required.Load() {
		return "", ErrComplete
	}
	if err := s.checkRate(meta.IP); err != nil {
		return "", err
	}
	actx := audit.WithActor(ctx, audit.Actor{Via: audit.ViaSystem, IP: meta.IP})
	st, err := setupdb.New(s.opts.Store.Reader()).GetState(ctx)
	if err != nil {
		return "", fmt.Errorf("setup: read state: %w", err)
	}
	if st.CompletedAt.Valid {
		return "", ErrComplete
	}
	if len(st.CodeHash) != sha256.Size || subtle.ConstantTimeCompare(st.CodeHash, hash(normalize(code))) != 1 {
		s.backoff.Fail(meta.IP)
		if err := s.opts.Store.Tx(actx, func(tx *store.Tx) error {
			return s.opts.Audit.Record(actx, tx, audit.Event{Action: audit.SetupCodeRejected, SubjectType: "setup"})
		}); err != nil {
			return "", err
		}
		return "", ErrInvalidCode
	}
	s.backoff.Reset(meta.IP)
	token := identity.RandomToken()
	err = s.opts.Store.Tx(actx, func(tx *store.Tx) error {
		if err := s.insertGrant(actx, tx, token, grantSetupSession, SessionLifetime); err != nil {
			return err
		}
		return s.opts.Audit.Record(actx, tx, audit.Event{Action: audit.SetupCodeAccepted, SubjectType: "setup"})
	})
	return token, err
}

func (s *Service) insertGrant(ctx context.Context, tx *store.Tx, token, kind string, life time.Duration) error {
	q := setupdb.New(tx)
	now := s.now()
	if err := q.DeleteExpiredGrants(ctx, now.UnixMilli()); err != nil {
		return fmt.Errorf("setup: prune grants: %w", err)
	}
	if err := q.InsertGrant(ctx, setupdb.InsertGrantParams{
		IDHash: hash(token), Kind: kind, CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(life).UnixMilli(),
	}); err != nil {
		return fmt.Errorf("setup: store grant: %w", err)
	}
	return nil
}

// Grant reports which kind of session the cookie value grants, or
// ErrNoGrant. A setup session stops counting once setup is complete.
func (s *Service) Grant(ctx context.Context, token string) (Kind, error) {
	if token == "" || len(token) > 128 {
		return "", ErrNoGrant
	}
	g, err := setupdb.New(s.opts.Store.Reader()).GetGrant(ctx, hash(token))
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoGrant
	}
	if err != nil {
		return "", fmt.Errorf("setup: read grant: %w", err)
	}
	if s.now().UnixMilli() >= g.ExpiresAt {
		return "", ErrNoGrant
	}
	switch g.Kind {
	case grantSetupSession:
		if !s.required.Load() {
			return "", ErrNoGrant
		}
		return KindSetup, nil
	case grantRecoverySession:
		return KindRecovery, nil
	}
	return "", ErrNoGrant // a recovery code is not a session
}

// actorFor is the audit actor of changes made with a grant: setup happens
// before anyone exists (system); recovery is audited as the CLI that
// issued it.
func actorFor(kind Kind, ip string) audit.Actor {
	if kind == KindRecovery {
		return audit.Actor{Via: audit.ViaCLI, IP: ip}
	}
	return audit.Actor{Via: audit.ViaSystem, IP: ip}
}

// SignIn is the wizard's view of the sign-in settings.
type SignIn struct {
	PublicURL      string
	ClientID       string
	ClientSecret   secrets.Meta
	AllowedDomains []string
	RedirectURI    string
}

// SignInSettings returns the current sign-in settings for a valid grant.
func (s *Service) SignInSettings(ctx context.Context, token string) (SignIn, error) {
	if _, err := s.Grant(ctx, token); err != nil {
		return SignIn{}, err
	}
	return s.signInView(ctx)
}

func (s *Service) signInView(ctx context.Context) (SignIn, error) {
	status, err := s.opts.Settings.SecretStatus(ctx)
	if err != nil {
		return SignIn{}, err
	}
	pub := settings.Get(s.opts.Settings, settings.InstancePublicURL)
	return SignIn{
		PublicURL:      pub,
		ClientID:       settings.Get(s.opts.Settings, settings.GoogleClientID),
		ClientSecret:   status["auth.google.client_secret"],
		AllowedDomains: settings.Get(s.opts.Settings, settings.AllowedDomains),
		RedirectURI:    identity.RedirectURI(pub),
	}, nil
}

// SignInUpdate is what the wizard stores. A nil ClientSecret keeps the
// stored secret; a nil Issuer keeps the stored issuer.
type SignInUpdate struct {
	PublicURL      string
	ClientID       string
	ClientSecret   *secrets.Value
	AllowedDomains []string
	Issuer         *string
}

// SaveSignIn stores the sign-in settings in one atomic settings update
// (each change audited), for a valid setup or recovery session. Every field
// is required, and the client secret must be given unless one is stored.
func (s *Service) SaveSignIn(ctx context.Context, token string, u SignInUpdate, meta identity.Meta) (SignIn, error) {
	kind, err := s.Grant(ctx, token)
	if err != nil {
		return SignIn{}, err
	}
	var fields []settings.FieldError
	req := func(field, value, msg string) {
		if strings.TrimSpace(value) == "" {
			fields = append(fields, settings.FieldError{Field: field, Message: msg})
		}
	}
	req(settings.InstancePublicURL.Name(), u.PublicURL, "Enter the URL people use to open dbairlock.")
	req(settings.GoogleClientID.Name(), u.ClientID, "Enter the client ID from the Google Cloud console.")
	if len(u.AllowedDomains) == 0 {
		fields = append(fields, settings.FieldError{Field: settings.AllowedDomains.Name(), Message: "Enter at least one domain."})
	}
	if u.ClientSecret == nil {
		status, err := s.opts.Settings.SecretStatus(ctx)
		if err != nil {
			return SignIn{}, err
		}
		if !status["auth.google.client_secret"].IsSet {
			fields = append(fields, settings.FieldError{Field: "auth.google.client_secret", Message: "Enter the client secret."})
		}
	}
	if len(fields) > 0 {
		return SignIn{}, &settings.ValidationError{Fields: fields}
	}
	ch := settings.Changes{
		Values: map[string]any{
			settings.InstancePublicURL.Name(): u.PublicURL,
			settings.GoogleClientID.Name():    u.ClientID,
			settings.AllowedDomains.Name():    u.AllowedDomains,
		},
		Secrets: map[string]settings.SecretChange{},
	}
	if u.Issuer != nil {
		ch.Values[settings.OIDCIssuer.Name()] = *u.Issuer
	}
	if u.ClientSecret != nil {
		ch.Secrets["auth.google.client_secret"] = settings.SecretChange{Value: *u.ClientSecret}
	}
	actx := audit.WithActor(ctx, actorFor(kind, meta.IP))
	if err := s.opts.Settings.Update(actx, ch); err != nil {
		return SignIn{}, err
	}
	return s.signInView(ctx)
}

// Complete finishes setup after a verified Google sign-in started with a
// setup session. In one transaction it creates the account as the first
// admin, marks setup complete (which invalidates the setup code), ends every
// setup session, records setup.completed and issues a normal session.
func (s *Service) Complete(ctx context.Context, token string, c identity.Claims, meta identity.Meta) (identity.User, identity.NewSession, error) {
	var (
		user identity.User
		sess identity.NewSession
	)
	err := s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		q := setupdb.New(tx)
		g, err := q.GetGrant(ctx, hash(token))
		if errors.Is(err, sql.ErrNoRows) || (err == nil && (g.Kind != grantSetupSession || s.now().UnixMilli() >= g.ExpiresAt)) {
			return ErrNoGrant
		}
		if err != nil {
			return fmt.Errorf("setup: read grant: %w", err)
		}
		actx := audit.WithActor(ctx, audit.Actor{Via: audit.ViaSystem, IP: meta.IP})
		if user, err = s.opts.Identity.CreateFirstAdmin(actx, tx, c, meta); err != nil {
			return err
		}
		n, err := q.MarkCompleted(ctx, setupdb.MarkCompletedParams{
			CompletedAt: sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}, CompletedBy: user.ID,
		})
		if err != nil {
			return fmt.Errorf("setup: complete: %w", err)
		}
		if n != 1 {
			return ErrComplete
		}
		if err := q.DeleteGrantsOfKind(ctx, grantSetupSession); err != nil {
			return fmt.Errorf("setup: end setup sessions: %w", err)
		}
		uctx := identity.WithPrincipal(ctx, identity.PrincipalOf(user), meta.IP)
		if err := s.opts.Audit.Record(uctx, tx, audit.Event{
			Action: audit.SetupCompleted, SubjectType: "setup",
			Payload: map[string]any{"admin_email": user.Email},
		}); err != nil {
			return err
		}
		sess, err = s.opts.Identity.IssueSession(uctx, tx, user.ID, meta)
		return err
	})
	if err != nil {
		return identity.User{}, identity.NewSession{}, err
	}
	s.required.Store(false)
	s.opts.Logger.Info("setup complete", slog.String("admin", user.Email))
	return user, sess, nil
}

// IssueRecovery creates a one-time recovery code, valid for
// RecoveryCodeLifetime, and returns the URL that uses it. Run by the
// `dbairlock recover` command, audited with via = cli.
func (s *Service) IssueRecovery(ctx context.Context) (string, time.Time, error) {
	code := identity.RandomToken()
	actx := audit.WithActor(ctx, audit.Actor{Via: audit.ViaCLI})
	expires := s.now().Add(RecoveryCodeLifetime)
	err := s.opts.Store.Tx(actx, func(tx *store.Tx) error {
		if err := s.insertGrant(actx, tx, code, grantRecoveryCode, RecoveryCodeLifetime); err != nil {
			return err
		}
		return s.opts.Audit.Record(actx, tx, audit.Event{
			Action: audit.RecoveryIssued, SubjectType: "setup",
			Payload: map[string]any{"expires_at": expires.UTC().Format(time.RFC3339)},
		})
	})
	if err != nil {
		return "", time.Time{}, err
	}
	// The code travels in the fragment, which browsers never send to a
	// server or proxy, so it stays out of access logs.
	return s.baseURL() + "/recover#code=" + code, expires, nil
}

// VerifyRecovery consumes a recovery code and returns the value of a
// recovery session cookie. A code works once and only before it expires.
func (s *Service) VerifyRecovery(ctx context.Context, code string, meta identity.Meta) (string, error) {
	if err := s.checkRate(meta.IP); err != nil {
		return "", err
	}
	actx := audit.WithActor(ctx, audit.Actor{Via: audit.ViaCLI, IP: meta.IP})
	token := identity.RandomToken()
	var used bool
	err := s.opts.Store.Tx(actx, func(tx *store.Tx) error {
		q := setupdb.New(tx)
		h := hash(strings.TrimSpace(code))
		g, err := q.GetGrant(actx, h)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && (g.Kind != grantRecoveryCode || s.now().UnixMilli() >= g.ExpiresAt)) {
			return s.opts.Audit.Record(actx, tx, audit.Event{Action: audit.RecoveryCodeRejected, SubjectType: "setup"})
		}
		if err != nil {
			return fmt.Errorf("setup: read grant: %w", err)
		}
		if _, err := q.DeleteGrant(actx, h); err != nil {
			return fmt.Errorf("setup: use recovery code: %w", err)
		}
		if err := s.insertGrant(actx, tx, token, grantRecoverySession, SessionLifetime); err != nil {
			return err
		}
		used = true
		return s.opts.Audit.Record(actx, tx, audit.Event{Action: audit.RecoveryCodeAccepted, SubjectType: "setup"})
	})
	if err != nil {
		return "", err
	}
	if !used {
		s.backoff.Fail(meta.IP)
		return "", ErrInvalidCode
	}
	s.backoff.Reset(meta.IP)
	return token, nil
}

// ConfirmRecovery ends a recovery after a verified Google sign-in proved
// the new settings work. It ends the recovery session and records
// setup.recovery_completed (via cli). It creates no session and no admin:
// the person then signs in normally.
func (s *Service) ConfirmRecovery(ctx context.Context, token string, c identity.Claims, meta identity.Meta) error {
	actx := audit.WithActor(ctx, audit.Actor{Via: audit.ViaCLI, IP: meta.IP})
	return s.opts.Store.Tx(actx, func(tx *store.Tx) error {
		q := setupdb.New(tx)
		g, err := q.GetGrant(actx, hash(token))
		if errors.Is(err, sql.ErrNoRows) || (err == nil && (g.Kind != grantRecoverySession || s.now().UnixMilli() >= g.ExpiresAt)) {
			return ErrNoGrant
		}
		if err != nil {
			return fmt.Errorf("setup: read grant: %w", err)
		}
		if _, err := q.DeleteGrant(actx, hash(token)); err != nil {
			return fmt.Errorf("setup: end recovery: %w", err)
		}
		return s.opts.Audit.Record(actx, tx, audit.Event{
			Action: audit.RecoveryCompleted, SubjectType: "setup",
			Payload: map[string]any{"confirmed_by": c.Email},
		})
	})
}
