package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func openTest(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func rawOpen(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func dbVersion(t *testing.T, db DBTX) int64 {
	t.Helper()
	var v int64
	if err := db.QueryRowContext(context.Background(), "SELECT max(version_id) FROM goose_db_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestOpenCreatesAndMigratesThenNoOp(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "data")
	s, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("data dir mode = %v, want 0700", fi.Mode().Perm())
	}
	fi, err = os.Stat(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("db file mode = %v, want 0600", fi.Mode().Perm())
	}
	v1 := dbVersion(t, s.Reader())
	if v1 < 1 {
		t.Fatalf("version = %d after migrate", v1)
	}
	var rows1 int
	if err := s.Reader().QueryRowContext(context.Background(), "SELECT count(*) FROM goose_db_version").Scan(&rows1); err != nil {
		t.Fatal(err)
	}
	if err := s.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Ready(context.Background()); !errors.Is(err, ErrNotReady) {
		t.Fatalf("Ready after Close = %v", err)
	}

	// Second start: nothing to apply, nothing recorded.
	s2 := openTest(t, dir)
	var rows2 int
	if err := s2.Reader().QueryRowContext(context.Background(), "SELECT count(*) FROM goose_db_version").Scan(&rows2); err != nil {
		t.Fatal(err)
	}
	if v2 := dbVersion(t, s2.Reader()); v2 != v1 || rows2 != rows1 {
		t.Fatalf("second open changed migrations: version %d->%d rows %d->%d", v1, v2, rows1, rows2)
	}
}

func TestEveryTableIsStrict(t *testing.T) {
	s := openTest(t, t.TempDir())
	rows, err := s.Reader().QueryContext(context.Background(),
		`SELECT name, strict FROM pragma_table_list WHERE schema = 'main' AND type = 'table'
		 AND name NOT LIKE 'sqlite_%' AND name <> 'goose_db_version'`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	n := 0
	for rows.Next() {
		var name string
		var strict int
		if err := rows.Scan(&name, &strict); err != nil {
			t.Fatal(err)
		}
		n++
		if strict != 1 {
			t.Errorf("table %s is not STRICT", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no tables found")
	}
}

func TestRefusesNewerDatabase(t *testing.T) {
	dir := t.TempDir()
	s := openTest(t, dir)
	if err := s.Tx(context.Background(), func(tx *Tx) error {
		_, err := tx.ExecContext(context.Background(),
			"INSERT INTO goose_db_version (version_id, is_applied) VALUES (999999, 1)")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	_, err := Open(context.Background(), dir)
	if !errors.Is(err, ErrDatabaseNewer) {
		t.Fatalf("Open = %v, want ErrDatabaseNewer", err)
	}
}

func TestOpenExistingRequiresFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := OpenExisting(context.Background(), dir); !errors.Is(err, ErrNotFound) {
		t.Fatalf("OpenExisting on empty dir = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("OpenExisting created a database")
	}
	openTest(t, dir)
	s, err := OpenExisting(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
}

func TestPragmas(t *testing.T) {
	s := openTest(t, t.TempDir())
	ctx := context.Background()
	check := func(name string, db DBTX, pragma, want string) {
		t.Helper()
		var got string
		if err := db.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if !strings.EqualFold(got, want) {
			t.Errorf("%s: PRAGMA %s = %q, want %q", name, pragma, got, want)
		}
	}
	err := s.Tx(ctx, func(tx *Tx) error {
		check("writer", tx, "journal_mode", "wal")
		check("writer", tx, "synchronous", "2") // FULL
		check("writer", tx, "foreign_keys", "1")
		check("writer", tx, "busy_timeout", "5000")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	r := s.Reader()
	check("reader", r, "journal_mode", "wal")
	check("reader", r, "synchronous", "2")
	check("reader", r, "foreign_keys", "1")
	check("reader", r, "busy_timeout", "5000")
	check("reader", r, "query_only", "1")
	if _, err := r.ExecContext(ctx, "CREATE TABLE nope (x INTEGER) STRICT"); err == nil {
		t.Fatal("reader accepted a write")
	}
}

func TestTxCommitRollbackPanic(t *testing.T) {
	s := openTest(t, t.TempDir())
	ctx := context.Background()
	if err := s.Tx(ctx, func(tx *Tx) error {
		_, err := tx.ExecContext(ctx, "CREATE TABLE t (v INTEGER NOT NULL) STRICT")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	insert := func(tx *Tx, v int) {
		if _, err := tx.ExecContext(ctx, "INSERT INTO t (v) VALUES (?)", v); err != nil {
			t.Fatal(err)
		}
	}
	count := func() int {
		var n int
		if err := s.Reader().QueryRowContext(ctx, "SELECT count(*) FROM t").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if err := s.Tx(ctx, func(tx *Tx) error { insert(tx, 1); return nil }); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	if err := s.Tx(ctx, func(tx *Tx) error { insert(tx, 2); return boom }); !errors.Is(err, boom) {
		t.Fatalf("Tx = %v, want boom", err)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic was swallowed")
			}
		}()
		_ = s.Tx(ctx, func(tx *Tx) error { insert(tx, 3); panic("kaboom") })
	}()
	if n := count(); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
	// The writer connection is usable after a panic.
	if err := s.Tx(ctx, func(tx *Tx) error { insert(tx, 4); return nil }); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 2 {
		t.Fatalf("rows = %d, want 2", n)
	}
}

// TestTxBeginsImmediate proves the write lock is taken at BEGIN: while a
// store transaction that has not written anything is open, another connection
// cannot start a write transaction.
func TestTxBeginsImmediate(t *testing.T) {
	s := openTest(t, t.TempDir())
	ctx := context.Background()
	raw := rawOpen(t, s.Path())
	err := s.Tx(ctx, func(*Tx) error {
		conn, err := raw.Conn(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close() }()
		if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err == nil {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
			t.Error("second writer acquired the lock; store.Tx did not BEGIN IMMEDIATE")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestStoreFullRollsBack checks what a full disk does to a transaction:
// SQLITE_FULL fails it as a whole, nothing of it remains, and the store
// works again once there is room. max_page_count stands in for the disk.
func TestStoreFullRollsBack(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, t.TempDir())
	exec := func(tx *Tx, q string) error {
		_, err := tx.ExecContext(ctx, q)
		return err
	}
	if err := s.Tx(ctx, func(tx *Tx) error {
		if err := exec(tx, `CREATE TABLE filler (b BLOB)`); err != nil {
			return err
		}
		var pages int64
		if err := tx.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pages); err != nil {
			return err
		}
		return exec(tx, "PRAGMA max_page_count = "+strconv.FormatInt(pages+20, 10))
	}); err != nil {
		t.Fatal(err)
	}
	err := s.Tx(ctx, func(tx *Tx) error {
		for range 1000 {
			if err := exec(tx, `INSERT INTO filler (b) VALUES (zeroblob(4096))`); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "full") {
		t.Fatalf("filling the database: %v", err)
	}
	var n int
	if err := s.Reader().QueryRowContext(ctx, `SELECT count(*) FROM filler`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows left by the failed transaction: %d %v", n, err)
	}
	if err := s.Tx(ctx, func(tx *Tx) error {
		if err := exec(tx, `PRAGMA max_page_count = 4294967294`); err != nil {
			return err
		}
		return exec(tx, `INSERT INTO filler (b) VALUES (zeroblob(4096))`)
	}); err != nil {
		t.Fatalf("write once there is room: %v", err)
	}
}

func TestOpenRefusesReadOnlyDatabase(t *testing.T) {
	dir := t.TempDir()
	s := openTest(t, dir)
	path := s.Path()
	_ = s.Close()

	// SQLite falls back to a read-only connection when it cannot open the
	// file for writing; mode=ro forces that here without needing root.
	ro, err := sql.Open("sqlite", dsn(path, false)+"&mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ro.Close() }()
	if err := checkWritable(context.Background(), ro, path); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("read-only connection: %v", err)
	}

	rw, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rw.Close() }()
	if err := checkWritable(context.Background(), rw, path); err != nil {
		t.Fatalf("writable connection: %v", err)
	}

	// A file left read-only is restored to 0600 and opened for writing.
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	s = openTest(t, dir)
	if err := s.Tx(context.Background(), func(tx *Tx) error {
		_, err := tx.ExecContext(context.Background(), `PRAGMA user_version = 0`)
		return err
	}); err != nil {
		t.Fatalf("write after reopening a 0400 file: %v", err)
	}
}
