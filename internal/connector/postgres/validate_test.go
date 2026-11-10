package postgres

import (
	"errors"
	"strings"
	"testing"

	"github.com/alvaroibarguen/dbairlock/internal/connector"
	"github.com/alvaroibarguen/dbairlock/internal/connector/connectortest"
)

// corpus is the Postgres-specific validation corpus. The shared, portable
// corpus is in connectortest.SharedCorpus.
func corpus() []connectortest.Case {
	upd, del, ins := connector.KindUpdate, connector.KindDelete, connector.KindInsert
	return []connectortest.Case{
		// Accepted.
		{Name: "pg: default schema qualified", SQL: "UPDATE public.users SET name = 'x' WHERE id = 1", Kind: upd, Schema: "public", Table: "users"},
		{Name: "pg: other schema", SQL: "DELETE FROM app.items WHERE id = 1", Kind: del, Schema: "app", Table: "items", Where: "id = 1"},
		{Name: "pg: quoted identifiers", SQL: `UPDATE "users" SET "name" = 'x' WHERE "id" = 1`, Kind: upd, Table: "users", Where: `"id" = 1`, Columns: []string{"name"}},
		{Name: "pg: mixed-case quoted table", SQL: `DELETE FROM "Users" WHERE id = 1`, Kind: del, Table: "Users"},
		{Name: "pg: alias", SQL: "DELETE FROM users AS u WHERE u.id = 1", Kind: del, Table: "users", Where: "u.id = 1"},
		{Name: "pg: alias correlated", SQL: "DELETE FROM users u WHERE EXISTS (SELECT 1 FROM orders o WHERE o.user_id = u.id)", Kind: del},
		{Name: "pg: double-colon cast", SQL: "UPDATE users SET updated_at = '2024-01-01'::timestamp WHERE id = 1", Kind: upd, Functions: []string{"cast"}},
		{Name: "pg: typed literal", SQL: "UPDATE users SET updated_at = timestamp '2024-01-01 00:00' WHERE id = 1", Kind: upd, Functions: []string{"cast"}},
		{Name: "pg: pg_catalog function", SQL: "UPDATE users SET name = pg_catalog.lower(name) WHERE id = 1", Kind: upd, Functions: []string{"lower"}},
		{Name: "pg: trim syntax", SQL: "UPDATE users SET name = trim(both ' ' from name) WHERE id = 1", Kind: upd, Functions: []string{"trim"}},
		{Name: "pg: ilike", SQL: "UPDATE users SET status = 'x' WHERE email ILIKE '%ANN%'", Kind: upd, Where: "email ILIKE '%ANN%'"},
		{Name: "pg: similar to", SQL: "UPDATE users SET status = 'x' WHERE email SIMILAR TO '%(ann|bob)%'", Kind: upd, Functions: []string{}},
		{Name: "pg: any array", SQL: "DELETE FROM users WHERE id = ANY (ARRAY[1, 2])", Kind: del, Where: "id = ANY (ARRAY[1, 2])"},
		{Name: "pg: is distinct from", SQL: "UPDATE users SET name = 'x' WHERE name IS DISTINCT FROM 'x' AND id = 1", Kind: upd},
		{Name: "pg: dollar quoting", SQL: "UPDATE users SET name = $$O'Brien$$ WHERE id = 1", Kind: upd},
		{Name: "pg: escape string", SQL: `UPDATE users SET name = E'a\nb' WHERE id = 1`, Kind: upd},
		{Name: "pg: multi-line where with comment", SQL: "DELETE FROM users\nWHERE id = 1\n  AND status = 'banned' -- only banned\n;",
			Kind: del, Where: "id = 1\n  AND status = 'banned'"},
		{Name: "pg: where with block comment inside", SQL: "DELETE FROM users WHERE id = 1 /* the first */ AND status = 'x'",
			Kind: del, Where: "id = 1 /* the first */ AND status = 'x'"},
		{Name: "pg: only", SQL: "UPDATE ONLY users SET name = 'x' WHERE id = 1", Kind: upd, Table: "users"},
		{Name: "pg: insert default", SQL: "INSERT INTO users (id, email, name, status) VALUES (10, 'a@example.com', DEFAULT, 'new')", Kind: ins},
		{Name: "pg: scalar subquery in set", SQL: "UPDATE orders SET user_id = (SELECT id FROM users WHERE email = 'bob@example.com') WHERE id = 1", Kind: upd},
		{Name: "pg: where keyword in a string", SQL: "UPDATE users SET name = 'x WHERE y' WHERE id = 1", Kind: upd, Where: "id = 1"},
		{Name: "pg: subquery with where in set", SQL: "UPDATE orders SET total = (SELECT 1 WHERE true) WHERE id = 1", Kind: upd, Where: "id = 1"},
		{Name: "pg: join in subquery", SQL: "DELETE FROM orders WHERE user_id IN (SELECT u.id FROM users u JOIN orders o ON o.user_id = u.id WHERE u.status = 'banned')", Kind: del},
		{Name: "pg: blocked schema allowed when not blocked", SQL: "DELETE FROM secret.keys WHERE id = 1", Kind: del, Schema: "secret"},

		// Rejected.
		{Name: "pg: update from", SQL: "UPDATE users SET name = 'x' FROM orders WHERE orders.user_id = users.id", Reject: connector.RejectMultipleTables},
		{Name: "pg: delete using", SQL: "DELETE FROM users USING orders WHERE orders.user_id = users.id", Reject: connector.RejectMultipleTables},
		{Name: "pg: on conflict do nothing", SQL: "INSERT INTO users (id, email, status) VALUES (1, 'a', 'b') ON CONFLICT DO NOTHING", Reject: connector.RejectUpsert},
		{Name: "pg: on conflict do update", SQL: "INSERT INTO users (id, email, status) VALUES (1, 'a', 'b') ON CONFLICT (id) DO UPDATE SET email = excluded.email", Reject: connector.RejectUpsert},
		{Name: "pg: insert returning", SQL: "INSERT INTO users (id, email, status) VALUES (1, 'a', 'b') RETURNING id", Reject: connector.RejectReturning},
		{Name: "pg: update returning", SQL: "UPDATE users SET name = 'x' WHERE id = 1 RETURNING *", Reject: connector.RejectReturning},
		{Name: "pg: delete returning", SQL: "DELETE FROM users WHERE id = 1 RETURNING id", Reject: connector.RejectReturning},
		{Name: "pg: merge", SQL: "MERGE INTO users u USING orders o ON o.user_id = u.id WHEN MATCHED THEN DELETE", Reject: connector.RejectStatementType},
		{Name: "pg: copy", SQL: "COPY users TO STDOUT", Reject: connector.RejectStatementType},
		{Name: "pg: lock", SQL: "LOCK TABLE users", Reject: connector.RejectStatementType},
		{Name: "pg: explain analyze", SQL: "EXPLAIN ANALYZE DELETE FROM users WHERE id = 1", Reject: connector.RejectStatementType},
		{Name: "pg: do block", SQL: "DO $$ BEGIN DELETE FROM users; END $$", Reject: connector.RejectStatementType},
		{Name: "pg: vacuum", SQL: "VACUUM users", Reject: connector.RejectStatementType},
		{Name: "pg: prepare", SQL: "PREPARE p AS DELETE FROM users WHERE id = $1", Reject: connector.RejectStatementType},
		{Name: "pg: set search_path", SQL: "SET search_path = evil", Reject: connector.RejectStatementType},
		{Name: "pg: set local role", SQL: "SET LOCAL ROLE postgres", Reject: connector.RejectStatementType},
		{Name: "pg: select into", SQL: "SELECT * INTO copy FROM users", Reject: connector.RejectStatementType},
		{Name: "pg: table statement", SQL: "TABLE users", Reject: connector.RejectStatementType},
		{Name: "pg: create function", SQL: "CREATE FUNCTION f() RETURNS int AS 'select 1' LANGUAGE sql", Reject: connector.RejectStatementType},
		{Name: "pg: comment only", SQL: "-- nothing here", Reject: connector.RejectEmpty},
		{Name: "pg: default values", SQL: "INSERT INTO users DEFAULT VALUES", Reject: connector.RejectInsertForm},
		{Name: "pg: values length mismatch", SQL: "INSERT INTO users (id, email) VALUES (1), (2, 'b')", Reject: connector.RejectInsertForm},
		{Name: "pg: overriding system value", SQL: "INSERT INTO users (id, email, status) OVERRIDING SYSTEM VALUE VALUES (1, 'a', 'b')", Reject: connector.RejectUnsupported},
		{Name: "pg: pg_catalog target", SQL: "DELETE FROM pg_catalog.pg_class WHERE oid = 1", Reject: connector.RejectSystemSchema},
		{Name: "pg: unqualified catalog target", SQL: "UPDATE pg_authid SET rolsuper = true WHERE rolname = 'app'", Reject: connector.RejectSystemSchema},
		{Name: "pg: catalog in subquery", SQL: "DELETE FROM users WHERE name IN (SELECT rolname FROM pg_authid)", Reject: connector.RejectSystemSchema},
		{Name: "pg: toast schema", SQL: "DELETE FROM pg_toast.pg_toast_1 WHERE chunk_id = 1", Reject: connector.RejectSystemSchema},
		{Name: "pg: blocked schema", SQL: "DELETE FROM secret.keys WHERE id = 1", Policy: &connector.Policy{BlockedSchemas: []string{"secret"}}, Reject: connector.RejectSystemSchema},
		{Name: "pg: three-part name", SQL: "DELETE FROM otherdb.public.users WHERE id = 1", Reject: connector.RejectOtherDatabase},
		{Name: "pg: function in other schema", SQL: "UPDATE users SET name = public.lower(name) WHERE id = 1", Reject: connector.RejectFunction},
		{Name: "pg: quoted function name", SQL: `UPDATE users SET name = "Lower"(name) WHERE id = 1`, Reject: connector.RejectFunction},
		{Name: "pg: function in subquery from", SQL: "DELETE FROM users WHERE id IN (SELECT * FROM generate_series(1, 3))", Reject: connector.RejectUnsupported},
		{Name: "pg: pg_sleep", SQL: "DELETE FROM users WHERE id = 1 AND pg_sleep(10) IS NOT NULL", Reject: connector.RejectFunction},
		{Name: "pg: set_config", SQL: "UPDATE users SET name = set_config('role', 'postgres', false) WHERE id = 1", Reject: connector.RejectFunction},
		{Name: "pg: nextval", SQL: "INSERT INTO users (id, email, status) VALUES (nextval('s'), 'a', 'b')", Reject: connector.RejectFunction},
		{Name: "pg: dblink", SQL: "DELETE FROM users WHERE id IN (SELECT id FROM users WHERE dblink_exec('x', 'y') = 'z')", Reject: connector.RejectFunction},
		{Name: "pg: aggregate in subquery", SQL: "DELETE FROM orders WHERE total > (SELECT max(total) FROM orders)", Reject: connector.RejectFunction},
		{Name: "pg: count star", SQL: "DELETE FROM users WHERE 0 < (SELECT count(*) FROM orders WHERE orders.user_id = users.id)", Reject: connector.RejectFunction},
		{Name: "pg: greatest not allowed", SQL: "UPDATE orders SET total = greatest(total, 1) WHERE id = 1", Reject: connector.RejectFunction},
		{Name: "pg: current_user not allowed", SQL: "UPDATE users SET name = current_user WHERE id = 1", Reject: connector.RejectFunction},
		{Name: "pg: cast not allowed", SQL: "UPDATE users SET name = 1::text WHERE id = 1", Policy: &connector.Policy{AllowedFunctions: []string{"lower"}}, Reject: connector.RejectFunction},
		{Name: "pg: custom type cast", SQL: "UPDATE users SET name = 'x'::myschema.mytype WHERE id = 1", Reject: connector.RejectFunction},
		{Name: "pg: operator in other schema", SQL: "DELETE FROM users WHERE id OPERATOR(public.=) 1", Reject: connector.RejectFunction},
		{Name: "pg: parameter", SQL: "DELETE FROM users WHERE id = $1", Reject: connector.RejectUnsupported},
		{Name: "pg: where current of", SQL: "DELETE FROM users WHERE CURRENT OF c", Reject: connector.RejectUnsupported},
		{Name: "pg: subscript assignment", SQL: "UPDATE users SET tags[1] = 'x' WHERE id = 1", Reject: connector.RejectUnsupported},
		{Name: "pg: multi-column set", SQL: "UPDATE users SET (name, status) = ('a', 'b') WHERE id = 1", Reject: connector.RejectUnsupported},
		{Name: "pg: locking subquery", SQL: "DELETE FROM users WHERE id IN (SELECT id FROM users FOR UPDATE)", Reject: connector.RejectUnsupported},
		{Name: "pg: union subquery", SQL: "DELETE FROM users WHERE id IN (SELECT 1 UNION SELECT 2)", Reject: connector.RejectUnsupported},
		{Name: "pg: with in subquery", SQL: "DELETE FROM users WHERE id IN (WITH x AS (SELECT 1) SELECT * FROM x)", Reject: connector.RejectWith},
		{Name: "pg: xml", SQL: "UPDATE users SET name = xmlelement(name a) WHERE id = 1", Reject: connector.RejectUnsupported},
		{Name: "pg: window function", SQL: "DELETE FROM users WHERE id IN (SELECT lower(email) OVER () FROM users)", Reject: connector.RejectFunction},
		{Name: "pg: named argument", SQL: "UPDATE users SET name = lower(x => name) WHERE id = 1", Reject: connector.RejectUnsupported},
		{Name: "pg: status equals status", SQL: "UPDATE users SET name = 'x' WHERE status = status", Reject: connector.RejectConstantWhere},
		{Name: "pg: boolean cast true", SQL: "DELETE FROM users WHERE 't'::boolean", Reject: connector.RejectConstantWhere},
		{Name: "pg: uncorrelated exists", SQL: "DELETE FROM users WHERE EXISTS (SELECT 1 FROM orders WHERE total > 100)", Reject: connector.RejectConstantWhere},
		{Name: "pg: or true with columns", SQL: "DELETE FROM users WHERE id IS NOT NULL OR true", Reject: connector.RejectConstantWhere},
		{Name: "pg: nul byte", SQL: "DELETE FROM users WHERE id = 1\x00", Reject: connector.RejectParse},
		{Name: "pg: stacked semicolons second statement", SQL: "DELETE FROM users WHERE id = 1;;SELECT 1", Reject: connector.RejectMultipleStatements},
	}
}

// TestValidate runs the shared and Postgres corpora without a database.
func TestValidate(t *testing.T) {
	connectortest.RunValidation(t, baseHarness())
}

func TestValidateDefaultSchema(t *testing.T) {
	cfg := validationConfig()
	cfg[keyDefaultSchema] = "app"
	st, err := New().Validate(cfg, "DELETE FROM users WHERE id = 1", connector.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Schema != "app" {
		t.Errorf("Schema = %q, want app", st.Schema)
	}
	st, err = New().Validate(cfg, "DELETE FROM public.users WHERE id = 1", connector.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Schema != "public" {
		t.Errorf("Schema = %q, want public", st.Schema)
	}
	// A system schema as the default is rejected.
	_, err = validate("DELETE FROM users WHERE id = 1", "pg_catalog", connector.Policy{})
	var rej *connector.RejectedError
	if !errors.As(err, &rej) || rej.Code != connector.RejectSystemSchema {
		t.Errorf("err = %v, want system_schema", err)
	}
}

func TestValidateRejectionPositions(t *testing.T) {
	cases := []struct {
		sql  string
		want string // the text at the reported position starts with this
	}{
		{"UPDATE users SET name = md5(name) WHERE id = 1", "md5("},
		{"DELETE FROM users WHERE id = 1; DROP TABLE users", " DROP"},
		{"DELETE FROM pg_catalog.pg_class WHERE oid = 1", "pg_catalog.pg_class"},
		{"UPDATE users SET name = 'x' FROM orders WHERE true", "orders"},
		{"DELETE FROM users WHERE id = $1", "$1"},
		{"UPDATE users SET name = 'x' WHERE id = 1 RETURNING id", "id"},
		{"UPDATE users SETT name = 1", "name"},
	}
	for _, c := range cases {
		_, err := validate(c.sql, "public", connector.Policy{})
		var rej *connector.RejectedError
		if !errors.As(err, &rej) {
			t.Errorf("%q: err = %v, want rejection", c.sql, err)
			continue
		}
		if rej.Position < 0 || !strings.HasPrefix(c.sql[rej.Position:], c.want) {
			t.Errorf("%q: position %d (%q), want text starting %q", c.sql, rej.Position, safeSlice(c.sql, rej.Position), c.want)
		}
	}
}

func safeSlice(s string, i int) string {
	if i < 0 || i > len(s) {
		return ""
	}
	return s[i:]
}

func TestValidateStatementFields(t *testing.T) {
	st, err := validate("INSERT INTO users (id, email, status) VALUES (1, 'a', 'b'), (2, 'c', 'd'), (3, 'e', 'f')", "public", connector.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if st.InsertRows != 3 || st.Where != "" {
		t.Errorf("statement = %+v", st)
	}
	st, err = validate("UPDATE users u SET name = lower(u.name), status = 'a' WHERE u.id = 1", "public", connector.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Alias != "u" || st.Where != "u.id = 1" || strings.Join(st.Columns, ",") != "name,status" {
		t.Errorf("statement = %+v", st)
	}
}

func TestStatementBody(t *testing.T) {
	for in, want := range map[string]string{
		"INSERT INTO t (a) VALUES (1);":            "INSERT INTO t (a) VALUES (1)",
		"INSERT INTO t (a) VALUES (1) -- c\n;\n":   "INSERT INTO t (a) VALUES (1)",
		"INSERT INTO t (a) VALUES (1) /* x */ ; ;": "INSERT INTO t (a) VALUES (1)",
		"INSERT INTO t (a) VALUES (';')":           "INSERT INTO t (a) VALUES (';')",
	} {
		got, err := statementBody(in)
		if err != nil || got != want {
			t.Errorf("statementBody(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func FuzzValidate(f *testing.F) {
	for _, c := range append(connectortest.SharedCorpus(), corpus()...) {
		f.Add(c.SQL)
	}
	f.Fuzz(func(t *testing.T, sql string) {
		st, err := validate(sql, "public", connector.Policy{})
		if err != nil {
			var rej *connector.RejectedError
			if !errors.As(err, &rej) {
				t.Fatalf("Validate(%q) returned a non-rejection error: %v", sql, err)
			}
			return
		}
		switch st.Kind {
		case connector.KindInsert:
		case connector.KindUpdate, connector.KindDelete:
			if st.Where == "" {
				t.Fatalf("accepted %s without WHERE text: %q", st.Kind, sql)
			}
		default:
			t.Fatalf("accepted kind %q: %q", st.Kind, sql)
		}
		if st.Table == "" || st.Schema == "" {
			t.Fatalf("accepted without a table: %q", sql)
		}
	})
}
