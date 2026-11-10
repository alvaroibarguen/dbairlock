package changes

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/changes/changesdb"
	"github.com/alvaroibarguen/dbairlock/internal/connector"
)

// Issue codes beyond the connector's rejection codes (connector.RejectCode).
const (
	CodeTooManyRows      = "too_many_rows"
	CodeNoRows           = "no_rows"
	CodeNotTransactional = "not_transactional"
	CodeTimeout          = "timeout"
	CodeDatabase         = "database_error"
	CodeUnavailable      = "target_unavailable"
	CodeArchived         = "connection_archived"
	CodeBusy             = "target_busy"
	CodeNotRun           = "not_run" // not dry-run because another statement is invalid
)

// Issue is why a statement, or a whole dry run, failed. Position is the
// byte offset into the statement text the message refers to, or -1.
type Issue struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	Position int    `json:"position"`
}

// Preview is the dry-run (or execution) result of one statement: counts,
// columns and, when the connection stores row images and the viewer may see
// them, the before and after rows. Values are as the connector returned
// them (see connector.Row).
type Preview struct {
	RowsAffected  int64             `json:"rows_affected"`
	Fingerprint   string            `json:"fingerprint,omitempty"`
	HasPrimaryKey bool              `json:"has_primary_key"`
	PrimaryKey    []string          `json:"primary_key"`
	Columns       []string          `json:"columns"`
	Before        []connector.Row   `json:"before,omitempty"`
	After         []connector.Row   `json:"after,omitempty"`
	Truncated     bool              `json:"truncated"`
	Session       map[string]string `json:"session,omitempty"`
	// RowImages is false when the rows are not included: the connection
	// does not store row images, or the viewer is an agent.
	RowImages bool `json:"row_images"`
}

func previewOf(r connector.Result, images bool) Preview {
	p := Preview{
		RowsAffected: r.RowsAffected, Fingerprint: r.Fingerprint, HasPrimaryKey: r.HasPrimaryKey,
		PrimaryKey: nonNil(r.PrimaryKey), Columns: nonNil(r.Columns), Truncated: r.Truncated,
		Session: r.Session, RowImages: images,
	}
	if images {
		p.Before, p.After = r.Before, r.After
	}
	return p
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Person is an author or reviewer as shown next to a change.
type Person struct {
	ID      string
	Email   string
	Name    string
	Removed bool
}

// Statement is one statement of a change's current revision.
type Statement struct {
	Position      int
	SQL           string
	Kind          connector.Kind
	Schema        string
	Table         string
	ExpectedRows  *int64
	Fingerprint   string
	HasPrimaryKey bool
	Issue         *Issue
	Preview       *Preview
	// PreviousPreview is the preview before the last refresh, kept when
	// that refresh found different data.
	PreviousPreview *Preview
}

// Review decisions.
const (
	DecisionApprove = "approve"
	DecisionReject  = "reject"
)

// Reasons an approval no longer counts.
const (
	OutdatedSQL     = "sql_changed"
	OutdatedPreview = "preview_changed"
	OutdatedRemoved = "reviewer_removed"
)

// Review is a reviewer's decision.
type Review struct {
	ID          string
	Reviewer    Person
	Decision    string
	Comment     string
	SQLHash     string
	PreviewHash string
	Revision    int
	CreatedAt   time.Time
	// Counts is true for an approval that counts towards the required
	// number now. Outdated says why an approval does not.
	Counts   bool
	Outdated string
}

// Drift describes why an execution stopped (connector.DriftError).
type Drift struct {
	Kind      connector.DriftKind `json:"kind"`
	Statement int                 `json:"statement"`
	Expected  string              `json:"expected"`
	Actual    string              `json:"actual"`
}

// StatementOutcome is the result of one statement in an execution.
type StatementOutcome struct {
	Position int     `json:"position"`
	Schema   string  `json:"schema"`
	Table    string  `json:"table"`
	Preview  Preview `json:"preview"`
}

// Execution outcomes.
const (
	OutcomeRunning     = "running"
	OutcomeExecuted    = "executed"
	OutcomeFailed      = "failed"
	OutcomeNotStarted  = "not_started"
	OutcomeInterrupted = "interrupted"
)

// Failure codes of a failed change (Change.StatusCode).
const (
	FailDrift       = "drift"
	FailTimeout     = "timeout"
	FailError       = "error"
	FailInvalid     = "invalid"
	FailInterrupted = "interrupted"
)

// InterruptedMessage is the message of a change found executing at start-up
// (PLAN.md 5.6).
const InterruptedMessage = "interrupted, outcome unknown, verify manually"

// Execution is one attempt to run a change.
type Execution struct {
	ID           string
	Attempt      int
	StartedAt    time.Time
	FinishedAt   *time.Time
	Outcome      string
	ErrorCode    string
	ErrorMessage string
	Drift        *Drift
	Results      []StatementOutcome
}

// Actions says what the viewer may do with a change now. The server checks
// every action again; this is for the dashboard to show the right buttons.
type Actions struct {
	Edit           bool
	Submit         bool
	Cancel         bool
	Approve        bool
	Reject         bool
	RefreshPreview bool
	ExecuteNow     bool
	RetryNow       bool
}

// Change is a change request as a viewer sees it.
type Change struct {
	ID             string
	Number         int64
	ConnectionID   string
	ConnectionName string
	Connector      string
	Author         Person
	Via            audit.Via
	APITokenID     string
	AgentName      string
	Title          string
	Reason         string
	Status         Status
	// StatusCode, StatusDetail and StatusMessage say why the change is in
	// its status: failed/drift/count, cancelled/author_removed, ...
	StatusCode       string
	StatusDetail     string
	StatusMessage    string
	SQLHash          string
	PreviewHash      string
	PreviewedAt      *time.Time
	PreviewError     string
	PreviewChanged   bool
	Revision         int
	Attempts         int
	NextAttemptAt    *time.Time
	LastAttemptError string
	ExecuteRequested bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
	SubmittedAt      *time.Time
	DecidedAt        *time.Time
	ExecutedAt       *time.Time
	ExpiresAt        *time.Time

	Statements []Statement
	Reviews    []Review
	Executions []Execution

	RequiredApprovals int
	Approvals         int
	AutoExecute       bool
	StoreRowImages    bool

	// AlreadyExisted is true when a submission matched an open change of
	// the same author with the same SQL, which is returned instead.
	AlreadyExisted bool

	// Sequence is set when the change is a step of a sequence.
	Sequence *SequenceRef

	Actions       Actions
	PreviewMaxAge time.Duration
	ServerTime    time.Time
}

// ByAgent reports whether an agent (an API token) submitted the change.
func (c Change) ByAgent() bool { return c.Via == audit.ViaMCP || c.APITokenID != "" }

func optTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.UnixMilli(v.Int64).UTC()
	return &t
}

// someInt is a present nullable integer.
func someInt(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }

func timeOf(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

func fromRow(r changesdb.ChangeRequest) Change {
	return Change{
		ID: r.ID, Number: r.Number, ConnectionID: r.ConnectionID,
		Author: Person{ID: r.AuthorID}, Via: audit.Via(r.Via), APITokenID: r.ApiTokenID, AgentName: r.AgentName,
		Title: r.Title, Reason: r.Reason, Status: Status(r.Status),
		StatusCode: r.StatusCode, StatusDetail: r.StatusDetail, StatusMessage: r.StatusMessage,
		SQLHash: r.SqlHash, PreviewHash: r.PreviewHash, PreviewedAt: optTime(r.PreviewedAt),
		PreviewError: r.PreviewError, PreviewChanged: r.PreviewChanged != 0,
		Revision: int(r.Revision), Attempts: int(r.Attempts), NextAttemptAt: optTime(r.NextAttemptAt),
		LastAttemptError: r.LastAttemptError, ExecuteRequested: r.ExecuteRequestedAt.Valid,
		CreatedAt: time.UnixMilli(r.CreatedAt).UTC(), UpdatedAt: time.UnixMilli(r.UpdatedAt).UTC(),
		SubmittedAt: optTime(r.SubmittedAt), DecidedAt: optTime(r.DecidedAt),
		ExecutedAt: optTime(r.ExecutedAt), ExpiresAt: optTime(r.ExpiresAt),
	}
}

func statementFromRow(r changesdb.ChangeStatement) (Statement, error) {
	st := Statement{
		Position: int(r.Position), SQL: r.SqlText, Kind: connector.Kind(r.Kind),
		Schema: r.SchemaName, Table: r.TableName, Fingerprint: r.ExpectedFingerprint,
		HasPrimaryKey: r.HasPrimaryKey != 0,
	}
	if r.ExpectedRows.Valid {
		n := r.ExpectedRows.Int64
		st.ExpectedRows = &n
	}
	var err error
	if st.Issue, err = decodeOpt[Issue](r.Error); err != nil {
		return st, err
	}
	if st.Preview, err = decodeOpt[Preview](r.Preview); err != nil {
		return st, err
	}
	if st.PreviousPreview, err = decodeOpt[Preview](r.PreviousPreview); err != nil {
		return st, err
	}
	return st, nil
}

// decode reads stored JSON, keeping numbers exact (json.Number) so cell
// values are shown exactly as the connector produced them.
func decode(raw string, v any) error {
	d := json.NewDecoder(bytes.NewReader([]byte(raw)))
	d.UseNumber()
	return d.Decode(v)
}

func decodeOpt[T any](v sql.NullString) (*T, error) {
	if !v.Valid {
		return nil, nil
	}
	var out T
	if err := decode(v.String, &out); err != nil {
		return nil, fmt.Errorf("changes: decode stored %T: %w", out, err)
	}
	return &out, nil
}

func encodeOpt(v any) (sql.NullString, error) {
	if v == nil {
		return sql.NullString{}, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("changes: encode: %w", err)
	}
	if string(b) == "null" {
		return sql.NullString{}, nil
	}
	return sql.NullString{String: string(b), Valid: true}, nil
}

func executionFromRow(r changesdb.Execution) (Execution, error) {
	e := Execution{
		ID: r.ID, Attempt: int(r.Attempt), StartedAt: time.UnixMilli(r.StartedAt).UTC(),
		FinishedAt: optTime(r.FinishedAt), Outcome: r.Outcome, ErrorCode: r.ErrorCode, ErrorMessage: r.ErrorMessage,
	}
	var err error
	if e.Drift, err = decodeOpt[Drift](r.Detail); err != nil {
		return e, err
	}
	res, err := decodeOpt[[]StatementOutcome](r.StatementResults)
	if err != nil {
		return e, err
	}
	if res != nil {
		e.Results = *res
	}
	return e, nil
}
