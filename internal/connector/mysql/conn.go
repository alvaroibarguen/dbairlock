package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-sql-driver/mysql"

	"github.com/alvaroibarguen/dbairlock/internal/connector"
)

// conn is one target: a small pool for introspection, plus the driver
// configuration used to open a dedicated connection for every dry run and
// execution.
type conn struct {
	pool *sql.DB
	base *mysql.Config // never modified; cloned per dedicated connection
	db   string
}

var _ connector.Conn = (*conn)(nil)

// acquire takes a pooled connection. Failing to connect or authenticate is
// ErrUnavailable: nothing has run.
func (c *conn) acquire(ctx context.Context) (*sql.Conn, error) {
	sc, err := c.pool.Conn(ctx)
	if err != nil {
		return nil, connector.Unavailable(err)
	}
	return sc, nil
}

// Ping connects and authenticates. The driver selects the configured
// database at connect time, so a missing database (or no access to it)
// fails here too.
func (c *conn) Ping(ctx context.Context) error {
	sc, err := c.acquire(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = sc.Close() }()
	var current sql.NullString
	if err := sc.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&current); err != nil {
		return fmt.Errorf("mysql: check database: %w", err)
	}
	if !current.Valid || current.String != c.db {
		return fmt.Errorf("mysql: the connection is not using database %q", c.db)
	}
	return nil
}

// ServerInfo reads VERSION() and version_comment.
func (c *conn) ServerInfo(ctx context.Context) (connector.ServerInfo, error) {
	sc, err := c.acquire(ctx)
	if err != nil {
		return connector.ServerInfo{}, err
	}
	defer func() { _ = sc.Close() }()
	var version, comment string
	if err := sc.QueryRowContext(ctx, "SELECT VERSION(), @@version_comment").Scan(&version, &comment); err != nil {
		return connector.ServerInfo{}, fmt.Errorf("mysql: server version: %w", err)
	}
	return parseServerInfo(version, comment), nil
}

var versionRe = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)`)

// parseServerInfo interprets VERSION(). Supported means an LTS release in
// supportedReleases. MariaDB reports itself in the version string; it may
// work but is not supported.
func parseServerInfo(version, comment string) connector.ServerInfo {
	si := connector.ServerInfo{Product: "MySQL", Version: version}
	if strings.Contains(strings.ToLower(version+" "+comment), "mariadb") {
		si.Product = "MariaDB"
	}
	m := versionRe.FindStringSubmatch(version)
	if m == nil {
		return si
	}
	si.Major, _ = strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if si.Product == "MySQL" {
		for _, r := range supportedReleases {
			if r.major == si.Major && r.minor == minor {
				si.Supported = true
			}
		}
	}
	return si
}

// Close closes the pool.
func (c *conn) Close() error {
	return c.pool.Close()
}

// MySQL error numbers the connector classifies.
const (
	erLockWaitTimeout = 1205 // innodb_lock_wait_timeout or lock_wait_timeout
	erQueryTimeout    = 3024 // max_execution_time
	erLockNowait      = 3572
)

// classify maps an error that happened after BEGIN. It never returns
// ErrUnavailable: once the transaction has begun the outcome is final.
func classify(err error) error {
	if err == nil {
		return nil
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		switch me.Number {
		case erLockWaitTimeout, erQueryTimeout, erLockNowait:
			return fmt.Errorf("%w: %w", connector.ErrTimeout, err)
		}
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", connector.ErrTimeout, err)
	}
	return err
}

// quoteIdent quotes one identifier with backticks, doubling any backtick
// inside.
func quoteIdent(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "``") + "`"
}

// quoteQualified quotes db.table.
func quoteQualified(db, table string) string {
	return quoteIdent(db) + "." + quoteIdent(table)
}
