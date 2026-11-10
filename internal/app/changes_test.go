package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/changes"
	"github.com/alvaroibarguen/dbairlock/internal/connector"
	"github.com/alvaroibarguen/dbairlock/internal/identity"
	"github.com/alvaroibarguen/dbairlock/internal/platform/clock"
	"github.com/alvaroibarguen/dbairlock/internal/platform/logx"
	"github.com/alvaroibarguen/dbairlock/internal/secrets"
	"github.com/alvaroibarguen/dbairlock/internal/settings"
	"github.com/alvaroibarguen/dbairlock/internal/store"
	"github.com/alvaroibarguen/dbairlock/internal/targets"
)

// Integration tests of changes and the executor (phase 7 checks), against
// real Postgres and MySQL targets. Each test gets a fresh instance (with a
// fake clock) and a fresh seeded database reached through a proxy.

type chgEnv struct {
	t     *testing.T
	a     *App
	clock *clock.Fake
	db    targetDB
	px    *proxy
	conn  string

	ada, bob, cat, dan identity.Principal // ada is the admin
}

func forEachTarget(t *testing.T, fn func(t *testing.T, srv targetServer)) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}
	t.Run("postgres", func(t *testing.T) { fn(t, postgresTargets(t)) })
	t.Run("mysql", func(t *testing.T) { fn(t, mysqlTargets(t)) })
}

func newChgEnv(t *testing.T, srv targetServer, policy targets.PolicyPatch) *chgEnv {
	t.Helper()
	c := clock.NewFake(time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC))
	a, err := New(Options{
		DataDir: t.TempDir(), Listen: "127.0.0.1:0", Dev: true, Version: "test", Logger: logx.Discard(),
		Clock: c, WebUI: fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>shell</title>")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	e := &chgEnv{t: t, a: a, clock: c}
	t.Cleanup(func() { _ = e.a.Close() })
	e.ada = e.login("ada@example.com", identity.RoleAdmin)
	e.bob = e.login("bob@example.com", identity.RoleMember)
	e.cat = e.login("cat@example.com", identity.RoleMember)
	e.dan = e.login("dan@example.com", identity.RoleMember)
	e.db = srv.newDB(t)
	e.px = newProxy(t, e.db.backend())
	e.conn = e.addConnection("target", policy)
	// Every transition wrote its audit event into an intact chain.
	t.Cleanup(func() {
		res, err := audit.NewVerifier(e.a.store.Reader()).Verify(context.Background())
		if err != nil || !res.OK() {
			t.Errorf("audit verify: %v %+v", err, res.Problem)
		}
	})
	return e
}

func (e *chgEnv) login(email string, role identity.Role) identity.Principal {
	e.t.Helper()
	_, sess, err := e.a.identity.DevLogin(context.Background(), email, role, identity.Meta{IP: "127.0.0.1"})
	if err != nil {
		e.t.Fatal(err)
	}
	p, err := e.a.identity.AuthenticateSession(context.Background(), sess.Token)
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *chgEnv) ctx(p identity.Principal) context.Context {
	return identity.WithPrincipal(context.Background(), p, "127.0.0.1")
}

func (e *chgEnv) addConnection(name string, policy targets.PolicyPatch) string {
	e.t.Helper()
	c, err := e.a.targets.Create(e.ctx(e.ada), targets.CreateInput{
		Name: name, Connector: e.db.connector(), Config: e.db.config("127.0.0.1", e.px.port()),
		Password: secrets.NewValue(tgtAppPassword), Policy: policy,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return c.ID
}

func (e *chgEnv) submitOn(p identity.Principal, conn string, stmts ...string) (changes.Change, error) {
	return e.a.changes.Create(e.ctx(p), p, changes.Draft{
		ConnectionID: conn, Title: "Test change", Reason: "Because the test says so.", Statements: stmts,
	}, true)
}

func (e *chgEnv) submit(p identity.Principal, stmts ...string) changes.Change {
	e.t.Helper()
	c, err := e.submitOn(p, e.conn, stmts...)
	if err != nil {
		e.t.Fatalf("submit %q: %v", stmts, err)
	}
	if c.Status != changes.StatusPendingReview {
		e.t.Fatalf("submitted change is %s", c.Status)
	}
	return c
}

func (e *chgEnv) get(p identity.Principal, c changes.Change) changes.Change {
	e.t.Helper()
	got, err := e.a.changes.Get(e.ctx(p), p, c.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return got
}

// approve approves c as p with the hashes p currently sees.
func (e *chgEnv) approve(p identity.Principal, c changes.Change) (changes.Change, error) {
	cur := e.get(p, c)
	return e.a.changes.Review(e.ctx(p), p, c.ID, changes.ReviewInput{
		Decision: changes.DecisionApprove, SQLHash: cur.SQLHash, PreviewHash: cur.PreviewHash,
	})
}

func (e *chgEnv) mustApprove(p identity.Principal, c changes.Change) changes.Change {
	e.t.Helper()
	got, err := e.approve(p, c)
	if err != nil {
		e.t.Fatalf("approve: %v", err)
	}
	return got
}

func (e *chgEnv) run() { e.a.executor.RunOnce(context.Background()) }

func (e *chgEnv) status(c changes.Change) changes.Change {
	e.t.Helper()
	return e.get(e.ada, c)
}

func (e *chgEnv) expectStatus(c changes.Change, want changes.Status) changes.Change {
	e.t.Helper()
	got := e.status(c)
	if got.Status != want {
		e.t.Fatalf("change #%d is %s (%s: %s), want %s", got.Number, got.Status, got.StatusCode, got.StatusMessage, want)
	}
	return got
}

func (e *chgEnv) name(id int) string {
	e.t.Helper()
	return e.db.value(e.t, "SELECT name FROM users WHERE id = "+itoa(id))
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

// actions returns the audit actions recorded for change c, in order.
func (e *chgEnv) actions(c changes.Change) []string {
	e.t.Helper()
	ev, err := e.a.changes.Events(e.ctx(e.ada), e.ada, c.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	out := make([]string, len(ev))
	for i, x := range ev {
		out[i] = string(x.Action)
	}
	return out
}

func (e *chgEnv) countAction(c changes.Change, action audit.Action) int {
	n := 0
	for _, a := range e.actions(c) {
		if a == string(action) {
			n++
		}
	}
	return n
}

func wantErr(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("error = %v, want %v", err, target)
	}
}

func intp(v int) *int    { return &v }
func boolp(v bool) *bool { return &v }

func TestChangeReview(t *testing.T) {
	forEachTarget(t, func(t *testing.T, srv targetServer) {
		t.Run("happy path", func(t *testing.T) {
			e := newChgEnv(t, srv, targets.PolicyPatch{})
			c := e.submit(e.ada, "UPDATE users SET name = 'Zed' WHERE id = 1")
			st := c.Statements[0]
			if st.ExpectedRows == nil || *st.ExpectedRows != 1 || st.Preview == nil || len(st.Preview.Before) != 1 || len(st.Preview.After) != 1 {
				t.Fatalf("preview = %+v", st.Preview)
			}
			if c.Actions.Approve || !c.Actions.Cancel || !c.Actions.Edit {
				t.Errorf("author actions = %+v", c.Actions)
			}
			if bc := e.get(e.bob, c); !bc.Actions.Approve || !bc.Actions.Reject || bc.Actions.Edit {
				t.Errorf("reviewer actions = %+v", bc.Actions)
			}
			c = e.mustApprove(e.bob, c)
			if c.Status != changes.StatusApproved || c.Approvals != 1 {
				t.Fatalf("after approval: %s, %d approvals", c.Status, c.Approvals)
			}
			e.run()
			c = e.expectStatus(c, changes.StatusExecuted)
			if e.name(1) != "Zed" {
				t.Errorf("name = %s", e.name(1))
			}
			if len(c.Executions) != 1 || c.Executions[0].Outcome != changes.OutcomeExecuted || c.Executions[0].Results[0].Preview.RowsAffected != 1 {
				t.Errorf("executions = %+v", c.Executions)
			}
			want := []string{"change.created", "change.submitted", "change.reviewed", "change.approved", "execution.started", "execution.succeeded"}
			if got := e.actions(c); strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("timeline = %v", got)
			}
		})

		t.Run("self-review refused", func(t *testing.T) {
			e := newChgEnv(t, srv, targets.PolicyPatch{})
			c := e.submit(e.ada, "UPDATE users SET name = 'Zed' WHERE id = 1")
			_, err := e.approve(e.ada, c)
			wantErr(t, err, changes.ErrSelfReview)
			_, err = e.a.changes.Review(e.ctx(e.ada), e.ada, c.ID, changes.ReviewInput{Decision: changes.DecisionReject, Comment: "no"})
			wantErr(t, err, changes.ErrSelfReview)
			e.expectStatus(c, changes.StatusPendingReview)
		})

		t.Run("review by a removed user refused", func(t *testing.T) {
			e := newChgEnv(t, srv, targets.PolicyPatch{})
			c := e.submit(e.ada, "UPDATE users SET name = 'Zed' WHERE id = 1")
			if _, err := e.a.identity.RemoveUser(e.ctx(e.ada), e.ada, e.cat.UserID); err != nil {
				t.Fatal(err)
			}
			_, err := e.approve(e.cat, c)
			wantErr(t, err, identity.ErrUnauthenticated)
			e.expectStatus(c, changes.StatusPendingReview)
		})

		t.Run("edit invalidates approval", func(t *testing.T) {
			e := newChgEnv(t, srv, targets.PolicyPatch{RequiredApprovals: intp(2)})
			c := e.submit(e.ada, "UPDATE users SET name = 'Zed' WHERE id = 1")
			c = e.mustApprove(e.bob, c)
			if c.Approvals != 1 || c.Status != changes.StatusPendingReview {
				t.Fatalf("after one approval: %s %d", c.Status, c.Approvals)
			}
			edited, err := e.a.changes.Edit(e.ctx(e.ada), e.ada, c.ID, changes.EditInput{
				Revision: c.Revision, Statements: []string{"UPDATE users SET name = 'Zoe' WHERE id = 1"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if edited.Revision != 2 || edited.SQLHash == c.SQLHash || edited.Approvals != 0 {
				t.Fatalf("edited: revision %d approvals %d", edited.Revision, edited.Approvals)
			}
			if r := edited.Reviews[0]; r.Counts || r.Outdated != changes.OutdatedSQL {
				t.Errorf("review after edit = %+v", r)
			}
			// An edit based on the old revision is a conflict.
			_, err = e.a.changes.Edit(e.ctx(e.ada), e.ada, c.ID, changes.EditInput{Revision: c.Revision, Title: strp("x")})
			wantErr(t, err, changes.ErrConflict)
			// Only the author edits.
			_, err = e.a.changes.Edit(e.ctx(e.bob), e.bob, c.ID, changes.EditInput{Revision: edited.Revision, Title: strp("x")})
			wantErr(t, err, changes.ErrNotAuthor)
		})

		t.Run("rejection", func(t *testing.T) {
			e := newChgEnv(t, srv, targets.PolicyPatch{})
			c := e.submit(e.ada, "UPDATE users SET name = 'Zed' WHERE id = 1")
			_, err := e.a.changes.Review(e.ctx(e.bob), e.bob, c.ID, changes.ReviewInput{Decision: changes.DecisionReject})
			wantErr(t, err, changes.ErrInvalid)
			c, err = e.a.changes.Review(e.ctx(e.bob), e.bob, c.ID, changes.ReviewInput{Decision: changes.DecisionReject, Comment: "Wrong user."})
			if err != nil {
				t.Fatal(err)
			}
			if c.Status != changes.StatusRejected || c.StatusMessage != "Wrong user." || c.Reviews[0].Comment != "Wrong user." {
				t.Errorf("rejected = %s %q", c.Status, c.StatusMessage)
			}
			e.run()
			if e.name(1) != "Ann" {
				t.Error("a rejected change ran")
			}
		})

		t.Run("approval needs a fresh preview", func(t *testing.T) {
			e := newChgEnv(t, srv, targets.PolicyPatch{})
			c := e.submit(e.ada, "UPDATE users SET name = 'Zed' WHERE id = 1")
			e.clock.Advance(5*time.Minute + time.Second)
			_, err := e.approve(e.bob, c)
			wantErr(t, err, changes.ErrPreviewStale)
			c, res, err := e.a.changes.RefreshPreview(e.ctx(e.bob), e.bob, c.ID)
			if err != nil || !res.OK || res.DataChanged {
				t.Fatalf("refresh: %+v %v", res, err)
			}
			// A second refresh at once is refused.
			_, _, err = e.a.changes.RefreshPreview(e.ctx(e.bob), e.bob, c.ID)
			var soon *changes.TooSoonError
			if !errors.As(err, &soon) {
				t.Errorf("second refresh = %v", err)
			}
			if _, err := e.approve(e.bob, c); err != nil {
				t.Fatalf("approve after refresh: %v", err)
			}
			e.expectStatus(c, changes.StatusApproved)
			// The limit comes from the setting.
			e2 := newChgEnv(t, srv, targets.PolicyPatch{})
			if err := e2.a.settings.Update(e2.ctx(e2.ada), settings.Changes{Values: map[string]any{"policy.preview_max_age_minutes": 10}}); err != nil {
				t.Fatal(err)
			}
			c2 := e2.submit(e2.ada, "UPDATE users SET name = 'Zed' WHERE id = 1")
			e2.clock.Advance(9 * time.Minute)
			e2.mustApprove(e2.bob, c2)
		})

		t.Run("refresh keeps or voids approvals", func(t *testing.T) {
			e := newChgEnv(t, srv, targets.PolicyPatch{RequiredApprovals: intp(2)})
			c := e.submit(e.ada, "UPDATE users SET status = 'gone' WHERE status = 'active'")
			c = e.mustApprove(e.bob, c)
			old := c
			// Same rows: the approval stands.
			e.clock.Advance(11 * time.Second)
			c, res, err := e.a.changes.RefreshPreview(e.ctx(e.cat), e.cat, c.ID)
			if err != nil || !res.OK || res.DataChanged || c.Approvals != 1 || c.PreviewHash != old.PreviewHash {
				t.Fatalf("unchanged refresh: %+v approvals %d err %v", res, c.Approvals, err)
			}
			// Different rows: it stops counting.
			e.db.exec(t, "UPDATE users SET status = 'active' WHERE id = 5")
			e.clock.Advance(11 * time.Second)
			c, res, err = e.a.changes.RefreshPreview(e.ctx(e.cat), e.cat, c.ID)
			if err != nil || !res.OK || !res.DataChanged {
				t.Fatalf("changed refresh: %+v %v", res, err)
			}
			if c.Approvals != 0 || c.Reviews[0].Outdated != changes.OutdatedPreview || !c.PreviewChanged {
				t.Errorf("after changed refresh: approvals %d review %+v", c.Approvals, c.Reviews[0])
			}
			if st := c.Statements[0]; *st.ExpectedRows != 4 || st.PreviousPreview == nil || st.PreviousPreview.RowsAffected != 3 {
				t.Errorf("statement after refresh = %+v", st)
			}
			// An approval for the old preview is refused; so is one for old SQL.
			_, err = e.a.changes.Review(e.ctx(e.cat), e.cat, c.ID, changes.ReviewInput{
				Decision: changes.DecisionApprove, SQLHash: c.SQLHash, PreviewHash: old.PreviewHash,
			})
			wantErr(t, err, changes.ErrPreviewChanged)
			_, err = e.a.changes.Review(e.ctx(e.cat), e.cat, c.ID, changes.ReviewInput{
				Decision: changes.DecisionApprove, SQLHash: changes.SQLHash([]string{"x"}), PreviewHash: c.PreviewHash,
			})
			wantErr(t, err, changes.ErrSQLChanged)
			e.mustApprove(e.cat, c)
			c = e.mustApprove(e.bob, c)
			if c.Status != changes.StatusApproved || c.Approvals != 2 {
				t.Errorf("after two fresh approvals: %s %d", c.Status, c.Approvals)
			}
			// Approving twice is refused.
			e2 := newChgEnv(t, srv, targets.PolicyPatch{RequiredApprovals: intp(2)})
			c2 := e2.submit(e2.ada, "UPDATE users SET name = 'Zed' WHERE id = 1")
			e2.mustApprove(e2.bob, c2)
			_, err = e2.approve(e2.bob, c2)
			wantErr(t, err, changes.ErrAlreadyApproved)
		})

		t.Run("a failed refresh blocks approval but not rejection", func(t *testing.T) {
			e := newChgEnv(t, srv, targets.PolicyPatch{})
			c := e.submit(e.ada, "DELETE FROM users WHERE id = 5")
			e.db.exec(t, "DELETE FROM users WHERE id = 5")
			c, res, err := e.a.changes.RefreshPreview(e.ctx(e.bob), e.bob, c.ID)
			if err != nil || res.OK || !strings.Contains(res.Problem, "matches no rows") {
				t.Fatalf("refresh = %+v %v", res, err)
			}
			if c.PreviewError == "" || c.Statements[0].Issue == nil || c.Statements[0].Issue.Code != changes.CodeNoRows {
				t.Errorf("stored failure = %q %+v", c.PreviewError, c.Statements[0].Issue)
			}
			_, err = e.approve(e.bob, c)
			wantErr(t, err, changes.ErrPreviewFailed)
			if _, err := e.a.changes.Review(e.ctx(e.bob), e.bob, c.ID, changes.ReviewInput{Decision: changes.DecisionReject, Comment: "Already gone."}); err != nil {
				t.Fatal(err)
			}
			e.expectStatus(c, changes.StatusRejected)
		})

		t.Run("cancellation", func(t *testing.T) {
			e := newChgEnv(t, srv, targets.PolicyPatch{})
			c := e.submit(e.dan, "UPDATE users SET name = 'Zed' WHERE id = 1")
			_, err := e.a.changes.Cancel(e.ctx(e.bob), e.bob, c.ID, "")
			wantErr(t, err, identity.ErrForbidden)
			c, err = e.a.changes.Cancel(e.ctx(e.dan), e.dan, c.ID, "Not needed.")
			if err != nil || c.Status != changes.StatusCancelled || c.StatusCode != changes.CancelledByAuthor {
				t.Fatalf("cancel = %s %s %v", c.Status, c.StatusCode, err)
			}
			_, err = e.a.changes.Cancel(e.ctx(e.dan), e.dan, c.ID, "")
			wantErr(t, err, changes.ErrInvalidState)
			// An admin may cancel someone else's approved change.
			c2 := e.submit(e.dan, "UPDATE users SET name = 'Zed' WHERE id = 2")
			if _, err := e.a.targets.Update(e.ctx(e.ada), e.conn, targets.UpdateInput{Policy: targets.PolicyPatch{AutoExecute: boolp(false)}}); err != nil {
				t.Fatal(err)
			}
			e.mustApprove(e.bob, c2)
			c2, err = e.a.changes.Cancel(e.ctx(e.ada), e.ada, c2.ID, "")
			if err != nil || c2.StatusCode != changes.CancelledByAdmin {
				t.Fatalf("admin cancel = %v", err)
			}
			e.run()
			if e.name(2) != "Bob" {
				t.Error("a cancelled change ran")
			}
		})

		t.Run("expiry", func(t *testing.T) {
			e := newChgEnv(t, srv, targets.PolicyPatch{ApprovalTTLHours: intp(1), AutoExecute: boolp(false)})
			pending := e.submit(e.ada, "UPDATE users SET name = 'Zed' WHERE id = 1")
			approved := e.submit(e.ada, "UPDATE users SET name = 'Zoe' WHERE id = 2")
			e.mustApprove(e.bob, approved)
			e.clock.Advance(59 * time.Minute)
			e.run()
			e.expectStatus(pending, changes.StatusPendingReview)
			e.clock.Advance(time.Minute)
			e.run()
			e.expectStatus(pending, changes.StatusExpired)
			e.expectStatus(approved, changes.StatusExpired)
			if _, err := e.a.changes.ExecuteNow(e.ctx(e.ada), e.ada, approved.ID); !errors.Is(err, changes.ErrInvalidState) {
				t.Errorf("execute an expired change = %v", err)
			}
			e.run()
			if e.name(2) != "Bob" {
				t.Error("an expired change ran")
			}
		})

		t.Run("two approvals required", func(t *testing.T) {
			e := newChgEnv(t, srv, targets.PolicyPatch{RequiredApprovals: intp(2)})
			c := e.submit(e.ada, "UPDATE users SET name = 'Zed' WHERE id = 1")
			c = e.mustApprove(e.bob, c)
			e.run()
			if c.Status != changes.StatusPendingReview || e.name(1) != "Ann" {
				t.Fatalf("after one of two approvals: %s", c.Status)
			}
			c = e.mustApprove(e.cat, c)
			if c.Status != changes.StatusApproved || c.Approvals != 2 {
				t.Fatalf("after two: %s %d", c.Status, c.Approvals)
			}
			e.run()
			e.expectStatus(c, changes.StatusExecuted)
			if e.name(1) != "Zed" {
				t.Error("not executed")
			}
		})
	})
}

func strp(s string) *string { return &s }

func TestChangeDrift(t *testing.T) {
	forEachTarget(t, func(t *testing.T, srv targetServer) {
		drift := func(t *testing.T, e *chgEnv, c changes.Change, kind connector.DriftKind) changes.Change {
			t.Helper()
			e.run()
			got := e.expectStatus(c, changes.StatusFailed)
			if got.StatusCode != changes.FailDrift || got.StatusDetail != string(kind) {
				t.Errorf("failure = %s/%s %q", got.StatusCode, got.StatusDetail, got.StatusMessage)
			}
			if ex := got.Executions[len(got.Executions)-1]; ex.Drift == nil || ex.Drift.Kind != kind {
				t.Errorf("execution = %+v", ex)
			}
			// A failed change is terminal: it never runs again.
			e.clock.Advance(time.Hour)
			e.run()
			e.expectStatus(c, changes.StatusFailed)
			return got
		}

		t.Run("count drift", func(t *testing.T) {
			e := newChgEnv(t, srv, targets.PolicyPatch{})
			c := e.submit(e.ada, "UPDATE users SET name = 'X' WHERE status = 'active'")
			e.mustApprove(e.bob, c)
			// Approved but not yet run: make the executor see a fourth row.
			e.db.exec(t, "UPDATE users SET status = 'active' WHERE id = 5")
			got := drift(t, e, c, connector.DriftCount)
			if got.StatusMessage != "This change was approved for 3 rows but would now affect 4 rows, so nothing was changed." {
				t.Errorf("message = %q", got.StatusMessage)
			}
			if n := e.db.value(t, "SELECT count(*) FROM users WHERE name = 'X'"); n != "0" {
				t.Errorf("%s rows changed", n)
			}
		})

		t.Run("same count, different rows", func(t *testing.T) {
			e := newChgEnv(t, srv, targets.PolicyPatch{AutoExecute: boolp(false)})
			c := e.submit(e.ada, "UPDATE users SET name = 'X' WHERE status = 'active'")
			e.mustApprove(e.bob, c)
			e.db.exec(t, "UPDATE users SET status = 'banned' WHERE id = 1")
			e.db.exec(t, "UPDATE users SET status = 'active' WHERE id = 5")
			if _, err := e.a.changes.ExecuteNow(e.ctx(e.ada), e.ada, c.ID); err != nil {
				t.Fatal(err)
			}
			got := drift(t, e, c, connector.DriftRows)
			if !strings.Contains(got.StatusMessage, "no longer the ones that were reviewed") {
				t.Errorf("message = %q", got.StatusMessage)
			}
			if n := e.db.value(t, "SELECT count(*) FROM users WHERE name = 'X'"); n != "0" {
				t.Errorf("%s rows changed", n)
			}
		})

		t.Run("overwritten value drift", func(t *testing.T) {
			e := newChgEnv(t, srv, targets.PolicyPatch{AutoExecute: boolp(false)})
			c := e.submit(e.ada, "UPDATE users SET name = 'Zed' WHERE id = 1")
			e.mustApprove(e.bob, c)
			e.db.exec(t, "UPDATE users SET name = 'Annie' WHERE id = 1")
			if _, err := e.a.changes.ExecuteNow(e.ctx(e.ada), e.ada, c.ID); err != nil {
				t.Fatal(err)
			}
			drift(t, e, c, connector.DriftRows)
			if e.name(1) != "Annie" {
				t.Errorf("name = %s", e.name(1))
			}
		})

		t.Run("a change to an unrelated column does not block", func(t *testing.T) {
			e := newChgEnv(t, srv, targets.PolicyPatch{AutoExecute: boolp(false)})
			c := e.submit(e.ada, "UPDATE users SET name = 'Zed' WHERE id = 1")
			e.mustApprove(e.bob, c)
			e.db.exec(t, "UPDATE users SET updated_at = '2026-01-01 00:00:00', email = 'ann@new.example' WHERE id = 1")
			if _, err := e.a.changes.ExecuteNow(e.ctx(e.ada), e.ada, c.ID); err != nil {
				t.Fatal(err)
			}
			e.run()
			e.expectStatus(c, changes.StatusExecuted)
			if e.name(1) != "Zed" {
				t.Error("not executed")
			}
		})

		t.Run("the second statement fails and the first is rolled back", func(t *testing.T) {
			e := newChgEnv(t, srv, targets.PolicyPatch{AutoExecute: boolp(false)})
			c := e.submit(e.ada, "UPDATE users SET name = 'Zed' WHERE id = 1", "DELETE FROM users WHERE id = 5")
			e.mustApprove(e.bob, c)
			e.db.exec(t, "DELETE FROM users WHERE id = 5")
			if _, err := e.a.changes.ExecuteNow(e.ctx(e.ada), e.ada, c.ID); err != nil {
				t.Fatal(err)
			}
			got := drift(t, e, c, connector.DriftCount)
			if ex := got.Executions[0]; ex.Drift.Statement != 1 {
				t.Errorf("drift on statement %d", ex.Drift.Statement)
			}
			if e.name(1) != "Ann" {
				t.Errorf("first statement not rolled back: %s", e.name(1))
			}
		})

		t.Run("a table without a primary key compares whole rows", func(t *testing.T) {
			e := newChgEnv(t, srv, targets.PolicyPatch{AutoExecute: boolp(false)})
			c := e.submit(e.ada, "DELETE FROM audit_notes WHERE user_id = 1")
			if c.Statements[0].HasPrimaryKey {
				t.Error("audit_notes reported a primary key")
			}
			e.mustApprove(e.bob, c)
			e.db.exec(t, "UPDATE audit_notes SET note = 'edited' WHERE user_id = 1")
			if _, err := e.a.changes.ExecuteNow(e.ctx(e.ada), e.ada, c.ID); err != nil {
				t.Fatal(err)
			}
			drift(t, e, c, connector.DriftRows)
		})

		t.Run("auto-execute off waits for the manual trigger", func(t *testing.T) {
			e := newChgEnv(t, srv, targets.PolicyPatch{AutoExecute: boolp(false)})
			c := e.submit(e.dan, "UPDATE users SET name = 'Zed' WHERE id = 1")
			c = e.mustApprove(e.bob, c)
			e.run()
			c = e.expectStatus(c, changes.StatusApproved)
			if e.name(1) != "Ann" || !c.Actions.ExecuteNow {
				t.Fatalf("ran without the trigger, or no trigger offered: %+v", c.Actions)
			}
			// Another member cannot trigger it; the author can.
			_, err := e.a.changes.ExecuteNow(e.ctx(e.cat), e.cat, c.ID)
			wantErr(t, err, identity.ErrForbidden)
			if _, err := e.a.changes.ExecuteNow(e.ctx(e.dan), e.dan, c.ID); err != nil {
				t.Fatal(err)
			}
			e.run()
			e.expectStatus(c, changes.StatusExecuted)
			if e.name(1) != "Zed" {
				t.Error("not executed")
			}
		})
	})
}

func TestChangeOutage(t *testing.T) {
	forEachTarget(t, func(t *testing.T, srv targetServer) {
		t.Run("an unreachable target defers, then executes", func(t *testing.T) {
			e := newChgEnv(t, srv, targets.PolicyPatch{})
			c := e.submit(e.ada, "UPDATE users SET name = 'Zed' WHERE id = 1")
			e.px.setDown(true)
			e.mustApprove(e.bob, c)
			e.run()
			got := e.expectStatus(c, changes.StatusApproved)
			if got.Attempts != 1 || got.NextAttemptAt == nil || !got.NextAttemptAt.Equal(e.clock.Now().Add(30*time.Second)) || got.LastAttemptError == "" {
				t.Fatalf("after one attempt: attempts %d next %v error %q", got.Attempts, got.NextAttemptAt, got.LastAttemptError)
			}
			if ex := got.Executions[0]; ex.Outcome != changes.OutcomeNotStarted {
				t.Errorf("execution = %+v", ex)
			}
			if e.countAction(c, audit.ExecutionDeferred) != 1 {
				t.Error("no execution.deferred event")
			}
			// Not before the backoff has passed.
			e.run()
			if e.status(c).Attempts != 1 {
				t.Error("retried before the backoff passed")
			}
			e.clock.Advance(30 * time.Second)
			e.run()
			got = e.expectStatus(c, changes.StatusApproved)
			if got.Attempts != 2 || !got.NextAttemptAt.Equal(e.clock.Now().Add(time.Minute)) {
				t.Errorf("second attempt: %d %v", got.Attempts, got.NextAttemptAt)
			}
			// An admin can try at once.
			if _, err := e.a.changes.RetryNow(e.ctx(e.bob), e.bob, c.ID); !errors.Is(err, identity.ErrForbidden) {
				t.Errorf("member retry = %v", err)
			}
			e.px.setDown(false)
			if _, err := e.a.changes.RetryNow(e.ctx(e.ada), e.ada, c.ID); err != nil {
				t.Fatal(err)
			}
			e.run()
			e.expectStatus(c, changes.StatusExecuted)
			if e.name(1) != "Zed" {
				t.Error("not executed")
			}
		})

		t.Run("a target down past the approval lifetime expires the change", func(t *testing.T) {
			e := newChgEnv(t, srv, targets.PolicyPatch{ApprovalTTLHours: intp(1)})
			c := e.submit(e.ada, "UPDATE users SET name = 'Zed' WHERE id = 1")
			e.mustApprove(e.bob, c)
			e.px.setDown(true)
			for i := 0; i < 20 && e.status(c).Status == changes.StatusApproved; i++ {
				e.run()
				e.clock.Advance(5 * time.Minute)
			}
			got := e.expectStatus(c, changes.StatusExpired)
			if got.Attempts < 3 {
				t.Errorf("attempts = %d", got.Attempts)
			}
			e.px.setDown(false)
			e.run()
			if e.name(1) != "Ann" {
				t.Error("an expired change ran")
			}
		})

		t.Run("a connection lost after the transaction began is final", func(t *testing.T) {
			e := newChgEnv(t, srv, targets.PolicyPatch{})
			if err := e.a.settings.Update(e.ctx(e.ada), settings.Changes{Values: map[string]any{"limits.lock_timeout_ms": 30000}}); err != nil {
				t.Fatal(err)
			}
			c := e.submit(e.ada, "UPDATE users SET name = 'Zed' WHERE id = 1")
			release := e.db.lock(t, "SELECT name FROM users WHERE id = 1 FOR UPDATE")
			e.mustApprove(e.bob, c)
			done := make(chan struct{})
			go func() {
				e.run()
				close(done)
			}()
			deadline := time.Now().Add(20 * time.Second) //nolint:forbidigo // real time for a real wait
			for e.status(c).Status != changes.StatusExecuting {
				if time.Now().After(deadline) { //nolint:forbidigo // real time for a real wait
					t.Fatal("the change never started executing")
				}
				time.Sleep(50 * time.Millisecond)
			}
			time.Sleep(time.Second) // blocked on the row lock inside the transaction
			e.px.killAll()
			release()
			<-done
			got := e.expectStatus(c, changes.StatusFailed)
			if got.StatusCode != changes.FailError {
				t.Errorf("failure = %s %q", got.StatusCode, got.StatusMessage)
			}
			e.clock.Advance(time.Hour)
			e.run()
			e.expectStatus(c, changes.StatusFailed)
			if e.name(1) != "Ann" {
				t.Errorf("name = %s", e.name(1))
			}
			if n := e.countAction(c, audit.ExecutionStarted); n != 1 {
				t.Errorf("started %d times", n)
			}
		})
	})
}

func TestChangeUserRemoval(t *testing.T) {
	srv := postgresTargets(t)
	t.Run("the removed user's open changes are cancelled", func(t *testing.T) {
		e := newChgEnv(t, srv, targets.PolicyPatch{AutoExecute: boolp(false)})
		draft, err := e.a.changes.Create(e.ctx(e.dan), e.dan, changes.Draft{
			ConnectionID: e.conn, Title: "Draft", Statements: []string{"UPDATE users SET name = 'Zed' WHERE id = 1"},
		}, false)
		if err != nil || draft.Status != changes.StatusDraft {
			t.Fatalf("draft: %v", err)
		}
		pending := e.submit(e.dan, "UPDATE users SET name = 'Zed' WHERE id = 2")
		approved := e.submit(e.dan, "UPDATE users SET name = 'Zed' WHERE id = 3")
		e.mustApprove(e.bob, approved)
		if _, err := e.a.identity.RemoveUser(e.ctx(e.ada), e.ada, e.dan.UserID); err != nil {
			t.Fatal(err)
		}
		for _, c := range []changes.Change{draft, pending, approved} {
			got := e.expectStatus(c, changes.StatusCancelled)
			if got.StatusCode != changes.CancelledAuthorGone || !got.Author.Removed {
				t.Errorf("#%d: %s, author removed %v", got.Number, got.StatusCode, got.Author.Removed)
			}
		}
	})

	t.Run("a change approved only by the removed user returns to review", func(t *testing.T) {
		e := newChgEnv(t, srv, targets.PolicyPatch{})
		c := e.submit(e.ada, "UPDATE users SET name = 'Zed' WHERE id = 1")
		e.mustApprove(e.bob, c)
		if _, err := e.a.identity.RemoveUser(e.ctx(e.ada), e.ada, e.bob.UserID); err != nil {
			t.Fatal(err)
		}
		got := e.expectStatus(c, changes.StatusPendingReview)
		if got.Approvals != 0 || got.Reviews[0].Outdated != changes.OutdatedRemoved {
			t.Errorf("approvals %d review %+v", got.Approvals, got.Reviews[0])
		}
		e.run()
		e.expectStatus(c, changes.StatusPendingReview)
		if e.name(1) != "Ann" {
			t.Error("executed without a valid approval")
		}
		// Restoring the reviewer does not bring the approval back.
		if _, err := e.a.identity.RestoreUser(e.ctx(e.ada), e.ada, e.bob.UserID); err != nil {
			t.Fatal(err)
		}
		if got := e.status(c); got.Approvals != 0 {
			t.Errorf("approvals after restore = %d", got.Approvals)
		}
	})

	t.Run("the executor never claims a change whose author is removed", func(t *testing.T) {
		e := newChgEnv(t, srv, targets.PolicyPatch{AutoExecute: boolp(false)})
		c := e.submit(e.dan, "UPDATE users SET name = 'Zed' WHERE id = 1")
		e.mustApprove(e.bob, c)
		if _, err := e.a.changes.ExecuteNow(e.ctx(e.dan), e.dan, c.ID); err != nil {
			t.Fatal(err)
		}
		// Mark the author removed without the removal hook, as if the hook
		// had been bypassed.
		err := e.a.store.Tx(context.Background(), func(tx *store.Tx) error {
			_, err := tx.ExecContext(context.Background(), "UPDATE users SET disabled_at = 1 WHERE id = ?", e.dan.UserID)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		e.run()
		got := e.expectStatus(c, changes.StatusCancelled)
		if got.StatusCode != changes.CancelledAuthorGone || e.name(1) != "Ann" {
			t.Errorf("%s, name %s", got.StatusCode, e.name(1))
		}
	})
}

func TestChangeDuplicates(t *testing.T) {
	srv := postgresTargets(t)
	e := newChgEnv(t, srv, targets.PolicyPatch{AutoExecute: boolp(false)})
	const q = "UPDATE users SET name = 'Zed' WHERE id = 1"
	first := e.submit(e.ada, q)
	again, err := e.submitOn(e.ada, e.conn, "  "+q+"\n")
	if err != nil || !again.AlreadyExisted || again.Number != first.Number {
		t.Fatalf("second submission: #%d existed=%v %v", again.Number, again.AlreadyExisted, err)
	}
	// A draft with the same SQL stays a draft when submitted.
	draft, err := e.a.changes.Create(e.ctx(e.ada), e.ada, changes.Draft{ConnectionID: e.conn, Title: "d", Statements: []string{q}}, false)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := e.a.changes.Submit(e.ctx(e.ada), e.ada, draft.ID, draft.Revision)
	if err != nil || !sub.AlreadyExisted || sub.Number != first.Number {
		t.Fatalf("submit draft: #%d %v", sub.Number, err)
	}
	e.expectStatus(draft, changes.StatusDraft)
	// The editor's preview says so too.
	pr, err := e.a.changes.Preview(e.ctx(e.ada), e.ada, e.conn, []string{q})
	if err != nil || pr.Duplicate == nil || pr.Duplicate.Number != first.Number {
		t.Errorf("preview duplicate = %+v %v", pr.Duplicate, err)
	}

	// Concurrent identical submissions create one change.
	const q2 = "UPDATE users SET name = 'Zoe' WHERE id = 2"
	var wg sync.WaitGroup
	results := make([]changes.Change, 4)
	errs := make([]error, 4)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = e.submitOn(e.bob, e.conn, q2)
		}(i)
	}
	wg.Wait()
	created := 0
	for i, r := range results {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if r.Number != results[0].Number {
			t.Errorf("concurrent submissions got #%d and #%d", r.Number, results[0].Number)
		}
		if !r.AlreadyExisted {
			created++
		}
	}
	if created != 1 {
		t.Errorf("%d submissions created a change", created)
	}

	// A different author, a different connection, or an executed change
	// each produce a new change.
	other := e.submit(e.cat, q)
	second := e.addConnection("second", targets.PolicyPatch{AutoExecute: boolp(false)})
	onOther, err := e.submitOn(e.ada, second, q)
	if err != nil || onOther.AlreadyExisted || other.Number == first.Number || onOther.Number == first.Number {
		t.Fatalf("other author #%d, other connection #%d (%v)", other.Number, onOther.Number, err)
	}
	e.mustApprove(e.bob, first)
	if _, err := e.a.changes.ExecuteNow(e.ctx(e.ada), e.ada, first.ID); err != nil {
		t.Fatal(err)
	}
	e.run()
	e.expectStatus(first, changes.StatusExecuted)
	// The row now says Zed, so the same UPDATE still matches one row.
	next := e.submit(e.ada, q)
	if next.AlreadyExisted || next.Number == first.Number {
		t.Errorf("after execution: #%d existed=%v", next.Number, next.AlreadyExisted)
	}
}

func TestChangeCrashRecovery(t *testing.T) {
	srv := postgresTargets(t)
	e := newChgEnv(t, srv, targets.PolicyPatch{})
	c := e.submit(e.ada, "UPDATE users SET name = 'Zed' WHERE id = 1")
	e.mustApprove(e.bob, c)
	// The executor claims the change (committed as executing) and the
	// process dies before the outcome is recorded.
	claim, err := e.a.changes.ClaimNext(audit.WithActor(context.Background(), audit.SystemActor))
	if err != nil || claim == nil {
		t.Fatalf("claim: %v %v", claim, err)
	}
	if claim.Attempt != 1 || claim.SQL[0] != "UPDATE users SET name = 'Zed' WHERE id = 1" {
		t.Errorf("claim = %+v", claim)
	}
	dir := e.a.opts.DataDir
	_ = e.a.Close()

	// Restart on the same data directory.
	b, err := New(Options{
		DataDir: dir, Listen: "127.0.0.1:0", Dev: true, Version: "test", Logger: logx.Discard(), Clock: e.clock,
		WebUI: fstest.MapFS{"index.html": {Data: []byte("x")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.a = b // closed and audit-checked by the env's cleanup
	if err := b.executor.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := b.changes.Get(e.ctx(e.ada), e.ada, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != changes.StatusFailed || got.StatusCode != changes.FailInterrupted || got.StatusMessage != changes.InterruptedMessage {
		t.Fatalf("after restart: %s %s %q", got.Status, got.StatusCode, got.StatusMessage)
	}
	if ex := got.Executions[0]; ex.Outcome != changes.OutcomeInterrupted {
		t.Errorf("execution = %+v", ex)
	}
	b.executor.RunOnce(context.Background())
	e.clock.Advance(time.Hour)
	b.executor.RunOnce(context.Background())
	if e.name(1) != "Ann" {
		t.Error("an interrupted change ran again")
	}
}

func TestChangeConcurrency(t *testing.T) {
	srv := postgresTargets(t)

	t.Run("two reviewers approving at once make one transition", func(t *testing.T) {
		e := newChgEnv(t, srv, targets.PolicyPatch{AutoExecute: boolp(false)})
		c := e.submit(e.ada, "UPDATE users SET name = 'Zed' WHERE id = 1")
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, p := range []identity.Principal{e.bob, e.cat} {
			wg.Add(1)
			go func(i int, p identity.Principal) {
				defer wg.Done()
				_, errs[i] = e.approve(p, c)
			}(i, p)
		}
		wg.Wait()
		ok := 0
		for _, err := range errs {
			if err == nil {
				ok++
			} else if !errors.Is(err, changes.ErrInvalidState) {
				t.Errorf("approve: %v", err)
			}
		}
		if ok != 1 || e.countAction(c, audit.ChangeApproved) != 1 {
			t.Errorf("%d approvals succeeded, %d transitions", ok, e.countAction(c, audit.ChangeApproved))
		}
	})

	t.Run("two edits from the same revision: one wins", func(t *testing.T) {
		e := newChgEnv(t, srv, targets.PolicyPatch{})
		c := e.submit(e.ada, "UPDATE users SET name = 'Zed' WHERE id = 1")
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, name := range []string{"Zoe", "Zia"} {
			wg.Add(1)
			go func(i int, name string) {
				defer wg.Done()
				_, errs[i] = e.a.changes.Edit(e.ctx(e.ada), e.ada, c.ID, changes.EditInput{
					Revision: c.Revision, Statements: []string{"UPDATE users SET name = '" + name + "' WHERE id = 1"},
				})
			}(i, name)
		}
		wg.Wait()
		conflicts := 0
		for _, err := range errs {
			if errors.Is(err, changes.ErrConflict) {
				conflicts++
			} else if err != nil {
				t.Errorf("edit: %v", err)
			}
		}
		if conflicts != 1 || e.status(c).Revision != 2 {
			t.Errorf("%d conflicts, revision %d", conflicts, e.status(c).Revision)
		}
	})

	t.Run("two approved changes on one database run one after the other", func(t *testing.T) {
		e := newChgEnv(t, srv, targets.PolicyPatch{})
		a := e.submit(e.ada, "UPDATE users SET name = 'Zed' WHERE id = 1")
		b := e.submit(e.ada, "UPDATE users SET name = 'Zoe' WHERE id = 2")
		e.mustApprove(e.bob, a)
		e.mustApprove(e.bob, b)
		sys := audit.WithActor(context.Background(), audit.SystemActor)
		claim, err := e.a.changes.ClaimNext(sys)
		if err != nil || claim == nil || claim.ChangeID != a.ID {
			t.Fatalf("first claim: %+v %v", claim, err)
		}
		// The database already has a change executing: nothing else is
		// claimed for it.
		if next, err := e.a.changes.ClaimNext(sys); err != nil || next != nil {
			t.Fatalf("second claim: %+v %v", next, err)
		}
		if _, err := e.a.changes.RecordDeferred(sys, *claim, "test"); err != nil {
			t.Fatal(err)
		}
		e.clock.Advance(time.Minute)
		e.run()
		e.expectStatus(a, changes.StatusExecuted)
		e.expectStatus(b, changes.StatusExecuted)
		if e.name(1) != "Zed" || e.name(2) != "Zoe" {
			t.Error("not both executed")
		}
	})
}

func TestChangeAgentsAndAPI(t *testing.T) {
	srv := postgresTargets(t)
	e := newChgEnv(t, srv, targets.PolicyPatch{})
	tok, err := e.a.identity.CreateToken(e.ctx(e.dan), e.dan, "claude-code", 0)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := e.a.identity.AuthenticateToken(context.Background(), tok.Secret)
	if err != nil {
		t.Fatal(err)
	}
	c := e.submit(agent, "UPDATE users SET name = 'Zed' WHERE id = 1")
	if !c.ByAgent() || c.AgentName != "claude-code" || c.Author.ID != e.dan.UserID {
		t.Errorf("agent change: via %s agent %q author %s", c.Via, c.AgentName, c.Author.Email)
	}
	// Agents see counts, never rows.
	if p := c.Statements[0].Preview; p == nil || p.RowImages || p.Before != nil || p.After != nil || p.RowsAffected != 1 {
		t.Errorf("agent preview = %+v", p)
	}
	pr, err := e.a.changes.Preview(e.ctx(agent), agent, e.conn, []string{"UPDATE users SET name = 'Q' WHERE id = 2"})
	if err != nil || pr.Statements[0].Preview.Before != nil || pr.Statements[0].Preview.RowsAffected != 1 {
		t.Errorf("agent preview_change = %+v %v", pr, err)
	}
	// People see rows.
	if p := e.get(e.bob, c).Statements[0].Preview; !p.RowImages || len(p.Before) != 1 {
		t.Errorf("reviewer preview = %+v", p)
	}
	// Agents cannot review, even their owner's colleagues' changes.
	other := e.submit(e.ada, "UPDATE users SET name = 'Zoe' WHERE id = 2")
	_, err = e.a.changes.Review(e.ctx(agent), agent, other.ID, changes.ReviewInput{Decision: changes.DecisionApprove})
	wantErr(t, err, changes.ErrAgentReview)
	// An agent sees only its owner's changes.
	if _, err := e.a.changes.Get(e.ctx(agent), agent, other.ID); !errors.Is(err, changes.ErrNotFound) {
		t.Errorf("agent reading another's change = %v", err)
	}

	// The REST API, as the dashboard uses it.
	cookie := func(p identity.Principal) *http.Cookie {
		_, sess, err := e.a.identity.DevLogin(context.Background(), p.Email, identity.RoleMember, identity.Meta{})
		if err != nil {
			t.Fatal(err)
		}
		return &http.Cookie{Name: "dbairlock_session", Value: sess.Token}
	}
	bobCookie := cookie(e.bob)
	do := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		var rd *strings.Reader
		if body == nil {
			rd = strings.NewReader("")
		} else {
			raw, _ := json.Marshal(body)
			rd = strings.NewReader(string(raw))
		}
		req := httptest.NewRequest(method, path, rd)
		req.Host = "127.0.0.1:8080"
		req.RemoteAddr = "127.0.0.1:40000"
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(bobCookie)
		rec := httptest.NewRecorder()
		e.a.Handler().ServeHTTP(rec, req)
		out := map[string]any{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	code, body := do("GET", "/api/changes?view=needs_my_review", nil)
	if code != http.StatusOK || body["needs_my_review"] != float64(2) {
		t.Fatalf("list: %d %v", code, body)
	}
	code, body = do("GET", "/api/changes/"+itoa(int(c.Number)), nil)
	if code != http.StatusOK || body["via"] != "mcp" || body["agent_name"] != "claude-code" || body["server_time"] == nil {
		t.Fatalf("get: %d %v", code, body)
	}
	actions := body["actions"].(map[string]any)
	if actions["approve"] != true {
		t.Errorf("actions = %v", actions)
	}
	code, body = do("POST", "/api/changes/"+c.ID+"/reviews", map[string]any{
		"decision": "approve", "sql_hash": body["sql_hash"], "preview_hash": "stale",
	})
	if code != http.StatusConflict || body["error"].(map[string]any)["code"] != "preview_changed" {
		t.Errorf("stale approve: %d %v", code, body)
	}
	code, body = do("POST", "/api/changes", map[string]any{
		"connection_id": e.conn, "title": "Bad", "statements": []string{"UPDATE users SET name = 'x'"}, "submit": true,
	})
	if code != http.StatusUnprocessableEntity || body["error"].(map[string]any)["code"] != "statements_invalid" {
		t.Errorf("invalid submit: %d %v", code, body)
	}
	code, body = do("GET", "/api/changes/"+c.ID+"/events", nil)
	if code != http.StatusOK || len(body["events"].([]any)) != 2 {
		t.Errorf("events: %d %v", code, body)
	}
}
