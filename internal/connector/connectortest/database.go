package connectortest

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/connector"
)

const testTimeout = 2 * time.Minute

// env is one test's target and an open connection to it.
type env struct {
	t   *testing.T
	h   Harness
	tg  Target
	cfg connector.Config
	c   connector.Conn
}

func newEnv(t *testing.T, h Harness) *env {
	t.Helper()
	tg := h.NewTarget(t)
	cfg, pw := tg.Config(LeastPrivilege)
	c, err := h.Connector.Open(context.Background(), cfg, pw)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return &env{t: t, h: h, tg: tg, cfg: cfg, c: c}
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), testTimeout)
	t.Cleanup(cancel)
	return c
}

func (e *env) stmts(p connector.Policy, sqls ...string) []connector.Statement {
	e.t.Helper()
	out := make([]connector.Statement, len(sqls))
	for i, s := range sqls {
		st, err := e.h.Connector.Validate(e.cfg, s, p)
		if err != nil {
			e.t.Fatalf("Validate(%q): %v", s, err)
		}
		out[i] = st
	}
	return out
}

func (e *env) dump() string {
	e.t.Helper()
	var b strings.Builder
	for _, q := range []string{
		"SELECT id, email, name, status, updated_at FROM users ORDER BY id",
		"SELECT id, user_id, total, status FROM orders ORDER BY id",
		"SELECT user_id, note FROM audit_notes ORDER BY user_id, note",
	} {
		rows, err := e.tg.AdminQuery(context.Background(), q)
		if err != nil {
			e.t.Fatalf("dump %q: %v", q, err)
		}
		for _, r := range rows {
			b.WriteString(strings.Join(r, "|"))
			b.WriteByte('\n')
		}
		b.WriteString("--\n")
	}
	return b.String()
}

func (e *env) admin(sql string) {
	e.t.Helper()
	if err := e.tg.Admin(context.Background(), sql); err != nil {
		e.t.Fatalf("admin %q: %v", sql, err)
	}
}

func (e *env) dryRun(lim connector.Limits, sqls ...string) []connector.Result {
	e.t.Helper()
	res, err := e.c.DryRun(ctx(e.t), e.stmts(connector.Policy{}, sqls...), lim, connector.RunInfo{Label: "dbairlock preview"})
	if err != nil {
		e.t.Fatalf("DryRun: %v", err)
	}
	if len(res) != len(sqls) {
		e.t.Fatalf("DryRun returned %d results for %d statements", len(res), len(sqls))
	}
	return res
}

func expectations(res []connector.Result) []connector.Expectation {
	out := make([]connector.Expectation, len(res))
	for i, r := range res {
		out[i] = connector.Expectation{Rows: r.RowsAffected, Fingerprint: r.Fingerprint}
	}
	return out
}

func limits() connector.Limits {
	return connector.Limits{MaxRows: 100, StatementTimeout: 10 * time.Second, LockTimeout: 2 * time.Second, MaxCellBytes: 1024}
}

func colIndex(t *testing.T, r connector.Result, name string) int {
	t.Helper()
	i := slices.Index(r.Columns, name)
	if i < 0 {
		t.Fatalf("column %q not in result columns %q", name, r.Columns)
	}
	return i
}

func runDatabase(t *testing.T, h Harness) {
	t.Run("dry run", func(t *testing.T) { testDryRun(t, h) })
	t.Run("execute", func(t *testing.T) { testExecute(t, h) })
	t.Run("drift", func(t *testing.T) { testDrift(t, h) })
	t.Run("fingerprint", func(t *testing.T) { testFingerprint(t, h) })
	t.Run("unavailable", func(t *testing.T) { testUnavailable(t, h) })
	t.Run("run label", func(t *testing.T) { testLabel(t, h) })
	t.Run("server info", func(t *testing.T) { testServerInfo(t, h) })
	t.Run("schema", func(t *testing.T) { testSchema(t, h) })
	t.Run("privileges", func(t *testing.T) { testPrivileges(t, h) })
	t.Run("cell values", func(t *testing.T) { testCells(t, h) })
}

func testDryRun(t *testing.T, h Harness) {
	e := newEnv(t, h)
	// Every dry-run case must leave the data exactly as it was.
	clean := e.dump()
	t.Cleanup(func() {
		if got := e.dump(); got != clean {
			t.Errorf("dry runs left a trace:\nbefore:\n%s\nafter:\n%s", clean, got)
		}
	})

	t.Run("one-row update", func(t *testing.T) {
		r := e.dryRun(limits(), "UPDATE users SET name = 'Zed' WHERE id = 1")[0]
		if r.RowsAffected != 1 || r.Fingerprint == "" || !r.HasPrimaryKey {
			t.Fatalf("result = %+v", r)
		}
		if !slices.Equal(r.PrimaryKey, []string{"id"}) {
			t.Errorf("PrimaryKey = %q", r.PrimaryKey)
		}
		if r.Schema != namespace(h, e.tg) || r.Table != "users" {
			t.Errorf("table = %s.%s", r.Schema, r.Table)
		}
		name := colIndex(t, r, "name")
		if len(r.Before) != 1 || r.Before[0][name] != "Ann" {
			t.Errorf("Before = %v", r.Before)
		}
		if len(r.After) != 1 || r.After[0][name] != "Zed" {
			t.Errorf("After = %v", r.After)
		}
		if e.dump() != clean {
			t.Fatal("data changed")
		}
	})
	t.Run("delete", func(t *testing.T) {
		r := e.dryRun(limits(), "DELETE FROM orders WHERE id = 4")[0]
		if r.RowsAffected != 1 || len(r.Before) != 1 || r.Fingerprint == "" {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("multi-row insert", func(t *testing.T) {
		r := e.dryRun(limits(), "INSERT INTO orders (id, user_id, total, status) VALUES (10, 1, 1.50, 'open'), (11, 2, 2.50, 'open')")[0]
		if r.RowsAffected != 2 || r.Fingerprint != "" {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("over max rows", func(t *testing.T) {
		lim := limits()
		lim.MaxRows = 2
		_, err := e.c.DryRun(ctx(t), e.stmts(connector.Policy{}, "UPDATE users SET status = 'x' WHERE status = 'active'"), lim, connector.RunInfo{})
		if !errors.Is(err, connector.ErrTooManyRows) {
			t.Fatalf("err = %v, want ErrTooManyRows", err)
		}
		var se *connector.StatementError
		if !errors.As(err, &se) || se.Index != 0 {
			t.Errorf("err = %#v, want a StatementError for statement 0", err)
		}
	})
	t.Run("zero rows", func(t *testing.T) {
		_, err := e.c.DryRun(ctx(t), e.stmts(connector.Policy{}, "DELETE FROM users WHERE id = 999"), limits(), connector.RunInfo{})
		if !errors.Is(err, connector.ErrNoRows) {
			t.Fatalf("err = %v, want ErrNoRows", err)
		}
	})
	t.Run("table without primary key", func(t *testing.T) {
		r := e.dryRun(limits(), "UPDATE audit_notes SET note = 'x' WHERE user_id = 2")[0]
		if r.HasPrimaryKey || r.RowsAffected != 1 || r.Fingerprint == "" || len(r.Before) != 1 {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("second statement depends on the first", func(t *testing.T) {
		res := e.dryRun(limits(),
			"INSERT INTO users (id, email, name, status) VALUES (6, 'six@example.com', 'Six', 'new')",
			"UPDATE users SET name = 'Sixth' WHERE id = 6")
		if res[0].RowsAffected != 1 || res[1].RowsAffected != 1 {
			t.Fatalf("results = %+v", res)
		}
	})
	t.Run("lock timeout", func(t *testing.T) {
		s := lockRow(t, e, "SELECT id FROM users WHERE id = 1 FOR UPDATE")
		defer func() { _ = s.Close() }()
		lim := limits()
		lim.LockTimeout = 300 * time.Millisecond
		_, err := e.c.DryRun(ctx(t), e.stmts(connector.Policy{}, "UPDATE users SET name = 'Zed' WHERE id = 1"), lim, connector.RunInfo{})
		if !errors.Is(err, connector.ErrTimeout) {
			t.Fatalf("err = %v, want ErrTimeout", err)
		}
		if errors.Is(err, connector.ErrUnavailable) {
			t.Fatal("a timeout after BEGIN must not be ErrUnavailable")
		}
	})
	t.Run("statement timeout", func(t *testing.T) {
		lim := limits()
		lim.StatementTimeout = 500 * time.Millisecond
		p := connector.Policy{AllowedFunctions: []string{h.SlowFunction}}
		_, err := e.c.DryRun(ctx(t), e.stmts(p, "UPDATE users SET name = 'Zed' WHERE id = 1 AND "+h.SlowWhere), lim, connector.RunInfo{})
		if !errors.Is(err, connector.ErrTimeout) {
			t.Fatalf("err = %v, want ErrTimeout", err)
		}
	})
}

// lockRow opens an admin session, begins a transaction and runs lockSQL,
// which must lock rows until the session is closed.
func lockRow(t *testing.T, e *env, lockSQL string) Session {
	t.Helper()
	s, err := e.tg.Session(context.Background())
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if err := s.Exec(context.Background(), e.h.BeginSQL); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := s.Exec(context.Background(), lockSQL); err != nil {
		t.Fatalf("lock: %v", err)
	}
	return s
}

func testExecute(t *testing.T, h Harness) {
	t.Run("success matches the dry run", func(t *testing.T) {
		e := newEnv(t, h)
		sqls := []string{
			"UPDATE users SET name = 'Zed' WHERE id = 1",
			"DELETE FROM orders WHERE id = 4",
			"INSERT INTO audit_notes (user_id, note) VALUES (1, 'renamed')",
		}
		dry := e.dryRun(limits(), sqls...)
		got, err := e.c.Execute(ctx(t), e.stmts(connector.Policy{}, sqls...), expectations(dry), limits(), connector.RunInfo{Label: "dbairlock change #1"})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		for i := range dry {
			if got[i].RowsAffected != dry[i].RowsAffected || got[i].Fingerprint != dry[i].Fingerprint {
				t.Errorf("statement %d: execute %d/%s, dry run %d/%s", i, got[i].RowsAffected, got[i].Fingerprint, dry[i].RowsAffected, dry[i].Fingerprint)
			}
		}
		rows, err := e.tg.AdminQuery(context.Background(), "SELECT name FROM users WHERE id = 1")
		if err != nil || len(rows) != 1 || rows[0][0] != "Zed" {
			t.Errorf("users 1 = %v, %v; want Zed", rows, err)
		}
		rows, _ = e.tg.AdminQuery(context.Background(), "SELECT id FROM orders WHERE id = 4")
		if len(rows) != 0 {
			t.Errorf("order 4 still exists")
		}
		rows, _ = e.tg.AdminQuery(context.Background(), "SELECT note FROM audit_notes WHERE note = 'renamed'")
		if len(rows) != 1 {
			t.Errorf("inserted note not found")
		}
	})
	t.Run("failure in the second statement rolls back the first", func(t *testing.T) {
		e := newEnv(t, h)
		clean := e.dump()
		first := e.dryRun(limits(), "UPDATE users SET name = 'Zed' WHERE id = 1")
		stmts := e.stmts(connector.Policy{},
			"UPDATE users SET name = 'Zed' WHERE id = 1",
			"INSERT INTO users (id, email, name, status) VALUES (1, 'dup@example.com', 'Dup', 'new')")
		exp := []connector.Expectation{expectations(first)[0], {Rows: 1}}
		_, err := e.c.Execute(ctx(t), stmts, exp, limits(), connector.RunInfo{Label: "dbairlock change #2"})
		if err == nil {
			t.Fatal("Execute succeeded; want a duplicate key error")
		}
		var se *connector.StatementError
		if !errors.As(err, &se) || se.Index != 1 {
			t.Errorf("err = %v, want a StatementError for statement 1", err)
		}
		if errors.Is(err, connector.ErrUnavailable) {
			t.Error("an error after BEGIN must not be ErrUnavailable")
		}
		if got := e.dump(); got != clean {
			t.Errorf("data changed:\n%s", got)
		}
	})
	t.Run("expectation count mismatch", func(t *testing.T) {
		e := newEnv(t, h)
		_, err := e.c.Execute(ctx(t), e.stmts(connector.Policy{}, "DELETE FROM users WHERE id = 3"), nil, limits(), connector.RunInfo{})
		if err == nil {
			t.Fatal("Execute without expectations succeeded")
		}
	})
}

func testDrift(t *testing.T, h Harness) {
	cases := []struct {
		name   string
		sql    string
		change []string
		kind   connector.DriftKind // "" means no drift: the change must execute
	}{
		{"more rows match", "UPDATE users SET status = 'x' WHERE status = 'active'",
			[]string{"UPDATE users SET status = 'active' WHERE id = 5"}, connector.DriftCount},
		{"same count different rows", "UPDATE users SET status = 'x' WHERE status = 'active'",
			[]string{"UPDATE users SET status = 'active' WHERE id = 5", "UPDATE users SET status = 'inactive' WHERE id = 4"}, connector.DriftRows},
		{"overwritten value changed", "UPDATE users SET name = 'Zed' WHERE id = 1",
			[]string{"UPDATE users SET name = 'Other' WHERE id = 1"}, connector.DriftRows},
		{"fewer rows match", "DELETE FROM orders WHERE user_id = 1",
			[]string{"UPDATE orders SET user_id = 2 WHERE id = 2"}, connector.DriftCount},
		{"deleted row replaced", "DELETE FROM orders WHERE status = 'paid'",
			[]string{"UPDATE orders SET status = 'open' WHERE id = 1", "UPDATE orders SET status = 'paid' WHERE id = 2"}, connector.DriftRows},
		{"no primary key, contents changed", "UPDATE audit_notes SET note = 'x' WHERE user_id = 2",
			[]string{"UPDATE audit_notes SET note = 'changed' WHERE user_id = 2"}, connector.DriftRows},
		{"unrelated column changed is not drift", "UPDATE users SET name = 'Zed' WHERE id = 1",
			[]string{"UPDATE users SET updated_at = '2030-01-01 00:00:00' WHERE id = 1"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, h)
			dry := e.dryRun(limits(), c.sql)
			for _, s := range c.change {
				e.admin(s)
			}
			changed := e.dump()
			_, err := e.c.Execute(ctx(t), e.stmts(connector.Policy{}, c.sql), expectations(dry), limits(), connector.RunInfo{Label: "dbairlock change #3"})
			if c.kind == "" {
				if err != nil {
					t.Fatalf("Execute: %v; want success", err)
				}
				return
			}
			var d *connector.DriftError
			if !errors.As(err, &d) {
				t.Fatalf("err = %v, want DriftError(%s)", err, c.kind)
			}
			if d.Kind != c.kind || d.Statement != 0 {
				t.Errorf("drift = %+v, want kind %s on statement 0", d, c.kind)
			}
			if got := e.dump(); got != changed {
				t.Errorf("drift changed data:\nbefore:\n%s\nafter:\n%s", changed, got)
			}
		})
	}
}

func testFingerprint(t *testing.T, h Harness) {
	t.Run("same data, two connections, any row order", func(t *testing.T) {
		e := newEnv(t, h)
		cfg, pw := e.tg.Config(LeastPrivilege)
		c2, err := h.Connector.Open(context.Background(), cfg, pw)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer func() { _ = c2.Close() }()
		for _, sql := range []string{
			"UPDATE users SET status = 'x' WHERE status = 'active'",
			"DELETE FROM audit_notes WHERE user_id = 1",
			"UPDATE audit_notes SET note = 'n' WHERE user_id < 10",
		} {
			st := e.stmts(connector.Policy{}, sql)
			a, err := e.c.DryRun(ctx(t), st, limits(), connector.RunInfo{})
			if err != nil {
				t.Fatalf("DryRun: %v", err)
			}
			b, err := c2.DryRun(ctx(t), st, limits(), connector.RunInfo{})
			if err != nil {
				t.Fatalf("DryRun: %v", err)
			}
			if a[0].Fingerprint != b[0].Fingerprint {
				t.Errorf("%s: fingerprints differ across connections", sql)
			}
			// Rewrite rows so the database stores (and may return) them in a
			// different physical order.
			e.admin("UPDATE users SET name = name WHERE id IN (1, 2)")
			e.admin("UPDATE audit_notes SET note = note WHERE user_id = 1")
			c, err := e.c.DryRun(ctx(t), st, limits(), connector.RunInfo{})
			if err != nil {
				t.Fatalf("DryRun: %v", err)
			}
			if a[0].Fingerprint != c[0].Fingerprint {
				t.Errorf("%s: fingerprint changed after rows were rewritten", sql)
			}
		}
	})
	t.Run("value longer than the display limit", func(t *testing.T) {
		e := newEnv(t, h)
		lim := limits()
		lim.MaxCellBytes = 8
		e.admin("UPDATE users SET name = 'abcdefghijklmnop-1' WHERE id = 2")
		a := e.dryRun(lim, "UPDATE users SET name = 'n' WHERE id = 2")[0]
		e.admin("UPDATE users SET name = 'abcdefghijklmnop-2' WHERE id = 2")
		b := e.dryRun(lim, "UPDATE users SET name = 'n' WHERE id = 2")[0]
		if a.Fingerprint == b.Fingerprint {
			t.Error("a change beyond the display limit did not change the fingerprint")
		}
		if !a.Truncated {
			t.Error("Truncated is false")
		}
		v, ok := a.Before[0][colIndex(t, a, "name")].(connector.TaggedValue)
		if !ok || !v.Truncated || v.Value != "abcdefgh" || v.Size != len("abcdefghijklmnop-1") {
			t.Errorf("display value = %#v", a.Before[0][colIndex(t, a, "name")])
		}
	})
}

func testUnavailable(t *testing.T, h Harness) {
	if h.Unreachable == nil {
		t.Fatal("the harness has no Unreachable function")
	}
	for _, u := range h.Unreachable(t) {
		t.Run(u.Name, func(t *testing.T) {
			c, err := h.Connector.Open(context.Background(), u.Config, u.Password)
			if err != nil {
				if !errors.Is(err, connector.ErrUnavailable) {
					t.Fatalf("Open: %v; want success or ErrUnavailable", err)
				}
				return
			}
			defer func() { _ = c.Close() }()
			if err := c.Ping(ctx(t)); !errors.Is(err, connector.ErrUnavailable) {
				t.Errorf("Ping = %v, want ErrUnavailable", err)
			}
			st, verr := h.Connector.Validate(u.Config, "UPDATE users SET name = 'Zed' WHERE id = 1", connector.Policy{})
			if verr != nil {
				t.Fatalf("Validate: %v", verr)
			}
			stmts := []connector.Statement{st}
			if _, err := c.DryRun(ctx(t), stmts, limits(), connector.RunInfo{}); !errors.Is(err, connector.ErrUnavailable) {
				t.Errorf("DryRun = %v, want ErrUnavailable", err)
			}
			exp := []connector.Expectation{{Rows: 1, Fingerprint: "x"}}
			if _, err := c.Execute(ctx(t), stmts, exp, limits(), connector.RunInfo{}); !errors.Is(err, connector.ErrUnavailable) {
				t.Errorf("Execute = %v, want ErrUnavailable", err)
			}
		})
	}
	t.Run("connection killed after begin", func(t *testing.T) {
		e := newEnv(t, h)
		clean := e.dump()
		dry := e.dryRun(limits(), "UPDATE users SET name = 'Zed' WHERE id = 1")
		label := "dbairlock change #4"
		s := lockRow(t, e, "SELECT id FROM users WHERE id = 1 FOR UPDATE")
		defer func() { _ = s.Close() }()
		lim := limits()
		lim.LockTimeout = 30 * time.Second
		lim.StatementTimeout = 60 * time.Second
		done := make(chan error, 1)
		go func() {
			_, err := e.c.Execute(context.Background(), e.stmts(connector.Policy{}, "UPDATE users SET name = 'Zed' WHERE id = 1"), expectations(dry), lim, connector.RunInfo{Label: label})
			done <- err
		}()
		waitLabel(t, e.tg, label)
		if err := e.tg.Kill(context.Background(), label); err != nil {
			t.Fatalf("Kill: %v", err)
		}
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("Execute succeeded on a killed connection")
			}
			if errors.Is(err, connector.ErrUnavailable) {
				t.Fatalf("Execute = %v; a connection lost after BEGIN must not be ErrUnavailable", err)
			}
		case <-time.After(time.Minute):
			t.Fatal("Execute did not return after its connection was killed")
		}
		_ = s.Close()
		if got := e.dump(); got != clean {
			t.Errorf("data changed:\n%s", got)
		}
	})
}

func waitLabel(t *testing.T, tg Target, label string) {
	t.Helper()
	var labels []string
	for range 600 { // 30 seconds
		var err error
		labels, err = tg.Labels(context.Background())
		if err != nil {
			t.Fatalf("Labels: %v", err)
		}
		if slices.Contains(labels, label) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("label %q never appeared; sessions: %q", label, labels)
}

func testLabel(t *testing.T, h Harness) {
	e := newEnv(t, h)
	dry := e.dryRun(limits(), "UPDATE users SET name = 'Zed' WHERE id = 1")
	label := "dbairlock change #123"
	s := lockRow(t, e, "SELECT id FROM users WHERE id = 1 FOR UPDATE")
	lim := limits()
	lim.LockTimeout = 30 * time.Second
	lim.StatementTimeout = 60 * time.Second
	done := make(chan error, 1)
	go func() {
		_, err := e.c.Execute(context.Background(), e.stmts(connector.Policy{}, "UPDATE users SET name = 'Zed' WHERE id = 1"), expectations(dry), lim, connector.RunInfo{Label: label})
		done <- err
	}()
	waitLabel(t, e.tg, label)
	if err := s.Close(); err != nil {
		t.Fatalf("release lock: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
	case <-time.After(time.Minute):
		t.Fatal("Execute did not finish")
	}
}

func testServerInfo(t *testing.T, h Harness) {
	e := newEnv(t, h)
	si, err := e.c.ServerInfo(ctx(t))
	if err != nil {
		t.Fatalf("ServerInfo: %v", err)
	}
	if si.Version == "" || si.Product == "" || !si.Supported {
		t.Errorf("ServerInfo = %+v", si)
	}
	if h.ServerMajor != 0 && si.Major != h.ServerMajor {
		t.Errorf("Major = %d, want %d", si.Major, h.ServerMajor)
	}
	if err := e.c.Ping(ctx(t)); err != nil {
		t.Errorf("Ping: %v", err)
	}
}

func testSchema(t *testing.T, h Harness) {
	e := newEnv(t, h)
	s, err := e.c.Schema(ctx(t))
	if err != nil {
		t.Fatalf("Schema: %v", err)
	}
	var ns *connector.Namespace
	for i := range s.Namespaces {
		if slices.Contains(h.SystemNamespaces, s.Namespaces[i].Name) {
			t.Errorf("system namespace %q reported", s.Namespaces[i].Name)
		}
		if s.Namespaces[i].Name == namespace(h, e.tg) {
			ns = &s.Namespaces[i]
		}
	}
	if ns == nil {
		t.Fatalf("default namespace %q missing from %+v", namespace(h, e.tg), s)
	}
	want := map[string][]string{"users": {"id"}, "orders": {"id"}, "audit_notes": nil}
	for name, pk := range want {
		i := slices.IndexFunc(ns.Tables, func(tb connector.Table) bool { return tb.Name == name })
		if i < 0 {
			t.Errorf("table %s missing", name)
			continue
		}
		tb := ns.Tables[i]
		if !slices.Equal(tb.PrimaryKey, pk) {
			t.Errorf("%s primary key = %q, want %q", name, tb.PrimaryKey, pk)
		}
		if name == "users" {
			var cols []string
			for _, c := range tb.Columns {
				cols = append(cols, c.Name)
				if c.Type == "" {
					t.Errorf("users.%s has no type", c.Name)
				}
				if c.Name == "id" && c.Nullable {
					t.Error("users.id is reported nullable")
				}
				if c.Name == "name" && !c.Nullable {
					t.Error("users.name is reported not nullable")
				}
			}
			if !slices.Equal(cols, []string{"id", "email", "name", "status", "updated_at"}) {
				t.Errorf("users columns = %q", cols)
			}
		}
	}
}

func testPrivileges(t *testing.T, h Harness) {
	e := newEnv(t, h)
	rep, err := e.c.Privileges(ctx(t))
	if err != nil {
		t.Fatalf("Privileges: %v", err)
	}
	if len(rep.Excess) != 0 {
		t.Errorf("least-privilege user reports excess: %+v", rep.Excess)
	}
	if !rep.CanSelect || !rep.CanInsert || !rep.CanUpdate || !rep.CanDelete {
		t.Errorf("least-privilege report = %+v", rep)
	}
	cfg, pw := e.tg.Config(Superuser)
	c, err := h.Connector.Open(context.Background(), cfg, pw)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = c.Close() }()
	rep, err = c.Privileges(ctx(t))
	if err != nil {
		t.Fatalf("Privileges: %v", err)
	}
	if len(rep.Excess) == 0 {
		t.Error("superuser reports no excess privileges")
	}
	for _, x := range rep.Excess {
		if x.Privilege == "" || x.Detail == "" {
			t.Errorf("incomplete excess entry %+v", x)
		}
	}
}

func testCells(t *testing.T, h Harness) {
	e := newEnv(t, h)
	lim := limits()
	lim.MaxCellBytes = h.Cells.MaxCellBytes
	r := e.dryRun(lim, h.Cells.Statement)[0]
	if len(r.Before) != 1 {
		t.Fatalf("Before = %v, want one row", r.Before)
	}
	row := r.Before[0]
	if raw, err := json.Marshal(row); err != nil {
		t.Fatalf("row is not JSON-safe: %v", err)
	} else if !json.Valid(raw) {
		t.Fatalf("row is not valid JSON: %s", raw)
	}
	truncated := false
	for col, want := range h.Cells.Expect {
		got, err := json.Marshal(row[colIndex(t, r, col)])
		if err != nil {
			t.Errorf("%s: %v", col, err)
			continue
		}
		if !jsonEqual(t, string(got), want) {
			t.Errorf("%s = %s, want %s", col, got, want)
		}
		if strings.Contains(want, `"truncated":true`) {
			truncated = true
		}
	}
	if truncated && !r.Truncated {
		t.Error("a value was truncated but Result.Truncated is false")
	}
}

func jsonEqual(t *testing.T, a, b string) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal([]byte(a), &x); err != nil {
		t.Errorf("invalid JSON %s: %v", a, err)
		return false
	}
	if err := json.Unmarshal([]byte(b), &y); err != nil {
		t.Errorf("invalid expected JSON %s: %v", b, err)
		return false
	}
	return reflect.DeepEqual(x, y)
}
