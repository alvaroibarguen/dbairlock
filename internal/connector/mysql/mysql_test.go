package mysql

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/connector"
	"github.com/alvaroibarguen/dbairlock/internal/connector/connectortest"
)

// TestMySQLSpecific covers behaviour outside the shared suite, on the
// newest supported version.
func TestMySQLSpecific(t *testing.T) {
	if testing.Short() {
		t.Skip("integration tests skipped in -short mode")
	}
	v := testVersions[len(testVersions)-1]
	srv, err := startServer(t, v.image)
	if err != nil {
		t.Skip("Docker is not available, skipping MySQL integration tests: " + err.Error())
	}
	t.Run("non-transactional table", func(t *testing.T) { testMyISAM(t, srv) })
	t.Run("statement fields must match the SQL", func(t *testing.T) { testTampered(t, srv) })
	t.Run("lexical sql_mode is pinned", func(t *testing.T) { testSQLMode(t, srv) })
	t.Run("update after image", func(t *testing.T) { testAfterImage(t, srv) })
	t.Run("trailing semicolon and comment", func(t *testing.T) { testTrailing(t, srv) })
	t.Run("timed-out statement releases its locks", func(t *testing.T) { testTimeoutKill(t, srv) })
	t.Run("program name on pooled connections", func(t *testing.T) { testProgramName(t, srv) })
	t.Run("missing database", func(t *testing.T) { testMissingDatabase(t, srv) })
	t.Run("hostile text on a server with every lexical sql_mode", func(t *testing.T) { testHostileOnServer(t, srv) })
}

func open(t *testing.T, tg connectortest.Target, role connectortest.Role, mutate func(connector.Config)) connector.Conn {
	t.Helper()
	cfg, pw := tg.Config(role)
	if mutate != nil {
		mutate(cfg)
	}
	c, err := New().Open(context.Background(), cfg, pw)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func tctx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func stmt(t *testing.T, tg connectortest.Target, sql string, p connector.Policy) connector.Statement {
	t.Helper()
	cfg, _ := tg.Config(connectortest.LeastPrivilege)
	st, err := New().Validate(cfg, sql, p)
	if err != nil {
		t.Fatalf("Validate(%q): %v", sql, err)
	}
	return st
}

func adminRows(t *testing.T, tg connectortest.Target, q string) [][]string {
	t.Helper()
	rows, err := tg.AdminQuery(context.Background(), q)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return rows
}

// The MyISAM table is rejected with ErrNotTransactional and its data is
// unchanged (phase 6 check).
func testMyISAM(t *testing.T, srv *server) {
	tg := srv.newTarget(t)
	for _, s := range []string{
		"CREATE TABLE legacy_notes (id int PRIMARY KEY, note text) ENGINE = MyISAM",
		"INSERT INTO legacy_notes (id, note) VALUES (1, 'kept forever')",
		"GRANT SELECT, INSERT, UPDATE, DELETE ON " + tg.(*target).db + ".legacy_notes TO '" + appUser + "'@'%'",
	} {
		if err := tg.Admin(context.Background(), s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	c := open(t, tg, connectortest.LeastPrivilege, nil)
	lim := connector.DefaultLimits()
	for _, sql := range []string{
		"UPDATE legacy_notes SET note = 'changed' WHERE id = 1",
		"DELETE FROM legacy_notes WHERE id = 1",
		"INSERT INTO legacy_notes (id, note) VALUES (2, 'new')",
	} {
		st := stmt(t, tg, sql, connector.Policy{})
		_, err := c.DryRun(tctx(t), []connector.Statement{st}, lim, connector.RunInfo{Label: "dbairlock preview"})
		if !errors.Is(err, connector.ErrNotTransactional) || errors.Is(err, connector.ErrUnavailable) {
			t.Errorf("DryRun(%s) = %v, want ErrNotTransactional", sql, err)
		}
		_, err = c.Execute(tctx(t), []connector.Statement{st}, []connector.Expectation{{Rows: 1}}, lim, connector.RunInfo{Label: "dbairlock change #9"})
		if !errors.Is(err, connector.ErrNotTransactional) {
			t.Errorf("Execute(%s) = %v, want ErrNotTransactional", sql, err)
		}
	}
	// A transactional statement before the MyISAM one must not commit
	// either.
	sts := []connector.Statement{
		stmt(t, tg, "UPDATE users SET name = 'Zed' WHERE id = 1", connector.Policy{}),
		stmt(t, tg, "UPDATE legacy_notes SET note = 'changed' WHERE id = 1", connector.Policy{}),
	}
	_, err := c.Execute(tctx(t), sts, []connector.Expectation{{Rows: 1}, {Rows: 1}}, lim, connector.RunInfo{})
	if err == nil {
		t.Error("Execute with a MyISAM statement succeeded")
	}
	rows := adminRows(t, tg, "SELECT (SELECT COUNT(*) FROM legacy_notes), (SELECT note FROM legacy_notes WHERE id = 1), (SELECT name FROM users WHERE id = 1)")
	if rows[0][0] != "1" || rows[0][1] != "kept forever" || rows[0][2] != "Ann" {
		t.Errorf("data changed: %v", rows)
	}
}

func testTampered(t *testing.T, srv *server) {
	tg := srv.newTarget(t)
	c := open(t, tg, connectortest.LeastPrivilege, nil)
	st := stmt(t, tg, "DELETE FROM users WHERE id = 1", connector.Policy{})
	for name, mutate := range map[string]func(*connector.Statement){
		"where":   func(s *connector.Statement) { s.Where = "1 = 1" },
		"table":   func(s *connector.Statement) { s.Table = "orders" },
		"schema":  func(s *connector.Statement) { s.Schema = "mysql" },
		"sql":     func(s *connector.Statement) { s.SQL = "DELETE FROM users WHERE id > 0" },
		"kind":    func(s *connector.Statement) { s.Kind = connector.KindUpdate },
		"invalid": func(s *connector.Statement) { s.SQL = "DELETE FROM users" },
		"comment": func(s *connector.Statement) { s.SQL = "DELETE FROM users WHERE id = 1 /*!OR 1=1*/" },
	} {
		bad := st
		mutate(&bad)
		_, err := c.DryRun(tctx(t), []connector.Statement{bad}, connector.DefaultLimits(), connector.RunInfo{})
		if err == nil {
			t.Errorf("%s: tampered statement accepted", name)
		}
		if errors.Is(err, connector.ErrUnavailable) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if rows := adminRows(t, tg, "SELECT COUNT(*) FROM users"); rows[0][0] != "5" {
		t.Errorf("users count = %s", rows[0][0])
	}
}

// With the server's sql_mode set to modes that change how text is
// tokenized, statements still mean what the validator understood.
func testSQLMode(t *testing.T, srv *server) {
	tg := srv.newTarget(t)
	set := func(mode string) {
		if err := tg.Admin(context.Background(), "SET GLOBAL sql_mode = '"+mode+"'"); err != nil {
			t.Fatal(err)
		}
	}
	set("ANSI_QUOTES,NO_BACKSLASH_ESCAPES,PIPES_AS_CONCAT,STRICT_TRANS_TABLES")
	t.Cleanup(func() {
		set("ONLY_FULL_GROUP_BY,STRICT_TRANS_TABLES,NO_ZERO_IN_DATE,NO_ZERO_DATE,ERROR_FOR_DIVISION_BY_ZERO,NO_ENGINE_SUBSTITUTION")
	})
	c := open(t, tg, connectortest.LeastPrivilege, nil)
	sts := []connector.Statement{
		stmt(t, tg, `UPDATE users SET name = "it\'s" WHERE id = 1 || id = 2`, connector.Policy{}),
	}
	dry, err := c.DryRun(tctx(t), sts, connector.DefaultLimits(), connector.RunInfo{})
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	if dry[0].RowsAffected != 2 {
		t.Fatalf("rows = %d, want 2 (|| must be OR)", dry[0].RowsAffected)
	}
	exp := []connector.Expectation{{Rows: dry[0].RowsAffected, Fingerprint: dry[0].Fingerprint}}
	if _, err := c.Execute(tctx(t), sts, exp, connector.DefaultLimits(), connector.RunInfo{}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	rows := adminRows(t, tg, "SELECT name FROM users WHERE id IN (1, 2) ORDER BY id")
	if len(rows) != 2 || rows[0][0] != "it's" || rows[1][0] != "it's" {
		t.Errorf("names = %v, want it's twice", rows)
	}
	// The server's semantic modes are kept: STRICT_TRANS_TABLES rejects a
	// value that is too long instead of truncating it.
	long := stmt(t, tg, "UPDATE users SET status = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' WHERE id = 3", connector.Policy{})
	if _, err := c.DryRun(tctx(t), []connector.Statement{long}, connector.DefaultLimits(), connector.RunInfo{}); err == nil {
		t.Error("a too-long value was accepted; the server's strict mode was not kept")
	}
}

func testAfterImage(t *testing.T, srv *server) {
	tg := srv.newTarget(t)
	c := open(t, tg, connectortest.LeastPrivilege, nil)
	st := stmt(t, tg, "UPDATE orders AS o SET total = o.total * 2 WHERE o.user_id = 1", connector.Policy{})
	res, err := c.DryRun(tctx(t), []connector.Statement{st}, connector.DefaultLimits(), connector.RunInfo{})
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	r := res[0]
	if r.RowsAffected != 2 || len(r.Before) != 2 || len(r.After) != 2 {
		t.Fatalf("result = %+v", r)
	}
	if r.Before[0][2] != "10.00" || r.After[0][2] != "20.00" || r.Before[1][2] != "20.00" || r.After[1][2] != "40.00" {
		t.Errorf("before %v, after %v", r.Before, r.After)
	}
	// The session settings that can change a statement's behaviour are
	// recorded for the audit trail.
	if r.Session["server_version"] == "" || strings.Contains(r.Session["sql_mode"], "ANSI_QUOTES") {
		t.Errorf("session = %v", r.Session)
	}
	// rows matched, not rows changed: setting a value to itself counts.
	same := stmt(t, tg, "UPDATE users SET status = 'active' WHERE id = 1", connector.Policy{})
	res, err = c.DryRun(tctx(t), []connector.Statement{same}, connector.DefaultLimits(), connector.RunInfo{})
	if err != nil || res[0].RowsAffected != 1 {
		t.Errorf("no-op update: %+v, %v; want 1 row", res, err)
	}
}

func testTrailing(t *testing.T, srv *server) {
	tg := srv.newTarget(t)
	c := open(t, tg, connectortest.LeastPrivilege, nil)
	st := stmt(t, tg, "DELETE FROM orders\nWHERE id = 4 -- the small one\n;", connector.Policy{})
	dry, err := c.DryRun(tctx(t), []connector.Statement{st}, connector.DefaultLimits(), connector.RunInfo{})
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	exp := []connector.Expectation{{Rows: dry[0].RowsAffected, Fingerprint: dry[0].Fingerprint}}
	if _, err := c.Execute(tctx(t), []connector.Statement{st}, exp, connector.DefaultLimits(), connector.RunInfo{}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rows := adminRows(t, tg, "SELECT COUNT(*) FROM orders WHERE id = 4"); rows[0][0] != "0" {
		t.Error("order 4 still exists")
	}
}

// A statement that overruns the statement timeout is killed on the server,
// so it does not keep its row locks after the call returns.
func testTimeoutKill(t *testing.T, srv *server) {
	tg := srv.newTarget(t)
	c := open(t, tg, connectortest.LeastPrivilege, nil)
	p := connector.Policy{AllowedFunctions: []string{"sleep"}}
	slow := stmt(t, tg, "UPDATE users SET name = 'Zed' WHERE id = 1 AND SLEEP(5) = 0", p)
	lim := connector.DefaultLimits()
	lim.StatementTimeout = 500 * time.Millisecond
	_, err := c.DryRun(tctx(t), []connector.Statement{slow}, lim, connector.RunInfo{})
	if !errors.Is(err, connector.ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	fast := stmt(t, tg, "UPDATE users SET name = 'Zed' WHERE id = 1", connector.Policy{})
	lim = connector.DefaultLimits()
	lim.LockTimeout = time.Second
	if _, err := c.DryRun(tctx(t), []connector.Statement{fast}, lim, connector.RunInfo{}); err != nil {
		t.Errorf("DryRun after a timeout: %v", err)
	}
}

func testProgramName(t *testing.T, srv *server) {
	tg := srv.newTarget(t)
	c := open(t, tg, connectortest.LeastPrivilege, nil)
	if err := c.Ping(tctx(t)); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	rows := adminRows(t, tg, "SELECT COUNT(*) FROM performance_schema.session_connect_attrs WHERE ATTR_NAME = 'program_name' AND ATTR_VALUE = 'dbairlock'")
	if rows[0][0] == "0" {
		t.Error("no session with program_name=dbairlock")
	}
}

func testMissingDatabase(t *testing.T, srv *server) {
	tg := srv.newTarget(t)
	c := open(t, tg, connectortest.LeastPrivilege, func(cfg connector.Config) { cfg[keyDatabase] = "nope" })
	if err := c.Ping(tctx(t)); err == nil {
		t.Error("Ping succeeded for a database that does not exist")
	}
}
