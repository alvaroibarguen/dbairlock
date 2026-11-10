package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/changes"
	"github.com/alvaroibarguen/dbairlock/internal/platform/logx"
	"github.com/alvaroibarguen/dbairlock/internal/secrets"
	"github.com/alvaroibarguen/dbairlock/internal/settings"
	"github.com/alvaroibarguen/dbairlock/internal/store"
	"github.com/alvaroibarguen/dbairlock/internal/targets"
)

// Fault tests (phase 11, robustness): what dbairlock does when a target
// hangs or loses a reply, when the data directory fills up or turns
// read-only, and when the master key goes missing. Unreachable targets and
// a connection lost while a statement waits are in TestChangeOutage.

// waitFor polls cond in real time until it holds or the deadline passes.
func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d) //nolint:forbidigo // real time for a real wait
	for !cond() {
		if time.Now().After(deadline) { //nolint:forbidigo // real time for a real wait
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (e *chgEnv) setLimits(values map[string]any) {
	e.t.Helper()
	if err := e.a.settings.Update(e.ctx(e.ada), settings.Changes{Values: values}); err != nil {
		e.t.Fatal(err)
	}
}

func TestFaultSlowTarget(t *testing.T) {
	forEachTarget(t, func(t *testing.T, srv targetServer) {
		t.Run("a target that stops answering before the transaction defers the change", func(t *testing.T) {
			e := newChgEnv(t, srv, targets.PolicyPatch{})
			c := e.submit(e.ada, "UPDATE users SET name = 'Zed' WHERE id = 1")
			e.mustApprove(e.bob, c)
			e.px.setStalled(true)
			start := time.Now() //nolint:forbidigo // measures a real timeout
			e.run()
			if d := time.Since(start); d > 30*time.Second { //nolint:forbidigo // measures a real timeout
				t.Errorf("a hung target held the executor for %s", d)
			}
			got := e.expectStatus(c, changes.StatusApproved)
			if got.Attempts != 1 || got.Executions[0].Outcome != changes.OutcomeNotStarted {
				t.Fatalf("after a hung attempt: attempts %d, execution %+v", got.Attempts, got.Executions[0])
			}
			e.px.setStalled(false)
			if e.name(1) != "Ann" {
				t.Fatal("the hung attempt changed the target")
			}
			e.clock.Advance(30 * time.Second)
			e.run()
			e.expectStatus(c, changes.StatusExecuted)
			if e.name(1) != "Zed" {
				t.Error("not executed after the target recovered")
			}
		})

		t.Run("a target that stops answering inside the transaction fails the change for good", func(t *testing.T) {
			e := newChgEnv(t, srv, targets.PolicyPatch{})
			e.setLimits(map[string]any{"limits.statement_timeout_ms": 2000, "limits.lock_timeout_ms": 30000})
			c := e.submit(e.ada, "UPDATE users SET name = 'Zed' WHERE id = 1")
			release := e.db.lock(t, "SELECT name FROM users WHERE id = 1 FOR UPDATE")
			e.mustApprove(e.bob, c)
			done := make(chan struct{})
			go func() {
				e.run()
				close(done)
			}()
			waitFor(t, "the change to start executing", 20*time.Second, func() bool {
				return e.status(c).Status == changes.StatusExecuting
			})
			time.Sleep(500 * time.Millisecond) // waiting for the row lock inside the transaction
			e.px.setStalled(true)
			release()
			select {
			case <-done:
			case <-time.After(60 * time.Second):
				t.Fatal("a hung target held the executor for a minute")
			}
			got := e.expectStatus(c, changes.StatusFailed)
			if got.StatusCode != changes.FailTimeout && got.StatusCode != changes.FailError {
				t.Errorf("failure = %s %q", got.StatusCode, got.StatusMessage)
			}
			e.px.setStalled(false)
			e.clock.Advance(time.Hour)
			e.run()
			e.expectStatus(c, changes.StatusFailed)
			if n := e.countAction(c, audit.ExecutionStarted); n != 1 {
				t.Errorf("started %d times", n)
			}
			// The server rolls back once it notices the client is gone.
			waitFor(t, "the target to roll back", 20*time.Second, func() bool { return e.name(1) == "Ann" })
		})

		t.Run("a statement slower than the lock timeout fails the change for good", func(t *testing.T) {
			e := newChgEnv(t, srv, targets.PolicyPatch{})
			e.setLimits(map[string]any{"limits.lock_timeout_ms": 1000})
			c := e.submit(e.ada, "UPDATE users SET name = 'Zed' WHERE id = 1")
			release := e.db.lock(t, "SELECT name FROM users WHERE id = 1 FOR UPDATE")
			e.mustApprove(e.bob, c)
			e.run()
			got := e.expectStatus(c, changes.StatusFailed)
			if got.StatusCode != changes.FailTimeout {
				t.Errorf("failure = %s %q", got.StatusCode, got.StatusMessage)
			}
			release()
			e.clock.Advance(time.Hour)
			e.run()
			e.expectStatus(c, changes.StatusFailed)
			if e.name(1) != "Ann" {
				t.Errorf("name = %s", e.name(1))
			}
		})
	})
}

// commitPattern is how each driver's COMMIT appears on the wire: pgx sends
// the query text NUL-terminated, go-sql-driver/mysql a COM_QUERY (0x03)
// packet.
var commitPattern = map[string][]byte{
	"postgres": []byte("commit\x00"),
	"mysql":    []byte("\x03COMMIT"),
}

func TestFaultCommitOutcomeUnknown(t *testing.T) {
	forEachTarget(t, func(t *testing.T, srv targetServer) {
		// The COMMIT reaches the target and commits, but the reply is lost.
		// dbairlock cannot know the outcome: the change must be failed with
		// a message saying so, and never run again.
		e := newChgEnv(t, srv, targets.PolicyPatch{})
		c := e.submit(e.ada, "UPDATE users SET name = 'Zed' WHERE id = 1")
		e.mustApprove(e.bob, c)
		e.px.loseReplyAfter(commitPattern[e.db.connector()])
		e.run()
		e.px.resetLoss()
		got := e.expectStatus(c, changes.StatusFailed)
		if got.StatusCode != changes.FailError || !strings.Contains(got.StatusMessage, "outcome unknown") {
			t.Errorf("failure = %s %q", got.StatusCode, got.StatusMessage)
		}
		if e.name(1) != "Zed" {
			t.Errorf("the commit did not reach the target (name %s); the test did not exercise a lost reply", e.name(1))
		}
		e.clock.Advance(time.Hour)
		e.run()
		e.expectStatus(c, changes.StatusFailed)
		if n := e.countAction(c, audit.ExecutionStarted); n != 1 {
			t.Errorf("started %d times", n)
		}
	})
}

// fillStore makes every write to the metadata database fail, as a full
// disk does, until free is called.
//
// A real full filesystem needs a size-limited mount (root), and capping
// SQLite's max_page_count is not deterministic: small rows still fit in
// the free space of existing pages. So every table gets triggers that
// abort every INSERT, UPDATE and DELETE with SQLite's own disk-full text.
// The application sees what it sees on a full disk: the statement fails
// and the transaction is rolled back. That a real SQLITE_FULL also rolls
// back cleanly is checked by TestStoreFullRollsBack in internal/store.
func fillStore(t *testing.T, st *store.Store) (free func()) {
	t.Helper()
	ctx := context.Background()
	var drops []string
	if err := st.Tx(ctx, func(tx *store.Tx) error {
		tables, err := tableNames(ctx, tx)
		if err != nil {
			return err
		}
		for _, tbl := range tables {
			for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
				name := "fault_full_" + strings.ToLower(op) + "_" + tbl
				q := `CREATE TRIGGER ` + name + ` BEFORE ` + op + ` ON ` + tbl +
					` BEGIN SELECT RAISE(ABORT, 'database or disk is full'); END`
				if _, err := tx.ExecContext(ctx, q); err != nil {
					return err
				}
				drops = append(drops, `DROP TRIGGER `+name)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		if err := st.Tx(ctx, func(tx *store.Tx) error {
			for _, q := range drops {
				if _, err := tx.ExecContext(ctx, q); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func tableNames(ctx context.Context, tx *store.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// makeReadOnly turns the store's writer connection read-only for good (a
// read-only connection cannot begin the transaction that would undo it).
func makeReadOnly(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	// The writer is a single long-lived connection, so the pragma sticks.
	if err := st.Tx(ctx, func(tx *store.Tx) error {
		_, err := tx.ExecContext(ctx, `PRAGMA query_only = ON`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestFaultStoreFull(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}
	srv := postgresTargets(t)

	t.Run("writes fail cleanly and leave nothing behind", func(t *testing.T) {
		e := newChgEnv(t, srv, targets.PolicyPatch{})
		c := e.submit(e.ada, "UPDATE users SET name = 'Zed' WHERE id = 1")
		events := len(e.actions(c))
		free := fillStore(t, e.a.store)
		// Submitting, reviewing and changing settings all fail; nothing
		// is half-written.
		for i := 0; i < 20; i++ {
			if _, err := e.submitOn(e.ada, e.conn, "UPDATE users SET name = 'Y"+itoa(i)+"' WHERE id = 2"); err == nil {
				t.Fatal("a submission succeeded on a full store")
			}
		}
		if _, err := e.approve(e.bob, c); err == nil {
			t.Fatal("an approval succeeded on a full store")
		}
		if err := e.a.settings.Update(e.ctx(e.ada), settings.Changes{Values: map[string]any{"instance.name": "full"}}); err == nil {
			t.Fatal("a settings change succeeded on a full store")
		}
		if got := e.status(c); got.Status != changes.StatusPendingReview || got.Approvals != 0 || len(got.Reviews) != 0 || len(e.actions(c)) != events {
			t.Fatalf("a failed write left state behind: %s, %d approvals, %d events", got.Status, got.Approvals, len(e.actions(c)))
		}
		if settings.Get(e.a.settings, settings.InstanceName) == "full" {
			t.Fatal("a failed settings change is visible")
		}
		var n int
		if err := e.a.store.Reader().QueryRowContext(context.Background(), `SELECT count(*) FROM change_requests`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("changes stored: %d %v", n, err)
		}
		// Reading still works, and so does everything once space is back.
		if err := e.a.store.Ready(context.Background()); err != nil {
			t.Fatalf("not ready while full: %v", err)
		}
		free()
		e.mustApprove(e.bob, c)
		e.run()
		e.expectStatus(c, changes.StatusExecuted)
	})

	t.Run("an outcome that cannot be recorded is retried, never the change", func(t *testing.T) {
		e := newChgEnv(t, srv, targets.PolicyPatch{})
		e.setLimits(map[string]any{"limits.lock_timeout_ms": 30000})
		c := e.submit(e.ada, "UPDATE users SET name = 'Zed' WHERE id = 1")
		release := e.db.lock(t, "SELECT name FROM users WHERE id = 1 FOR UPDATE")
		e.mustApprove(e.bob, c)
		done := make(chan struct{})
		go func() {
			e.run()
			close(done)
		}()
		waitFor(t, "the change to start executing", 20*time.Second, func() bool {
			return e.status(c).Status == changes.StatusExecuting
		})
		// The disk fills up while the change waits for its row lock; the
		// target then commits, and recording that fails.
		free := fillStore(t, e.a.store)
		release()
		waitFor(t, "the target to commit", 20*time.Second, func() bool { return e.name(1) == "Zed" })
		time.Sleep(1500 * time.Millisecond) // at least one failed attempt to record
		if got := e.status(c); got.Status != changes.StatusExecuting {
			t.Fatalf("recorded %s on a full store", got.Status)
		}
		free()
		select {
		case <-done:
		case <-time.After(60 * time.Second):
			t.Fatal("the outcome was not recorded after space came back")
		}
		e.expectStatus(c, changes.StatusExecuted)
		if n := e.countAction(c, audit.ExecutionStarted); n != 1 {
			t.Errorf("started %d times", n)
		}
	})

	t.Run("stopping while the outcome cannot be recorded marks it interrupted", func(t *testing.T) {
		e := newChgEnv(t, srv, targets.PolicyPatch{})
		e.setLimits(map[string]any{"limits.lock_timeout_ms": 30000})
		c := e.submit(e.ada, "UPDATE users SET name = 'Zed' WHERE id = 1")
		release := e.db.lock(t, "SELECT name FROM users WHERE id = 1 FOR UPDATE")
		e.mustApprove(e.bob, c)
		ctx, cancel := context.WithCancel(context.Background())
		stopped := make(chan error, 1)
		go func() { stopped <- e.a.executor.Run(ctx) }()
		waitFor(t, "the change to start executing", 20*time.Second, func() bool {
			return e.status(c).Status == changes.StatusExecuting
		})
		free := fillStore(t, e.a.store)
		release()
		waitFor(t, "the target to commit", 20*time.Second, func() bool { return e.name(1) == "Zed" })
		cancel() // the process is stopped while the disk is still full
		select {
		case err := <-stopped:
			if err != nil {
				t.Errorf("executor: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("the executor did not stop")
		}
		e.expectStatus(c, changes.StatusExecuting)
		free()

		dir := e.a.opts.DataDir
		_ = e.a.Close()
		b, err := New(Options{
			DataDir: dir, Listen: "127.0.0.1:0", Dev: true, Version: "test", Logger: logx.Discard(), Clock: e.clock,
			WebUI: fstest.MapFS{"index.html": {Data: []byte("x")}},
		})
		if err != nil {
			t.Fatal(err)
		}
		e.a = b
		if err := b.executor.Recover(context.Background()); err != nil {
			t.Fatal(err)
		}
		got := e.expectStatus(c, changes.StatusFailed)
		if got.StatusCode != changes.FailInterrupted {
			t.Errorf("after restart: %s %q", got.StatusCode, got.StatusMessage)
		}
		b.executor.RunOnce(context.Background())
		if n := e.countAction(c, audit.ExecutionStarted); n != 1 {
			t.Errorf("started %d times", n)
		}
	})
}

// newStoppedInstance sets up an instance in a fresh data directory (setup
// complete, so a start writes nothing on its own; the Google client secret
// stored, so the database holds a master key check) and stops it.
func newStoppedInstance(t *testing.T) string {
	t.Helper()
	hs := newHarness(t)
	hs.completeSetup()
	if err := hs.app.Close(); err != nil {
		t.Fatal(err)
	}
	return hs.dir
}

func startInstance(dir string) (*App, error) {
	return New(Options{
		DataDir: dir, Listen: "127.0.0.1:0", Version: "test", Logger: logx.Discard(),
		WebUI: fstest.MapFS{"index.html": {Data: []byte("x")}},
	})
}

func TestFaultMasterKeyMissing(t *testing.T) {
	dir := newStoppedInstance(t)
	key := filepath.Join(dir, secrets.KeyFileName)
	if err := os.Remove(key); err != nil {
		t.Fatal(err)
	}
	a, err := startInstance(dir)
	if err == nil {
		_ = a.Close()
		t.Fatal("started without its master key")
	}
	if !errors.Is(err, secrets.ErrKeyMismatch) || !strings.Contains(err.Error(), "restore master.key from your backup") {
		t.Errorf("error = %v", err)
	}
	// It must not have created a new key in its place: that would make the
	// stored secrets unrecoverable once the operator restores the real one.
	if _, err := os.Stat(key); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a new master.key was created: %v", err)
	}

	// An unreadable key file is refused the same way, without a new key.
	dir = newStoppedInstance(t)
	key = filepath.Join(dir, secrets.KeyFileName)
	if err := os.Chmod(key, 0); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() != 0 { // root reads it anyway
		if a, err := startInstance(dir); err == nil {
			_ = a.Close()
			t.Fatal("started with an unreadable master key")
		} else if !strings.Contains(err.Error(), "master.key") {
			t.Errorf("error does not name the key file: %v", err)
		}
	}
}

func TestFaultReadOnlyDataDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions do not apply to root")
	}
	t.Run("read-only database file", func(t *testing.T) {
		dir := newStoppedInstance(t)
		db := filepath.Join(dir, store.FileName)
		if err := os.Chmod(db, 0o400); err != nil {
			t.Fatal(err)
		}
		// The file belongs to this user, so the store restores its 0600
		// mode before opening it. It once opened it read-only instead: the
		// instance then started, reported ready and failed every write.
		a, err := startInstance(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = a.Close() }()
		ctx := audit.WithActor(context.Background(), audit.SystemActor)
		if err := a.settings.Update(ctx, settings.Changes{Values: map[string]any{"instance.name": "rw"}}); err != nil {
			t.Fatalf("started on a read-only database file but cannot write: %v", err)
		}
	})

	t.Run("read-only data directory", func(t *testing.T) {
		dir := newStoppedInstance(t)
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		// SQLite cannot create its write-ahead log: refuse to start, and
		// say why.
		a, err := startInstance(dir)
		if err == nil {
			_ = a.Close()
			t.Fatal("started on a read-only data directory")
		}
		if !errors.Is(err, store.ErrReadOnly) || !strings.Contains(err.Error(), "writable") {
			t.Errorf("error = %v", err)
		}
	})

	t.Run("database turns read-only while running", func(t *testing.T) {
		dir := newStoppedInstance(t)
		a, err := startInstance(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = a.Close() }()
		// An open file descriptor stays writable after a chmod, so make
		// the writer connection itself read-only, as a remount would.
		ctx := audit.WithActor(context.Background(), audit.SystemActor)
		makeReadOnly(t, a.store)
		before := len(auditRows(t, a))
		if err := a.settings.Update(ctx, settings.Changes{Values: map[string]any{"instance.name": "ro"}}); err == nil {
			t.Fatal("a write succeeded on a read-only database")
		}
		if settings.Get(a.settings, settings.InstanceName) == "ro" || len(auditRows(t, a)) != before {
			t.Fatal("a failed write left state behind")
		}
		res, err := audit.NewVerifier(a.store.Reader()).Verify(context.Background())
		if err != nil || !res.OK() {
			t.Fatalf("audit verify: %v %+v", err, res.Problem)
		}
	})
}

func auditRows(t *testing.T, a *App) []int64 {
	t.Helper()
	rows, err := a.store.Reader().QueryContext(context.Background(), `SELECT seq FROM audit_events`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []int64
	for rows.Next() {
		var s int64
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}
