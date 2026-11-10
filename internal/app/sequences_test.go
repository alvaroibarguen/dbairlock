package app

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/changes"
	"github.com/alvaroibarguen/dbairlock/internal/identity"
	"github.com/alvaroibarguen/dbairlock/internal/secrets"
	"github.com/alvaroibarguen/dbairlock/internal/targets"
)

// Sequences: ordered changes across connections (migration 00009). The
// state machine is tested against real targets, like single changes: a
// fresh instance with a fake clock, and fresh seeded databases.

// addTarget adds a fresh database of srv as connection name, behind its own
// proxy.
func (e *chgEnv) addTarget(srv targetServer, name string) (string, targetDB, *proxy) {
	e.t.Helper()
	db := srv.newDB(e.t)
	px := newProxy(e.t, db.backend())
	c, err := e.a.targets.Create(e.ctx(e.ada), targets.CreateInput{
		Name: name, Connector: db.connector(), Config: db.config("127.0.0.1", px.port()),
		Password: secrets.NewValue(tgtAppPassword),
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return c.ID, db, px
}

func step(conn string, stmts ...string) changes.StepDraft {
	return changes.StepDraft{ConnectionID: conn, Statements: stmts}
}

func (e *chgEnv) submitSeq(p identity.Principal, steps ...changes.StepDraft) changes.Sequence {
	e.t.Helper()
	q, err := e.a.changes.CreateSequence(e.ctx(p), p, changes.SequenceDraft{
		Title: "Move a customer", Reason: "Ticket 12.", Steps: steps,
	})
	if err != nil {
		e.t.Fatalf("submit sequence: %v", err)
	}
	return q
}

func (e *chgEnv) seq(q changes.Sequence) changes.Sequence {
	e.t.Helper()
	got, err := e.a.changes.GetSequence(e.ctx(e.ada), e.ada, q.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return got
}

// stepOf returns step i (0-based) of q in full, as p sees it.
func (e *chgEnv) stepOf(p identity.Principal, q changes.Sequence, i int) changes.Change {
	e.t.Helper()
	return e.get(p, e.seq(q).Steps[i])
}

func (e *chgEnv) expectSeq(q changes.Sequence, status changes.SequenceStatus, current int, steps ...changes.Status) changes.Sequence {
	e.t.Helper()
	got := e.seq(q)
	if got.Status != status || got.CurrentStep != current {
		e.t.Fatalf("sequence #%d is %s at step %d (%s), want %s at step %d", got.Number, got.Status, got.CurrentStep+1,
			got.StatusMessage, status, current+1)
	}
	for i, want := range steps {
		if st := got.Steps[i]; st.Status != want {
			e.t.Fatalf("step %d (#%d) is %s (%s: %s), want %s", i+1, st.Number, st.Status, st.StatusCode, st.StatusMessage, want)
		}
	}
	return got
}

// seqActions returns the sequence's own audit actions, in order.
func (e *chgEnv) seqActions(q changes.Sequence) []string {
	e.t.Helper()
	ev, err := e.a.changes.SequenceEvents(e.ctx(e.ada), e.ada, q.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	out := make([]string, len(ev))
	for i, x := range ev {
		out[i] = string(x.Action)
	}
	return out
}

// approveStep approves the current step i of q as p, and runs the executor.
func (e *chgEnv) approveStep(p identity.Principal, q changes.Sequence, i int) {
	e.t.Helper()
	e.mustApprove(p, e.seq(q).Steps[i])
	e.run()
}

func TestSequenceStateMachine(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}
	srv := postgresTargets(t)

	t.Run("gated review, advance and complete", func(t *testing.T) {
		e := newChgEnv(t, srv, targets.PolicyPatch{})
		other, odb, _ := e.addTarget(srv, "billing")
		q := e.submitSeq(e.ada,
			step(e.conn, "UPDATE users SET name = 'S1' WHERE id = 1"),
			step(other, "UPDATE users SET name = 'S2' WHERE id = 2"),
			step(e.conn, "UPDATE users SET name = 'S3' WHERE id = 3"),
		)
		if q.Number < 1 || len(q.Steps) != 3 || q.Steps[0].Number != q.Number+1 || q.Steps[2].Number != q.Number+3 {
			t.Fatalf("numbers: sequence #%d, steps %d", q.Number, len(q.Steps))
		}
		if q.Steps[1].Title != "Move a customer · step 2 of 3" || q.Steps[1].Sequence == nil || q.Steps[1].Sequence.Step != 1 {
			t.Errorf("step 2 = %q %+v", q.Steps[1].Title, q.Steps[1].Sequence)
		}
		e.expectSeq(q, changes.SequenceRunning, 0, changes.StatusPendingReview, changes.StatusDraft, changes.StatusDraft)

		// Only step 1 was dry-run; the waiting steps have no preview.
		s2 := e.stepOf(e.bob, q, 1)
		if s2.PreviewedAt != nil || s2.PreviewHash != "" || s2.Statements[0].Preview != nil || s2.Statements[0].ExpectedRows != nil ||
			s2.Statements[0].Table != "users" {
			t.Fatalf("waiting step = %+v", s2.Statements[0])
		}
		// Gating: a waiting step cannot be reviewed, previewed or
		// submitted, and the dashboard offers none of it.
		if s2.Actions.Approve || s2.Actions.Reject || s2.Actions.RefreshPreview || s2.Actions.Submit {
			t.Errorf("waiting step actions = %+v", s2.Actions)
		}
		_, err := e.a.changes.Review(e.ctx(e.bob), e.bob, s2.ID, changes.ReviewInput{Decision: changes.DecisionApprove, SQLHash: s2.SQLHash})
		var se *changes.StateError
		if !errors.As(err, &se) || !strings.Contains(se.Message, "step 2 of sequence") {
			t.Errorf("approve a waiting step: %v", err)
		}
		_, err = e.a.changes.Review(e.ctx(e.bob), e.bob, s2.ID, changes.ReviewInput{Decision: changes.DecisionReject, Comment: "no"})
		wantErr(t, err, changes.ErrInvalidState)
		_, _, err = e.a.changes.RefreshPreview(e.ctx(e.bob), e.bob, s2.ID)
		wantErr(t, err, changes.ErrInvalidState)
		_, err = e.a.changes.Submit(e.ctx(e.ada), e.ada, s2.ID, s2.Revision)
		wantErr(t, err, changes.ErrInvalidState)
		// The usual review rules hold on the current step.
		_, err = e.approve(e.ada, q.Steps[0])
		wantErr(t, err, changes.ErrSelfReview)

		e.clock.Advance(time.Minute)
		e.approveStep(e.bob, q, 0)
		if e.name(1) != "S1" {
			t.Fatalf("step 1 did not run")
		}
		// Step 2 was previewed right after step 1 ran, and is in review
		// with the normal policy.
		e.expectSeq(q, changes.SequenceRunning, 1, changes.StatusExecuted, changes.StatusPendingReview, changes.StatusDraft)
		s2 = e.stepOf(e.bob, q, 1)
		if s2.PreviewedAt == nil || !s2.PreviewedAt.Equal(e.clock.Now()) || s2.SubmittedAt == nil ||
			s2.Statements[0].ExpectedRows == nil || *s2.Statements[0].ExpectedRows != 1 || !s2.Actions.Approve {
			t.Fatalf("step 2 after step 1 ran: previewed %v rows %v actions %+v", s2.PreviewedAt, s2.Statements[0].ExpectedRows, s2.Actions)
		}
		if odb.value(t, "SELECT name FROM users WHERE id = 2") != "Bob" {
			t.Fatal("step 2 ran before it was approved")
		}
		// Its freshness window starts at that preview.
		e.clock.Advance(5*time.Minute + time.Second)
		_, err = e.approve(e.bob, s2)
		wantErr(t, err, changes.ErrPreviewStale)
		if _, _, err := e.a.changes.RefreshPreview(e.ctx(e.bob), e.bob, s2.ID); err != nil {
			t.Fatal(err)
		}
		e.approveStep(e.bob, q, 1)
		e.approveStep(e.cat, q, 2)
		e.expectSeq(q, changes.SequenceCompleted, 2, changes.StatusExecuted, changes.StatusExecuted, changes.StatusExecuted)
		if odb.value(t, "SELECT name FROM users WHERE id = 2") != "S2" || e.name(3) != "S3" {
			t.Fatal("steps 2 and 3 did not run")
		}
		want := "sequence.created,sequence.submitted,sequence.advanced,sequence.advanced,sequence.completed"
		if got := strings.Join(e.seqActions(q), ","); got != want {
			t.Errorf("sequence timeline = %s", got)
		}
		want = "change.created,change.submitted,change.preview_refreshed,change.reviewed,change.approved,execution.started,execution.succeeded"
		if got := strings.Join(e.actions(s2), ","); got != want {
			t.Errorf("step 2 timeline = %s", got)
		}
		// Finished: nothing left to cancel.
		_, err = e.a.changes.CancelSequence(e.ctx(e.ada), e.ada, q.ID, "")
		wantErr(t, err, changes.ErrInvalidState)
	})

	t.Run("a step depends on the one before", func(t *testing.T) {
		e := newChgEnv(t, srv, targets.PolicyPatch{})
		// Step 2 matches no rows until step 1 has run: it could not be
		// dry-run at submission, and is not.
		q := e.submitSeq(e.ada,
			step(e.conn, "INSERT INTO users (id, email, name, status) VALUES (6, 'fay@example.com', 'Fay', 'active')"),
			step(e.conn, "UPDATE users SET status = 'banned' WHERE id = 6"),
		)
		e.approveStep(e.bob, q, 0)
		s2 := e.stepOf(e.bob, q, 1)
		if s2.Status != changes.StatusPendingReview || s2.PreviewError != "" || *s2.Statements[0].ExpectedRows != 1 {
			t.Fatalf("step 2 = %s %q", s2.Status, s2.PreviewError)
		}
		e.approveStep(e.bob, q, 1)
		e.expectSeq(q, changes.SequenceCompleted, 1)
		if v := e.db.value(t, "SELECT status FROM users WHERE id = 6"); v != "banned" {
			t.Errorf("status = %s", v)
		}
	})

	t.Run("the next step's preview fails", func(t *testing.T) {
		e := newChgEnv(t, srv, targets.PolicyPatch{})
		q := e.submitSeq(e.ada,
			step(e.conn, "DELETE FROM users WHERE id = 5"),
			step(e.conn, "UPDATE users SET name = 'Gone' WHERE id = 5"),
		)
		e.approveStep(e.bob, q, 0)
		// Submitted anyway, with the failure stored: like a failed preview
		// update, it cannot be approved.
		s2 := e.expectStatus(e.seq(q).Steps[1], changes.StatusPendingReview)
		if !strings.Contains(s2.PreviewError, "matches no rows") {
			t.Fatalf("preview error = %q", s2.PreviewError)
		}
		_, err := e.approve(e.bob, s2)
		wantErr(t, err, changes.ErrPreviewFailed)
		// A rejection stops the sequence; step 1 stays applied.
		if _, err := e.a.changes.Review(e.ctx(e.bob), e.bob, s2.ID, changes.ReviewInput{Decision: changes.DecisionReject, Comment: "Row is gone."}); err != nil {
			t.Fatal(err)
		}
		got := e.expectSeq(q, changes.SequenceStopped, 1, changes.StatusExecuted, changes.StatusRejected)
		if got.StatusCode != changes.StopRejected || got.StatusMessage != "Stopped: step 2 was rejected." {
			t.Errorf("stopped = %s %q", got.StatusCode, got.StatusMessage)
		}
	})

	t.Run("the database is unreachable when the next step is previewed", func(t *testing.T) {
		e := newChgEnv(t, srv, targets.PolicyPatch{})
		other, _, px := e.addTarget(srv, "billing")
		q := e.submitSeq(e.ada,
			step(e.conn, "UPDATE users SET name = 'S1' WHERE id = 1"),
			step(other, "UPDATE users SET name = 'S2' WHERE id = 2"),
		)
		px.setDown(true)
		e.approveStep(e.bob, q, 0)
		s2 := e.expectStatus(e.seq(q).Steps[1], changes.StatusPendingReview)
		if !strings.Contains(s2.PreviewError, "connect") {
			t.Fatalf("preview error = %q", s2.PreviewError)
		}
		px.setDown(false)
		e.clock.Advance(changes.RefreshInterval)
		if _, res, err := e.a.changes.RefreshPreview(e.ctx(e.bob), e.bob, s2.ID); err != nil || !res.OK {
			t.Fatalf("refresh: %+v %v", res, err)
		}
		e.approveStep(e.bob, q, 1)
		e.expectSeq(q, changes.SequenceCompleted, 1)
	})

	for _, tc := range []struct {
		name    string
		code    string
		message string
		stop    func(e *chgEnv, q changes.Sequence)
	}{
		{"stops when the current step is rejected", changes.StopRejected, "Stopped: step 2 was rejected.", func(e *chgEnv, q changes.Sequence) {
			if _, err := e.a.changes.Review(e.ctx(e.bob), e.bob, q.Steps[1].ID, changes.ReviewInput{Decision: changes.DecisionReject, Comment: "Wrong row."}); err != nil {
				e.t.Fatal(err)
			}
		}},
		{"stops when the current step fails", changes.StopFailed, "Stopped: step 2 failed.", func(e *chgEnv, q changes.Sequence) {
			e.mustApprove(e.bob, e.seq(q).Steps[1])
			// Someone else changes the value step 2 would overwrite: drift.
			e.db.exec(e.t, "UPDATE users SET name = 'Changed' WHERE id = 2")
			e.run()
			if st := e.expectStatus(q.Steps[1], changes.StatusFailed); st.StatusCode != changes.FailDrift {
				e.t.Fatalf("step 2 failed with %s", st.StatusCode)
			}
		}},
		{"stops when the current step expires", changes.StopExpired, "Stopped: step 2 expired.", func(e *chgEnv, _ changes.Sequence) {
			e.clock.Advance(73 * time.Hour)
			e.run()
		}},
		{"stops when the author cancels", changes.StopCancelled, "Stopped: step 2 was cancelled.", func(e *chgEnv, q changes.Sequence) {
			// Another member cannot.
			_, err := e.a.changes.CancelSequence(e.ctx(e.bob), e.bob, q.ID, "")
			wantErr(e.t, err, identity.ErrForbidden)
			if _, err := e.a.changes.CancelSequence(e.ctx(e.ada), e.ada, q.ID, "Not needed."); err != nil {
				e.t.Fatal(err)
			}
		}},
		{"stops when a step is cancelled on its own", changes.StopCancelled, "Stopped: step 2 was cancelled.", func(e *chgEnv, q changes.Sequence) {
			if _, err := e.a.changes.Cancel(e.ctx(e.ada), e.ada, q.Steps[1].ID, ""); err != nil {
				e.t.Fatal(err)
			}
		}},
		{"stops when the author is removed", changes.StopCancelled, "Stopped: step 2 was cancelled.", func(e *chgEnv, _ changes.Sequence) {
			if _, err := e.a.identity.RemoveUser(e.ctx(e.ada), e.ada, e.cat.UserID); err != nil {
				e.t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newChgEnv(t, srv, targets.PolicyPatch{})
			author := e.ada
			if strings.Contains(tc.name, "removed") {
				author = e.cat
			}
			q := e.submitSeq(author,
				step(e.conn, "UPDATE users SET name = 'S1' WHERE id = 1"),
				step(e.conn, "UPDATE users SET name = 'S2' WHERE id = 2"),
				step(e.conn, "UPDATE users SET name = 'S3' WHERE id = 3"),
			)
			e.approveStep(e.dan, q, 0)
			tc.stop(e, q)
			got := e.expectSeq(q, changes.SequenceStopped, 1, changes.StatusExecuted)
			if got.StatusCode != tc.code || got.StatusMessage != tc.message {
				t.Errorf("stopped: %s %q", got.StatusCode, got.StatusMessage)
			}
			s3 := got.Steps[2]
			if s3.Status != changes.StatusCancelled || s3.StatusCode != changes.CancelledSequenceStopped || s3.StatusMessage != tc.message {
				t.Errorf("step 3: %s %s %q", s3.Status, s3.StatusCode, s3.StatusMessage)
			}
			// Step 1 stays applied; nothing after it ran.
			e.run()
			if e.name(1) != "S1" || e.name(3) != "Cat" {
				t.Errorf("names: %s %s", e.name(1), e.name(3))
			}
			if got.Actions.Cancel {
				t.Error("a stopped sequence offers Cancel")
			}
			acts := e.seqActions(q)
			if acts[len(acts)-1] != string(audit.SequenceStopped) || e.countAction(s3, audit.ChangeCancelled) != 1 {
				t.Errorf("timeline = %v", acts)
			}
		})
	}

	t.Run("submission checks every step and creates nothing on a problem", func(t *testing.T) {
		e := newChgEnv(t, srv, targets.PolicyPatch{})
		before := e.count("SELECT count(*) FROM change_requests")
		_, err := e.a.changes.CreateSequence(e.ctx(e.ada), e.ada, changes.SequenceDraft{
			Title: "One step", Steps: []changes.StepDraft{step(e.conn, "UPDATE users SET name = 'x' WHERE id = 1")},
		})
		var ie *changes.InputError
		if !errors.As(err, &ie) || ie.Fields[0].Field != "steps" {
			t.Errorf("one step: %v", err)
		}
		// A later step is validated (dialect, one table, a WHERE) though
		// not dry-run.
		_, err = e.a.changes.CreateSequence(e.ctx(e.ada), e.ada, changes.SequenceDraft{Title: "Bad", Steps: []changes.StepDraft{
			step(e.conn, "UPDATE users SET name = 'x' WHERE id = 1"),
			step(e.conn, "UPDATE users SET name = 'y'"),
		}})
		var stepErr *changes.StepError
		if !errors.As(err, &stepErr) || stepErr.Step != 1 || !errors.Is(err, changes.ErrInvalid) {
			t.Errorf("invalid step 2: %v", err)
		}
		// Step 1 is dry-run.
		_, err = e.a.changes.CreateSequence(e.ctx(e.ada), e.ada, changes.SequenceDraft{Title: "No rows", Steps: []changes.StepDraft{
			step(e.conn, "UPDATE users SET name = 'x' WHERE id = 99"),
			step(e.conn, "UPDATE users SET name = 'y' WHERE id = 1"),
		}})
		if !errors.As(err, &stepErr) || stepErr.Step != 0 {
			t.Errorf("step 1 matching no rows: %v", err)
		}
		_, err = e.a.changes.CreateSequence(e.ctx(e.ada), e.ada, changes.SequenceDraft{Title: "Bad", Steps: []changes.StepDraft{
			step(e.conn, "UPDATE users SET name = 'x' WHERE id = 1"), step("nope", "DELETE FROM users WHERE id = 1"),
		}})
		if !errors.As(err, &ie) || ie.Fields[0].Field != "steps[1].connection_id" {
			t.Errorf("unknown connection: %v", err)
		}
		if n := e.count("SELECT count(*) FROM change_requests"); n != before {
			t.Fatal("a refused sequence created changes")
		}
		// A retry returns the running sequence.
		steps := []changes.StepDraft{
			step(e.conn, "UPDATE users SET name = 'x' WHERE id = 1"), step(e.conn, "UPDATE users SET name = 'y' WHERE id = 2"),
		}
		q := e.submitSeq(e.ada, steps...)
		again := e.submitSeq(e.ada, steps...)
		if again.ID != q.ID || !again.AlreadyExisted {
			t.Errorf("retry created #%d", again.Number)
		}
		// Steps are exempt from the single-change duplicate rule.
		if c, err := e.submitOn(e.ada, e.conn, "UPDATE users SET name = 'x' WHERE id = 1"); err != nil || c.AlreadyExisted {
			t.Errorf("single change with step 1's SQL: %v %v", err, c.AlreadyExisted)
		}
	})

	t.Run("editing a waiting step validates it without a dry run", func(t *testing.T) {
		e := newChgEnv(t, srv, targets.PolicyPatch{})
		q := e.submitSeq(e.ada,
			step(e.conn, "UPDATE users SET name = 'S1' WHERE id = 1"),
			step(e.conn, "UPDATE users SET name = 'S2' WHERE id = 2"),
		)
		s2 := q.Steps[1]
		_, err := e.a.changes.Edit(e.ctx(e.ada), e.ada, s2.ID, changes.EditInput{Revision: s2.Revision, Statements: []string{"DELETE FROM users"}})
		wantErr(t, err, changes.ErrInvalid)
		// Matches no rows now, which is fine for a step that waits.
		edited, err := e.a.changes.Edit(e.ctx(e.ada), e.ada, s2.ID, changes.EditInput{
			Revision: s2.Revision, Statements: []string{"UPDATE users SET name = 'S2' WHERE id = 42"},
		})
		if err != nil || edited.Status != changes.StatusDraft || edited.PreviewError != "" || edited.Statements[0].Preview != nil {
			t.Fatalf("edit: %v %s %q", err, edited.Status, edited.PreviewError)
		}
		// Only the author edits.
		_, err = e.a.changes.Edit(e.ctx(e.bob), e.bob, s2.ID, changes.EditInput{Revision: edited.Revision, Title: strp("x")})
		wantErr(t, err, changes.ErrNotAuthor)
	})

	t.Run("agents submit and follow but never see rows or approve", func(t *testing.T) {
		e := newChgEnv(t, srv, targets.PolicyPatch{})
		tok, err := e.a.identity.CreateToken(e.ctx(e.dan), e.dan, "agent", 0)
		if err != nil {
			t.Fatal(err)
		}
		agent, err := e.a.identity.AuthenticateToken(context.Background(), tok.Secret)
		if err != nil {
			t.Fatal(err)
		}
		q, err := e.a.changes.CreateSequence(e.ctx(agent), agent, changes.SequenceDraft{Title: "By an agent", Steps: []changes.StepDraft{
			step(e.conn, "UPDATE users SET name = 'S1' WHERE id = 1"), step(e.conn, "UPDATE users SET name = 'S2' WHERE id = 2"),
		}})
		if err != nil {
			t.Fatal(err)
		}
		if !q.ByAgent() || q.AgentName != "agent" || !q.Steps[0].ByAgent() {
			t.Errorf("agent sequence = %v %q", q.Via, q.AgentName)
		}
		got, err := e.a.changes.Get(e.ctx(agent), agent, q.Steps[0].ID)
		if err != nil || got.Statements[0].Preview == nil || got.Statements[0].Preview.Before != nil || got.Statements[0].Preview.RowImages {
			t.Fatalf("agent sees rows: %v", err)
		}
		_, err = e.a.changes.Review(e.ctx(agent), agent, q.Steps[0].ID, changes.ReviewInput{Decision: changes.DecisionApprove})
		wantErr(t, err, changes.ErrAgentReview)
		// Another person's sequence does not exist for the agent.
		mine := e.submitSeq(e.bob, step(e.conn, "UPDATE users SET name = 'x' WHERE id = 3"), step(e.conn, "UPDATE users SET name = 'y' WHERE id = 4"))
		_, err = e.a.changes.GetSequence(e.ctx(agent), agent, strconv.FormatInt(mine.Number, 10))
		wantErr(t, err, changes.ErrNotFound)
	})
}

// A real Postgres -> MySQL -> Postgres sequence: a step that depends on an
// earlier one, then a failure that stops a second sequence.
func TestSequenceAcrossDatabases(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}
	pg := postgresTargets(t)
	my := mysqlTargets(t)
	e := newChgEnv(t, pg, targets.PolicyPatch{})
	billing, mdb, _ := e.addTarget(my, "billing")

	t.Run("completes", func(t *testing.T) {
		q := e.submitSeq(e.ada,
			step(e.conn, "INSERT INTO users (id, email, name, status) VALUES (6, 'fay@example.com', 'Fay', 'active')"),
			step(billing, "INSERT INTO users (id, email, name, status) VALUES (6, 'fay@example.com', 'Fay', 'active')",
				"UPDATE users SET status = 'inactive' WHERE id = 5"),
			step(e.conn, "UPDATE users SET status = 'migrated' WHERE id = 6"),
		)
		if q.Steps[0].Connector != "postgres" || q.Steps[1].Connector != "mysql" || q.Steps[2].Connector != "postgres" {
			t.Fatalf("connectors: %s %s %s", q.Steps[0].Connector, q.Steps[1].Connector, q.Steps[2].Connector)
		}
		e.approveStep(e.bob, q, 0)
		e.approveStep(e.bob, q, 1)
		// Step 3 matched no rows at submission; after step 1 it matches one.
		if s3 := e.stepOf(e.bob, q, 2); s3.PreviewError != "" || *s3.Statements[0].ExpectedRows != 1 {
			t.Fatalf("step 3 preview: %q", s3.PreviewError)
		}
		e.approveStep(e.bob, q, 2)
		e.expectSeq(q, changes.SequenceCompleted, 2, changes.StatusExecuted, changes.StatusExecuted, changes.StatusExecuted)
		if v := e.db.value(t, "SELECT status FROM users WHERE id = 6"); v != "migrated" {
			t.Errorf("postgres status = %s", v)
		}
		if v := mdb.value(t, "SELECT CONCAT(name, '/', (SELECT status FROM users WHERE id = 5)) FROM users WHERE id = 6"); v != "Fay/inactive" {
			t.Errorf("mysql = %s", v)
		}
		// Each step ran with its own label on its own database.
		for i, st := range e.seq(q).Steps {
			full := e.get(e.ada, st)
			if len(full.Executions) != 1 || full.Executions[0].Outcome != changes.OutcomeExecuted {
				t.Errorf("step %d executions = %+v", i+1, full.Executions)
			}
		}
	})

	t.Run("a failure stops it and earlier steps stay applied", func(t *testing.T) {
		q := e.submitSeq(e.ada,
			step(e.conn, "UPDATE users SET name = 'Ann2' WHERE id = 1"),
			step(billing, "UPDATE users SET name = 'Bob2' WHERE id = 2"),
			step(e.conn, "UPDATE users SET name = 'Cat2' WHERE id = 3"),
		)
		e.approveStep(e.bob, q, 0)
		e.mustApprove(e.bob, e.seq(q).Steps[1])
		mdb.exec(t, "UPDATE users SET name = 'Someone else' WHERE id = 2")
		e.run()
		got := e.expectSeq(q, changes.SequenceStopped, 1, changes.StatusExecuted, changes.StatusFailed, changes.StatusCancelled)
		if got.StatusMessage != "Stopped: step 2 failed." {
			t.Errorf("message = %q", got.StatusMessage)
		}
		if e.name(1) != "Ann2" || e.name(3) != "Cat" || mdb.value(t, "SELECT name FROM users WHERE id = 2") != "Someone else" {
			t.Errorf("rows: %s %s %s", e.name(1), e.name(3), mdb.value(t, "SELECT name FROM users WHERE id = 2"))
		}
		ev, err := e.a.changes.SequenceEvents(e.ctx(e.ada), e.ada, q.ID)
		if err != nil {
			t.Fatal(err)
		}
		last := ev[len(ev)-1]
		if last.Action != audit.SequenceStopped || !strings.Contains(string(last.Payload), `"applied_steps":[1]`) ||
			!strings.Contains(string(last.Payload), `"cancelled_steps":[3]`) {
			t.Errorf("stopped event = %s %s", last.Action, last.Payload)
		}
	})
}
