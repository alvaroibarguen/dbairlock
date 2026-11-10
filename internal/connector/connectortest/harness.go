// Package connectortest is the conformance suite every connector must pass.
//
// A connector's test file builds a Harness and calls Run. The suite tests
// behaviour, not implementation: validation (a shared portable corpus plus
// the connector's own), dry runs that leave no trace, execution with drift
// detection, the row fingerprint, ErrUnavailable semantics, the run label,
// server info, schema, privileges and cell values.
//
// # Seed fixture
//
// Every database returned by Harness.NewTarget must contain, in the
// connection's default namespace, exactly these tables and rows (column
// types are the connector's natural equivalents):
//
//	users (id integer PRIMARY KEY, email varchar NOT NULL, name varchar NULL,
//	       status varchar NOT NULL, updated_at timestamp NULL)
//	  (1, 'ann@example.com', 'Ann', 'active',   NULL)
//	  (2, 'bob@example.com', 'Bob', 'active',   NULL)
//	  (3, 'cat@example.com', 'Cat', 'banned',   NULL)
//	  (4, 'dan@example.com', 'Dan', 'active',   NULL)
//	  (5, 'eve@example.com', 'Eve', 'inactive', NULL)
//
//	orders (id integer PRIMARY KEY, user_id integer NOT NULL,
//	        total decimal(10,2) NOT NULL, status varchar NOT NULL)
//	  (1, 1, 10.00, 'paid'), (2, 1, 20.00, 'open'),
//	  (3, 2, 150.00, 'open'), (4, 4, 5.00, 'open')
//
//	audit_notes (user_id integer NOT NULL, note varchar NULL) -- no primary key
//	  (1, 'created'), (1, 'created'), (2, 'verified')
//
// plus whatever table the harness's CellCase uses. The least-privilege
// user has exactly SELECT, INSERT, UPDATE and DELETE on these tables.
package connectortest

import (
	"context"
	"testing"

	"github.com/alvaroibarguen/dbairlock/internal/connector"
)

// Role selects which database user a Config connects as.
type Role int

// Roles.
const (
	// LeastPrivilege has only SELECT, INSERT, UPDATE and DELETE.
	LeastPrivilege Role = iota
	// Superuser has every privilege.
	Superuser
)

// Target is one fresh, seeded database.
type Target interface {
	// Config returns the connection config and password for role.
	Config(role Role) (connector.Config, []byte)
	// Admin runs a statement as a superuser on this database.
	Admin(ctx context.Context, sql string) error
	// AdminQuery runs a query as a superuser and returns every value as
	// text, with NULL as the string "<NULL>".
	AdminQuery(ctx context.Context, sql string) ([][]string, error)
	// Session opens a separate superuser session, used to hold row locks.
	Session(ctx context.Context) (Session, error)
	// Labels returns the run labels of the sessions currently connected to
	// this database, as the database's own activity view shows them.
	Labels(ctx context.Context) ([]string, error)
	// Kill terminates every session whose label is label.
	Kill(ctx context.Context, label string) error
}

// TransactionTarget is implemented by a Target whose run label is visible
// before the run's transaction begins (MySQL sets it as a connection
// attribute at connect time; Postgres sets it inside the transaction).
// Tests that must act after BEGIN also wait for InTransaction.
type TransactionTarget interface {
	// InTransaction reports whether a session labelled label has an open
	// transaction on the server.
	InTransaction(ctx context.Context, label string) (bool, error)
}

// NamespaceTarget is implemented by a Target whose default namespace is
// not Harness.DefaultNamespace: for example a MySQL target, whose namespace
// is its own database, created fresh for each target.
type NamespaceTarget interface {
	Namespace() string
}

// namespace returns the default namespace of tg.
func namespace(h Harness, tg Target) string {
	if n, ok := tg.(NamespaceTarget); ok {
		return n.Namespace()
	}
	return h.DefaultNamespace
}

// Session is a database session the suite drives directly.
type Session interface {
	Exec(ctx context.Context, sql string) error
	Close() error
}

// Unreachable is a connection that must fail with ErrUnavailable.
type Unreachable struct {
	Name     string // e.g. "stopped", "wrong password", "refused"
	Config   connector.Config
	Password []byte
}

// CellCase checks cell value conversion. Statement must be an UPDATE that
// matches exactly one row; the suite dry-runs it with MaxCellBytes and
// compares the before image's values, by column, to Expect (JSON text).
type CellCase struct {
	Statement    string
	MaxCellBytes int
	Expect       map[string]string
}

// Harness supplies everything connector-specific.
type Harness struct {
	Connector connector.Connector

	// ValidationConfig is a valid config used for pure validation tests.
	// Its default namespace is DefaultNamespace.
	ValidationConfig connector.Config
	DefaultNamespace string

	// Corpus holds the connector's own validation cases. Together with the
	// shared corpus there must be at least 60.
	Corpus []Case

	// Skip, when not empty, skips every test that needs a database, with
	// this reason (for example, Docker is not available).
	Skip string

	// NewTarget returns a fresh database with the seed fixture.
	NewTarget func(t *testing.T) Target
	// Unreachable returns connections that cannot be used: a stopped
	// server, a wrong password and a refused connection.
	Unreachable func(t *testing.T) []Unreachable

	// ServerMajor is the major version under test.
	ServerMajor int
	// SystemNamespaces must never appear in Schema.
	SystemNamespaces []string

	// BeginSQL starts a transaction in a Session, e.g. "BEGIN".
	BeginSQL string
	// SlowFunction is a function that sleeps, and SlowWhere a WHERE
	// condition using it that takes at least two seconds per row, e.g.
	// "pg_sleep", "pg_sleep(2) IS NOT NULL".
	SlowFunction, SlowWhere string

	Cells CellCase
}

// Run runs the whole conformance suite.
func Run(t *testing.T, h Harness) {
	t.Run("validation", func(t *testing.T) { RunValidation(t, h) })
	t.Run("database", func(t *testing.T) {
		if h.Skip != "" {
			t.Skip(h.Skip)
		}
		runDatabase(t, h)
	})
}
