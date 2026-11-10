package connector

import (
	"errors"
	"fmt"
)

// Sentinel errors. Connectors wrap them (with %w) so callers can test with
// errors.Is while still seeing the underlying cause.
var (
	// ErrUnavailable means the target could not be reached, authenticated
	// to, or a transaction could not be begun. It is a promise that no
	// statement has run: the executor retries on it. A connector returns it
	// only for failures before BEGIN returned successfully, classified by
	// its own control flow, never by inspecting error text.
	ErrUnavailable = errors.New("target unavailable")

	// ErrTooManyRows means a statement matches or affects more than
	// Limits.MaxRows rows.
	ErrTooManyRows = errors.New("statement affects too many rows")

	// ErrNoRows means a statement affects no rows at the dry run.
	ErrNoRows = errors.New("statement affects no rows")

	// ErrNotTransactional means a statement touches a table whose changes
	// cannot be rolled back (for example a MySQL MyISAM table or a Postgres
	// foreign table).
	ErrNotTransactional = errors.New("table is not transactional")

	// ErrTimeout means a statement hit the statement or lock timeout.
	ErrTimeout = errors.New("statement timed out")
)

// RejectCode classifies why a statement was rejected by validation.
type RejectCode string

// Rejection codes. They are stable and shared by all connectors so the
// conformance suite and the dashboard can rely on them.
const (
	RejectParse              RejectCode = "parse_error"
	RejectEmpty              RejectCode = "empty"
	RejectMultipleStatements RejectCode = "multiple_statements"
	RejectStatementType      RejectCode = "statement_type"
	RejectMissingWhere       RejectCode = "missing_where"
	RejectConstantWhere      RejectCode = "constant_where"
	RejectMultipleTables     RejectCode = "multiple_tables"
	RejectInsertForm         RejectCode = "insert_form" // INSERT ... SELECT, DEFAULT VALUES, no column list
	RejectUpsert             RejectCode = "upsert"      // ON CONFLICT, ON DUPLICATE KEY UPDATE, REPLACE
	RejectReturning          RejectCode = "returning"
	RejectWith               RejectCode = "with"
	RejectFunction           RejectCode = "function"
	RejectSystemSchema       RejectCode = "system_schema"
	RejectOtherDatabase      RejectCode = "other_database"
	RejectExecutableComment  RejectCode = "executable_comment"
	RejectUnsupported        RejectCode = "unsupported" // a construct the walker does not explicitly handle
)

// RejectedError is a validation rejection.
type RejectedError struct {
	Code   RejectCode
	Reason string // human-readable, names the reason
	// Position is the byte offset into the statement text the reason refers
	// to, or -1 when unknown.
	Position int
}

func (e *RejectedError) Error() string {
	if e.Position >= 0 {
		return fmt.Sprintf("rejected: %s (at offset %d)", e.Reason, e.Position)
	}
	return "rejected: " + e.Reason
}

// Reject builds a *RejectedError.
func Reject(code RejectCode, pos int, format string, args ...any) *RejectedError {
	return &RejectedError{Code: code, Reason: fmt.Sprintf(format, args...), Position: pos}
}

// DriftKind says how an execution differed from its dry run.
type DriftKind string

// Drift kinds (PLAN.md 5.6).
const (
	// DriftRows: different rows match now, or the values being overwritten
	// have changed.
	DriftRows DriftKind = "rows"
	// DriftCount: a different number of rows matched or was affected.
	DriftCount DriftKind = "count"
)

// DriftError means Execute found the data different from the dry run. The
// whole transaction was rolled back; nothing changed on the target.
type DriftError struct {
	Statement int // zero-based index of the statement
	Kind      DriftKind
	Expected  string // rows count or fingerprint, as recorded by the dry run
	Actual    string // the same measure at execution
}

func (e *DriftError) Error() string {
	return fmt.Sprintf("statement %d: drift (%s): expected %s, found %s", e.Statement+1, e.Kind, e.Expected, e.Actual)
}

// StatementError attributes an error to one statement of a dry run or
// execution. It wraps the cause, so errors.Is(err, ErrTooManyRows) and the
// like work through it.
type StatementError struct {
	Index int // zero-based
	Err   error
}

func (e *StatementError) Error() string {
	return fmt.Sprintf("statement %d: %v", e.Index+1, e.Err)
}

func (e *StatementError) Unwrap() error { return e.Err }

// Unavailable wraps cause as ErrUnavailable.
func Unavailable(cause error) error {
	return fmt.Errorf("%w: %w", ErrUnavailable, cause)
}
