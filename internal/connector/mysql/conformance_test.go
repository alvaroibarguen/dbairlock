package mysql

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/testcontainers/testcontainers-go"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"

	"github.com/alvaroibarguen/dbairlock/internal/connector"
	"github.com/alvaroibarguen/dbairlock/internal/connector/connectortest"
)

//go:embed testdata/seed.sql
var seedSQL string

// The oldest and newest supported LTS releases (docs/VERSIONS.md).
var testVersions = []struct {
	major int
	image string
}{
	{8, "mysql:8.4"},
	{9, "mysql:9.7"},
}

const (
	rootPassword = "root-secret"
	appUser      = "dbairlock_app"
	appPassword  = "app-secret"
)

func TestConformance(t *testing.T) {
	for _, v := range testVersions {
		t.Run(fmt.Sprintf("mysql%d", v.major), func(t *testing.T) {
			h := baseHarness()
			h.ServerMajor = v.major
			switch srv, err := startServer(t, v.image); {
			case testing.Short():
				h.Skip = "integration tests skipped in -short mode"
			case err != nil:
				h.Skip = "Docker is not available, skipping MySQL integration tests: " + err.Error()
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
		DefaultNamespace: "app",
		Corpus:           corpus(),
		SystemNamespaces: []string{"mysql", "information_schema", "performance_schema", "sys"},
		BeginSQL:         "START TRANSACTION",
		SlowFunction:     "sleep",
		SlowWhere:        "SLEEP(2) = 0",
		Cells: connectortest.CellCase{
			Statement:    "UPDATE cell_values SET note = 'm' WHERE id = 1",
			MaxCellBytes: 64,
			Expect: map[string]string{
				"note":     `"n"`,
				"n_null":   `null`,
				"big":      `{"type":"text","value":"` + strings.Repeat("x", 64) + `","size":100,"truncated":true}`,
				"bin":      `{"type":"bytes","value":"AP8Q","size":3}`,
				"doc":      `{"a":1,"b":[true,null,"s"]}`,
				"ts":       `"2024-03-04T03:06:07.123456Z"`,
				"amount":   `"12345.678900"`,
				"flag":     `1`,
				"big_int":  `"9007199254740993"`,
				"uns":      `"18446744073709551615"`,
				"f":        `"1.5"`,
				"d":        `"2024-01-02"`,
				"plain_ts": `"2024-01-02T03:04:05"`,
				"tm":       `"12:34:56"`,
				"yr":       `2024`,
				"bits":     `{"type":"bytes","value":"BQ==","size":1}`,
				"kind":     `"b c"`,
				"tags":     `"x,y"`,
				"uni":      `"ñandú €"`,
			},
		},
	}
}

func validationConfig() connector.Config {
	return connector.Config{
		keyHost: "db.internal", keyPort: 3306, keyDatabase: "app", keyUsername: "app", keyTLSMode: tlsDisabled,
	}
}

// server is one MySQL container shared by the tests of one version.
type server struct {
	host string
	port int
	n    atomic.Int64
}

// adminDB opens a root connection pool to db, with multi-statement
// support for seeding. It is a test helper, not how the connector
// connects.
func (s *server) adminDB(db string) (*sql.DB, error) {
	cfg := mysql.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(s.host, strconv.Itoa(s.port))
	cfg.User = "root"
	cfg.Passwd = rootPassword
	cfg.DBName = db
	cfg.MultiStatements = true
	c, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(c), nil
}

func (s *server) config(db, user string) connector.Config {
	return connector.Config{
		keyHost: s.host, keyPort: s.port, keyDatabase: db, keyUsername: user, keyTLSMode: tlsDisabled,
	}
}

func runContainer(t *testing.T, image string) (*tcmysql.MySQLContainer, string, int, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var c *tcmysql.MySQLContainer
	var err error
	func() {
		// testcontainers may panic when no Docker host can be found.
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("%v", r)
			}
		}()
		c, err = tcmysql.Run(ctx, image,
			tcmysql.WithUsername("root"),
			tcmysql.WithPassword(rootPassword),
			tcmysql.WithDatabase("app"),
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
	port, err := c.MappedPort(ctx, "3306/tcp")
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
	db, err := s.adminDB("")
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := db.ExecContext(ctx, fmt.Sprintf("CREATE USER '%s'@'%%' IDENTIFIED BY '%s'", appUser, appPassword)); err != nil {
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
	root, err := s.adminDB("")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = root.Close() }()
	if _, err := root.ExecContext(ctx, "CREATE DATABASE "+db); err != nil {
		t.Fatalf("create database: %v", err)
	}
	admin, err := s.adminDB(db)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	if _, err := admin.ExecContext(ctx, seedSQL); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := admin.ExecContext(ctx, fmt.Sprintf("GRANT SELECT, INSERT, UPDATE, DELETE ON %s.* TO '%s'@'%%'", db, appUser)); err != nil {
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
	stopped := s.config("app", "root")
	stopped[keyHost], stopped[keyPort] = host, port

	refused := s.config("app", appUser)
	refused[keyHost], refused[keyPort] = "127.0.0.1", refusedPort

	return []connectortest.Unreachable{
		{Name: "stopped", Config: stopped, Password: []byte(rootPassword)},
		{Name: "wrong password", Config: s.config("app", appUser), Password: []byte("wrong")},
		{Name: "refused", Config: refused, Password: []byte(appPassword)},
	}
}

type target struct {
	s     *server
	db    string
	admin *sql.DB
}

var _ connectortest.NamespaceTarget = (*target)(nil)

// Namespace is the target's own database: the only namespace a MySQL
// connection has.
func (tg *target) Namespace() string { return tg.db }

func (tg *target) Config(role connectortest.Role) (connector.Config, []byte) {
	if role == connectortest.Superuser {
		return tg.s.config(tg.db, "root"), []byte(rootPassword)
	}
	return tg.s.config(tg.db, appUser), []byte(appPassword)
}

func (tg *target) Admin(ctx context.Context, sql string) error {
	_, err := tg.admin.ExecContext(ctx, sql)
	return err
}

func (tg *target) AdminQuery(ctx context.Context, q string) ([][]string, error) {
	rows, err := tg.admin.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out [][]string
	for rows.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make([]string, len(cols))
		for i, v := range vals {
			if v.Valid {
				row[i] = v.String
			} else {
				row[i] = "<NULL>"
			}
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (tg *target) Session(ctx context.Context) (connectortest.Session, error) {
	db, err := tg.s.adminDB(tg.db)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	c, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &session{db: db, c: c}, nil
}

// Labels reads the run label attribute of every connected session.
func (tg *target) Labels(ctx context.Context) ([]string, error) {
	rows, err := tg.AdminQuery(ctx, "SELECT ATTR_VALUE FROM performance_schema.session_connect_attrs WHERE ATTR_NAME = '"+labelAttr+"'")
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
	rows, err := tg.AdminQuery(ctx, "SELECT PROCESSLIST_ID FROM performance_schema.session_connect_attrs WHERE ATTR_NAME = '"+labelAttr+"' AND ATTR_VALUE = '"+strings.ReplaceAll(label, "'", "''")+"'")
	if err != nil {
		return err
	}
	for _, r := range rows {
		id, err := strconv.ParseUint(r[0], 10, 64)
		if err != nil {
			return err
		}
		if _, err := tg.admin.ExecContext(ctx, "KILL "+strconv.FormatUint(id, 10)); err != nil {
			return err
		}
	}
	return nil
}

type session struct {
	db *sql.DB
	c  *sql.Conn
}

func (s *session) Exec(ctx context.Context, sql string) error {
	_, err := s.c.ExecContext(ctx, sql)
	return err
}

// Close closes the connection itself (closing the session's own pool, not
// just returning the connection to it), so its transaction ends and its
// locks are released.
func (s *session) Close() error {
	_ = s.c.Close()
	return s.db.Close()
}
