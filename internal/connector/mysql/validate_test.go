package mysql

import (
	"errors"
	"strings"
	"testing"

	"github.com/alvaroibarguen/dbairlock/internal/connector"
	"github.com/alvaroibarguen/dbairlock/internal/connector/connectortest"
)

// corpus is the MySQL-specific validation corpus. The shared, portable
// corpus is in connectortest.SharedCorpus. Every rejection of phase 6 step
// 3 has at least one case here.
func corpus() []connectortest.Case {
	upd, del, ins := connector.KindUpdate, connector.KindDelete, connector.KindInsert
	return []connectortest.Case{
		// Accepted.
		{Name: "my: own database qualified", SQL: "UPDATE app.users SET name = 'x' WHERE id = 1", Kind: upd, Table: "users", Where: "id = 1"},
		{Name: "my: own database quoted", SQL: "DELETE FROM `app`.`users` WHERE `id` = 1", Kind: del, Table: "users", Where: "`id` = 1"},
		{Name: "my: backtick identifiers", SQL: "UPDATE `users` SET `name` = 'x' WHERE `id` = 1", Kind: upd, Table: "users", Columns: []string{"name"}},
		{Name: "my: alias", SQL: "UPDATE users u SET u.name = 'x' WHERE u.id = 1", Kind: upd, Table: "users", Where: "u.id = 1", Columns: []string{"name"}},
		{Name: "my: delete alias", SQL: "DELETE FROM users AS u WHERE u.id = 1", Kind: del, Table: "users", Where: "u.id = 1"},
		{Name: "my: alias correlated", SQL: "DELETE FROM users u WHERE EXISTS (SELECT 1 FROM orders o WHERE o.user_id = u.id)", Kind: del},
		{Name: "my: double-quoted string", SQL: `UPDATE users SET name = "O'Brien" WHERE id = 1`, Kind: upd, Where: "id = 1"},
		{Name: "my: backslash escape", SQL: `UPDATE users SET name = 'a\'b' WHERE id = 1`, Kind: upd, Where: "id = 1"},
		{Name: "my: hash comment", SQL: "DELETE FROM users WHERE id = 1 # the first", Kind: del, Where: "id = 1"},
		{Name: "my: double dash without space is minus", SQL: "DELETE FROM users WHERE id = 3 --1", Kind: del, Where: "id = 3 --1"},
		{Name: "my: plain block comment", SQL: "DELETE FROM users WHERE id = 1 /* the first */ AND status = 'x'", Kind: del, Where: "id = 1 /* the first */ AND status = 'x'"},
		{Name: "my: multi-line where with comment", SQL: "DELETE FROM users\nWHERE id = 1\n  AND status = 'banned' -- only banned\n;",
			Kind: del, Where: "id = 1\n  AND status = 'banned'"},
		{Name: "my: where keyword in a string", SQL: "UPDATE users SET name = 'x WHERE y' WHERE id = 1", Kind: upd, Where: "id = 1"},
		{Name: "my: where in a set subquery", SQL: "UPDATE orders SET user_id = (SELECT id FROM users WHERE email = 'bob@example.com') WHERE id = 1", Kind: upd, Where: "id = 1"},
		{Name: "my: insert default", SQL: "INSERT INTO users (id, email, name, status) VALUES (10, 'a@example.com', DEFAULT, 'new')", Kind: ins},
		{Name: "my: insert value keyword", SQL: "INSERT INTO users (id, email, status) VALUE (10, 'a', 'b')", Kind: ins},
		{Name: "my: typed literal", SQL: "UPDATE users SET updated_at = TIMESTAMP '2024-01-01 00:00:00' WHERE id = 1", Kind: upd, Functions: []string{"cast"}},
		{Name: "my: convert is its own function", SQL: "UPDATE users SET name = CONVERT(name, CHAR) WHERE id = 1",
			Policy: &connector.Policy{AllowedFunctions: []string{"convert"}}, Kind: upd, Functions: []string{"convert"}},
		{Name: "my: trim syntax", SQL: "UPDATE users SET name = TRIM(BOTH ' ' FROM name) WHERE id = 1", Kind: upd, Functions: []string{"trim"}},
		{Name: "my: null-safe equal", SQL: "UPDATE users SET name = 'x' WHERE name <=> NULL AND id = 1", Kind: upd},
		{Name: "my: regexp", SQL: "UPDATE users SET status = 'x' WHERE email REGEXP '^a'", Kind: upd, Functions: []string{}},
		{Name: "my: row comparison", SQL: "DELETE FROM orders WHERE (user_id, status) IN ((1, 'open'), (2, 'open'))", Kind: del},
		{Name: "my: join in subquery", SQL: "DELETE FROM orders WHERE user_id IN (SELECT u.id FROM users u JOIN orders o ON o.user_id = u.id WHERE u.status = 'banned')", Kind: del},
		{Name: "my: collate", SQL: "UPDATE users SET status = 'x' WHERE email = 'ANN@example.com' COLLATE utf8mb4_bin", Kind: upd},
		{Name: "my: and with a true constant", SQL: "UPDATE users SET name = 'x' WHERE id = 1 AND 1 = 1", Kind: upd, Where: "id = 1 AND 1 = 1"},
		{Name: "my: or with a false constant", SQL: "DELETE FROM users WHERE id = 1 OR 1 = 2", Kind: del},
		{Name: "my: sleep allowed by policy", SQL: "DELETE FROM users WHERE id = 1 AND SLEEP(0) = 0",
			Policy: &connector.Policy{AllowedFunctions: []string{"sleep"}}, Kind: del, Functions: []string{"sleep"}},

		// Rejected: statement types.
		{Name: "my: replace", SQL: "REPLACE INTO users (id, email, status) VALUES (1, 'a', 'b')", Reject: connector.RejectUpsert},
		{Name: "my: replace set", SQL: "REPLACE users SET id = 1", Reject: connector.RejectUpsert},
		{Name: "my: on duplicate key update", SQL: "INSERT INTO users (id, email, status) VALUES (1, 'a', 'b') ON DUPLICATE KEY UPDATE email = 'c'", Reject: connector.RejectUpsert},
		{Name: "my: on duplicate key update with row alias", SQL: "INSERT INTO users (id, email, status) VALUES (1, 'a', 'b') AS n ON DUPLICATE KEY UPDATE email = n.email", Reject: connector.RejectUpsert, Also: []connector.RejectCode{connector.RejectUnsupported}},
		{Name: "my: values function", SQL: "UPDATE users SET name = VALUES(name) WHERE id = 1", Reject: connector.RejectUpsert, Also: []connector.RejectCode{connector.RejectParse, connector.RejectUnsupported}},
		{Name: "my: load data", SQL: "LOAD DATA INFILE '/etc/passwd' INTO TABLE users", Reject: connector.RejectStatementType},
		{Name: "my: use", SQL: "USE mysql", Reject: connector.RejectStatementType},
		{Name: "my: lock tables", SQL: "LOCK TABLES users WRITE", Reject: connector.RejectStatementType},
		{Name: "my: start transaction", SQL: "START TRANSACTION", Reject: connector.RejectStatementType},
		{Name: "my: set names", SQL: "SET NAMES latin1", Reject: connector.RejectStatementType},
		{Name: "my: set sql_mode", SQL: "SET SESSION sql_mode = 'ANSI_QUOTES'", Reject: connector.RejectStatementType},
		{Name: "my: prepare", SQL: "PREPARE s FROM 'DELETE FROM users'", Reject: connector.RejectStatementType},
		{Name: "my: handler", SQL: "HANDLER users OPEN", Reject: connector.RejectStatementType, Also: []connector.RejectCode{connector.RejectParse}},
		{Name: "my: table statement", SQL: "TABLE users", Reject: connector.RejectStatementType},
		{Name: "my: select into outfile", SQL: "SELECT * FROM users INTO OUTFILE '/tmp/x'", Reject: connector.RejectStatementType},
		{Name: "my: explain", SQL: "EXPLAIN DELETE FROM users WHERE id = 1", Reject: connector.RejectStatementType},
		{Name: "my: rename table", SQL: "RENAME TABLE users TO u2", Reject: connector.RejectStatementType},
		{Name: "my: create procedure", SQL: "CREATE PROCEDURE p() DELETE FROM users", Reject: connector.RejectStatementType, Also: []connector.RejectCode{connector.RejectParse}},
		{Name: "my: comment only", SQL: "# nothing here", Reject: connector.RejectEmpty},

		// Rejected: executable comments, before parsing.
		{Name: "my: executable comment", SQL: "DELETE FROM users WHERE id = 1 /*!50000 OR 1=1 */", Reject: connector.RejectExecutableComment},
		{Name: "my: executable comment hiding a statement", SQL: "/*! DROP TABLE users */", Reject: connector.RejectExecutableComment},
		{Name: "my: optimizer hint comment", SQL: "UPDATE /*+ NO_INDEX(users) */ users SET name = 'x' WHERE id = 1", Reject: connector.RejectExecutableComment},
		{Name: "my: mariadb executable comment", SQL: "DELETE FROM users WHERE id = 1 /*M! OR 1=1 */", Reject: connector.RejectExecutableComment},
		{Name: "my: tidb executable comment", SQL: "DELETE FROM users WHERE id = 1 /*T! OR 1=1 */", Reject: connector.RejectExecutableComment},
		{Name: "my: executable comment in a string", SQL: "UPDATE users SET name = '/*!x*/' WHERE id = 1", Reject: connector.RejectExecutableComment},

		// Rejected: multi-table forms.
		{Name: "my: multi-table update", SQL: "UPDATE users, orders SET users.name = 'x' WHERE users.id = orders.user_id", Reject: connector.RejectMultipleTables},
		{Name: "my: update join", SQL: "UPDATE users u JOIN orders o ON o.user_id = u.id SET u.name = 'x' WHERE o.id = 1", Reject: connector.RejectMultipleTables},
		{Name: "my: multi-table delete", SQL: "DELETE users FROM users JOIN orders ON orders.user_id = users.id WHERE orders.id = 1", Reject: connector.RejectMultipleTables},
		{Name: "my: delete using", SQL: "DELETE FROM users USING users JOIN orders ON orders.user_id = users.id WHERE orders.id = 1", Reject: connector.RejectMultipleTables},
		{Name: "my: delete alias list", SQL: "DELETE u FROM users u WHERE u.id = 1", Reject: connector.RejectMultipleTables},
		{Name: "my: set other table column", SQL: "UPDATE users SET orders.status = 'x' WHERE id = 1", Reject: connector.RejectMultipleTables},

		// Rejected: INSERT forms.
		{Name: "my: insert set", SQL: "INSERT INTO users SET id = 10, email = 'a', status = 'b'", Reject: connector.RejectInsertForm},
		{Name: "my: insert select with columns", SQL: "INSERT INTO users (id) SELECT id + 100 FROM users", Reject: connector.RejectInsertForm},
		{Name: "my: insert table", SQL: "INSERT INTO users (id) TABLE orders", Reject: connector.RejectInsertForm, Also: []connector.RejectCode{connector.RejectParse}},
		{Name: "my: values length mismatch", SQL: "INSERT INTO users (id, email) VALUES (1), (2, 'b')", Reject: connector.RejectInsertForm},
		{Name: "my: insert qualified column", SQL: "INSERT INTO users (users.id, email, status) VALUES (1, 'a', 'b')", Reject: connector.RejectUnsupported},

		// Rejected: modifiers, ORDER BY and LIMIT.
		{Name: "my: low priority update", SQL: "UPDATE LOW_PRIORITY users SET name = 'x' WHERE id = 1", Reject: connector.RejectUnsupported},
		{Name: "my: low priority delete", SQL: "DELETE LOW_PRIORITY FROM users WHERE id = 1", Reject: connector.RejectUnsupported},
		{Name: "my: high priority insert", SQL: "INSERT HIGH_PRIORITY INTO users (id, email, status) VALUES (1, 'a', 'b')", Reject: connector.RejectUnsupported},
		{Name: "my: delayed insert", SQL: "INSERT DELAYED INTO users (id, email, status) VALUES (1, 'a', 'b')", Reject: connector.RejectUnsupported},
		{Name: "my: insert ignore", SQL: "INSERT IGNORE INTO users (id, email, status) VALUES (1, 'a', 'b')", Reject: connector.RejectUnsupported},
		{Name: "my: update ignore", SQL: "UPDATE IGNORE users SET id = 2 WHERE id = 1", Reject: connector.RejectUnsupported},
		{Name: "my: delete ignore", SQL: "DELETE IGNORE FROM users WHERE id = 1", Reject: connector.RejectUnsupported},
		{Name: "my: delete quick", SQL: "DELETE QUICK FROM users WHERE id = 1", Reject: connector.RejectUnsupported},
		{Name: "my: update order by", SQL: "UPDATE users SET name = 'x' WHERE status = 'active' ORDER BY id", Reject: connector.RejectUnsupported},
		{Name: "my: update limit", SQL: "UPDATE users SET name = 'x' WHERE status = 'active' LIMIT 1", Reject: connector.RejectUnsupported},
		{Name: "my: delete order by limit", SQL: "DELETE FROM users WHERE status = 'active' ORDER BY id DESC LIMIT 1", Reject: connector.RejectUnsupported},
		{Name: "my: index hint", SQL: "DELETE FROM users WHERE id IN (SELECT user_id FROM orders FORCE INDEX (PRIMARY) WHERE id = 1)", Reject: connector.RejectUnsupported},
		{Name: "my: partition selection", SQL: "DELETE FROM users PARTITION (p0) WHERE id = 1", Reject: connector.RejectUnsupported},

		// Rejected: variables and parameters.
		{Name: "my: user variable", SQL: "DELETE FROM users WHERE id = @id", Reject: connector.RejectUnsupported},
		{Name: "my: user variable assignment", SQL: "UPDATE users SET name = (@x := 'a') WHERE id = 1", Reject: connector.RejectUnsupported},
		{Name: "my: system variable", SQL: "UPDATE users SET name = @@hostname WHERE id = 1", Reject: connector.RejectUnsupported},
		{Name: "my: parameter marker", SQL: "DELETE FROM users WHERE id = ?", Reject: connector.RejectUnsupported},

		// Rejected: other databases and system schemas.
		{Name: "my: other database target", SQL: "DELETE FROM other.users WHERE id = 1", Reject: connector.RejectOtherDatabase},
		{Name: "my: other database in subquery", SQL: "DELETE FROM users WHERE id IN (SELECT id FROM other.users)", Reject: connector.RejectOtherDatabase},
		{Name: "my: other database column", SQL: "DELETE FROM users WHERE other.users.id = 1", Reject: connector.RejectOtherDatabase},
		{Name: "my: other database case", SQL: "DELETE FROM APP.users WHERE id = 1", Reject: connector.RejectOtherDatabase},
		{Name: "my: mysql schema target", SQL: "UPDATE mysql.user SET authentication_string = '' WHERE user = 'root'", Reject: connector.RejectSystemSchema},
		{Name: "my: performance schema in subquery", SQL: "DELETE FROM users WHERE name IN (SELECT user FROM performance_schema.accounts)", Reject: connector.RejectSystemSchema},
		{Name: "my: sys schema", SQL: "DELETE FROM sys.sys_config WHERE variable = 'x'", Reject: connector.RejectSystemSchema},
		{Name: "my: blocked database", SQL: "DELETE FROM users WHERE id = 1", Policy: &connector.Policy{BlockedSchemas: []string{"app"}}, Reject: connector.RejectSystemSchema},

		// Rejected: functions.
		{Name: "my: sleep", SQL: "DELETE FROM users WHERE id = 1 AND SLEEP(10) = 0", Reject: connector.RejectFunction},
		{Name: "my: benchmark", SQL: "UPDATE users SET name = BENCHMARK(1000000, MD5('x')) WHERE id = 1", Reject: connector.RejectFunction},
		{Name: "my: load_file", SQL: "UPDATE users SET name = LOAD_FILE('/etc/passwd') WHERE id = 1", Reject: connector.RejectFunction},
		{Name: "my: get_lock", SQL: "DELETE FROM users WHERE id = 1 AND GET_LOCK('x', 10) = 1", Reject: connector.RejectFunction},
		{Name: "my: stored function", SQL: "UPDATE users SET name = app.my_func(name) WHERE id = 1", Reject: connector.RejectFunction},
		{Name: "my: aggregate in subquery", SQL: "DELETE FROM orders WHERE total > (SELECT MAX(total) FROM orders o2)", Reject: connector.RejectFunction},
		{Name: "my: count star", SQL: "DELETE FROM users WHERE 0 < (SELECT COUNT(*) FROM orders WHERE orders.user_id = users.id)", Reject: connector.RejectFunction},
		{Name: "my: date_add not allowed", SQL: "UPDATE users SET updated_at = updated_at + INTERVAL 1 DAY WHERE id = 1", Reject: connector.RejectFunction},
		{Name: "my: json operator", SQL: "DELETE FROM users WHERE name->'$.a' = 1", Reject: connector.RejectFunction},
		{Name: "my: convert not allowed", SQL: "UPDATE users SET name = CONVERT(name USING latin1) WHERE id = 1", Reject: connector.RejectFunction},
		{Name: "my: cast not allowed", SQL: "UPDATE users SET name = CAST(1 AS CHAR) WHERE id = 1", Policy: &connector.Policy{AllowedFunctions: []string{"lower"}}, Reject: connector.RejectFunction},
		{Name: "my: current_user", SQL: "UPDATE users SET name = CURRENT_USER() WHERE id = 1", Reject: connector.RejectFunction},
		{Name: "my: window function", SQL: "DELETE FROM users WHERE id IN (SELECT ROW_NUMBER() OVER () FROM users)", Reject: connector.RejectFunction},

		// Rejected: constant WHERE.
		{Name: "my: where 1", SQL: "DELETE FROM users WHERE 1", Reject: connector.RejectConstantWhere},
		{Name: "my: where numeric string", SQL: "DELETE FROM users WHERE '1abc'", Reject: connector.RejectConstantWhere},
		{Name: "my: or numeric string", SQL: "DELETE FROM users WHERE id = 2 OR '1abc'", Reject: connector.RejectConstantWhere},
		{Name: "my: or pipes", SQL: "DELETE FROM users WHERE id = 2 || 1", Reject: connector.RejectConstantWhere},
		{Name: "my: or collation-dependent", SQL: "DELETE FROM users WHERE id = 2 OR 'a' = 'A'", Reject: connector.RejectConstantWhere},
		{Name: "my: or is true", SQL: "DELETE FROM users WHERE id = 2 OR TRUE IS TRUE", Reject: connector.RejectConstantWhere},
		{Name: "my: status equals status", SQL: "UPDATE users SET name = 'x' WHERE status = status", Reject: connector.RejectConstantWhere},
		{Name: "my: null-safe self", SQL: "DELETE FROM users WHERE id <=> id", Reject: connector.RejectConstantWhere},
		{Name: "my: uncorrelated exists", SQL: "DELETE FROM users WHERE EXISTS (SELECT 1 FROM orders WHERE total > 100)", Reject: connector.RejectConstantWhere},
		{Name: "my: or uncorrelated exists", SQL: "DELETE FROM users WHERE id = 1 OR EXISTS (SELECT 1 FROM orders WHERE total > 100)", Reject: connector.RejectConstantWhere},

		// Rejected: other constructs.
		{Name: "my: union subquery", SQL: "DELETE FROM users WHERE id IN (SELECT 1 UNION SELECT 2)", Reject: connector.RejectUnsupported},
		{Name: "my: derived table", SQL: "DELETE FROM users WHERE id IN (SELECT id FROM (SELECT id FROM orders) AS x)", Reject: connector.RejectUnsupported},
		{Name: "my: locking subquery", SQL: "DELETE FROM users WHERE id IN (SELECT id FROM orders FOR UPDATE)", Reject: connector.RejectUnsupported},
		{Name: "my: with in subquery", SQL: "DELETE FROM users WHERE id IN (WITH x AS (SELECT 1) SELECT * FROM x)", Reject: connector.RejectWith},
		{Name: "my: with update", SQL: "WITH x AS (SELECT 1 AS id) UPDATE users SET name = 'a' WHERE id IN (SELECT id FROM x)", Reject: connector.RejectWith},
		{Name: "my: match against", SQL: "DELETE FROM users WHERE MATCH (name) AGAINST ('x')", Reject: connector.RejectUnsupported},
		{Name: "my: default of column", SQL: "UPDATE users SET name = DEFAULT(name) WHERE id = 1", Reject: connector.RejectUnsupported},
		{Name: "my: default in where", SQL: "DELETE FROM users WHERE name = DEFAULT", Reject: connector.RejectUnsupported, Also: []connector.RejectCode{connector.RejectParse}},
		{Name: "my: nul byte", SQL: "DELETE FROM users WHERE id = 1\x00", Reject: connector.RejectParse},
		{Name: "my: stacked semicolons second statement", SQL: "DELETE FROM users WHERE id = 1;;SELECT 1", Reject: connector.RejectMultipleStatements},
		{Name: "my: unterminated string", SQL: "UPDATE users SET name = 'x WHERE id = 1", Reject: connector.RejectParse},
	}
}

// TestValidate runs the shared and MySQL corpora without a database.
func TestValidate(t *testing.T) {
	connectortest.RunValidation(t, baseHarness())
}

func TestValidateSchemaIsTheDatabase(t *testing.T) {
	cfg := validationConfig()
	cfg[keyDatabase] = "shop"
	for _, sql := range []string{"DELETE FROM users WHERE id = 1", "DELETE FROM shop.users WHERE id = 1"} {
		st, err := New().Validate(cfg, sql, connector.Policy{})
		if err != nil {
			t.Fatal(err)
		}
		if st.Schema != "shop" {
			t.Errorf("%s: Schema = %q, want shop", sql, st.Schema)
		}
	}
	_, err := New().Validate(cfg, "DELETE FROM app.users WHERE id = 1", connector.Policy{})
	var rej *connector.RejectedError
	if !errors.As(err, &rej) || rej.Code != connector.RejectOtherDatabase {
		t.Errorf("err = %v, want other_database", err)
	}
	_, err = validate("DELETE FROM users WHERE id = 1", "mysql", connector.Policy{})
	if !errors.As(err, &rej) || rej.Code != connector.RejectSystemSchema {
		t.Errorf("err = %v, want system_schema", err)
	}
	_, err = validate("DELETE FROM users WHERE id = 1", "", connector.Policy{})
	if err == nil {
		t.Error("a connection without a database validated a statement")
	}
}

func TestValidateRejectionPositions(t *testing.T) {
	cases := []struct {
		sql  string
		want string // the text at the reported position starts with this
	}{
		{"UPDATE users SET name = md5(name) WHERE id = 1", "md5("},
		{"DELETE FROM users WHERE id = 1 /*!50000 OR 1=1 */", "/*!"},
		{"DELETE FROM users WHERE id = @x", "@x"},
		{"DELETE FROM users WHERE id = 1; DROP TABLE users", "DROP"},
	}
	for _, c := range cases {
		_, err := validate(c.sql, "app", connector.Policy{})
		var rej *connector.RejectedError
		if !errors.As(err, &rej) {
			t.Errorf("%q: err = %v, want rejection", c.sql, err)
			continue
		}
		if rej.Position < 0 || !strings.HasPrefix(c.sql[rej.Position:], c.want) {
			t.Errorf("%q: position %d, want text starting %q", c.sql, rej.Position, c.want)
		}
	}
}

func TestValidateStatementFields(t *testing.T) {
	st, err := validate("INSERT INTO users (id, email, status) VALUES (1, 'a', 'b'), (2, 'c', 'd'), (3, 'e', 'f')", "app", connector.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if st.InsertRows != 3 || st.Where != "" || st.Schema != "app" {
		t.Errorf("statement = %+v", st)
	}
	st, err = validate("UPDATE users u SET name = lower(u.name), u.status = 'a' WHERE u.id = 1", "app", connector.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Alias != "u" || st.Where != "u.id = 1" || strings.Join(st.Columns, ",") != "name,status" {
		t.Errorf("statement = %+v", st)
	}
}

func TestExecutableComment(t *testing.T) {
	for in, want := range map[string]int{
		"/*! x */":          0,
		"a /*+ x */":        2,
		"a /*M! x */":       2,
		"a /*t! x */":       2,
		"a /* x */":         -1,
		"a /*x */":          -1,
		"a /*ab! */":        -1,
		"/*":                -1,
		"DELETE /*!*/ x":    7,
		"no comment at all": -1,
	} {
		if got := executableComment(in); got != want {
			t.Errorf("executableComment(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestWhereBounds(t *testing.T) {
	for in, want := range map[string]string{
		"DELETE FROM t WHERE a = 1":                         "a = 1",
		"DELETE FROM t WHERE a = 1;":                        "a = 1",
		"DELETE FROM t WHERE a = 1 -- c\n; ;":               "a = 1",
		"DELETE FROM t WHERE a = ';' # c":                   "a = ';'",
		"UPDATE t SET b = (SELECT 1 WHERE 1) WHERE a = 2":   "a = 2",
		"DELETE FROM t WHERE a = 'it''s' AND b = \"q\\\"\"": "a = 'it''s' AND b = \"q\\\"\"",
		"DELETE FROM t WHERE `where` = 1":                   "`where` = 1",
		"DELETE FROM t WHERE t.where = 1":                   "t.where = 1",
	} {
		s, e, ok := whereBounds(in)
		if !ok {
			t.Errorf("whereBounds(%q) failed", in)
			continue
		}
		if got := strings.TrimSpace(in[s:e]); got != want {
			t.Errorf("whereBounds(%q) = %q, want %q", in, got, want)
		}
	}
	for _, in := range []string{"DELETE FROM t", "DELETE FROM t WHERE 'open", "DELETE FROM t WHERE a = 1 /* open"} {
		if _, _, ok := whereBounds(in); ok {
			t.Errorf("whereBounds(%q) succeeded", in)
		}
	}
}

func FuzzValidate(f *testing.F) {
	for _, c := range append(connectortest.SharedCorpus(), corpus()...) {
		f.Add(c.SQL)
	}
	f.Fuzz(func(t *testing.T, sql string) {
		st, err := validate(sql, "app", connector.Policy{})
		if err != nil {
			var rej *connector.RejectedError
			if !errors.As(err, &rej) {
				t.Fatalf("Validate(%q) returned a non-rejection error: %v", sql, err)
			}
			if rej.Position >= len(sql)+1 {
				t.Fatalf("Validate(%q): position %d beyond the text", sql, rej.Position)
			}
			return
		}
		switch st.Kind {
		case connector.KindInsert:
			if st.InsertRows == 0 || len(st.Columns) == 0 {
				t.Fatalf("accepted an INSERT without VALUES rows or columns: %q", sql)
			}
		case connector.KindUpdate, connector.KindDelete:
			if st.Where == "" {
				t.Fatalf("accepted %s without WHERE text: %q", st.Kind, sql)
			}
		default:
			t.Fatalf("accepted kind %q: %q", st.Kind, sql)
		}
		if st.Table == "" || st.Schema != "app" {
			t.Fatalf("accepted without a table in the connection's database: %q", sql)
		}
		if executableComment(sql) >= 0 {
			t.Fatalf("accepted an executable comment: %q", sql)
		}
		// The WHERE text must itself validate as the WHERE of a statement
		// on the same table, with the same functions.
		if st.Where != "" {
			if _, err := validateWith("DELETE FROM t WHERE "+st.Where, "app", connector.Policy{}, true); err != nil {
				var rej *connector.RejectedError
				if errors.As(err, &rej) && rej.Code != connector.RejectConstantWhere {
					t.Fatalf("WHERE text %q of %q does not validate: %v", st.Where, sql, err)
				}
			}
		}
		// Validation is deterministic.
		again, err := validate(sql, "app", connector.Policy{})
		if err != nil || again.Where != st.Where || again.Table != st.Table || again.Kind != st.Kind {
			t.Fatalf("validation of %q is not stable: %+v vs %+v, %v", sql, st, again, err)
		}
	})
}
