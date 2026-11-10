package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alvaroibarguen/dbairlock/internal/connector"
)

// conn is one target: a small pgx pool plus the connection's default schema.
type conn struct {
	pool   *pgxpool.Pool
	schema string
}

var _ connector.Conn = (*conn)(nil)

// Ping connects, authenticates and checks that the default schema exists.
// Failing to connect or authenticate is ErrUnavailable.
func (c *conn) Ping(ctx context.Context) error {
	pc, err := c.pool.Acquire(ctx)
	if err != nil {
		return connector.Unavailable(err)
	}
	defer pc.Release()
	var exists bool
	err = pc.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname = $1::text)`, c.schema).Scan(&exists)
	if err != nil {
		return fmt.Errorf("postgres: check default schema: %w", err)
	}
	if !exists {
		return fmt.Errorf("postgres: the default schema %q does not exist in this database", c.schema)
	}
	return nil
}

// ServerInfo reads server_version_num.
func (c *conn) ServerInfo(ctx context.Context) (connector.ServerInfo, error) {
	var num, version string
	err := c.pool.QueryRow(ctx, `SELECT pg_catalog.current_setting('server_version_num'), pg_catalog.current_setting('server_version')`).Scan(&num, &version)
	if err != nil {
		if isConnectError(err) {
			return connector.ServerInfo{}, connector.Unavailable(err)
		}
		return connector.ServerInfo{}, fmt.Errorf("postgres: server version: %w", err)
	}
	return parseServerInfo(num, version)
}

func parseServerInfo(num, version string) (connector.ServerInfo, error) {
	n, err := strconv.Atoi(strings.TrimSpace(num))
	if err != nil {
		return connector.ServerInfo{}, fmt.Errorf("postgres: unexpected server_version_num %q", num)
	}
	major := n / 10000
	return connector.ServerInfo{
		Product:   "PostgreSQL",
		Version:   version,
		Major:     major,
		Supported: major >= minSupportedMajor && major <= maxSupportedMajor,
	}, nil
}

// Close closes the pool.
func (c *conn) Close() error {
	c.pool.Close()
	return nil
}

// isConnectError reports whether err happened while establishing a
// connection (dial, TLS, authentication), before any statement was sent.
func isConnectError(err error) bool {
	var ce *pgconn.ConnectError
	return errors.As(err, &ce)
}

// classify maps an error that happened after BEGIN. It never returns
// ErrUnavailable: once the transaction has begun the outcome is final.
func classify(err error) error {
	if err == nil {
		return nil
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case "57014", "55P03": // query_canceled (statement_timeout), lock_not_available (lock_timeout)
			return fmt.Errorf("%w: %w", connector.ErrTimeout, err)
		}
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) || pgconn.Timeout(err) {
		return fmt.Errorf("%w: %w", connector.ErrTimeout, err)
	}
	return err
}

// quoteIdent quotes one identifier.
func quoteIdent(s string) string { return pgx.Identifier{s}.Sanitize() }

// quoteQualified quotes schema.table.
func quoteQualified(schema, table string) string { return pgx.Identifier{schema, table}.Sanitize() }
