// Package connector is the contract every database connector implements.
//
// A connector is a database product (Postgres, MySQL). It is a stateless
// value: Info, ConfigFields, ValidateConfig and Validate need no database, and
// all per-target state lives in the Conn that Open returns. A connector
// knows nothing about dbairlock's domain (no change ids, users, audit or
// store); it takes statements and limits and returns results.
//
// This package imports nothing from internal/ except platform/*. Concrete
// connectors live in subpackages and are registered only by internal/app.
package connector

import (
	"context"
	"time"
)

// Connector is one database product.
type Connector interface {
	// Info describes the connector: id, display name, editor dialect and
	// default port.
	Info() Info

	// ConfigFields describes the connection form. The dashboard renders the
	// form from it without connector-specific code.
	ConfigFields() []Field

	// ValidateConfig checks the non-secret connection fields and returns a
	// normalized copy with defaults applied and values in their canonical
	// Go types (string, int, bool).
	ValidateConfig(cfg Config) (Config, error)

	// Validate checks one statement against the safety rules (PLAN.md 5.1)
	// for a connection with configuration cfg. It is pure: it never touches
	// a database. A rejection is a *RejectedError.
	Validate(cfg Config, sql string, p Policy) (Statement, error)

	// Open creates a connection handle (typically a small pool) for one
	// target. cfg must have passed ValidateConfig. Open does not have to
	// connect eagerly; connectivity errors may surface on first use.
	Open(ctx context.Context, cfg Config, password []byte) (Conn, error)
}

// Conn is an open connection to one target database. It is safe for
// concurrent use, though callers serialize dry runs and executions per
// target.
type Conn interface {
	// Ping checks that the target can be reached and authenticated to, and
	// that the configured default namespace exists.
	Ping(ctx context.Context) error

	// ServerInfo reports the product and version of the target.
	ServerInfo(ctx context.Context) (ServerInfo, error)

	// Privileges reports what the connection's database user may do, and
	// any privileges beyond what dbairlock needs.
	Privileges(ctx context.Context) (PrivilegeReport, error)

	// Schema lists the user namespaces, tables, columns and primary keys.
	// System schemas are never included.
	Schema(ctx context.Context) (Schema, error)

	// DryRun runs stmts in order in one transaction and always rolls back
	// (PLAN.md 5.4). It returns one Result per statement.
	DryRun(ctx context.Context, stmts []Statement, lim Limits, run RunInfo) ([]Result, error)

	// Execute runs stmts in order in one transaction, checking each against
	// its Expectation (PLAN.md 5.6), and commits only if nothing drifted.
	// expected must have one entry per statement.
	Execute(ctx context.Context, stmts []Statement, expected []Expectation, lim Limits, run RunInfo) ([]Result, error)

	// Close releases every connection held by this handle.
	Close() error
}

// Info describes a connector.
type Info struct {
	ID          string `json:"id"`           // stable identifier, e.g. "postgres"
	DisplayName string `json:"display_name"` // e.g. "PostgreSQL"
	Dialect     string `json:"dialect"`      // editor dialect, e.g. "postgresql"
	DefaultPort int    `json:"default_port"`
}

// FieldType is the kind of input a connection form field takes.
type FieldType string

// Field types.
const (
	FieldString FieldType = "string"
	FieldText   FieldType = "text" // multi-line string, e.g. a PEM certificate
	FieldInt    FieldType = "int"
	FieldEnum   FieldType = "enum"
	FieldBool   FieldType = "bool"
	FieldSecret FieldType = "secret" // stored in the secret store, never in Config
)

// Field is one input of a connection form.
type Field struct {
	Key      string    `json:"key"`
	Label    string    `json:"label"`
	Type     FieldType `json:"type"`
	Required bool      `json:"required"`
	Default  any       `json:"default,omitempty"`
	Options  []string  `json:"options,omitempty"` // for FieldEnum
	Help     string    `json:"help,omitempty"`
}

// Config holds the non-secret connection fields, keyed by Field.Key. After
// ValidateConfig, values are string, int or bool.
type Config map[string]any

// String returns the string value of key, or "" if it is absent or not a
// string.
func (c Config) String(key string) string {
	s, _ := c[key].(string)
	return s
}

// Int returns the int value of key, or 0 if it is absent or not an int.
func (c Config) Int(key string) int {
	switch v := c[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

// Policy is the per-connection validation policy set by admins.
type Policy struct {
	// AllowedFunctions lists the function names statements may call, in
	// lower case. Nil means DefaultAllowedFunctions; an empty non-nil slice
	// allows no functions at all.
	AllowedFunctions []string
	// BlockedSchemas lists namespaces statements may not reference, in
	// addition to the connector's system schemas.
	BlockedSchemas []string
}

// DefaultAllowedFunctions is the default function allowlist (PLAN.md 5.1):
// small and free of side effects.
func DefaultAllowedFunctions() []string {
	return []string{
		"now", "current_timestamp", "lower", "upper", "trim", "coalesce",
		"concat", "length", "nullif", "abs", "round", "date", "cast",
	}
}

// Allows reports whether the policy allows calling function name (compared
// case-insensitively by the caller, which passes it in lower case).
func (p Policy) Allows(name string) bool {
	list := p.AllowedFunctions
	if list == nil {
		list = DefaultAllowedFunctions()
	}
	for _, f := range list {
		if f == name {
			return true
		}
	}
	return false
}

// Kind is the type of a validated statement.
type Kind string

// Statement kinds. Nothing else is ever accepted.
const (
	KindInsert Kind = "insert"
	KindUpdate Kind = "update"
	KindDelete Kind = "delete"
)

// Statement is a validated statement.
type Statement struct {
	SQL        string   `json:"sql"`                   // the original text, executed verbatim
	Kind       Kind     `json:"kind"`                  // insert, update or delete
	Schema     string   `json:"schema"`                // namespace, written or the connection default
	Table      string   `json:"table"`                 // unqualified table name
	Alias      string   `json:"alias,omitempty"`       // table alias used in the statement, if any
	Where      string   `json:"where,omitempty"`       // source text of the WHERE clause (update, delete)
	Columns    []string `json:"columns,omitempty"`     // columns set (update) or inserted (insert), in order
	Functions  []string `json:"functions,omitempty"`   // functions called, sorted, lower case
	InsertRows int      `json:"insert_rows,omitempty"` // number of VALUES rows (insert)
}

// Expectation is what the dry run recorded for one statement and what
// Execute must find again.
type Expectation struct {
	Rows        int64  `json:"rows"`        // rows affected at the dry run
	Fingerprint string `json:"fingerprint"` // row fingerprint; empty for insert
}

// Limits bound one dry run or execution.
type Limits struct {
	MaxRows          int           // a statement may affect at most this many rows
	StatementTimeout time.Duration // per statement, enforced on the database and by a context deadline
	LockTimeout      time.Duration // how long to wait for a row lock
	MaxCellBytes     int           // display truncation of a single cell value
}

// DefaultLimits returns conservative defaults. The row limit and the
// timeouts are settings (per connection and per instance; targets.Limits
// fills them in). MaxCellBytes stays fixed: it only truncates what a
// preview displays and stores, while fingerprints always cover complete
// values, so it cannot weaken a check, and 4 KB keeps stored previews and
// review pages small.
func DefaultLimits() Limits {
	return Limits{
		MaxRows:          1000,
		StatementTimeout: 10 * time.Second,
		LockTimeout:      2 * time.Second,
		MaxCellBytes:     4096,
	}
}

// RunInfo labels a dry run or execution on the target, e.g.
// "dbairlock change #123" or "dbairlock preview". Connectors attach it to the
// database session so it shows in the target's own logs and activity views,
// and never put it into statement text.
type RunInfo struct {
	Label string
}

// Row is one row of cell values, in Result.Columns order. Values are
// JSON-safe: nil, bool, int64, float64, string, []any, map[string]any or
// TaggedValue.
type Row []any

// Result is the outcome of one statement in a dry run or execution.
type Result struct {
	Schema        string   `json:"schema"`
	Table         string   `json:"table"`
	RowsAffected  int64    `json:"rows_affected"`
	Fingerprint   string   `json:"fingerprint,omitempty"` // hex SHA-256; empty for insert
	HasPrimaryKey bool     `json:"has_primary_key"`
	PrimaryKey    []string `json:"primary_key,omitempty"`
	Columns       []string `json:"columns"`
	Before        []Row    `json:"before,omitempty"` // matched rows before the statement (update, delete)
	After         []Row    `json:"after,omitempty"`  // rows after the statement (update with a key; insert in a dry run)
	Truncated     bool     `json:"truncated"`        // some cell was cut for display
	// Session records the session settings the statement ran under that can
	// change its behaviour (for example MySQL's sql_mode and server
	// version), so they appear in the audit trail. Connectors that have
	// none leave it nil.
	Session map[string]string `json:"session,omitempty"`
}

// Schema is the introspected structure of a target.
type Schema struct {
	Namespaces []Namespace `json:"namespaces"`
}

// Namespace is a Postgres schema, or the configured MySQL database.
type Namespace struct {
	Name   string  `json:"name"`
	Tables []Table `json:"tables"`
}

// Table is one table with its columns and primary key.
type Table struct {
	Name       string   `json:"name"`
	Columns    []Column `json:"columns"`
	PrimaryKey []string `json:"primary_key,omitempty"`
}

// Column is one table column.
type Column struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
}

// PrivilegeReport says what the database user may do.
type PrivilegeReport struct {
	User      string            `json:"user"`
	CanSelect bool              `json:"can_select"` // on at least one table of the default namespace
	CanInsert bool              `json:"can_insert"`
	CanUpdate bool              `json:"can_update"`
	CanDelete bool              `json:"can_delete"`
	Excess    []ExcessPrivilege `json:"excess,omitempty"`
}

// ExcessPrivilege is a privilege beyond SELECT, INSERT, UPDATE and DELETE.
type ExcessPrivilege struct {
	Privilege string `json:"privilege"` // e.g. "SUPERUSER", "CREATE ON SCHEMA public"
	Detail    string `json:"detail"`
	Fix       string `json:"fix,omitempty"` // a statement an admin can run to remove it
}

// ServerInfo describes the target server.
type ServerInfo struct {
	Product   string `json:"product"`
	Version   string `json:"version"`
	Major     int    `json:"major"`
	Supported bool   `json:"supported"` // the version is in the connector's supported range
}
