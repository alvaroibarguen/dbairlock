package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/connector"
	"github.com/alvaroibarguen/dbairlock/internal/connector/connectortest"
)

// TestPostgresSpecific covers behaviour outside the shared suite, on the
// newest supported version.
func TestPostgresSpecific(t *testing.T) {
	if testing.Short() {
		t.Skip("integration tests skipped in -short mode")
	}
	v := testVersions[len(testVersions)-1]
	srv, err := startServer(t, v.image)
	if err != nil {
		t.Skip("Docker is not available, skipping Postgres integration tests: " + err.Error())
	}
	t.Run("search path is pinned to the default schema", func(t *testing.T) { testSearchPath(t, srv) })
	t.Run("statement fields must match the SQL", func(t *testing.T) { testTampered(t, srv) })
	t.Run("missing default schema", func(t *testing.T) { testMissingSchema(t, srv) })
	t.Run("update after image", func(t *testing.T) { testAfterImage(t, srv) })
	t.Run("hostile text on a server with standard_conforming_strings off", func(t *testing.T) { testHostileOnServer(t, srv) })
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

// With the database user's own search_path set to another schema that
// contains a table of the same name, an unqualified statement still
// affects the table in the connection's default schema.
func testSearchPath(t *testing.T, srv *server) {
	tg := srv.newTarget(t).(*target)
	for _, s := range []string{
		"CREATE SCHEMA other",
		"CREATE TABLE other.users (LIKE public.users INCLUDING ALL)",
		"INSERT INTO other.users SELECT * FROM public.users",
		fmt.Sprintf("GRANT USAGE ON SCHEMA other TO %s", appUser),
		fmt.Sprintf("GRANT SELECT, INSERT, UPDATE, DELETE ON other.users TO %s", appUser),
		fmt.Sprintf("ALTER ROLE %s IN DATABASE %s SET search_path = other", appUser, tg.db),
	} {
		if err := tg.Admin(context.Background(), s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	c := open(t, tg, connectortest.LeastPrivilege, nil)
	cfg, _ := tg.Config(connectortest.LeastPrivilege)
	st, err := New().Validate(cfg, "UPDATE users SET name = 'Zed' WHERE id = 1", connector.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Schema != "public" {
		t.Fatalf("Schema = %q", st.Schema)
	}
	lim := connector.DefaultLimits()
	dry, err := c.DryRun(tctx(t), []connector.Statement{st}, lim, connector.RunInfo{Label: "dbairlock preview"})
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	exp := []connector.Expectation{{Rows: dry[0].RowsAffected, Fingerprint: dry[0].Fingerprint}}
	if _, err := c.Execute(tctx(t), []connector.Statement{st}, exp, lim, connector.RunInfo{Label: "dbairlock change #1"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	rows, err := tg.AdminQuery(context.Background(), "SELECT (SELECT name FROM public.users WHERE id = 1), (SELECT name FROM other.users WHERE id = 1)")
	if err != nil {
		t.Fatal(err)
	}
	if rows[0][0] != "Zed" || rows[0][1] != "Ann" {
		t.Errorf("public.users name = %s, other.users name = %s; want Zed, Ann", rows[0][0], rows[0][1])
	}
}

func testTampered(t *testing.T, srv *server) {
	tg := srv.newTarget(t)
	c := open(t, tg, connectortest.LeastPrivilege, nil)
	cfg, _ := tg.Config(connectortest.LeastPrivilege)
	st, err := New().Validate(cfg, "DELETE FROM users WHERE id = 1", connector.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*connector.Statement){
		"where":  func(s *connector.Statement) { s.Where = "true" },
		"table":  func(s *connector.Statement) { s.Table = "orders" },
		"schema": func(s *connector.Statement) { s.Schema = "pg_catalog" },
		"sql":    func(s *connector.Statement) { s.SQL = "DELETE FROM users WHERE id > 0" },
		"kind":   func(s *connector.Statement) { s.Kind = connector.KindUpdate },
		"invalid": func(s *connector.Statement) {
			s.SQL = "DELETE FROM users"
		},
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
	rows, _ := tg.AdminQuery(context.Background(), "SELECT count(*) FROM users")
	if rows[0][0] != "5" {
		t.Errorf("users count = %s", rows[0][0])
	}
}

func testMissingSchema(t *testing.T, srv *server) {
	tg := srv.newTarget(t)
	c := open(t, tg, connectortest.LeastPrivilege, func(cfg connector.Config) { cfg[keyDefaultSchema] = "nope" })
	err := c.Ping(tctx(t))
	if err == nil || errors.Is(err, connector.ErrUnavailable) {
		t.Errorf("Ping = %v, want a missing-schema error", err)
	}
}

func testAfterImage(t *testing.T, srv *server) {
	tg := srv.newTarget(t)
	c := open(t, tg, connectortest.LeastPrivilege, nil)
	cfg, _ := tg.Config(connectortest.LeastPrivilege)
	st, err := New().Validate(cfg, "UPDATE orders AS o SET total = o.total * 2 WHERE o.user_id = 1", connector.Policy{})
	if err != nil {
		t.Fatal(err)
	}
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
}
