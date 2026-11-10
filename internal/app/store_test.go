package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/platform/clock"
	"github.com/alvaroibarguen/dbairlock/internal/platform/ids"
	"github.com/alvaroibarguen/dbairlock/internal/store"
)

func TestReadyzFollowsStore(t *testing.T) {
	a := newTestApp(t)
	get := func() int {
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		return rec.Code
	}
	if code := get(); code != http.StatusOK {
		t.Fatalf("readyz = %d with an open store", code)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if code := get(); code != http.StatusServiceUnavailable {
		t.Fatalf("readyz = %d after the store closed", code)
	}
}

func TestVerifyAudit(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "data")
	if _, err := VerifyAudit(ctx, dir); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("missing database: %v", err)
	}

	st, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	rec := audit.NewRecorder(clock.Real{}, ids.UUIDv7{})
	actx := audit.WithActor(ctx, audit.SystemActor)
	for range 3 {
		if err := st.Tx(actx, func(tx *store.Tx) error { return rec.Record(actx, tx, audit.Event{Action: audit.SettingChanged}) }); err != nil {
			t.Fatal(err)
		}
	}
	path := st.Path()
	_ = st.Close()

	res, err := VerifyAudit(ctx, dir)
	if err != nil || !res.OK() || res.Checked != 3 {
		t.Fatalf("good database: %+v %v", res, err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, q := range []string{"DROP TRIGGER audit_events_no_update", `UPDATE audit_events SET payload = '{"x":1}' WHERE seq = 2`} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	res, err = VerifyAudit(ctx, dir)
	if err != nil || res.OK() || res.Problem.Seq != 2 {
		t.Fatalf("tampered database: %+v %v", res, err)
	}
}
