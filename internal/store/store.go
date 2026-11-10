// Package store opens the SQLite metadata database, applies the embedded
// migrations and provides the transaction helper every module writes through.
//
// The store owns two handles on the same file: a writer limited to a single
// connection, whose transactions begin with BEGIN IMMEDIATE, and a pool of
// read-only connections. Domain modules never see either *sql.DB; they get a
// *Tx inside Store.Tx, or the read-only DBTX from Store.Reader, and wrap them
// in their own sqlc package behind a repository interface.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

// FileName is the database file inside the data directory.
const FileName = "dbairlock.db"

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrations returns the embedded migration folder.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(err) // the embed pattern above guarantees the directory exists
	}
	return sub
}

var (
	// ErrDatabaseNewer means the database was migrated by a newer dbairlock
	// than this binary. Starting would risk corrupting data the binary does
	// not understand.
	ErrDatabaseNewer = errors.New("store: database is at a newer migration than this binary knows")
	// ErrNotReady means the store is closed or not yet migrated.
	ErrNotReady = errors.New("store: not ready")
	// ErrNotFound means the database file does not exist (see OpenExisting).
	ErrNotFound = errors.New("store: database file not found")
)

// DBTX is the query surface sqlc-generated packages accept. Both *Tx and
// the handle returned by Store.Reader implement it.
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Store is an open, migrated database.
type Store struct {
	path   string
	writer *sql.DB
	reader *sql.DB
	ready  atomic.Bool
}

// Open creates dataDir (mode 0700) if needed, opens <dataDir>/dbairlock.db,
// and applies pending migrations. It fails with ErrDatabaseNewer if the
// database is ahead of this binary.
func Open(ctx context.Context, dataDir string) (*Store, error) {
	if dataDir == "" {
		return nil, errors.New("store: data directory is required")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("store: create data directory: %w", err)
	}
	return open(ctx, filepath.Join(dataDir, FileName))
}

// OpenExisting is Open for commands that must not create a database, such as
// `dbairlock audit verify`. It fails with ErrNotFound if the file is missing.
func OpenExisting(ctx context.Context, dataDir string) (*Store, error) {
	path := filepath.Join(dataDir, FileName)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return nil, fmt.Errorf("store: %w", err)
	}
	return open(ctx, path)
}

// dsn builds a modernc.org/sqlite DSN. Every connection gets the same pragmas;
// the driver runs them each time the pool opens a connection.
func dsn(path string, readOnly bool) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(FULL)")
	q.Add("_pragma", "foreign_keys(ON)")
	// Sorts and temporary tables stay in memory: the container image runs
	// with a read-only root filesystem, so there is no writable /tmp, and
	// the metadata store is small.
	q.Add("_pragma", "temp_store(MEMORY)")
	if readOnly {
		q.Add("_pragma", "query_only(ON)")
	} else {
		// database/sql BeginTx then issues BEGIN IMMEDIATE, taking the write
		// lock up front instead of failing with SQLITE_BUSY on upgrade.
		q.Set("_txlock", "immediate")
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}
	return u.String()
}

func open(ctx context.Context, path string) (s *Store, err error) {
	// Restrict an existing file before opening it. SQLite opens a file it
	// cannot write read-only without saying so, and that connection would
	// stay read-only even after a later chmod.
	if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("store: restrict database file permissions: %w", err)
	}
	writer, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		return nil, fmt.Errorf("store: open writer: %w", err)
	}
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)
	writer.SetConnMaxLifetime(0)
	writer.SetConnMaxIdleTime(0)
	defer func() {
		if err != nil {
			_ = writer.Close()
		}
	}()
	// Connect the writer first: it is the connection that switches a new file
	// to WAL mode, which the read-only connections cannot do.
	if err := writer.PingContext(ctx); err != nil {
		if isReadOnly(err) {
			return nil, readOnlyError(path, err)
		}
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("store: restrict database file permissions: %w", err)
	}
	if err := checkWritable(ctx, writer, path); err != nil {
		return nil, err
	}
	if err := migrate(ctx, writer); err != nil {
		return nil, err
	}

	reader, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		return nil, fmt.Errorf("store: open reader: %w", err)
	}
	n := max(4, runtime.GOMAXPROCS(0))
	reader.SetMaxOpenConns(n)
	reader.SetMaxIdleConns(n)
	if err := reader.PingContext(ctx); err != nil {
		_ = reader.Close()
		return nil, fmt.Errorf("store: open reader: %w", err)
	}

	s = &Store{path: path, writer: writer, reader: reader}
	s.ready.Store(true)
	return s, nil
}

// ErrReadOnly means the database cannot be written: the file, its
// directory or the filesystem is read-only. Starting anyway would serve
// requests and then fail every change.
var ErrReadOnly = errors.New("store: the database cannot be written")

// checkWritable writes the header field user_version back unchanged in a
// transaction it rolls back, so a database SQLite could only open
// read-only is refused at start rather than at the first real write.
func checkWritable(ctx context.Context, writer *sql.DB, path string) error {
	tx, err := writer.BeginTx(ctx, nil) // BEGIN IMMEDIATE (see dsn)
	if err == nil {
		var v int64
		if err = tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v); err == nil {
			_, err = tx.ExecContext(ctx, `PRAGMA user_version = `+strconv.FormatInt(v, 10))
		}
		if rbErr := tx.Rollback(); err == nil && rbErr != nil {
			err = rbErr
		}
	}
	if err != nil {
		return readOnlyError(path, err)
	}
	return nil
}

func readOnlyError(path string, err error) error {
	return fmt.Errorf("%w: %s (check that the file, the data directory and the filesystem are writable by this user): %w",
		ErrReadOnly, path, err)
}

// isReadOnly reports whether err is SQLite's SQLITE_READONLY, in any of
// its extended forms (for example a read-only directory, where the
// write-ahead log cannot be created).
func isReadOnly(err error) bool {
	const sqliteReadOnly = 8
	var coded interface{ Code() int }
	return errors.As(err, &coded) && coded.Code()&0xff == sqliteReadOnly
}

// migrate applies every pending embedded migration on the writer handle.
func migrate(ctx context.Context, db *sql.DB) error {
	p, err := goose.NewProvider(goose.DialectSQLite3, db, Migrations(),
		goose.WithDisableGlobalRegistry(true))
	if err != nil {
		return fmt.Errorf("store: load migrations: %w", err)
	}
	current, target, err := p.GetVersions(ctx)
	if err != nil {
		return fmt.Errorf("store: read migration version: %w", err)
	}
	if current > target {
		return fmt.Errorf("%w (database %d, binary %d)", ErrDatabaseNewer, current, target)
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	return nil
}

// Path returns the database file path.
func (s *Store) Path() string { return s.path }

// Size returns the bytes used by the database file and its write-ahead log.
func (s *Store) Size() (int64, error) {
	var total int64
	for _, p := range []string{s.path, s.path + "-wal"} {
		fi, err := os.Stat(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("store: %w", err)
		}
		total += fi.Size()
	}
	return total, nil
}

// MigrationVersions returns the database's migration version and the
// newest migration this binary carries. Open applies every pending
// migration, so they differ only if the database changed underneath.
func (s *Store) MigrationVersions(ctx context.Context) (current, latest int64, err error) {
	p, err := goose.NewProvider(goose.DialectSQLite3, s.reader, Migrations(),
		goose.WithDisableGlobalRegistry(true))
	if err != nil {
		return 0, 0, fmt.Errorf("store: load migrations: %w", err)
	}
	current, latest, err = p.GetVersions(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("store: read migration version: %w", err)
	}
	return current, latest, nil
}

// Ready returns nil when the store is open and migrated and the database
// answers. /readyz reports through this.
func (s *Store) Ready(ctx context.Context) error {
	if s == nil || !s.ready.Load() {
		return ErrNotReady
	}
	if err := s.reader.PingContext(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrNotReady, err)
	}
	return nil
}

// Close closes both handles. The store is not ready afterwards.
func (s *Store) Close() error {
	if !s.ready.Swap(false) {
		return nil
	}
	return errors.Join(s.reader.Close(), s.writer.Close())
}

// Reader returns the read-only connection pool. Writes through it fail.
func (s *Store) Reader() DBTX { return readHandle{s.reader} }

// readHandle hides the *sql.DB so callers cannot begin their own transactions.
type readHandle struct{ db *sql.DB }

func (r readHandle) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return r.db.ExecContext(ctx, q, args...)
}

func (r readHandle) PrepareContext(ctx context.Context, q string) (*sql.Stmt, error) {
	return r.db.PrepareContext(ctx, q)
}

func (r readHandle) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return r.db.QueryContext(ctx, q, args...)
}

func (r readHandle) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return r.db.QueryRowContext(ctx, q, args...)
}
