package audit_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/platform/clock"
	"github.com/alvaroibarguen/dbairlock/internal/platform/ids"
	"github.com/alvaroibarguen/dbairlock/internal/store"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

type env struct {
	store *store.Store
	clock *clock.Fake
	rec   *audit.Recorder
}

func newEnv(t *testing.T) *env {
	t.Helper()
	s, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	c := clock.NewFake(t0)
	return &env{store: s, clock: c, rec: audit.NewRecorder(c, &ids.Sequential{Prefix: "evt"})}
}

var alice = audit.Actor{UserID: "u-alice", Email: "alice@example.com", Via: audit.ViaWeb, IP: "10.0.0.1"}

func actorCtx() context.Context { return audit.WithActor(context.Background(), alice) }

func (e *env) record(ctx context.Context, t *testing.T, ev audit.Event) {
	t.Helper()
	if err := e.store.Tx(ctx, func(tx *store.Tx) error { return e.rec.Record(ctx, tx, ev) }); err != nil {
		t.Fatal(err)
	}
}

func (e *env) verify(t *testing.T) audit.Result {
	t.Helper()
	res, err := audit.NewVerifier(e.store.Reader()).Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (e *env) count(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.store.Reader().QueryRowContext(context.Background(), "SELECT count(*) FROM audit_events").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// raw opens the database file directly, bypassing the store, as an attacker
// with the file would.
func raw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func (e *env) seed(t *testing.T, n int) {
	t.Helper()
	for i := range n {
		e.clock.Advance(time.Second)
		e.record(actorCtx(), t, audit.Event{Action: audit.ChangeSubmitted, SubjectType: "change", SubjectID: fmt.Sprint("c-", i), Payload: map[string]any{"i": i}})
	}
}

func TestEmptyChainVerifies(t *testing.T) {
	e := newEnv(t)
	if res := e.verify(t); !res.OK() || res.Checked != 0 {
		t.Fatalf("empty: %+v", res)
	}
}

func TestUpdateAndDeleteAreRejected(t *testing.T) {
	e := newEnv(t)
	e.seed(t, 3)
	ctx := context.Background()
	for _, q := range []string{
		"UPDATE audit_events SET payload = '{}' WHERE seq = 2",
		"UPDATE audit_events SET action = 'x.y'",
		"DELETE FROM audit_events WHERE seq = 2",
		"DELETE FROM audit_events",
	} {
		err := e.store.Tx(ctx, func(tx *store.Tx) error {
			_, err := tx.ExecContext(ctx, q)
			return err
		})
		if err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Errorf("%s: err = %v, want append-only abort", q, err)
		}
	}
	// The same holds for a raw connection while the triggers exist.
	db := raw(t, e.store.Path())
	if _, err := db.ExecContext(ctx, "DELETE FROM audit_events"); err == nil {
		t.Error("raw DELETE succeeded")
	}
	if n := e.count(t); n != 3 {
		t.Fatalf("rows = %d, want 3", n)
	}
	if res := e.verify(t); !res.OK() || res.Checked != 3 {
		t.Fatalf("verify: %+v", res)
	}
}

func TestConcurrentRecordsVerify(t *testing.T) {
	e := newEnv(t)
	const workers, each = 20, 50
	var wg sync.WaitGroup
	errs := make(chan error, workers*each)
	for w := range workers {
		wg.Go(func() {
			ctx := audit.WithActor(context.Background(), audit.Actor{UserID: fmt.Sprint("u-", w), Via: audit.ViaMCP, APITokenID: "tok"})
			for i := range each {
				err := e.store.Tx(ctx, func(tx *store.Tx) error {
					return e.rec.Record(ctx, tx, audit.Event{
						Action: audit.SettingChanged, SubjectType: "setting", SubjectID: "s",
						Payload: map[string]int{"worker": w, "i": i},
					})
				})
				if err != nil {
					errs <- err
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	res := e.verify(t)
	if !res.OK() || res.Checked != workers*each || res.LastSeq != workers*each {
		t.Fatalf("verify: %+v (problem %v)", res, res.Problem)
	}
}

func TestTamperedRowIsReported(t *testing.T) {
	e := newEnv(t)
	e.seed(t, 10)
	ctx := context.Background()
	db := raw(t, e.store.Path())
	if _, err := db.ExecContext(ctx, "DROP TRIGGER audit_events_no_update"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE audit_events SET payload = '{"i":999}' WHERE seq = 7`); err != nil {
		t.Fatal(err)
	}
	res := e.verify(t)
	if res.OK() || res.Problem.Seq != 7 || !strings.Contains(res.Problem.Reason, "hash mismatch") {
		t.Fatalf("verify: %+v %v", res, res.Problem)
	}
	if res.Checked != 6 {
		t.Fatalf("checked = %d, want 6", res.Checked)
	}
}

func TestEditedHashIsReported(t *testing.T) {
	e := newEnv(t)
	e.seed(t, 5)
	ctx := context.Background()
	db := raw(t, e.store.Path())
	if _, err := db.ExecContext(ctx, "DROP TRIGGER audit_events_no_update"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE audit_events SET hash = randomblob(32) WHERE seq = 3`); err != nil {
		t.Fatal(err)
	}
	res := e.verify(t)
	if res.OK() || res.Problem.Seq != 3 {
		t.Fatalf("verify: %v", res.Problem)
	}
}

func TestDeletedRowIsReported(t *testing.T) {
	e := newEnv(t)
	e.seed(t, 10)
	ctx := context.Background()
	db := raw(t, e.store.Path())
	if _, err := db.ExecContext(ctx, "DROP TRIGGER audit_events_no_delete"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM audit_events WHERE seq = 4"); err != nil {
		t.Fatal(err)
	}
	res := e.verify(t)
	if res.OK() || res.Problem.Seq != 4 || !strings.Contains(res.Problem.Reason, "missing") {
		t.Fatalf("verify: %+v %v", res, res.Problem)
	}
}

func TestDeletedTailIsReported(t *testing.T) {
	e := newEnv(t)
	e.seed(t, 10)
	ctx := context.Background()
	db := raw(t, e.store.Path())
	if _, err := db.ExecContext(ctx, "DROP TRIGGER audit_events_no_delete"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM audit_events WHERE seq > 8"); err != nil {
		t.Fatal(err)
	}
	res := e.verify(t)
	if res.OK() || res.Problem.Seq != 9 {
		t.Fatalf("verify: %+v %v", res, res.Problem)
	}
	// Recording after the deletion does not hide it: the new event continues
	// from seq 11, leaving the gap visible.
	e.seed(t, 1)
	res = e.verify(t)
	if res.OK() || res.Problem.Seq != 9 {
		t.Fatalf("verify after append: %v", res.Problem)
	}
}

func TestDeletedFirstRowIsReported(t *testing.T) {
	e := newEnv(t)
	e.seed(t, 3)
	ctx := context.Background()
	db := raw(t, e.store.Path())
	if _, err := db.ExecContext(ctx, "DROP TRIGGER audit_events_no_delete"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM audit_events WHERE seq = 1"); err != nil {
		t.Fatal(err)
	}
	if res := e.verify(t); res.OK() || res.Problem.Seq != 1 {
		t.Fatalf("verify: %v", res.Problem)
	}
}

// createWidgets makes a stand-in business table for the transaction tests.
func createWidgets(t *testing.T, e *env) {
	t.Helper()
	ctx := context.Background()
	if err := e.store.Tx(ctx, func(tx *store.Tx) error {
		_, err := tx.ExecContext(ctx, "CREATE TABLE widgets (id TEXT PRIMARY KEY, name TEXT NOT NULL CHECK (name <> '')) STRICT")
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func countWidgets(t *testing.T, e *env) int {
	t.Helper()
	var n int
	if err := e.store.Reader().QueryRowContext(context.Background(), "SELECT count(*) FROM widgets").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestFailedBusinessWriteLeavesNoAuditRow(t *testing.T) {
	e := newEnv(t)
	createWidgets(t, e)
	ctx := actorCtx()
	err := e.store.Tx(ctx, func(tx *store.Tx) error {
		if err := e.rec.Record(ctx, tx, audit.Event{Action: "widget.created", SubjectType: "widget", SubjectID: "w1"}); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO widgets (id, name) VALUES ('w1', '')") // violates CHECK
		return err
	})
	if err == nil {
		t.Fatal("business write succeeded")
	}
	if n := e.count(t); n != 0 {
		t.Fatalf("audit rows = %d, want 0", n)
	}
}

func TestFailedAuditWriteRollsBackBusinessWrite(t *testing.T) {
	e := newEnv(t)
	createWidgets(t, e)
	cases := map[string]struct {
		ctx context.Context
		ev  audit.Event
	}{
		"no actor":       {context.Background(), audit.Event{Action: "widget.created"}},
		"bad action":     {actorCtx(), audit.Event{Action: "Widget Created"}},
		"bad payload":    {actorCtx(), audit.Event{Action: "widget.created", Payload: func() {}}},
		"array payload":  {actorCtx(), audit.Event{Action: "widget.created", Payload: []string{"x"}}},
		"web actor anon": {audit.WithActor(context.Background(), audit.Actor{Via: audit.ViaWeb}), audit.Event{Action: "widget.created"}},
		"bad channel":    {audit.WithActor(context.Background(), audit.Actor{UserID: "u", Via: "email"}), audit.Event{Action: "widget.created"}},
	}
	for name, tc := range cases {
		err := e.store.Tx(tc.ctx, func(tx *store.Tx) error {
			if _, err := tx.ExecContext(tc.ctx, "INSERT INTO widgets (id, name) VALUES ('w1', 'gear')"); err != nil {
				return err
			}
			return e.rec.Record(tc.ctx, tx, tc.ev)
		})
		if err == nil {
			t.Errorf("%s: audit write succeeded", name)
		}
		if name == "no actor" && !errors.Is(err, audit.ErrNoActor) {
			t.Errorf("no actor: err = %v", err)
		}
		if n := countWidgets(t, e); n != 0 {
			t.Fatalf("%s: widgets = %d, business write was not rolled back", name, n)
		}
	}
	if n := e.count(t); n != 0 {
		t.Fatalf("audit rows = %d", n)
	}
}

func TestSuccessfulWriteCommitsBoth(t *testing.T) {
	e := newEnv(t)
	createWidgets(t, e)
	ctx := audit.WithActor(context.Background(), audit.SystemActor)
	err := e.store.Tx(ctx, func(tx *store.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO widgets (id, name) VALUES ('w1', 'gear')"); err != nil {
			return err
		}
		return e.rec.Record(ctx, tx, audit.Event{Action: "widget.created", SubjectType: "widget", SubjectID: "w1"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if countWidgets(t, e) != 1 || e.count(t) != 1 {
		t.Fatal("expected one widget and one audit row")
	}
}

func TestRecordStoresInjectedTimeAndID(t *testing.T) {
	e := newEnv(t)
	e.record(actorCtx(), t, audit.Event{Action: audit.SecretSet, SubjectType: "secret", SubjectID: "smtp", Payload: map[string]any{"b": 1, "a": "<x>"}})
	page, err := audit.NewReader(e.store.Reader()).List(context.Background(), audit.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 {
		t.Fatalf("entries = %d", len(page.Entries))
	}
	got := page.Entries[0]
	if got.ID != "evt-000001" || !got.OccurredAt.Equal(t0) || got.Seq != 1 || got.Actor != alice ||
		got.Action != audit.SecretSet || got.SubjectID != "smtp" || string(got.Payload) != `{"a":"<x>","b":1}` {
		t.Fatalf("entry = %+v payload %s", got, got.Payload)
	}
}

func TestRecordTruncatesLongStrings(t *testing.T) {
	e := newEnv(t)
	e.record(actorCtx(), t, audit.Event{Action: audit.SettingChanged, Payload: map[string]string{"sql": strings.Repeat("x", 20000)}})
	page, err := audit.NewReader(e.store.Reader()).List(context.Background(), audit.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]string
	if err := json.Unmarshal(page.Entries[0].Payload, &p); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(p["sql"], "...[truncated: 20000 bytes]") || len(p["sql"]) > audit.MaxPayloadString+64 {
		t.Fatalf("not truncated: len %d", len(p["sql"]))
	}
	if !e.verify(t).OK() {
		t.Fatal("chain broken")
	}
}

func TestReaderFiltersAndPaging(t *testing.T) {
	e := newEnv(t)
	bob := audit.Actor{UserID: "u-bob", Via: audit.ViaMCP, APITokenID: "tok-1"}
	for i := range 30 {
		e.clock.Advance(time.Minute)
		ctx := actorCtx()
		action := audit.ChangeSubmitted
		if i%3 == 0 {
			ctx = audit.WithActor(context.Background(), bob)
			action = audit.SettingChanged
		}
		e.record(ctx, t, audit.Event{Action: action, SubjectType: "change", SubjectID: fmt.Sprint("c-", i%5)})
	}
	r := audit.NewReader(e.store.Reader())
	ctx := context.Background()

	// Paging walks every event once, newest first.
	var seqs []int64
	f := audit.Filter{Limit: 7}
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("paging did not terminate")
		}
		p, err := r.List(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		for _, en := range p.Entries {
			seqs = append(seqs, en.Seq)
		}
		if p.NextBeforeSeq == 0 {
			break
		}
		f.BeforeSeq = p.NextBeforeSeq
	}
	if len(seqs) != 30 || seqs[0] != 30 || seqs[29] != 1 {
		t.Fatalf("seqs = %v", seqs)
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] != seqs[i-1]-1 {
			t.Fatalf("not strictly descending: %v", seqs)
		}
	}

	check := func(name string, f audit.Filter, want int, ok func(audit.Entry) bool) {
		t.Helper()
		f.Limit = audit.MaxPageSize
		p, err := r.List(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Entries) != want {
			t.Errorf("%s: %d entries, want %d", name, len(p.Entries), want)
		}
		for _, en := range p.Entries {
			if !ok(en) {
				t.Errorf("%s: unexpected entry %+v", name, en)
			}
		}
	}
	check("actor", audit.Filter{ActorUserID: "u-bob"}, 10, func(en audit.Entry) bool { return en.Actor.UserID == "u-bob" })
	check("action prefix", audit.Filter{ActionPrefix: "change."}, 20, func(en audit.Entry) bool { return en.Action == audit.ChangeSubmitted })
	check("prefix is literal", audit.Filter{ActionPrefix: "%"}, 0, nil)
	check("subject", audit.Filter{SubjectType: "change", SubjectID: "c-1"}, 6, func(en audit.Entry) bool { return en.SubjectID == "c-1" })
	from, to := t0.Add(5*time.Minute), t0.Add(10*time.Minute) // events at minutes 5..9
	check("time range", audit.Filter{From: from, To: to}, 5, func(en audit.Entry) bool {
		return !en.OccurredAt.Before(from) && en.OccurredAt.Before(to)
	})
	check("combined", audit.Filter{ActorUserID: "u-bob", ActionPrefix: "setting.", SubjectID: "c-0"}, 2, func(en audit.Entry) bool {
		return en.Actor.UserID == "u-bob" && en.SubjectID == "c-0"
	})
}

// fakeSecret stands in for secrets.Value, which this package cannot import.
type fakeSecret struct{ b []byte }

func (fakeSecret) SecretMarker() {}

type ptrSecret struct{}

func (*ptrSecret) SecretMarker() {}

type keySecret string

func (keySecret) SecretMarker() {}

func TestRecordRejectsSecretsAnywhereInPayload(t *testing.T) {
	e := newEnv(t)
	s := fakeSecret{b: []byte("hunter2")}
	type deep struct {
		hidden any
	}
	payloads := map[string]any{
		"top level":        s,
		"pointer":          &s,
		"map value":        map[string]any{"password": s},
		"map key":          map[keySecret]int{"k": 1},
		"slice":            []any{"x", s},
		"array":            [1]fakeSecret{s},
		"nested struct":    struct{ A struct{ B []map[string]any } }{A: struct{ B []map[string]any }{B: []map[string]any{{"k": &s}}}},
		"unexported field": deep{hidden: s},
		"pointer receiver": map[string]ptrSecret{"k": {}},
	}
	for name, p := range payloads {
		ctx := actorCtx()
		err := e.store.Tx(ctx, func(tx *store.Tx) error {
			return e.rec.Record(ctx, tx, audit.Event{Action: audit.SettingChanged, Payload: p})
		})
		if !errors.Is(err, audit.ErrSecretInPayload) {
			t.Errorf("%s: err = %v", name, err)
		}
		if err != nil && strings.Contains(err.Error(), "hunter2") {
			t.Errorf("%s: error leaks the value: %v", name, err)
		}
	}
	if n := e.count(t); n != 0 {
		t.Fatalf("%d events written", n)
	}

	// Ordinary payloads, including cycles and nil values, still record.
	type node struct{ Next *node }
	n := &node{}
	n.Next = n
	e.record(actorCtx(), t, audit.Event{Action: audit.SettingChanged, Payload: map[string]any{"a": nil, "b": []string{"x"}, "c": (*fakeSecret)(nil) == nil}})
	if err := e.store.Tx(actorCtx(), func(tx *store.Tx) error {
		return e.rec.Record(actorCtx(), tx, audit.Event{Action: audit.SettingChanged, Payload: struct{ N *node }{N: n}})
	}); errors.Is(err, audit.ErrSecretInPayload) {
		t.Fatalf("cyclic payload: %v", err)
	}
}
