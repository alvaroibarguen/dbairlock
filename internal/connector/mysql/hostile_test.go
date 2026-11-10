package mysql

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
	fullwidthSemi = "\xef\xbc\x9b" // U+FF1B FULLWIDTH SEMICOLON
	cyrillicE     = "\xd0\x95"     // U+0415 CYRILLIC CAPITAL LETTER IE, looks like E
)

func hostileCorpus() []connectortest.Case {
	upd, del := connector.KindUpdate, connector.KindDelete
	parse, multi, exec := connector.RejectParse, connector.RejectMultipleStatements, connector.RejectExecutableComment
	constant := connector.RejectConstantWhere
	return []connectortest.Case{
		// MySQL does not nest comments: the first */ closes.
		{Name: "hostile: nested comment", SQL: "DELETE FROM users WHERE id = 1 /* outer /* inner */ ; DROP TABLE users; */", Reject: parse},
		{Name: "hostile: nested comment exposes OR", SQL: "DELETE FROM users WHERE id = 1 /* outer /* inner */ OR id > 0 -- */",
			Kind: del, Where: "id = 1 /* outer /* inner */ OR id > 0"},
		{Name: "hostile: slash inside comment opener", SQL: "UPDATE users SET name = 'x' WHERE id = 1 /*/ OR id > 0 */", Kind: upd, Where: "id = 1"},
		{Name: "hostile: empty comment", SQL: "UPDATE users SET name = 'x' WHERE id = 1 /**/ OR id > 0", Kind: upd, Where: "id = 1 /**/ OR id > 0"},
		// Executable comments, wherever they are, even inside strings.
		{Name: "hostile: executable comment", SQL: "UPDATE users SET name = 'x' WHERE id = 1 /*! OR 1=1 */", Reject: exec},
		{Name: "hostile: versioned executable comment", SQL: "UPDATE users SET name = 'x' WHERE id = 1 /*!50000 OR 1=1 */", Reject: exec},
		{Name: "hostile: optimizer hint", SQL: "UPDATE /*+ BKA(users) */ users SET name = 'x' WHERE id = 1", Reject: exec},
		{Name: "hostile: executable comment opener in a string", SQL: "UPDATE users SET name = '/*!' WHERE id = 1", Reject: exec},
		{Name: "hostile: space after comment bang", SQL: "UPDATE users SET name = 'x' WHERE id = 1 /* ! OR 1=1 */", Kind: upd, Where: "id = 1"},
		// "-- " needs whitespace or a control character after it; "#" needs
		// nothing.
		{Name: "hostile: double dash without space is minus minus", SQL: "DELETE FROM users WHERE id = 2--1", Kind: del, Where: "id = 2--1"},
		{Name: "hostile: double dash then digit", SQL: "DELETE FROM users WHERE id = 1 --1", Kind: del, Where: "id = 1 --1"},
		{Name: "hostile: double dash tab", SQL: "DELETE FROM users WHERE id = 2 --\t OR id > 0", Kind: del, Where: "id = 2"},
		{Name: "hostile: double dash vertical tab", SQL: "DELETE FROM users WHERE id = 2 --\v OR id > 0", Kind: del, Where: "id = 2"},
		{Name: "hostile: double dash form feed", SQL: "DELETE FROM users WHERE id = 2 --\f OR id > 0", Kind: del, Where: "id = 2"},
		{Name: "hostile: double dash carriage return", SQL: "DELETE FROM users WHERE id = 2 --\r OR id > 0", Kind: del, Where: "id = 2"},
		{Name: "hostile: double dash control character", SQL: "DELETE FROM users WHERE id = 2 --\x01 OR id > 0", Reject: parse},
		{Name: "hostile: double dash delete character", SQL: "DELETE FROM users WHERE id = 2 --\x7f OR id > 0", Reject: parse},
		{Name: "hostile: hash comment", SQL: "DELETE FROM users WHERE id = 1 #; DELETE FROM users\n", Kind: del, Where: "id = 1"},
		{Name: "hostile: hash comment with carriage return", SQL: "DELETE FROM users WHERE id = 2 #\r OR id > 0", Kind: del, Where: "id = 2"},
		// Unicode whitespace and look-alikes are identifier characters.
		{Name: "hostile: no-break space after DELETE", SQL: "DELETE" + nbsp + "FROM users WHERE id = 1", Reject: parse},
		{Name: "hostile: no-break space before WHERE", SQL: "DELETE FROM users" + nbsp + "WHERE id = 1", Reject: parse},
		{Name: "hostile: no-break spaces make one alias", SQL: "DELETE FROM users WHERE" + nbsp + "NOT" + nbsp + "status", Reject: connector.RejectMissingWhere},
		{Name: "hostile: fullwidth semicolon", SQL: "DELETE FROM users WHERE id = 1" + fullwidthSemi + " DROP TABLE users", Reject: parse},
		{Name: "hostile: homoglyph WHERE", SQL: "DELETE FROM users WH" + cyrillicE + "RE id = 1", Reject: parse},
		{Name: "hostile: homoglyph WHERE becomes alias", SQL: "DELETE FROM users WH" + cyrillicE + "RE", Reject: connector.RejectMissingWhere},
		{Name: "hostile: substitute character", SQL: "DELETE FROM users WHERE id = 1\x1a", Reject: parse},
		// Case and quoting.
		{Name: "hostile: mixed-case keywords", SQL: "dElEtE fRoM users wHeRe id = 1", Kind: del, Table: "users", Where: "id = 1"},
		{Name: "hostile: keyword-named table and column", SQL: "DELETE FROM `where` WHERE `where` = 1", Kind: del, Table: "where", Where: "`where` = 1"},
		{Name: "hostile: keyword column and string", SQL: "DELETE FROM users WHERE `WHERE` = 'WHERE'", Kind: del, Where: "`WHERE` = 'WHERE'"},
		{Name: "hostile: WHERE inside a set value", SQL: "UPDATE users SET `where` = 'WHERE x' WHERE id = 1", Kind: upd, Where: "id = 1", Columns: []string{"where"}},
		{Name: "hostile: uppercase system database", SQL: "DELETE FROM MYSQL.user WHERE user = 'x'", Reject: connector.RejectSystemSchema},
		{Name: "hostile: quoted system database", SQL: "DELETE FROM `mysql`.user WHERE user = 'x'", Reject: connector.RejectSystemSchema},
		{Name: "hostile: other database by case", SQL: "DELETE FROM APP.users WHERE id = 1", Reject: connector.RejectOtherDatabase},
		{Name: "hostile: backquoted function", SQL: "DELETE FROM users WHERE id = 1 AND `sleep`(1) = 0", Reject: connector.RejectFunction},
		{Name: "hostile: function name then space", SQL: "DELETE FROM users WHERE id = 1 AND SLEEP (1) = 0", Reject: connector.RejectFunction},
		// Stacked statements.
		{Name: "hostile: stacked after a hash comment line", SQL: "DELETE FROM users WHERE id = 1; # \n DELETE FROM users WHERE id = 2", Reject: multi},
		{Name: "hostile: client delimiter command", SQL: "DELETE FROM users WHERE id = 1 \\G", Reject: parse},
		// Backslashes: the session is pinned to backslash escapes, as the
		// parser assumes, whatever the server's sql_mode.
		{Name: "hostile: backslash before quote", SQL: `UPDATE users SET name = 'a\' WHERE id = 1 -- ' WHERE id > 0`, Kind: upd, Where: "id > 0"},
		{Name: "hostile: escaped backslash before quote", SQL: `UPDATE users SET name = 'a\\' WHERE id = 1 -- ' WHERE id > 0`, Kind: upd, Where: "id = 1"},
		{Name: "hostile: backslash in double-quoted string", SQL: `UPDATE users SET name = "a\" WHERE id = 1 -- " WHERE id > 0`, Kind: upd, Where: "id > 0"},
		{Name: "hostile: doubled quote", SQL: `UPDATE users SET name = 'a''' WHERE id = 1 -- ' WHERE id > 0`, Kind: upd, Where: "id = 1"},
		// Constant WHERE clauses in disguise.
		{Name: "hostile: pipes are OR", SQL: "DELETE FROM users WHERE id = 1 || true", Reject: constant},
		{Name: "hostile: string that is true", SQL: "DELETE FROM users WHERE id = 1 OR ' 1'", Reject: constant},
		{Name: "hostile: hex literal that is true", SQL: "DELETE FROM users WHERE id = 1 OR X'31'", Reject: constant},
		{Name: "hostile: null-safe self comparison", SQL: "DELETE FROM users WHERE id <=> id", Reject: constant},
		{Name: "hostile: case that is true", SQL: "DELETE FROM users WHERE id = 1 OR CASE WHEN 1 THEN 1 END", Reject: constant},
		{Name: "hostile: user variable", SQL: "DELETE FROM users WHERE id = 1 OR @a", Reject: connector.RejectUnsupported},
		{Name: "hostile: system variable", SQL: "UPDATE users SET name = @@version WHERE id = 1", Reject: connector.RejectUnsupported},
		// Statement modifiers that change what a statement does.
		{Name: "hostile: delete ignore", SQL: "DELETE LOW_PRIORITY QUICK IGNORE FROM users WHERE id = 1", Reject: connector.RejectUnsupported},
		{Name: "hostile: insert row constructor", SQL: "INSERT INTO users (id, email, status) VALUES ROW(1, 'a', 'b')", Reject: connector.RejectInsertForm},
		{Name: "hostile: insert table", SQL: "INSERT INTO users (id, email, status) TABLE users", Reject: connector.RejectInsertForm},
		{Name: "hostile: set qualified with another database", SQL: "UPDATE users SET other.users.name = 'x' WHERE id = 1", Reject: connector.RejectOtherDatabase},
		{Name: "hostile: set on another table", SQL: "UPDATE users SET orders.name = 'x' WHERE id = 1", Reject: connector.RejectMultipleTables},
	}
}

// TestHostileValidation runs the hostile corpus without a database.
func TestHostileValidation(t *testing.T) {
	h := baseHarness()
	h.Corpus = hostileCorpus()
	connectortest.RunValidation(t, h)
}

// testHostileOnServer runs accepted hostile statements on a server whose
// sql_mode has every lexical flag the parser does not assume. Each must
// change exactly the rows its WHERE text selects for the preview.
func testHostileOnServer(t *testing.T, srv *server) {
	tg := srv.newTarget(t)
	set := func(mode string) {
		if err := tg.Admin(context.Background(), "SET GLOBAL sql_mode = '"+mode+"'"); err != nil {
			t.Fatal(err)
		}
	}
	set("ANSI,NO_BACKSLASH_ESCAPES,HIGH_NOT_PRECEDENCE,STRICT_TRANS_TABLES")
	t.Cleanup(func() {
		set("ONLY_FULL_GROUP_BY,STRICT_TRANS_TABLES,NO_ZERO_IN_DATE,NO_ZERO_DATE,ERROR_FOR_DIVISION_BY_ZERO,NO_ENGINE_SUBSTITUTION")
	})
	c := open(t, tg, connectortest.LeastPrivilege, nil)
	lim := connector.DefaultLimits()

	cases := []struct {
		sql     string
		ids     string // the ids of the rows changed, in order
		nameNow string // the new name of the first changed row, for updates
	}{
		// With NO_BACKSLASH_ESCAPES the server would end the string at the
		// backslash and run WHERE id = 1.
		{`UPDATE users SET name = 'a\' WHERE id = 1 -- ' WHERE id = 2`, "[2]", "a' WHERE id = 1 -- "},
		// With ANSI_QUOTES "..." would be an identifier.
		{`UPDATE users SET name = "a\" WHERE id = 1 -- " WHERE id = 3`, "[3]", `a" WHERE id = 1 -- `},
		// With PIPES_AS_CONCAT || would not be OR.
		{"DELETE FROM users WHERE id = 1 || id = 2", "[1 2]", ""},
		// With HIGH_NOT_PRECEDENCE this would be (NOT id) BETWEEN 2 AND 5.
		{"UPDATE users SET name = 'x' WHERE NOT id BETWEEN 2 AND 5", "[1]", "x"},
		{"DELETE FROM users WHERE id = 2--1", "[3]", ""},
		{"DELETE FROM users WHERE id = 1 --1", "[2]", ""},
		{"DELETE FROM users WHERE id = 2 --\t OR id > 0", "[2]", ""},
		{"DELETE FROM users WHERE id = 2 --\r OR id > 0", "[2]", ""},
		{"DELETE FROM users WHERE id = 2 #\r OR id > 0", "[2]", ""},
		{"DELETE FROM users WHERE id = 1 /* outer /* inner */ OR id = 2 -- */", "[1 2]", ""},
		{"UPDATE users SET name = 'x' WHERE id = 4 /*/ OR id > 0 */", "[4]", "x"},
		{"UPDATE users SET name = 'a' 'b' WHERE id = 5", "[5]", "ab"},
		{"dElEtE fRoM users wHeRe `id` = 4", "[4]", ""},
	}
	for _, tc := range cases {
		st := stmt(t, tg, tc.sql, connector.Policy{})
		res, err := c.DryRun(tctx(t), []connector.Statement{st}, lim, connector.RunInfo{Label: "dbairlock preview"})
		if err != nil {
			t.Errorf("DryRun(%q): %v", tc.sql, err)
			continue
		}
		r := res[0]
		var ids []any
		for _, row := range r.Before {
			ids = append(ids, row[0])
		}
		if fmt.Sprint(ids) != tc.ids || r.RowsAffected != int64(len(r.Before)) {
			t.Errorf("%q: before image ids %v, %d rows affected; want %s", tc.sql, ids, r.RowsAffected, tc.ids)
		}
		if tc.nameNow != "" && (len(r.After) == 0 || r.After[0][2] != tc.nameNow) {
			t.Errorf("%q: after image %v, want name %q", tc.sql, r.After, tc.nameNow)
		}
	}

	// Executed, the first statement changes exactly the previewed row.
	st := stmt(t, tg, cases[0].sql, connector.Policy{})
	dry, err := c.DryRun(tctx(t), []connector.Statement{st}, lim, connector.RunInfo{})
	if err != nil {
		t.Fatal(err)
	}
	exp := []connector.Expectation{{Rows: dry[0].RowsAffected, Fingerprint: dry[0].Fingerprint}}
	if _, err := c.Execute(tctx(t), []connector.Statement{st}, exp, lim, connector.RunInfo{Label: "dbairlock change #1"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	rows := adminRows(t, tg, "SELECT id, name FROM users ORDER BY id")
	want := [][]string{{"1", "Ann"}, {"2", "a' WHERE id = 1 -- "}, {"3", "Cat"}, {"4", "Dan"}, {"5", "Eve"}}
	if fmt.Sprint(rows) != fmt.Sprint(want) {
		t.Errorf("users = %q, want %q", rows, want)
	}
}
