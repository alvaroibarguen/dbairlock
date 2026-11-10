// Package changes owns change requests: their statements, dry-run previews,
// reviews and the state machine (PLAN.md sections 5.4, 5.5 and 6). The
// executor drives the execution half through the methods in execution.go.
//
// Rules that matter for safety live here, and only here, so the dashboard
// and the MCP server behave identically:
//
//   - every status change goes through transition (status.go);
//   - an approval is bound to the change's sql_hash (what runs) and
//     preview_hash (the data it was shown to affect) and needs a preview
//     taken within policy.preview_max_age_minutes;
//   - the author never reviews their own change, agents never review, and
//     removed users neither review nor keep earlier approvals;
//   - every row image a viewer receives passes through visiblePreview.
//
// Dry runs talk to a target database, so they happen outside any store
// transaction: run the dry run, then open a short transaction, check that
// the change has not moved on (its revision), and save. Every state change
// records its audit event in the same transaction.
//
// Authorization goes through identity.Can only.
package changes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/connector"
	"github.com/alvaroibarguen/dbairlock/internal/identity"
	"github.com/alvaroibarguen/dbairlock/internal/platform/clock"
	"github.com/alvaroibarguen/dbairlock/internal/platform/ids"
	"github.com/alvaroibarguen/dbairlock/internal/settings"
	"github.com/alvaroibarguen/dbairlock/internal/store"
	"github.com/alvaroibarguen/dbairlock/internal/targets"
)

// Limits of one change (phase 7). They are constants, not settings (see
// docs/configuration.md): they bound what one review can honestly cover,
// and a change that needs more should be split, not allowed by an admin.
// The database CHECK constraints in migration 00005 repeat them.
const (
	MaxStatements     = 20
	MaxStatementBytes = 64 << 10
	MaxTitle          = 200
	MaxReason         = 4000
	MaxComment        = 4000
	// RefreshInterval is the shortest time between two preview refreshes of
	// one change. Fixed because each refresh is a real dry run (row locks
	// included) on a production database: it protects the target, not a
	// preference of the team.
	RefreshInterval = 10 * time.Second
	// PreviewLabel labels every dry run on the target.
	PreviewLabel = "dbairlock preview"
)

// ExecutionLabel labels the execution of change number on the target.
func ExecutionLabel(number int64) string { return fmt.Sprintf("dbairlock change #%d", number) }

// Transactor is the store surface changes needs.
type Transactor interface {
	Tx(ctx context.Context, fn func(tx *store.Tx) error) error
	Reader() store.DBTX
}

// Auditor records audit events inside the caller's transaction.
type Auditor interface {
	Record(ctx context.Context, tx *store.Tx, ev audit.Event) error
}

// AuditReader lists audit events; a change's timeline is a filtered view.
type AuditReader interface {
	List(ctx context.Context, f audit.Filter) (audit.Page, error)
}

// Targets is the targets module as changes uses it.
type Targets interface {
	Get(ctx context.Context, id string) (targets.Connection, error)
	Connector(id string) (connector.Connector, bool)
	Limits(c targets.Connection) connector.Limits
	WithConn(ctx context.Context, id string, fn func(ctx context.Context, c targets.Connection, conn connector.Conn) error) error
}

// Directory resolves people and API tokens. db is a store transaction when
// the answer must agree with what the transaction commits (a reviewer
// removed a moment ago), or nil for the reader.
type Directory interface {
	Users(ctx context.Context, db store.DBTX, ids []string) (map[string]identity.User, error)
	TokenName(ctx context.Context, db store.DBTX, id string) (string, error)
}

// Options configure the changes module.
type Options struct {
	Store     Transactor
	Audit     Auditor
	AuditLog  AuditReader
	Targets   Targets
	Directory Directory
	Settings  settings.Source
	Clock     clock.Clock
	IDs       ids.Generator
	Logger    *slog.Logger
	// Notifier is optional; without it nothing is notified.
	Notifier Notifier
}

// Service is the changes module.
type Service struct {
	opts Options

	wakeMu sync.Mutex
	wake   func()
}

// New returns the changes module.
func New(opts Options) (*Service, error) {
	if opts.Store == nil || opts.Audit == nil || opts.AuditLog == nil || opts.Targets == nil ||
		opts.Directory == nil || opts.Settings == nil || opts.Clock == nil || opts.IDs == nil || opts.Logger == nil {
		return nil, errors.New("changes: store, audit, audit log, targets, directory, settings, clock, ids and logger are required")
	}
	return &Service{opts: opts}, nil
}

// SetWaker registers fn to be called after a change becomes ready to run
// (approved, execution requested, retry requested). The executor uses it
// to start without waiting for its ticker.
func (s *Service) SetWaker(fn func()) {
	s.wakeMu.Lock()
	defer s.wakeMu.Unlock()
	s.wake = fn
}

func (s *Service) wakeExecutor() {
	s.wakeMu.Lock()
	fn := s.wake
	s.wakeMu.Unlock()
	if fn != nil {
		fn()
	}
}

func (s *Service) now() time.Time { return s.opts.Clock.Now() }

// PreviewMaxAge is how old a preview may be for an approval
// (policy.preview_max_age_minutes).
func (s *Service) PreviewMaxAge() time.Duration {
	return time.Duration(settings.Get(s.opts.Settings, settings.PreviewMaxAgeMinutes)) * time.Minute
}

// Errors. Transports map them to responses; messages are safe to show.
var (
	ErrNotFound = errors.New("changes: change not found")
	// ErrConflict means the change moved on since the request's revision:
	// someone edited it, or it changed status. Reload and try again.
	ErrConflict = errors.New("changes: the change was modified; reload it")
	// ErrInvalidState means the action is not possible in the change's
	// current status.
	ErrInvalidState = errors.New("changes: not possible in the change's current status")
	// ErrSelfReview: the author cannot review their own change.
	ErrSelfReview = errors.New("changes: you cannot review your own change")
	// ErrAgentReview: changes are reviewed by people, never through an API
	// token.
	ErrAgentReview = errors.New("changes: agents cannot review changes")
	// ErrNotAuthor: only the author may do this.
	ErrNotAuthor = errors.New("changes: only the author can do this")
	// ErrPreviewStale: the preview is older than the approval limit.
	ErrPreviewStale = errors.New("changes: the preview is too old to approve")
	// ErrPreviewFailed: the last preview failed; the change cannot be
	// approved until the author edits it or a later preview succeeds.
	ErrPreviewFailed = errors.New("changes: the last preview failed")
	// ErrPreviewChanged: the approval was for an older preview.
	ErrPreviewChanged = errors.New("changes: the preview changed since you loaded it")
	// ErrSQLChanged: the approval was for an older revision of the SQL.
	ErrSQLChanged = errors.New("changes: the SQL changed since you loaded it")
	// ErrAlreadyApproved: the reviewer already approved this SQL and preview.
	ErrAlreadyApproved = errors.New("changes: you already approved this change")
	// ErrDuplicate: the author already has an open change with this SQL on
	// this connection.
	ErrDuplicate = errors.New("changes: you already have an open change with this SQL")
	// ErrTargetUnavailable: the target database could not be reached.
	ErrTargetUnavailable = errors.New("changes: the database could not be reached")
	// ErrInvalid matches every *InputError and *DryRunError.
	ErrInvalid = errors.New("changes: invalid change")
)

// StateError is ErrInvalidState with a sentence for the person.
type StateError struct{ Message string }

func (e *StateError) Error() string { return "changes: " + e.Message }

// Is makes errors.Is(err, ErrInvalidState) match.
func (e *StateError) Is(target error) bool { return target == ErrInvalidState }

func stateErr(format string, args ...any) error {
	return &StateError{Message: fmt.Sprintf(format, args...)}
}

// TooSoonError is returned by RefreshPreview within RefreshInterval of the
// previous refresh.
type TooSoonError struct{ RetryAfter time.Duration }

func (e *TooSoonError) Error() string { return "changes: the preview was refreshed a moment ago" }

// DuplicateError is ErrDuplicate with the existing change.
type DuplicateError struct {
	ID     string
	Number int64
}

func (e *DuplicateError) Error() string {
	return fmt.Sprintf("changes: you already have an open change with this SQL (#%d)", e.Number)
}

// Is makes errors.Is(err, ErrDuplicate) match.
func (e *DuplicateError) Is(target error) bool { return target == ErrDuplicate }

// FieldError is one invalid input field: "connection_id", "title",
// "reason", "comment", "statements" or "statements[<i>]".
type FieldError struct {
	Field   string
	Message string
}

// InputError lists every invalid field. Nothing was changed.
type InputError struct{ Fields []FieldError }

func (e *InputError) Error() string {
	parts := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		parts[i] = f.Field + ": " + f.Message
	}
	return "changes: invalid input: " + strings.Join(parts, "; ")
}

// Is makes errors.Is(err, ErrInvalid) match.
func (e *InputError) Is(target error) bool { return target == ErrInvalid }

type fieldErrs []FieldError

func (f *fieldErrs) add(field, format string, args ...any) {
	*f = append(*f, FieldError{Field: field, Message: fmt.Sprintf(format, args...)})
}

func (f fieldErrs) err() error {
	if len(f) == 0 {
		return nil
	}
	return &InputError{Fields: f}
}

// DryRunError means statements failed validation or the dry run, so the
// change could not be submitted. Nothing was submitted. Statements carries
// every statement's outcome so an author (or agent) can fix all of them in
// one pass; Problem is set when the run as a whole failed (for example the
// target could not be reached).
type DryRunError struct {
	Statements []StatementResult
	Problem    *Issue
}

func (e *DryRunError) Error() string {
	if e.Problem != nil {
		return "changes: " + e.Problem.Message
	}
	var parts []string
	for _, s := range e.Statements {
		if s.Issue != nil {
			parts = append(parts, fmt.Sprintf("statement %d: %s", s.Position+1, s.Issue.Message))
		}
	}
	return "changes: " + strings.Join(parts, "; ")
}

// Is makes errors.Is(err, ErrInvalid) match, and ErrTargetUnavailable when
// the target could not be reached.
func (e *DryRunError) Is(target error) bool {
	if target == ErrTargetUnavailable {
		return e.Problem != nil && e.Problem.Code == CodeUnavailable
	}
	return target == ErrInvalid
}

// msOf converts a time to stored milliseconds.
func msOf(t time.Time) int64 { return t.UnixMilli() }

// isAgent reports whether p acts through an API token. Agents propose;
// people approve.
func isAgent(p identity.Principal) bool {
	return p.APITokenID != "" || p.Via == audit.ViaMCP
}

func connectionResource(id string) identity.Resource {
	return identity.Resource{Type: "connection", ID: id}
}
