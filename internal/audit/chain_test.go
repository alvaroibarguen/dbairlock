package audit

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/platform/clock"
	"github.com/alvaroibarguen/dbairlock/internal/platform/ids"
	"github.com/alvaroibarguen/dbairlock/internal/store"
)

// An attacker who edits a row and recomputes that row's hash still breaks the
// next row's prev_hash link.
func TestRehashedEditBreaksNextLink(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	rec := NewRecorder(clock.NewFake(time.UnixMilli(0)), &ids.Sequential{Prefix: "e"})
	actx := WithActor(ctx, SystemActor)
	for range 5 {
		if err := s.Tx(actx, func(tx *store.Tx) error { return rec.Record(actx, tx, Event{Action: SettingChanged}) }); err != nil {
			t.Fatal(err)
		}
	}

	db, err := sql.Open("sqlite", s.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(ctx, "DROP TRIGGER audit_events_no_update"); err != nil {
		t.Fatal(err)
	}
	rows, err := NewRepository(s.Reader()).After(ctx, 2, 1)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	r := rows[0]
	forged := record{Seq: r.Seq, ID: r.ID, OccurredAt: r.OccurredAt, Via: r.Via, Action: "setting.forged", Payload: r.Payload}
	h, err := chainHash(r.PrevHash, forged)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE audit_events SET action = ?, hash = ? WHERE seq = 3", forged.Action, h); err != nil {
		t.Fatal(err)
	}

	res, err := NewVerifier(s.Reader()).Verify(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK() || res.Problem.Seq != 4 || res.Problem.Reason != "prev_hash does not match the previous event's hash" {
		t.Fatalf("verify: %+v %v", res, res.Problem)
	}
}
