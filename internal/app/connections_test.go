package app

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/connector"
	"github.com/alvaroibarguen/dbairlock/internal/connector/postgres"
	"github.com/alvaroibarguen/dbairlock/internal/targets"
)

// connectionSecret is the password used throughout; no response may
// contain it.
const connectionSecret = "pw-Kq3vX9-never-returned"

func pgBody(name, host string, port int, db, user, password string) map[string]any {
	return map[string]any{
		"name": name, "connector": "postgres", "description": "Test database " + name,
		"config": map[string]any{
			"host": host, "port": port, "database": db, "default_schema": "public",
			"username": user, "tls_mode": "disable",
		},
		"password": password,
	}
}

func TestConnectionsAPI(t *testing.T) {
	hs := newHarness(t)
	ada := hs.completeSetup()
	bob := hs.browser("198.51.100.2")
	bob.signIn(bobMember)

	var bodies []string
	do := func(b *browser, method, path string, body any) response {
		t.Helper()
		r := b.do(method, path, body)
		bodies = append(bodies, r.Body.String())
		return r
	}

	// Both connectors are registered, with their forms.
	r := do(bob, "GET", "/api/connectors", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("connectors: %d %s", r.Code, r.Body)
	}
	conns := r.body["connectors"].([]any)
	if len(conns) != 2 || conns[0].(map[string]any)["id"] != "mysql" || conns[1].(map[string]any)["id"] != "postgres" {
		t.Fatalf("connectors = %v", conns)
	}
	if d := conns[1].(map[string]any)["dialect"]; d != "postgresql" {
		t.Errorf("postgres dialect = %v", d)
	}

	// Port 1 on loopback refuses connections; nothing connects on create.
	r = do(ada, "POST", "/api/connections", pgBody("prod-app", "127.0.0.1", 1, "app", "app", connectionSecret))
	if r.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", r.Code, r.Body)
	}
	id := r.body["id"].(string)
	pw := r.body["password"].(map[string]any)
	if pw["is_set"] != true || pw["set_by"] != "ada@example.com" {
		t.Errorf("password status = %v", pw)
	}
	policy := r.body["policy"].(map[string]any)
	if policy["required_approvals"] != float64(1) || policy["max_rows"] != float64(100) {
		t.Errorf("policy = %v", policy)
	}

	// Members may see connections but not change them.
	if r := do(bob, "GET", "/api/connections", nil); r.Code != http.StatusOK || len(r.body["connections"].([]any)) != 1 {
		t.Errorf("member list: %d %s", r.Code, r.Body)
	}
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/connections", pgBody("x", "127.0.0.1", 1, "a", "a", "p")},
		{"PATCH", "/api/connections/" + id, map[string]any{"description": "x"}},
		{"POST", "/api/connections/" + id + "/archive", nil},
		{"POST", "/api/connections/" + id + "/test", nil},
		{"POST", "/api/connections/test", map[string]any{"connector": "postgres", "config": map[string]any{}, "connection_id": id}},
	} {
		if r := do(bob, c.method, c.path, c.body); r.Code != http.StatusForbidden {
			t.Errorf("member %s %s: %d %s", c.method, c.path, r.Code, r.Body)
		}
	}

	// Validation names the field; duplicate names conflict.
	r = do(ada, "POST", "/api/connections", pgBody("other", "169.254.169.254", 5432, "app", "app", "p"))
	if r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), `"field":"config"`) {
		t.Errorf("link-local host: %d %s", r.Code, r.Body)
	}
	r = do(ada, "POST", "/api/connections", pgBody("prod-app", "127.0.0.1", 1, "app", "app", "p"))
	if r.Code != http.StatusConflict || r.errCode() != "name_taken" {
		t.Errorf("duplicate: %d %s", r.Code, r.Body)
	}
	bad := pgBody("ok-name", "127.0.0.1", 1, "app", "app", "p")
	bad["config"].(map[string]any)["password"] = "in-config"
	if r := do(ada, "POST", "/api/connections", bad); r.Code != http.StatusUnprocessableEntity {
		t.Errorf("password in config: %d %s", r.Code, r.Body)
	}

	// A test that cannot connect is a result, not an error, and is recorded.
	r = do(ada, "POST", "/api/connections/"+id+"/test", nil)
	if r.Code != http.StatusOK || r.body["ok"] != false || !strings.HasPrefix(r.body["message"].(string), "Could not connect") {
		t.Errorf("test: %d %s", r.Code, r.Body)
	}
	r = do(ada, "GET", "/api/connections/"+id, nil)
	if lt, _ := r.body["last_test"].(map[string]any); lt == nil || lt["ok"] != false {
		t.Errorf("last_test = %v", r.body["last_test"])
	}
	r = do(ada, "POST", "/api/connections/test", map[string]any{
		"connector": "postgres", "connection_id": id,
		"config": map[string]any{"host": "127.0.0.1", "port": 2, "database": "app", "username": "app", "tls_mode": "disable"},
	})
	if r.Code != http.StatusOK || r.body["ok"] != false {
		t.Errorf("unsaved test: %d %s", r.Code, r.Body)
	}
	if r := do(bob, "GET", "/api/connections/"+id+"/schema", nil); r.Code != http.StatusBadGateway || r.errCode() != "target_unavailable" {
		t.Errorf("schema of an unreachable target: %d %s", r.Code, r.Body)
	}

	// Update: policy and password; the response still carries no password.
	r = do(ada, "PATCH", "/api/connections/"+id, map[string]any{
		"password": connectionSecret + "-2",
		"policy":   map[string]any{"max_rows": 5, "allowed_functions": []string{"LOWER", "now"}},
	})
	if r.Code != http.StatusOK {
		t.Fatalf("update: %d %s", r.Code, r.Body)
	}
	policy = r.body["policy"].(map[string]any)
	if policy["max_rows"] != float64(5) || fmt.Sprint(policy["allowed_functions"]) != "[lower now]" {
		t.Errorf("policy after update = %v", policy)
	}

	// Archive hides it; its history and record remain.
	if r := do(ada, "POST", "/api/connections/"+id+"/archive", nil); r.Code != http.StatusOK || r.body["archived_at"] == nil {
		t.Errorf("archive: %d %s", r.Code, r.Body)
	}
	if r := do(ada, "GET", "/api/connections", nil); len(r.body["connections"].([]any)) != 0 {
		t.Errorf("archived connection still listed: %s", r.Body)
	}
	if r := do(ada, "GET", "/api/connections/"+id, nil); r.Code != http.StatusOK || r.body["password"].(map[string]any)["is_set"] != false {
		t.Errorf("archived get: %d %s", r.Code, r.Body)
	}
	if r := do(ada, "PATCH", "/api/connections/"+id, map[string]any{"description": "x"}); r.Code != http.StatusConflict || r.errCode() != "archived" {
		t.Errorf("update archived: %d %s", r.Code, r.Body)
	}
	if r := do(ada, "GET", "/api/connections/nope", nil); r.Code != http.StatusNotFound {
		t.Errorf("unknown id: %d", r.Code)
	}

	for _, b := range bodies {
		if strings.Contains(b, connectionSecret) {
			t.Fatalf("a response contains the password: %s", b)
		}
	}
	page, err := audit.NewReader(hs.app.store.Reader()).List(context.Background(), audit.Filter{Limit: audit.MaxPageSize})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range page.Entries {
		seen[string(e.Action)] = true
		if strings.Contains(string(e.Payload), connectionSecret) {
			t.Fatalf("audit %s contains the password", e.Action)
		}
	}
	for _, a := range []string{"connection.created", "connection.updated", "connection.tested", "connection.archived", "secret.set", "secret.replaced", "secret.cleared"} {
		if !seen[a] {
			t.Errorf("no %s audit event", a)
		}
	}
}

// pgServer is a Postgres container for the multiple-connections check.
type pgServer struct {
	host string
	port int
}

func startPostgres(t *testing.T) (*pgServer, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var c *tcpostgres.PostgresContainer
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("%v", r)
			}
		}()
		c, err = tcpostgres.Run(ctx, "postgres:18-alpine",
			tcpostgres.WithDatabase("postgres"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("super-secret"),
			tcpostgres.BasicWaitStrategies())
	}()
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })
	host, err := c.Host(ctx)
	if err != nil {
		return nil, err
	}
	p, err := c.MappedPort(ctx, "5432/tcp")
	if err != nil {
		return nil, err
	}
	return &pgServer{host: host, port: int(p.Num())}, nil
}

func (s *pgServer) exec(t *testing.T, db string, stmts ...string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, fmt.Sprintf("host=%s port=%d dbname=%s user=postgres password=super-secret sslmode=disable", s.host, s.port, db))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	for _, q := range stmts {
		if _, err := conn.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func (s *pgServer) query(t *testing.T, db, q string) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, fmt.Sprintf("host=%s port=%d dbname=%s user=postgres password=super-secret sslmode=disable", s.host, s.port, db))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var out string
	if err := conn.QueryRow(ctx, q).Scan(&out); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return out
}

// seed creates database db (unless it is "postgres") with a users table,
// and a least-privilege user.
func (s *pgServer) seed(t *testing.T, db, user, password string) {
	t.Helper()
	if db != "postgres" {
		s.exec(t, "postgres", "CREATE DATABASE "+db)
	}
	s.exec(t, "postgres", fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s'", user, password))
	s.exec(t, db,
		"REVOKE CREATE ON SCHEMA public FROM PUBLIC",
		"CREATE TABLE users (id integer PRIMARY KEY, name text NOT NULL)",
		"INSERT INTO users VALUES (1, 'Ann'), (2, 'Bob')",
		"GRANT USAGE ON SCHEMA public TO "+user,
		"GRANT SELECT, INSERT, UPDATE, DELETE ON users TO "+user,
	)
}

// TestMultipleConnections is the phase 5 check: Postgres databases side by
// side, with different users, passwords and policies, work independently,
// both on two servers and as two databases on one server.
func TestMultipleConnections(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}
	one, err := startPostgres(t)
	if err != nil {
		t.Skip("Docker is not available: " + err.Error())
	}
	two, err := startPostgres(t)
	if err != nil {
		t.Skip("Docker is not available: " + err.Error())
	}
	one.seed(t, "app", "u_a", "pw-a")
	one.seed(t, "app2", "u_c", "pw-c")
	two.seed(t, "app", "u_b", "pw-b")

	hs := newHarness(t)
	ada := hs.completeSetup()
	create := func(name string, s *pgServer, db, user, pw string, policy map[string]any) string {
		t.Helper()
		body := pgBody(name, s.host, s.port, db, user, pw)
		body["policy"] = policy
		r := ada.do("POST", "/api/connections", body)
		if r.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", name, r.Code, r.Body)
		}
		return r.body["id"].(string)
	}
	a := create("db-a", one, "app", "u_a", "pw-a", map[string]any{"max_rows": 5})
	b := create("db-b", two, "app", "u_b", "pw-b", map[string]any{"required_approvals": 2})
	c := create("db-c", one, "app2", "u_c", "pw-c", map[string]any{"store_row_images": false})

	test := func(id string) map[string]any {
		t.Helper()
		r := ada.do("POST", "/api/connections/"+id+"/test", nil)
		if r.Code != http.StatusOK {
			t.Fatalf("test: %d %s", r.Code, r.Body)
		}
		return r.body
	}
	for _, id := range []string{a, b, c} {
		res := test(id)
		if res["ok"] != true || len(res["privileges"].(map[string]any)["excess"].([]any)) != 0 {
			t.Errorf("test %s = %v", id, res)
		}
		if srv := res["server"].(map[string]any); srv["supported"] != true {
			t.Errorf("server = %v", srv)
		}
	}
	// The superuser shows the excess-privilege warning, with the fix.
	r := ada.do("POST", "/api/connections/test", map[string]any{
		"connector": "postgres", "password": "super-secret",
		"config": map[string]any{"host": one.host, "port": one.port, "database": "app", "username": "postgres", "tls_mode": "disable"},
	})
	if r.Code != http.StatusOK || r.body["ok"] != true || !strings.Contains(r.Body.String(), "NOSUPERUSER") || len(r.body["warnings"].([]any)) == 0 {
		t.Errorf("superuser test: %d %s", r.Code, r.Body)
	}
	if r := ada.do("GET", "/api/connections/"+a+"/schema", nil); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"name":"users"`) {
		t.Errorf("schema: %d %s", r.Code, r.Body)
	}

	// Each change executes against the database it was submitted to.
	ctx := context.Background()
	pg := postgres.New()
	run := func(id, sql string) {
		t.Helper()
		err := hs.app.targets.WithConn(ctx, id, func(ctx context.Context, conn targets.Connection, cn connector.Conn) error {
			st, err := pg.Validate(conn.Config, sql, conn.ConnectorPolicy())
			if err != nil {
				return err
			}
			lim := hs.app.targets.Limits(conn)
			dry, err := cn.DryRun(ctx, []connector.Statement{st}, lim, connector.RunInfo{Label: "dbairlock preview"})
			if err != nil {
				return err
			}
			_, err = cn.Execute(ctx, []connector.Statement{st}, []connector.Expectation{{Rows: dry[0].RowsAffected, Fingerprint: dry[0].Fingerprint}}, lim, connector.RunInfo{Label: "dbairlock change #1"})
			return err
		})
		if err != nil {
			t.Fatalf("run on %s: %v", id, err)
		}
	}
	run(a, "UPDATE users SET name = 'from-a' WHERE id = 1")
	run(b, "UPDATE users SET name = 'from-b' WHERE id = 1")
	run(c, "UPDATE users SET name = 'from-c' WHERE id = 1")
	for _, chk := range []struct {
		s    *pgServer
		db   string
		want string
	}{{one, "app", "from-a"}, {two, "app", "from-b"}, {one, "app2", "from-c"}} {
		if got := chk.s.query(t, chk.db, "SELECT name FROM users WHERE id = 1"); got != chk.want {
			t.Errorf("%s:%d/%s users[1] = %q, want %q", chk.s.host, chk.s.port, chk.db, got, chk.want)
		}
	}

	// The policy is per connection: a's max_rows limits only a.
	conA, _ := hs.app.targets.Get(ctx, a)
	conB, _ := hs.app.targets.Get(ctx, b)
	if hs.app.targets.Limits(conA).MaxRows != 5 || hs.app.targets.Limits(conB).MaxRows != 100 || conB.Policy.RequiredApprovals != 2 {
		t.Errorf("policies: a %+v, b %+v", conA.Policy, conB.Policy)
	}

	for _, pair := range []struct{ name, other string }{{"two servers", b}, {"two databases on one server", c}} {
		t.Run(pair.name, func(t *testing.T) {
			// A slow run on a does not block the other.
			holding, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			go func() {
				done <- hs.app.targets.WithConn(ctx, a, func(context.Context, targets.Connection, connector.Conn) error {
					close(holding)
					<-release
					return nil
				})
			}()
			<-holding
			// Waiting for a's run mutex would end in this deadline.
			short, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := hs.app.targets.WithConn(short, pair.other, func(ctx context.Context, _ targets.Connection, cn connector.Conn) error {
				return cn.Ping(ctx)
			})
			cancel()
			close(release)
			if err != nil {
				t.Errorf("run on the other connection while a is busy: %v", err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}

	// Changing a's password takes effect on the next connection and does
	// not disturb the others.
	one.exec(t, "postgres", "ALTER ROLE u_a PASSWORD 'pw-a2'")
	one.exec(t, "postgres", "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = 'u_a'")
	if res := test(a); res["ok"] != false {
		t.Errorf("test with the old password = %v", res)
	}
	if r := ada.do("PATCH", "/api/connections/"+a, map[string]any{"password": "pw-a2"}); r.Code != http.StatusOK {
		t.Fatalf("update password: %d %s", r.Code, r.Body)
	}
	if res := test(a); res["ok"] != true {
		t.Errorf("test with the new password = %v", res)
	}
	for _, id := range []string{b, c} {
		if res := test(id); res["ok"] != true {
			t.Errorf("test %s after a's password changed = %v", id, res)
		}
	}
	run(a, "UPDATE users SET name = 'again-a' WHERE id = 2")
	if got := one.query(t, "app", "SELECT name FROM users WHERE id = 2"); got != "again-a" {
		t.Errorf("a after the password change: %q", got)
	}
}
