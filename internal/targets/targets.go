// Package targets owns connections: the configured target databases, each
// using one connector (PLAN 4.3). It stores their non-secret config and
// review policy, keeps each password in the secret store, runs the
// connection test with the privilege check, caches schemas, and hands out
// connector connections through WithConn.
//
// Many connections per connector: every piece of per-target state (the
// secret, the connection pool, the schema cache, the run mutex and the
// policy) is keyed by the connection id, never by the connector id. A
// connector value is stateless and shared.
//
// Authorization is the caller's job (httpapi checks identity.Can before
// calling in); this module trusts its callers and records every state change
// in the audit log inside the same transaction.
package targets

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/connector"
	"github.com/alvaroibarguen/dbairlock/internal/platform/clock"
	"github.com/alvaroibarguen/dbairlock/internal/platform/ids"
	"github.com/alvaroibarguen/dbairlock/internal/secrets"
	"github.com/alvaroibarguen/dbairlock/internal/settings"
	"github.com/alvaroibarguen/dbairlock/internal/store"
)

// Transactor is the store surface targets needs.
type Transactor interface {
	Tx(ctx context.Context, fn func(tx *store.Tx) error) error
	Reader() store.DBTX
}

// Auditor records audit events inside the caller's transaction.
type Auditor interface {
	Record(ctx context.Context, tx *store.Tx, ev audit.Event) error
}

// Secrets is the secret store as targets uses it: it sets and describes
// connection passwords, and opens one at the moment a connection is made.
// targets is one of the three modules allowed a secrets.Opener.
type Secrets interface {
	secrets.Writer
	secrets.Opener
}

// Connectors looks up a connector by id. *connector.Registry implements it.
type Connectors interface {
	Get(id string) (connector.Connector, bool)
	List() []connector.Connector
}

// Options configure the targets module.
type Options struct {
	Store      Transactor
	Audit      Auditor
	Secrets    Secrets
	Settings   settings.Source
	Connectors Connectors
	Clock      clock.Clock
	IDs        ids.Generator
	Logger     *slog.Logger
}

// Errors.
var (
	// ErrNotFound means there is no connection with that id.
	ErrNotFound = errors.New("targets: connection not found")
	// ErrNameTaken means another active connection already has the name.
	ErrNameTaken = errors.New("targets: a connection with this name already exists")
	// ErrArchived means the connection is archived and cannot be changed or
	// used.
	ErrArchived = errors.New("targets: connection is archived")
	// ErrInvalid matches every *ValidationError with errors.Is.
	ErrInvalid = errors.New("targets: invalid connection")
)

// FieldError is one invalid input. Field is "name", "description",
// "connector", "password", "config" or "config.<key>", or "policy.<field>".
type FieldError struct {
	Field   string
	Message string
}

// ValidationError lists every invalid field of a rejected create or update.
// Nothing was changed.
type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		parts[i] = f.Field + ": " + f.Message
	}
	return "targets: invalid connection: " + strings.Join(parts, "; ")
}

// Is makes errors.Is(err, ErrInvalid) true.
func (e *ValidationError) Is(target error) bool { return target == ErrInvalid }

// Policy is a connection's review policy.
type Policy struct {
	RequiredApprovals int      `json:"required_approvals"`
	MaxRows           int      `json:"max_rows"`
	ApprovalTTLHours  int      `json:"approval_ttl_hours"`
	StoreRowImages    bool     `json:"store_row_images"`
	AutoExecute       bool     `json:"auto_execute"`
	AllowedFunctions  []string `json:"allowed_functions"`
}

// Bounds of the policy fields. They mirror the policy.default_* settings.
const (
	minApprovals, maxApprovals = 1, 5
	minMaxRows, maxMaxRows     = 1, 10000
	minTTLHours, maxTTLHours   = 1, 720
	maxAllowedFunctions        = 100
	maxDescription             = 500
)

// Connection is one configured target database. It never carries the
// password, only whether it is set, by whom and when.
type Connection struct {
	ID          string
	Name        string
	Description string
	Connector   string
	Config      connector.Config
	Policy      Policy
	Password    secrets.Meta
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ArchivedAt  *time.Time
	ArchivedBy  string
	LastTest    *TestResult
}

// ConnectorPolicy is the validation policy statements on this connection
// are checked against. Callers validate with it immediately before every
// dry run and execution.
func (c Connection) ConnectorPolicy() connector.Policy {
	return connector.Policy{AllowedFunctions: append([]string{}, c.Policy.AllowedFunctions...)}
}

// TestResult is the outcome of a connection test.
type TestResult struct {
	OK       bool      `json:"ok"`
	TestedAt time.Time `json:"tested_at"`
	// Message says why the test failed. Empty when OK.
	Message    string                     `json:"message,omitempty"`
	Server     *connector.ServerInfo      `json:"server,omitempty"`
	Privileges *connector.PrivilegeReport `json:"privileges,omitempty"`
	// Warnings are problems that do not stop the connection from working:
	// excess privileges, missing privileges, an unsupported version.
	Warnings []string `json:"warnings"`
}

// Service is the targets module.
type Service struct {
	opts Options

	mu    sync.Mutex
	pools map[string]*pooled       // by connection id
	locks map[string]chan struct{} // run mutex, by connection id

	schemaMu sync.Mutex
	schemas  map[string]cachedSchema // by connection id
}

// New returns the targets module. Every registered connector must declare
// at most one secret field: it is the connection's password.
func New(opts Options) (*Service, error) {
	if opts.Store == nil || opts.Audit == nil || opts.Secrets == nil || opts.Settings == nil ||
		opts.Connectors == nil || opts.Clock == nil || opts.IDs == nil || opts.Logger == nil {
		return nil, errors.New("targets: store, audit, secrets, settings, connectors, clock, ids and logger are required")
	}
	for _, c := range opts.Connectors.List() {
		n := 0
		for _, f := range c.ConfigFields() {
			if f.Type == connector.FieldSecret {
				n++
			}
		}
		if n > 1 {
			return nil, fmt.Errorf("targets: connector %q declares %d secret fields; at most one (the password) is supported", c.Info().ID, n)
		}
	}
	return &Service{
		opts:    opts,
		pools:   map[string]*pooled{},
		locks:   map[string]chan struct{}{},
		schemas: map[string]cachedSchema{},
	}, nil
}

// Connectors returns every registered connector, sorted by id.
func (s *Service) Connectors() []connector.Connector { return s.opts.Connectors.List() }

// Connector returns the registered connector with id, for validating
// statements against it.
func (s *Service) Connector(id string) (connector.Connector, bool) { return s.opts.Connectors.Get(id) }

// passwordRef is where a connection's password lives in the secret store.
func passwordRef(id string) secrets.Ref {
	return secrets.Ref{Kind: secrets.KindTargetPassword, OwnerType: "connection", OwnerID: id}
}

// passwordKey returns the key of the connector's secret field, or "".
func passwordKey(c connector.Connector) string {
	for _, f := range c.ConfigFields() {
		if f.Type == connector.FieldSecret {
			return f.Key
		}
	}
	return ""
}

// defaultPolicy builds a new connection's policy from the policy.default_*
// settings and the connector's default function allowlist.
func (s *Service) defaultPolicy() Policy {
	src := s.opts.Settings
	return Policy{
		RequiredApprovals: settings.Get(src, settings.DefaultRequiredApprovals),
		MaxRows:           settings.Get(src, settings.DefaultMaxRows),
		ApprovalTTLHours:  settings.Get(src, settings.DefaultApprovalTTLHours),
		StoreRowImages:    settings.Get(src, settings.DefaultStoreRowImages),
		AutoExecute:       settings.Get(src, settings.DefaultAutoExecute),
		AllowedFunctions:  connector.DefaultAllowedFunctions(),
	}
}

// Limits returns the limits for one dry run or execution on c: its max rows,
// the instance timeouts and the display truncation.
func (s *Service) Limits(c Connection) connector.Limits {
	lim := connector.DefaultLimits()
	lim.MaxRows = c.Policy.MaxRows
	lim.StatementTimeout = time.Duration(settings.Get(s.opts.Settings, settings.StatementTimeoutMS)) * time.Millisecond
	lim.LockTimeout = time.Duration(settings.Get(s.opts.Settings, settings.LockTimeoutMS)) * time.Millisecond
	return lim
}

func sortedCopy(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}
