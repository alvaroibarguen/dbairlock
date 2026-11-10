package connectortest

import (
	"errors"
	"slices"
	"testing"

	"github.com/alvaroibarguen/dbairlock/internal/connector"
)

// Case is one validation case. A case with an empty Reject must be
// accepted; the other fields, when set, are checked against the Statement.
type Case struct {
	Name   string
	SQL    string
	Policy *connector.Policy // nil means the default policy

	// Reject is the expected rejection code; Also lists acceptable
	// alternatives (dialects classify a few constructs differently).
	Reject connector.RejectCode
	Also   []connector.RejectCode

	Kind      connector.Kind
	Schema    string // "" means the default namespace
	Table     string
	Where     string
	Columns   []string
	Functions []string // checked when not nil
}

// MinCases is the minimum number of validation cases per connector.
const MinCases = 60

// SharedCorpus returns the portable validation cases every connector must
// classify the same way. They use the seed fixture's tables.
func SharedCorpus() []Case {
	upd, del, ins := connector.KindUpdate, connector.KindDelete, connector.KindInsert
	md5 := &connector.Policy{AllowedFunctions: []string{"md5"}}
	none := &connector.Policy{AllowedFunctions: []string{}}
	return []Case{
		// Accepted forms.
		{Name: "update one row", SQL: "UPDATE users SET name = 'Zed' WHERE id = 1",
			Kind: upd, Table: "users", Where: "id = 1", Columns: []string{"name"}, Functions: []string{}},
		{Name: "update several columns", SQL: "UPDATE users SET name = 'x', status = 'active' WHERE id IN (1, 2)",
			Kind: upd, Table: "users", Where: "id IN (1, 2)", Columns: []string{"name", "status"}},
		{Name: "delete", SQL: "DELETE FROM users WHERE id = 3", Kind: del, Table: "users", Where: "id = 3"},
		{Name: "insert one row", SQL: "INSERT INTO users (id, email, name, status) VALUES (10, 'a@example.com', 'A', 'new')",
			Kind: ins, Table: "users", Columns: []string{"id", "email", "name", "status"}},
		{Name: "insert several rows", SQL: "INSERT INTO orders (id, user_id, total, status) VALUES (10, 1, 1.50, 'open'), (11, 2, 2.50, 'open')",
			Kind: ins, Table: "orders", Columns: []string{"id", "user_id", "total", "status"}},
		{Name: "insert null", SQL: "INSERT INTO audit_notes (user_id, note) VALUES (3, NULL)", Kind: ins, Table: "audit_notes"},
		{Name: "trailing semicolon", SQL: "DELETE FROM users WHERE id = 3;", Kind: del, Table: "users", Where: "id = 3"},
		{Name: "comments", SQL: "-- fix the name\nUPDATE users SET name = 'x' /* typo */ WHERE id = 1 -- done",
			Kind: upd, Table: "users", Where: "id = 1"},
		{Name: "boolean logic", SQL: "UPDATE users SET status = 'x' WHERE (id = 1 OR id = 2) AND status <> 'x'",
			Kind: upd, Table: "users", Where: "(id = 1 OR id = 2) AND status <> 'x'"},
		{Name: "subquery", SQL: "DELETE FROM orders WHERE user_id IN (SELECT id FROM users WHERE status = 'banned')",
			Kind: del, Table: "orders", Where: "user_id IN (SELECT id FROM users WHERE status = 'banned')"},
		{Name: "correlated exists", SQL: "DELETE FROM users WHERE EXISTS (SELECT 1 FROM orders WHERE orders.user_id = users.id AND orders.total > 100)",
			Kind: del, Table: "users"},
		{Name: "allowed functions", SQL: "UPDATE users SET name = upper(name) WHERE lower(email) = 'ann@example.com'",
			Kind: upd, Table: "users", Functions: []string{"lower", "upper"}},
		{Name: "coalesce", SQL: "UPDATE users SET name = coalesce(name, 'none') WHERE id = 1", Kind: upd, Functions: []string{"coalesce"}},
		{Name: "nullif", SQL: "UPDATE users SET name = nullif(name, '') WHERE id = 1", Kind: upd, Functions: []string{"nullif"}},
		{Name: "now", SQL: "UPDATE users SET updated_at = now() WHERE id = 1", Kind: upd, Functions: []string{"now"}},
		{Name: "current_timestamp", SQL: "UPDATE users SET updated_at = CURRENT_TIMESTAMP WHERE id = 1", Kind: upd, Functions: []string{"current_timestamp"}},
		{Name: "cast", SQL: "UPDATE orders SET total = CAST('12.50' AS DECIMAL(10,2)) WHERE id = 1", Kind: upd, Functions: []string{"cast"}},
		{Name: "is null", SQL: "DELETE FROM audit_notes WHERE note IS NULL", Kind: del, Table: "audit_notes", Where: "note IS NULL"},
		{Name: "like", SQL: "UPDATE users SET status = 'x' WHERE email LIKE '%@example.com'", Kind: upd, Where: "email LIKE '%@example.com'"},
		{Name: "between", SQL: "DELETE FROM orders WHERE id BETWEEN 1 AND 3", Kind: del, Where: "id BETWEEN 1 AND 3"},
		{Name: "not in", SQL: "DELETE FROM orders WHERE id NOT IN (1, 2)", Kind: del, Where: "id NOT IN (1, 2)"},
		{Name: "case", SQL: "UPDATE users SET status = CASE WHEN id = 1 THEN 'a' ELSE 'b' END WHERE id IN (1, 2)", Kind: upd},
		{Name: "arithmetic", SQL: "UPDATE orders SET total = total * 2 WHERE id = 1", Kind: upd, Columns: []string{"total"}},
		{Name: "set default", SQL: "UPDATE users SET name = DEFAULT WHERE id = 1", Kind: upd},
		{Name: "policy allows a function", SQL: "UPDATE users SET name = md5(name) WHERE id = 1", Policy: md5, Kind: upd, Functions: []string{"md5"}},

		// Rejected.
		{Name: "empty", SQL: "", Reject: connector.RejectEmpty},
		{Name: "blank", SQL: "   \n\t ", Reject: connector.RejectEmpty},
		{Name: "garbage", SQL: "UPDATE users SET WHERE", Reject: connector.RejectParse},
		{Name: "select", SQL: "SELECT * FROM users", Reject: connector.RejectStatementType},
		{Name: "create table", SQL: "CREATE TABLE x (id int)", Reject: connector.RejectStatementType},
		{Name: "drop table", SQL: "DROP TABLE users", Reject: connector.RejectStatementType},
		{Name: "alter table", SQL: "ALTER TABLE users ADD COLUMN x int", Reject: connector.RejectStatementType},
		{Name: "truncate", SQL: "TRUNCATE TABLE users", Reject: connector.RejectStatementType},
		{Name: "grant", SQL: "GRANT SELECT ON users TO someone", Reject: connector.RejectStatementType},
		{Name: "begin", SQL: "BEGIN", Reject: connector.RejectStatementType},
		{Name: "commit", SQL: "COMMIT", Reject: connector.RejectStatementType},
		{Name: "rollback", SQL: "ROLLBACK", Reject: connector.RejectStatementType},
		{Name: "set", SQL: "SET autocommit = 0", Reject: connector.RejectStatementType},
		{Name: "call", SQL: "CALL do_things()", Reject: connector.RejectStatementType},
		{Name: "two statements", SQL: "UPDATE users SET name = 'a' WHERE id = 1; DELETE FROM users WHERE id = 2", Reject: connector.RejectMultipleStatements},
		{Name: "smuggled second statement", SQL: "DELETE FROM users WHERE id = 1; DROP TABLE users", Reject: connector.RejectMultipleStatements},
		{Name: "update without where", SQL: "UPDATE users SET name = 'a'", Reject: connector.RejectMissingWhere},
		{Name: "delete without where", SQL: "DELETE FROM users", Reject: connector.RejectMissingWhere},
		{Name: "where 1=1", SQL: "DELETE FROM users WHERE 1=1", Reject: connector.RejectConstantWhere},
		{Name: "where true", SQL: "UPDATE users SET name = 'a' WHERE true", Reject: connector.RejectConstantWhere},
		{Name: "where string equality", SQL: "DELETE FROM users WHERE 'a' = 'a'", Reject: connector.RejectConstantWhere},
		{Name: "where column equals itself", SQL: "DELETE FROM users WHERE id = id", Reject: connector.RejectConstantWhere},
		{Name: "where or true", SQL: "DELETE FROM users WHERE id = 2 OR 1 = 1", Reject: connector.RejectConstantWhere},
		{Name: "where not false", SQL: "DELETE FROM users WHERE NOT (1 = 2)", Reject: connector.RejectConstantWhere},
		{Name: "insert select", SQL: "INSERT INTO users (id, email, name, status) SELECT id + 100, email, name, status FROM users", Reject: connector.RejectInsertForm},
		{Name: "insert without columns", SQL: "INSERT INTO users VALUES (10, 'a@example.com', 'A', 'new', NULL)", Reject: connector.RejectInsertForm},
		{Name: "with", SQL: "WITH x AS (SELECT 1 AS id) DELETE FROM users WHERE id IN (SELECT id FROM x)", Reject: connector.RejectWith},
		{Name: "function not allowed in set", SQL: "UPDATE users SET name = md5(name) WHERE id = 1", Reject: connector.RejectFunction},
		{Name: "function not allowed in where", SQL: "DELETE FROM orders WHERE total > floor(1.5)", Reject: connector.RejectFunction},
		{Name: "function not allowed in values", SQL: "INSERT INTO users (id, email, name, status) VALUES (10, md5('x'), 'A', 'new')", Reject: connector.RejectFunction},
		{Name: "function not allowed in subquery", SQL: "DELETE FROM orders WHERE user_id IN (SELECT id FROM users WHERE md5(email) = 'x')", Reject: connector.RejectFunction},
		{Name: "empty allowlist", SQL: "UPDATE users SET name = lower(name) WHERE id = 1", Policy: none, Reject: connector.RejectFunction},
		{Name: "system schema target", SQL: "DELETE FROM information_schema.tables WHERE table_name = 'x'",
			Reject: connector.RejectSystemSchema, Also: []connector.RejectCode{connector.RejectOtherDatabase}},
		{Name: "system schema in subquery", SQL: "DELETE FROM users WHERE email IN (SELECT table_name FROM information_schema.tables)",
			Reject: connector.RejectSystemSchema, Also: []connector.RejectCode{connector.RejectOtherDatabase}},
	}
}

// RunValidation runs the shared corpus and the harness's own cases against
// Connector.Validate. It needs no database.
func RunValidation(t *testing.T, h Harness) {
	cases := append(SharedCorpus(), h.Corpus...)
	if len(cases) < MinCases {
		t.Errorf("only %d validation cases; at least %d are required", len(cases), MinCases)
	}
	seen := map[string]bool{}
	for _, c := range cases {
		if seen[c.Name] {
			t.Errorf("duplicate case name %q", c.Name)
		}
		seen[c.Name] = true
		t.Run(c.Name, func(t *testing.T) { checkCase(t, h, c) })
	}
}

func checkCase(t *testing.T, h Harness, c Case) {
	t.Helper()
	p := connector.Policy{}
	if c.Policy != nil {
		p = *c.Policy
	}
	st, err := h.Connector.Validate(h.ValidationConfig, c.SQL, p)
	if c.Reject != "" {
		var rej *connector.RejectedError
		if !errors.As(err, &rej) {
			t.Fatalf("Validate(%q) = %+v, %v; want rejection %s", c.SQL, st, err, c.Reject)
		}
		if rej.Code != c.Reject && !slices.Contains(c.Also, rej.Code) {
			t.Fatalf("Validate(%q) rejected with %s (%s); want %s", c.SQL, rej.Code, rej.Reason, c.Reject)
		}
		if rej.Reason == "" {
			t.Fatalf("rejection without a reason")
		}
		if rej.Position >= len(c.SQL)+1 {
			t.Fatalf("rejection position %d is beyond the text (%d bytes)", rej.Position, len(c.SQL))
		}
		return
	}
	if err != nil {
		t.Fatalf("Validate(%q): %v", c.SQL, err)
	}
	if st.SQL != c.SQL {
		t.Errorf("SQL = %q, want the original text", st.SQL)
	}
	switch st.Kind {
	case connector.KindInsert, connector.KindUpdate, connector.KindDelete:
	default:
		t.Fatalf("kind %q is not one of the three accepted kinds", st.Kind)
	}
	if c.Kind != "" && st.Kind != c.Kind {
		t.Errorf("Kind = %s, want %s", st.Kind, c.Kind)
	}
	wantSchema := c.Schema
	if wantSchema == "" {
		wantSchema = h.DefaultNamespace
	}
	if st.Schema != wantSchema {
		t.Errorf("Schema = %q, want %q", st.Schema, wantSchema)
	}
	if c.Table != "" && st.Table != c.Table {
		t.Errorf("Table = %q, want %q", st.Table, c.Table)
	}
	if c.Where != "" && st.Where != c.Where {
		t.Errorf("Where = %q, want %q", st.Where, c.Where)
	}
	if (st.Kind == connector.KindUpdate || st.Kind == connector.KindDelete) && st.Where == "" {
		t.Errorf("an %s has no WHERE text", st.Kind)
	}
	if c.Columns != nil && !slices.Equal(st.Columns, c.Columns) {
		t.Errorf("Columns = %q, want %q", st.Columns, c.Columns)
	}
	if c.Functions != nil {
		got := st.Functions
		if got == nil {
			got = []string{}
		}
		if !slices.Equal(got, c.Functions) {
			t.Errorf("Functions = %q, want %q", got, c.Functions)
		}
	}
}
