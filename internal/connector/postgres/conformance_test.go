package postgres

import (
	"context"
	_ "embed"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/alvaroibarguen/dbairlock/internal/connector"
	"github.com/alvaroibarguen/dbairlock/internal/connector/connectortest"
)

//go:embed testdata/seed.sql
var seedSQL string

// The oldest and newest supported major versions (docs/VERSIONS.md).
var testVersions = []struct {
	major int
	image string
}{
	{14, "postgres:14-alpine"},
	{18, "postgres:18-alpine"},
}

const (
	superPassword = "super-secret"
	appUser       = "dbairlock_app"
	appPassword   = "app-secret"
)

func TestConformance(t *testing.T) {
	for _, v := range testVersions {
		t.Run(fmt.Sprintf("postgres%d", v.major), func(t *testing.T) {
			h := baseHarness()
			h.ServerMajor = v.major
			switch srv, err := startServer(t, v.image); {
			case testing.Short():
				h.Skip = "integration tests skipped in -short mode"
			case err != nil:
				h.Skip = "Docker is not available, skipping Postgres integration tests: " + err.Error()
			default:
				h.NewTarget = srv.newTarget
				h.Unreachable = func(t *testing.T) []connectortest.Unreachable { return srv.unreachable(t, v.image) }
			}
			connectortest.Run(t, h)
		})
	}
}

func baseHarness() connectortest.Harness {
	return connectortest.Harness{
		Connector:        New(),
		ValidationConfig: validationConfig(),
		DefaultNamespace: "public",
		Corpus:           corpus(),
		SystemNamespaces: []string{"pg_catalog", "information_schema", "pg_toast"},
		BeginSQL:         "BEGIN",
		SlowFunction:     "pg_sleep",
		SlowWhere:        "pg_sleep(2) IS NOT NULL",
		Cells: connectortest.CellCase{
			Statement:    "UPDATE cell_values SET note = 'm' WHERE id = 1",
			MaxCellBytes: 64,
			Expect: map[string]string{
				"note":     `"n"`,
				"n_null":   `null`,
				"big":      `{"type":"text","value":"` + repeatX(64) + `","size":100,"truncated":true}`,
				"bin":      `{"type":"bytes","value":"AP8Q","size":3}`,
				"doc":      `{"a":1,"b":[true,null,"s"]}`,
				"ts":       `"2024-03-04T03:06:07.123456Z"`,
				"amount":   `"12345.678900"`,
				"tags":     `["a","b c",null,"q\"x"]`,
				"nums":     `[1,2,3]`,
				"flag":     `true`,
				"big_int":  `"9007199254740993"`,
				"f":        `"1.5"`,
				"d":        `"2024-01-02"`,
				"plain_ts": `"2024-01-02T03:04:05"`,
			},
		},
	}
}

func repeatX(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}

func validationConfig() connector.Config {
	return connector.Config{
		keyHost: "db.internal", keyPort: 5432, keyDatabase: "app", keyDefaultSchema: "public",
		keyUsername: "app", keyTLSMode: tlsDisable,
	}
}

// server is one Postgres container shared by the tests of one version.
type server struct {
	host string
	port int
	n    atomic.Int64
}

func (s *server) dsn(db, user, password string) string {
	return fmt.Sprintf("host=%s port=%d dbname=%s user=%s password=%s sslmode=disable", s.host, s.port, db, user, password)
}

func (s *server) config(db, user string) connector.Config {
	return connector.Config{
		keyHost: s.host, keyPort: s.port, keyDatabase: db, keyDefaultSchema: "public",
		keyUsername: user, keyTLSMode: tlsDisable,
	}
}

func runContainer(t *testing.T, image string) (*tcpostgres.PostgresContainer, string, int, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var c *tcpostgres.PostgresContainer
	var err error
	func() {
		// testcontainers may panic when no Docker host can be found.
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("%v", r)
			}
		}()
		c, err = tcpostgres.Run(ctx, image,
			tcpostgres.WithDatabase("postgres"),
			tcpostgres.WithUsername("postgres"),
			tcpostgres.WithPassword(superPassword),
			tcpostgres.BasicWaitStrategies(),
		)
	}()
	if c != nil {
		t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })
	}
	if err != nil {
		return nil, "", 0, err
	}
	host, err := c.Host(ctx)
	if err != nil {
		return nil, "", 0, err
	}
	port, err := c.MappedPort(ctx, "5432/tcp")
	if err != nil {
		return nil, "", 0, err
	}
	return c, host, int(port.Num()), nil
}

func startServer(t *testing.T, image string) (*server, error) {
	t.Helper()
	if testing.Short() {
		return nil, nil
	}
	_, host, port, err := runContainer(t, image)
	if err != nil {
		return nil, err
	}
	s := &server{host: host, port: port}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	c, err := pgx.Connect(ctx, s.dsn("postgres", "postgres", superPassword))
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close(context.Background()) }()
	if _, err := c.Exec(ctx, fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s'", appUser, appPassword)); err != nil {
		return nil, err
	}
	return s, nil
}

// newTarget creates a fresh database with the seed fixture.
func (s *server) newTarget(t *testing.T) connectortest.Target {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	db := "t" + strconv.FormatInt(s.n.Add(1), 10)
	root, err := pgx.Connect(ctx, s.dsn("postgres", "postgres", superPassword))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := root.Exec(ctx, "CREATE DATABASE "+db); err != nil {
		t.Fatalf("create database: %v", err)
	}
	_ = root.Close(ctx)
	admin, err := pgx.Connect(ctx, s.dsn(db, "postgres", superPassword))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	if _, err := admin.Exec(ctx, seedSQL); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := admin.Exec(ctx, fmt.Sprintf(`GRANT USAGE ON SCHEMA public TO %[1]s;
		GRANT SELECT, INSERT, UPDATE, DELETE ON users, orders, audit_notes, cell_values TO %[1]s`, appUser)); err != nil {
		t.Fatalf("grant: %v", err)
	}
	return &target{s: s, db: db, admin: admin}
}

func (s *server) unreachable(t *testing.T, image string) []connectortest.Unreachable {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refusedPort := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	c, host, port, err := runContainer(t, image)
	if err != nil {
		t.Fatalf("start container to stop: %v", err)
	}
	if err := c.Stop(context.Background(), nil); err != nil {
		t.Fatalf("stop container: %v", err)
	}
	stopped := s.config("postgres", "postgres")
	stopped[keyHost], stopped[keyPort] = host, port

	refused := s.config("postgres", appUser)
	refused[keyHost], refused[keyPort] = "127.0.0.1", refusedPort

	return []connectortest.Unreachable{
		{Name: "stopped", Config: stopped, Password: []byte(superPassword)},
		{Name: "wrong password", Config: s.config("postgres", appUser), Password: []byte("wrong")},
		{Name: "refused", Config: refused, Password: []byte(appPassword)},
	}
}

type target struct {
	s     *server
	db    string
	mu    sync.Mutex
	admin *pgx.Conn
}

func (tg *target) Config(role connectortest.Role) (connector.Config, []byte) {
	if role == connectortest.Superuser {
		return tg.s.config(tg.db, "postgres"), []byte(superPassword)
	}
	return tg.s.config(tg.db, appUser), []byte(appPassword)
}

func (tg *target) Admin(ctx context.Context, sql string) error {
	tg.mu.Lock()
	defer tg.mu.Unlock()
	_, err := tg.admin.Exec(ctx, sql)
	return err
}

func (tg *target) AdminQuery(ctx context.Context, sql string) ([][]string, error) {
	tg.mu.Lock()
	defer tg.mu.Unlock()
	rows, err := tg.admin.Query(ctx, sql, pgx.QueryExecModeSimpleProtocol)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]string
	for rows.Next() {
		raw := rows.RawValues()
		row := make([]string, len(raw))
		for i, v := range raw {
			if v == nil {
				row[i] = "<NULL>"
			} else {
				row[i] = string(v)
			}
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (tg *target) Session(ctx context.Context) (connectortest.Session, error) {
	c, err := pgx.Connect(ctx, tg.s.dsn(tg.db, "postgres", superPassword))
	if err != nil {
		return nil, err
	}
	return &session{c: c}, nil
}

func (tg *target) Labels(ctx context.Context) ([]string, error) {
	rows, err := tg.AdminQuery(ctx, "SELECT application_name FROM pg_stat_activity WHERE datname = current_database()")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r[0])
	}
	return out, nil
}

func (tg *target) Kill(ctx context.Context, label string) error {
	tg.mu.Lock()
	defer tg.mu.Unlock()
	_, err := tg.admin.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name = $1", label)
	return err
}

type session struct{ c *pgx.Conn }

func (s *session) Exec(ctx context.Context, sql string) error {
	_, err := s.c.Exec(ctx, sql)
	return err
}

func (s *session) Close() error { return s.c.Close(context.Background()) }
