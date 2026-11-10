package app

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// Target databases for the changes and executor integration tests: one
// Postgres and one MySQL container, shared by the tests, with a fresh
// seeded database per test. dbairlock reaches each database through a TCP
// proxy the test can take down, to simulate an unreachable target.

const (
	tgtAppUser     = "chg_app"
	tgtAppPassword = "chg-app-secret"
	tgtSuper       = "super-secret"
)

// targetServer creates fresh databases.
type targetServer interface {
	name() string
	newDB(t *testing.T) targetDB
}

// targetDB is one fresh, seeded database:
//
//	users (id int PRIMARY KEY, email, name, status, updated_at)
//	  (1 ann Ann active) (2 bob Bob active) (3 cat Cat banned) (4 dan Dan active) (5 eve Eve inactive)
//	audit_notes (user_id int, note) -- no primary key
//	  (1 created) (1 created) (2 verified)
type targetDB interface {
	connector() string
	// config is the connection config with host and port as given.
	config(host string, port int) map[string]any
	backend() string
	exec(t *testing.T, sql string)
	// value runs a query as a superuser and returns its first value as
	// text ("<NULL>" for NULL).
	value(t *testing.T, sql string) string
	// lock runs sql in a superuser transaction that stays open (holding the
	// row locks it took) until release is called.
	lock(t *testing.T, sql string) (release func())
}

var seedStatements = []string{
	`CREATE TABLE users (id int PRIMARY KEY, email varchar(200) NOT NULL, name varchar(200),
		status varchar(20) NOT NULL, updated_at timestamp NULL)`,
	`INSERT INTO users (id, email, name, status) VALUES
		(1, 'ann@example.com', 'Ann', 'active'), (2, 'bob@example.com', 'Bob', 'active'),
		(3, 'cat@example.com', 'Cat', 'banned'), (4, 'dan@example.com', 'Dan', 'active'),
		(5, 'eve@example.com', 'Eve', 'inactive')`,
	`CREATE TABLE audit_notes (user_id int NOT NULL, note varchar(200))`,
	`INSERT INTO audit_notes (user_id, note) VALUES (1, 'created'), (1, 'created'), (2, 'verified')`,
}

// startContainer runs a container, turning a testcontainers panic (no
// Docker) into an error.
func startContainer(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	return fn()
}

// --- Postgres ---

type pgTargets struct {
	host string
	port int
	n    atomic.Int64
}

var (
	pgOnce   sync.Once
	pgShared *pgTargets
	pgErr    error
	myOnce   sync.Once
	myShared *myTargets
	myErr    error
)

func (s *pgTargets) name() string { return "postgres" }

func (s *pgTargets) dsn(db string) string {
	return fmt.Sprintf("host=%s port=%d dbname=%s user=postgres password=%s sslmode=disable", s.host, s.port, db, tgtSuper)
}

func (s *pgTargets) connect(t *testing.T, db string) *pgx.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := pgx.Connect(ctx, s.dsn(db))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return c
}

func (s *pgTargets) newDB(t *testing.T) targetDB {
	t.Helper()
	db := "chg" + strconv.FormatInt(s.n.Add(1), 10)
	root := s.connect(t, "postgres")
	defer func() { _ = root.Close(context.Background()) }()
	if _, err := root.Exec(context.Background(), "CREATE DATABASE "+db); err != nil {
		t.Fatalf("create database: %v", err)
	}
	d := &pgDB{s: s, db: db}
	for _, q := range seedStatements {
		d.exec(t, q)
	}
	d.exec(t, "GRANT USAGE ON SCHEMA public TO "+tgtAppUser)
	d.exec(t, "GRANT SELECT, INSERT, UPDATE, DELETE ON users, audit_notes TO "+tgtAppUser)
	return d
}

type pgDB struct {
	s  *pgTargets
	db string
}

func (d *pgDB) connector() string { return "postgres" }
func (d *pgDB) backend() string   { return net.JoinHostPort(d.s.host, strconv.Itoa(d.s.port)) }

func (d *pgDB) config(host string, port int) map[string]any {
	return map[string]any{
		"host": host, "port": port, "database": d.db, "default_schema": "public",
		"username": tgtAppUser, "tls_mode": "disable",
	}
}

func (d *pgDB) exec(t *testing.T, q string) {
	t.Helper()
	c := d.s.connect(t, d.db)
	defer func() { _ = c.Close(context.Background()) }()
	if _, err := c.Exec(context.Background(), q); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func (d *pgDB) value(t *testing.T, q string) string {
	t.Helper()
	c := d.s.connect(t, d.db)
	defer func() { _ = c.Close(context.Background()) }()
	var v *string
	if err := c.QueryRow(context.Background(), "SELECT ("+q+")::text").Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	if v == nil {
		return "<NULL>"
	}
	return *v
}

func (d *pgDB) lock(t *testing.T, q string) func() {
	t.Helper()
	c := d.s.connect(t, d.db)
	tx, err := c.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), q); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			_ = tx.Rollback(context.Background())
			_ = c.Close(context.Background())
		})
	}
	t.Cleanup(release)
	return release
}

func postgresTargets(t *testing.T) *pgTargets {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}
	pgOnce.Do(func() {
		var srv *pgServer
		pgErr = startContainer(func() error {
			var err error
			srv, err = startPostgresShared()
			return err
		})
		if pgErr != nil {
			return
		}
		pgShared = &pgTargets{host: srv.host, port: srv.port}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		c, err := pgx.Connect(ctx, pgShared.dsn("postgres"))
		if err != nil {
			pgErr = err
			return
		}
		defer func() { _ = c.Close(ctx) }()
		_, pgErr = c.Exec(ctx, fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s'", tgtAppUser, tgtAppPassword))
	})
	if pgErr != nil {
		t.Skip("Docker is not available: " + pgErr.Error())
	}
	return pgShared
}

// --- MySQL ---

type myTargets struct {
	host string
	port int
	n    atomic.Int64
}

func (s *myTargets) name() string { return "mysql" }

func (s *myTargets) open(db string) (*sql.DB, error) {
	cfg := mysqldriver.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(s.host, strconv.Itoa(s.port))
	cfg.User = "root"
	cfg.Passwd = tgtSuper
	cfg.DBName = db
	c, err := mysqldriver.NewConnector(cfg)
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(c), nil
}

func (s *myTargets) newDB(t *testing.T) targetDB {
	t.Helper()
	db := "chg" + strconv.FormatInt(s.n.Add(1), 10)
	root, err := s.open("")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if _, err := root.ExecContext(context.Background(), "CREATE DATABASE "+db); err != nil {
		t.Fatalf("create database: %v", err)
	}
	d := &myDB{s: s, db: db}
	for _, q := range seedStatements {
		d.exec(t, strings.Replace(q, "timestamp NULL", "datetime(6) NULL", 1))
	}
	d.exec(t, fmt.Sprintf("GRANT SELECT, INSERT, UPDATE, DELETE ON %s.* TO '%s'@'%%'", db, tgtAppUser))
	return d
}

type myDB struct {
	s  *myTargets
	db string
}

func (d *myDB) connector() string { return "mysql" }
func (d *myDB) backend() string   { return net.JoinHostPort(d.s.host, strconv.Itoa(d.s.port)) }

func (d *myDB) config(host string, port int) map[string]any {
	return map[string]any{"host": host, "port": port, "database": d.db, "username": tgtAppUser, "tls_mode": "disabled"}
}

func (d *myDB) conn(t *testing.T) *sql.DB {
	t.Helper()
	c, err := d.s.open(d.db)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (d *myDB) exec(t *testing.T, q string) {
	t.Helper()
	c := d.conn(t)
	defer func() { _ = c.Close() }()
	if _, err := c.ExecContext(context.Background(), q); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func (d *myDB) value(t *testing.T, q string) string {
	t.Helper()
	c := d.conn(t)
	defer func() { _ = c.Close() }()
	var v sql.NullString
	if err := c.QueryRowContext(context.Background(), "SELECT CAST(("+q+") AS CHAR)").Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	if !v.Valid {
		return "<NULL>"
	}
	return v.String
}

func (d *myDB) lock(t *testing.T, q string) func() {
	t.Helper()
	c := d.conn(t)
	tx, err := c.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(context.Background(), q); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			_ = tx.Rollback()
			_ = c.Close()
		})
	}
	t.Cleanup(release)
	return release
}

func mysqlTargets(t *testing.T) *myTargets {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}
	myOnce.Do(func() {
		myErr = startContainer(func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			c, err := tcmysql.Run(ctx, "mysql:9.7",
				tcmysql.WithUsername("root"), tcmysql.WithPassword(tgtSuper), tcmysql.WithDatabase("app"))
			if c != nil {
				containers = append(containers, c)
			}
			if err != nil {
				return err
			}
			host, err := c.Host(ctx)
			if err != nil {
				return err
			}
			port, err := c.MappedPort(ctx, "3306/tcp")
			if err != nil {
				return err
			}
			myShared = &myTargets{host: host, port: int(port.Num())}
			root, err := myShared.open("")
			if err != nil {
				return err
			}
			defer func() { _ = root.Close() }()
			_, err = root.ExecContext(ctx, fmt.Sprintf("CREATE USER '%s'@'%%' IDENTIFIED BY '%s'", tgtAppUser, tgtAppPassword))
			return err
		})
	})
	if myErr != nil {
		t.Skip("Docker is not available: " + myErr.Error())
	}
	return myShared
}

// containers started for the whole package run; TestMain terminates them.
var containers []testcontainers.Container

func TestMain(m *testing.M) {
	code := m.Run()
	for _, c := range containers {
		_ = testcontainers.TerminateContainer(c)
	}
	os.Exit(code)
}

// startPostgresShared starts the shared Postgres container.
func startPostgresShared() (*pgServer, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	c, err := tcpostgres.Run(ctx, "postgres:18-alpine",
		tcpostgres.WithDatabase("postgres"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword(tgtSuper),
		tcpostgres.BasicWaitStrategies())
	if c != nil {
		containers = append(containers, c)
	}
	if err != nil {
		return nil, err
	}
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

// --- proxy ---

// proxy forwards TCP connections to a backend. Taken down, it closes every
// forwarded connection and refuses new ones (accepts and closes them at
// once), as a stopped database would.
type proxy struct {
	ln      net.Listener
	backend string

	mu    sync.Mutex
	down  bool
	conns map[net.Conn]struct{}
}

func newProxy(t *testing.T, backend string) *proxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &proxy{ln: ln, backend: backend, conns: map[net.Conn]struct{}{}}
	t.Cleanup(func() {
		_ = ln.Close()
		p.killAll()
	})
	go p.serve()
	return p
}

func (p *proxy) port() int { return p.ln.Addr().(*net.TCPAddr).Port }

func (p *proxy) serve() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		down := p.down
		p.mu.Unlock()
		if down {
			_ = c.Close()
			continue
		}
		go p.forward(c)
	}
}

func (p *proxy) forward(c net.Conn) {
	b, err := net.DialTimeout("tcp", p.backend, 5*time.Second)
	if err != nil {
		_ = c.Close()
		return
	}
	p.mu.Lock()
	p.conns[c], p.conns[b] = struct{}{}, struct{}{}
	p.mu.Unlock()
	done := make(chan struct{}, 2)
	pipe := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		done <- struct{}{}
	}
	go pipe(b, c)
	go pipe(c, b)
	<-done
	_ = c.Close()
	_ = b.Close()
	p.mu.Lock()
	delete(p.conns, c)
	delete(p.conns, b)
	p.mu.Unlock()
}

// setDown takes the proxy down (closing every connection) or brings it up.
func (p *proxy) setDown(down bool) {
	p.mu.Lock()
	p.down = down
	p.mu.Unlock()
	if down {
		p.killAll()
	}
}

// killAll closes every forwarded connection, as a network failure would.
func (p *proxy) killAll() {
	p.mu.Lock()
	conns := make([]net.Conn, 0, len(p.conns))
	for c := range p.conns {
		conns = append(conns, c)
	}
	p.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}
