package postgres

import (
	"context"
	"fmt"
	"testing"

	"github.com/alvaroibarguen/dbairlock/internal/connector"
	"github.com/alvaroibarguen/dbairlock/internal/connector/connectortest"
)

// Phase 11 safety audit: SQL written to slip past validation. Every case
// is either rejected or accepted as exactly one single-table INSERT,
// UPDATE or DELETE with the WHERE text the server will run. Outcomes are
// recorded in docs/report-notes/safety.md.

// Non-ASCII characters used below, as UTF-8 byte escapes so that the
// source stays readable.
const (
	nbsp          = "\xc2\xa0"     // U+00A0 NO-BREAK SPACE
	zeroWidth     = "\xe2\x80\x8b" // U+200B ZERO WIDTH SPACE
	fullwidthSemi = "\xef\xbc\x9b" // U+FF1B FULLWIDTH SEMICOLON
	cyrillicE     = "\xd0\x95"     // U+0415 CYRILLIC CAPITAL LETTER IE, looks like E
	cyrillicA     = "\xd0\xb0"     // U+0430 CYRILLIC SMALL LETTER A, looks like a
	lineSep       = "\xe2\x80\xa8" // U+2028 LINE SEPARATOR
	nextLine      = "\xc2\x85"     // U+0085 NEXT LINE
)

// hostileCorpus is run through the same checks as the validation corpus.
func hostileCorpus() []connectortest.Case {
	upd, del := connector.KindUpdate, connector.KindDelete
	parse, stmtType, multi := connector.RejectParse, connector.RejectStatementType, connector.RejectMultipleStatements
	return []connectortest.Case{
		// Nested comments: Postgres nests /* */, so the "second statement"
		// below is inside the comment and the text is one DELETE.
		{Name: "hostile: nested comment hides text", SQL: "DELETE FROM users WHERE id = 1 /* outer /* inner */ ; DROP TABLE users; */",
			Kind: del, Table: "users", Where: "id = 1"},
		{Name: "hostile: nested comment closed early", SQL: "DELETE FROM users WHERE id = 1 /* a */ */ OR true", Reject: parse},
		{Name: "hostile: nested comment then or true", SQL: "DELETE FROM users WHERE id = 1 /* o /* i */ */ OR true", Reject: connector.RejectConstantWhere},
		{Name: "hostile: unterminated nested comment", SQL: "/* /* */ DELETE FROM users WHERE id = 1", Reject: parse},
		// Unicode whitespace and look-alikes are identifier characters to
		// Postgres, never whitespace or punctuation.
		{Name: "hostile: no-break space after DELETE", SQL: "DELETE" + nbsp + "FROM users WHERE id = 1", Reject: parse},
		{Name: "hostile: no-break space before WHERE", SQL: "DELETE FROM users" + nbsp + "WHERE id = 1", Reject: parse},
		{Name: "hostile: fullwidth semicolon", SQL: "DELETE FROM users WHERE id = 1" + fullwidthSemi + " DROP TABLE users", Reject: parse},
		{Name: "hostile: fullwidth semicolon spaced", SQL: "DELETE FROM users WHERE id = 1 " + fullwidthSemi + "DROP TABLE users", Reject: parse},
		{Name: "hostile: zero-width space after number", SQL: "DELETE FROM users WHERE id = 1" + zeroWidth, Reject: parse},
		{Name: "hostile: homoglyph WHERE", SQL: "DELETE FROM users WH" + cyrillicE + "RE id = 1", Reject: parse},
		{Name: "hostile: homoglyph WHERE becomes alias", SQL: "DELETE FROM users WH" + cyrillicE + "RE", Reject: connector.RejectMissingWhere},
		{Name: "hostile: homoglyph AS", SQL: "DELETE FROM users " + cyrillicA + "s u WHERE id = 1", Reject: parse},
		{Name: "hostile: line separator does not end a comment", SQL: "DELETE FROM users WHERE id = 1 -- " + lineSep + " OR true", Kind: del, Where: "id = 1"},
		{Name: "hostile: next line does not end a comment", SQL: "DELETE FROM users WHERE id = 1 --" + nextLine + " OR true", Kind: del, Where: "id = 1"},
		{Name: "hostile: carriage return ends a comment", SQL: "DELETE FROM users WHERE id = 1 --\r OR true", Reject: connector.RejectConstantWhere},
		{Name: "hostile: control whitespace", SQL: "DELETE\fFROM\vusers\rWHERE\tid = 1", Kind: del, Table: "users", Where: "id = 1"},
		// Case and quoting.
		{Name: "hostile: mixed-case keywords", SQL: "dElEtE fRoM users wHeRe id = 1", Kind: del, Table: "users", Where: "id = 1"},
		{Name: "hostile: keyword-named table and column", SQL: `DELETE FROM "where" WHERE "where" = 1`, Kind: del, Table: "where", Where: `"where" = 1`},
		{Name: "hostile: keyword column and string", SQL: `DELETE FROM users WHERE "WHERE" = 'WHERE'`, Kind: del, Where: `"WHERE" = 'WHERE'`},
		{Name: "hostile: WHERE inside a set value", SQL: `UPDATE users SET "where" = 'WHERE x' WHERE id = 1`, Kind: upd, Where: "id = 1", Columns: []string{"where"}},
		{Name: "hostile: every name a keyword", SQL: `UPDATE "UPDATE" SET "SET" = 1 WHERE "WHERE" = 2`, Kind: upd, Table: "UPDATE", Where: `"WHERE" = 2`},
		{Name: "hostile: uppercase catalog schema", SQL: "DELETE FROM PG_CATALOG.pg_class WHERE oid = 1", Reject: connector.RejectSystemSchema},
		{Name: "hostile: unicode-escaped catalog table", SQL: `DELETE FROM U&"\0070g_authid" WHERE rolname = 'x'`, Reject: connector.RejectSystemSchema},
		{Name: "hostile: unicode-escaped table", SQL: `DELETE FROM U&"\0075sers" WHERE id = 1`, Kind: del, Table: "users"},
		{Name: "hostile: quoted function name is case-sensitive", SQL: `DELETE FROM users WHERE id = 1 AND "lower"(name) = 'x'`, Kind: del, Functions: []string{"lower"}},
		// Stacked statements.
		{Name: "hostile: stacked after a comment line", SQL: "DELETE FROM users WHERE id = 1; -- \n DELETE FROM users WHERE id = 2", Reject: multi},
		{Name: "hostile: semicolon inside a line comment", SQL: "DELETE FROM users WHERE id = 1 --; DELETE FROM users\n", Kind: del, Where: "id = 1"},
		{Name: "hostile: select then delete", SQL: "SELECT 1; DELETE FROM users WHERE id = 1", Reject: multi},
		{Name: "hostile: two inserts", SQL: "INSERT INTO users (id, email, status) VALUES (1, 'a', 'b'); INSERT INTO users (id, email, status) VALUES (2, 'a', 'b')", Reject: multi},
		{Name: "hostile: psql meta-command", SQL: "DELETE FROM users WHERE id = 1 \\gexec", Reject: parse},
		{Name: "hostile: explain wraps a delete", SQL: "EXPLAIN DELETE FROM users WHERE id = 1", Reject: stmtType},
		{Name: "hostile: data-modifying CTE in a value", SQL: "UPDATE users SET name = (WITH d AS (DELETE FROM orders RETURNING 1) SELECT 'x') WHERE id = 1", Reject: connector.RejectWith},
		{Name: "hostile: delete inside a value", SQL: "INSERT INTO users (id, email, status) VALUES (1, 'a', (DELETE FROM users WHERE id = 1 RETURNING status))", Reject: parse},
		// Dollar quoting, escape strings and Unicode strings.
		{Name: "hostile: dollar quote hides a statement", SQL: "UPDATE users SET name = $a$ ; DROP TABLE users; $a$ WHERE id = 1", Kind: upd, Where: "id = 1"},
		{Name: "hostile: mismatched dollar tags", SQL: "UPDATE users SET name = $a$x$b$ WHERE id = 1", Reject: parse},
		{Name: "hostile: dollar tag that looks like a parameter", SQL: "UPDATE users SET name = $1$x WHERE id = 1", Reject: parse},
		{Name: "hostile: escaped quote in E string", SQL: `UPDATE users SET name = E'\'' WHERE id = 1 --'`, Kind: upd, Where: "id = 1"},
		{Name: "hostile: escaped backslash in E string", SQL: `UPDATE users SET name = E'\\' WHERE id = 1 --' WHERE id > 0`, Kind: upd, Where: "id = 1"},
		// With standard_conforming_strings on (pinned for every run), a
		// backslash is an ordinary character in '...'.
		{Name: "hostile: backslash before quote", SQL: `UPDATE users SET name = 'x\' WHERE id = 1 --' WHERE id > 0`, Kind: upd, Where: "id = 1", Functions: []string{}},
		{Name: "hostile: backslash hides a function", SQL: `UPDATE users SET name = 'x\' WHERE id = 1 --' || current_user WHERE id = 1`, Kind: upd, Where: "id = 1", Functions: []string{}},
		{Name: "hostile: unicode string", SQL: `UPDATE users SET name = U&'!0041' UESCAPE '!' WHERE id = 1`, Kind: upd, Where: "id = 1"},
		// Constant WHERE clauses in disguise.
		{Name: "hostile: null is null", SQL: "DELETE FROM users WHERE id = 1 OR NULL IS NULL", Reject: connector.RejectConstantWhere},
		{Name: "hostile: true is true", SQL: "DELETE FROM users WHERE TRUE IS TRUE", Reject: connector.RejectConstantWhere},
		{Name: "hostile: scalar subquery true", SQL: "DELETE FROM users WHERE (SELECT true)", Reject: connector.RejectConstantWhere},
		{Name: "hostile: string equality", SQL: "DELETE FROM users WHERE id = 1 OR 'a' = 'a'", Reject: connector.RejectConstantWhere},
		// Functions reached indirectly.
		{Name: "hostile: function in ANY subquery", SQL: "DELETE FROM users WHERE id = 1 AND name = ANY(SELECT lo_import('/etc/passwd')::text)", Reject: connector.RejectFunction},
		{Name: "hostile: current_setting", SQL: "DELETE FROM users WHERE id = 1 AND current_setting('x') = 'y'", Reject: connector.RejectFunction},
	}
}

// TestHostileValidation runs the hostile corpus without a database.
func TestHostileValidation(t *testing.T) {
	h := baseHarness()
	h.Corpus = hostileCorpus()
	connectortest.RunValidation(t, h)
}

// testHostileOnServer runs accepted hostile statements on a database whose
// default for standard_conforming_strings is off, as some legacy
// installations have it. With that setting the server reads a backslash in
// '...' as an escape while the parser (and so the validator) does not, so
// the text the reviewer approved would run as a different statement: a
// different WHERE, and functions the allowlist never saw. The connector
// pins the setting on for every transaction.
func testHostileOnServer(t *testing.T, srv *server) {
	tg := srv.newTarget(t).(*target)
	if err := tg.Admin(context.Background(), fmt.Sprintf("ALTER DATABASE %s SET standard_conforming_strings = off", tg.db)); err != nil {
		t.Fatal(err)
	}
	c := open(t, tg, connectortest.LeastPrivilege, nil)
	cfg, _ := tg.Config(connectortest.LeastPrivilege)
	lim := connector.DefaultLimits()

	cases := []struct {
		sql       string
		rows      int64
		nameAfter string // the users.name of the first after-image row, when set
	}{
		{`UPDATE users SET name = 'x\' WHERE id = 1 --' WHERE id > 0`, 1, `x\`},
		{`UPDATE users SET name = 'x\' WHERE id = 1 --' || current_user WHERE id = 1`, 1, `x\`},
		{"DELETE FROM users WHERE id = 1 /* outer /* inner */ ; DELETE FROM orders; */", 1, ""},
		{"UPDATE users SET name = $a$ ; DELETE FROM orders; $a$ WHERE id = 2", 1, " ; DELETE FROM orders; "},
		{`UPDATE users SET name = E'\'' WHERE id = 3 --' OR id > 0`, 1, "'"},
		{`UPDATE users SET name = U&'!0041' UESCAPE '!' WHERE id = 4`, 1, "A"},
		{"dElEtE fRoM users wHeRe id = 5 -- " + lineSep + " OR true", 1, ""},
		{`DELETE FROM users WHERE "status" = 'banned' --' OR true`, 1, ""},
	}
	for _, tc := range cases {
		st, err := New().Validate(cfg, tc.sql, connector.Policy{})
		if err != nil {
			t.Fatalf("Validate(%q): %v", tc.sql, err)
		}
		res, err := c.DryRun(tctx(t), []connector.Statement{st}, lim, connector.RunInfo{Label: "dbairlock preview"})
		if err != nil {
			t.Fatalf("DryRun(%q): %v", tc.sql, err)
		}
		r := res[0]
		// The rows the WHERE text matched (the before image the reviewer
		// sees) are the rows the statement changed.
		if r.RowsAffected != tc.rows || int64(len(r.Before)) != r.RowsAffected {
			t.Errorf("%q: %d rows affected, %d in the before image; want %d", tc.sql, r.RowsAffected, len(r.Before), tc.rows)
		}
		if tc.nameAfter != "" {
			if len(r.After) != 1 || r.After[0][2] != tc.nameAfter {
				t.Errorf("%q: after image %v, want name %q", tc.sql, r.After, tc.nameAfter)
			}
		}
	}

	// Executed, the first statement changes exactly the previewed row.
	st, err := New().Validate(cfg, cases[0].sql, connector.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	dry, err := c.DryRun(tctx(t), []connector.Statement{st}, lim, connector.RunInfo{})
	if err != nil {
		t.Fatal(err)
	}
	exp := []connector.Expectation{{Rows: dry[0].RowsAffected, Fingerprint: dry[0].Fingerprint}}
	if _, err := c.Execute(tctx(t), []connector.Statement{st}, exp, lim, connector.RunInfo{Label: "dbairlock change #1"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	rows, err := tg.AdminQuery(context.Background(), "SELECT id, name FROM users ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"1", `x\`}, {"2", "Bob"}, {"3", "Cat"}, {"4", "Dan"}, {"5", "Eve"}}
	if fmt.Sprint(rows) != fmt.Sprint(want) {
		t.Errorf("users = %v, want %v", rows, want)
	}
	if n, _ := tg.AdminQuery(context.Background(), "SELECT count(*) FROM orders"); n[0][0] != "4" {
		t.Errorf("orders count = %s", n[0][0])
	}
}
