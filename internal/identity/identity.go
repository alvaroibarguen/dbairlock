// Package identity owns people and their credentials: users and roles,
// browser sessions, API tokens and Google sign-in. It also holds the one
// authorization function, Can.
//
// Secrets: only the SHA-256 of a session cookie or an API token is stored.
// The Google client secret is opened from the secret store at the moment of
// the token exchange and never kept.
package identity

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/platform/clock"
	"github.com/alvaroibarguen/dbairlock/internal/platform/ids"
	"github.com/alvaroibarguen/dbairlock/internal/platform/ratelimit"
	"github.com/alvaroibarguen/dbairlock/internal/secrets"
	"github.com/alvaroibarguen/dbairlock/internal/settings"
	"github.com/alvaroibarguen/dbairlock/internal/store"
)

// Role is a user's role. v1 has two; decisions go through Can, never through
// comparing roles.
type Role string

// Roles.
const (
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
)

func (r Role) valid() bool { return r == RoleAdmin || r == RoleMember }

// Principal is who is making a request: a signed-in user, through the
// dashboard (a session) or an agent (an API token). Its role is private to
// this package, so authorization can only happen through Can.
type Principal struct {
	UserID     string
	Email      string
	Name       string
	Via        audit.Via
	APITokenID string
	role       Role
}

// Actor converts p to the audit actor, tagged with the client address.
func (p Principal) Actor(ip string) audit.Actor {
	return audit.Actor{UserID: p.UserID, Email: p.Email, Via: p.Via, APITokenID: p.APITokenID, IP: ip}
}

type principalKey struct{}

// WithPrincipal returns a context carrying p and its audit actor.
func WithPrincipal(ctx context.Context, p Principal, ip string) context.Context {
	ctx = context.WithValue(ctx, principalKey{}, p)
	return audit.WithActor(ctx, p.Actor(ip))
}

// PrincipalFrom returns the principal carried by ctx.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// Transactor is the store surface identity needs.
type Transactor interface {
	Tx(ctx context.Context, fn func(tx *store.Tx) error) error
	Reader() store.DBTX
}

// Auditor records audit events inside the caller's transaction.
type Auditor interface {
	Record(ctx context.Context, tx *store.Tx, ev audit.Event) error
}

// UserRemoved is published inside the removal transaction. The changes
// module (phase 7) subscribes to cancel the user's open changes and to stop
// counting their approvals; an error from a subscriber aborts the removal.
type UserRemoved struct {
	UserID    string
	RemovedBy string
}

// RemovalSubscriber reacts to a removal inside its transaction.
type RemovalSubscriber func(ctx context.Context, tx *store.Tx, ev UserRemoved) error

// Options configure the identity module.
type Options struct {
	Store    Transactor
	Audit    Auditor
	Secrets  secrets.Opener
	Settings settings.Source
	Clock    clock.Clock
	IDs      ids.Generator
	Logger   *slog.Logger
	// HTTPClient talks to the OpenID provider. Defaults to a client with a
	// 10 second timeout.
	HTTPClient *http.Client
	// Dev enables DevLogin. Only set with --dev (loopback only).
	Dev bool
}

// Service is the identity module.
type Service struct {
	opts    Options
	flowKey []byte // signs sign-in flow cookies; a restart voids flows in progress

	oidcMu sync.Mutex
	oidc   *oidcClient

	authLimit    *ratelimit.Limiter
	tokenBackoff *ratelimit.Backoff

	subMu       sync.Mutex
	subscribers []RemovalSubscriber
}

// New returns the identity module.
func New(opts Options) (*Service, error) {
	if opts.Store == nil || opts.Audit == nil || opts.Secrets == nil || opts.Settings == nil ||
		opts.Clock == nil || opts.IDs == nil || opts.Logger == nil {
		return nil, errors.New("identity: store, audit, secrets, settings, clock, ids and logger are required")
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("identity: %w", err)
	}
	return &Service{
		opts: opts, flowKey: key,
		// Sign-in starts and callbacks: 20 per minute per client address.
		// Not a setting: brute-force protection must not be switchable
		// off, and no real person signs in 20 times a minute.
		authLimit: ratelimit.New(opts.Clock, 3*time.Second, 20),
		// Requests with a bad API token: 10 free failures per client
		// address, then a lock-out that doubles from 1 second up to 5
		// minutes. Tokens cannot be guessed; this bounds the lookups a
		// misbehaving or hostile client can cause.
		tokenBackoff: ratelimit.NewBackoff(opts.Clock, 10, time.Second, 5*time.Minute),
	}, nil
}

// OnUserRemoved registers fn to run inside every user-removal transaction.
func (s *Service) OnUserRemoved(fn RemovalSubscriber) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	s.subscribers = append(s.subscribers, fn)
}

// SettingsChanged drops the cached OpenID provider when a setting it
// depends on changed, so the next sign-in rebuilds it.
func (s *Service) SettingsChanged(_ context.Context, changed []string) {
	for _, k := range changed {
		switch k {
		case settings.OIDCIssuer.Name(), settings.GoogleClientID.Name(), settings.InstancePublicURL.Name(),
			"auth.google.client_secret":
			s.oidcMu.Lock()
			s.oidc = nil
			s.oidcMu.Unlock()
			return
		}
	}
}

// Meta describes the client of a request, for sessions and audit events.
type Meta struct {
	IP        string
	UserAgent string
}

// Errors returned to transports. They carry no detail that could help an
// attacker; DeniedError carries a reason code for the sign-in page.
var (
	ErrNotFound      = errors.New("identity: not found")
	ErrLastAdmin     = errors.New("identity: there must always be at least one enabled admin")
	ErrInvalid       = errors.New("identity: invalid input")
	ErrRateLimited   = errors.New("identity: too many attempts")
	ErrDevOnly       = errors.New("identity: only available in --dev mode")
	ErrNotConfigured = errors.New("identity: Google sign-in is not configured")
)

// InvalidError is an ErrInvalid with a message safe to show.
type InvalidError struct{ Message string }

func (e *InvalidError) Error() string { return "identity: " + e.Message }

// Is makes errors.Is(err, ErrInvalid) match.
func (e *InvalidError) Is(target error) bool { return target == ErrInvalid }

// RateLimitedError is ErrRateLimited with the time to wait.
type RateLimitedError struct{ RetryAfter time.Duration }

func (e *RateLimitedError) Error() string { return "identity: too many attempts" }

// Is makes errors.Is(err, ErrRateLimited) match.
func (e *RateLimitedError) Is(target error) bool { return target == ErrRateLimited }

func (s *Service) allow(ip string) error {
	if ok, wait := s.authLimit.Allow(ip); !ok {
		return &RateLimitedError{RetryAfter: wait}
	}
	return nil
}

func (s *Service) now() time.Time { return s.opts.Clock.Now() }

func ms(t time.Time) int64 { return t.UnixMilli() }

// systemCtx returns ctx with a system actor, for events recorded before
// anyone is signed in (a denied sign-in).
func systemCtx(ctx context.Context, ip string) context.Context {
	return audit.WithActor(ctx, audit.Actor{Via: audit.ViaSystem, IP: ip})
}
