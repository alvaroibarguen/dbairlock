package changes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/changes/changesdb"
	"github.com/alvaroibarguen/dbairlock/internal/connector"
	"github.com/alvaroibarguen/dbairlock/internal/store"
	"github.com/alvaroibarguen/dbairlock/internal/targets"
)

// The execution half of a change, driven by internal/executor. Every
// method here expects ctx to carry the system actor.

// Claim is a change the executor has claimed: it is executing, and that is
// committed in the store before anything touches the target (PLAN.md 5.6).
type Claim struct {
	ChangeID     string
	Number       int64
	ConnectionID string
	Revision     int
	Attempt      int
	ExecutionID  string
	// SQL and Expected hold the current revision's statements and what
	// their dry run recorded, in order.
	SQL      []string
	Expected []connector.Expectation
}

// Backoff is the wait before attempt n+1 after n attempts could not reach
// the target: 30 seconds, then 1, 2 and 5 minutes, then every 5 minutes.
func Backoff(attempts int) time.Duration {
	switch {
	case attempts <= 1:
		return 30 * time.Second
	case attempts == 2:
		return time.Minute
	case attempts == 3:
		return 2 * time.Minute
	default:
		return 5 * time.Minute
	}
}

// FailInterrupted marks every change found executing as failed with code
// interrupted (PLAN.md 5.6). Call it once at start-up, before claiming
// anything: such a change may or may not have committed on the target, so
// it is never run again.
func (s *Service) FailInterrupted(ctx context.Context) (int, error) {
	rows, err := changesdb.New(s.opts.Store.Reader()).ListExecuting(ctx)
	if err != nil {
		return 0, fmt.Errorf("changes: list executing changes: %w", err)
	}
	n := 0
	for _, r := range rows {
		err := s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
			q := changesdb.New(tx)
			cur, err := q.GetChange(ctx, r.ID)
			if err != nil {
				return fmt.Errorf("changes: read change: %w", err)
			}
			to, err := transition(Status(cur.Status), EventFail)
			if err != nil {
				return nil
			}
			now := msOf(s.now())
			if err := q.InterruptExecutions(ctx, changesdb.InterruptExecutionsParams{
				FinishedAt: someInt(now), ErrorMessage: InterruptedMessage, ChangeID: cur.ID,
			}); err != nil {
				return fmt.Errorf("changes: interrupt execution: %w", err)
			}
			if err := q.FinishChange(ctx, changesdb.FinishChangeParams{
				Status: string(to), StatusCode: FailInterrupted, StatusMessage: InterruptedMessage,
				UpdatedAt: now, ID: cur.ID,
			}); err != nil {
				return fmt.Errorf("changes: fail interrupted change: %w", err)
			}
			n++
			return s.record(ctx, tx, audit.ExecutionInterrupted, cur.ID, map[string]any{
				"number": cur.Number, "message": InterruptedMessage,
			})
		})
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// ClaimNext claims the oldest approved change that is due (its
// next_attempt_at has passed), whose connection runs approved changes
// automatically or for which execution was requested, and whose connection
// has no change executing. It returns nil when there is none.
//
// Before claiming it checks again that the change may run: a change on an
// archived connection, or whose author was removed, is cancelled; one whose
// counting approvals fell below the required number returns to review.
func (s *Service) ClaimNext(ctx context.Context) (*Claim, error) {
	now := s.now()
	cands, err := changesdb.New(s.opts.Store.Reader()).ClaimCandidates(ctx, msOf(now))
	if err != nil {
		return nil, fmt.Errorf("changes: list approved changes: %w", err)
	}
	busy := map[string]bool{}
	for _, cand := range cands {
		if busy[cand.ConnectionID] {
			continue
		}
		c, err := s.opts.Targets.Get(ctx, cand.ConnectionID)
		if err != nil {
			s.opts.Logger.Error("executor: cannot read the connection of an approved change",
				"change", cand.Number, "error", err)
			continue
		}
		if c.ArchivedAt == nil && !c.Policy.AutoExecute && !cand.ExecuteRequestedAt.Valid {
			continue
		}
		claim, err := s.claim(ctx, cand.ID, c)
		if err != nil {
			return nil, err
		}
		if claim != nil {
			return claim, nil
		}
		busy[cand.ConnectionID] = true
	}
	return nil, nil
}

func (s *Service) claim(ctx context.Context, id string, c targets.Connection) (*Claim, error) {
	var out *Claim
	err := s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		q := changesdb.New(tx)
		cur, err := q.GetChange(ctx, id)
		if err != nil {
			return fmt.Errorf("changes: read change: %w", err)
		}
		if Status(cur.Status) != StatusApproved {
			return nil
		}
		if c.ArchivedAt != nil {
			return s.cancelTx(ctx, tx, cur, CancelledArchived, "The connection was archived before the change ran.")
		}
		authors, err := s.opts.Directory.Users(ctx, tx, []string{cur.AuthorID})
		if err != nil {
			return err
		}
		if a, ok := authors[cur.AuthorID]; !ok || a.Removed() {
			return s.cancelTx(ctx, tx, cur, CancelledAuthorGone, "The author was removed.")
		}
		ok, err := s.stillApproved(ctx, tx, cur)
		if err != nil || !ok {
			return err
		}
		stmts, err := q.ListStatements(ctx, changesdb.ListStatementsParams{ChangeID: cur.ID, Revision: cur.Revision})
		if err != nil {
			return fmt.Errorf("changes: read statements: %w", err)
		}
		claim := &Claim{
			ChangeID: cur.ID, Number: cur.Number, ConnectionID: cur.ConnectionID, Revision: int(cur.Revision),
		}
		for _, st := range stmts {
			if !st.ExpectedRows.Valid {
				return fmt.Errorf("changes: change #%d statement %d has no dry-run result", cur.Number, st.Position+1)
			}
			claim.SQL = append(claim.SQL, st.SqlText)
			claim.Expected = append(claim.Expected, connector.Expectation{
				Rows: st.ExpectedRows.Int64, Fingerprint: st.ExpectedFingerprint,
			})
		}
		if len(claim.SQL) == 0 {
			return fmt.Errorf("changes: change #%d has no statements", cur.Number)
		}
		if _, err := transition(Status(cur.Status), EventClaim); err != nil {
			return err
		}
		now := msOf(s.now())
		claimed, err := q.ClaimChange(ctx, changesdb.ClaimChangeParams{UpdatedAt: now, ID: cur.ID})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil // another change on this connection is executing
			}
			return fmt.Errorf("changes: claim: %w", err)
		}
		attempts, err := q.CountExecutions(ctx, cur.ID)
		if err != nil {
			return fmt.Errorf("changes: count executions: %w", err)
		}
		claim.Attempt = int(attempts) + 1
		claim.ExecutionID = s.opts.IDs.NewID()
		if err := q.InsertExecution(ctx, changesdb.InsertExecutionParams{
			ID: claim.ExecutionID, ChangeID: cur.ID, Attempt: int64(claim.Attempt), StartedAt: now,
		}); err != nil {
			return fmt.Errorf("changes: record execution: %w", err)
		}
		if err := s.record(ctx, tx, audit.ExecutionStarted, claimed.ID, map[string]any{
			"number": claimed.Number, "attempt": claim.Attempt, "connection_id": c.ID, "connection": c.Name,
			"sql_hash": claimed.SqlHash, "preview_hash": claimed.PreviewHash, "label": ExecutionLabel(claimed.Number),
		}); err != nil {
			return err
		}
		out = claim
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RecordExecuted records that the target committed claim's change.
// Row images are stored only if the connection stores them.
func (s *Service) RecordExecuted(ctx context.Context, claim Claim, results []connector.Result) error {
	images := false
	if c, err := s.opts.Targets.Get(ctx, claim.ConnectionID); err == nil {
		images = c.Policy.StoreRowImages
	}
	outcomes := make([]StatementOutcome, len(results))
	audits := make([]map[string]any, len(results))
	for i, r := range results {
		outcomes[i] = StatementOutcome{Position: i, Schema: r.Schema, Table: r.Table, Preview: previewOf(r, images)}
		a := map[string]any{"position": i, "table": r.Schema + "." + r.Table, "rows_affected": r.RowsAffected}
		if r.Fingerprint != "" {
			a["fingerprint"] = r.Fingerprint
		}
		if len(r.Session) > 0 {
			a["session"] = r.Session
		}
		audits[i] = a
	}
	resJSON, err := encodeOpt(outcomes)
	if err != nil {
		return err
	}
	return s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		q := changesdb.New(tx)
		cur, err := q.GetChange(ctx, claim.ChangeID)
		if err != nil {
			return fmt.Errorf("changes: read change: %w", err)
		}
		to, err := transition(Status(cur.Status), EventSucceed)
		if err != nil {
			return err
		}
		now := msOf(s.now())
		if err := q.FinishExecution(ctx, changesdb.FinishExecutionParams{
			FinishedAt: someInt(now), Outcome: OutcomeExecuted, StatementResults: resJSON, ID: claim.ExecutionID,
		}); err != nil {
			return fmt.Errorf("changes: record execution: %w", err)
		}
		if err := q.FinishChange(ctx, changesdb.FinishChangeParams{
			Status: string(to), ExecutedAt: someInt(now), UpdatedAt: now, ID: cur.ID,
		}); err != nil {
			return fmt.Errorf("changes: mark executed: %w", err)
		}
		return s.record(ctx, tx, audit.ExecutionSucceeded, cur.ID, map[string]any{
			"number": cur.Number, "attempt": claim.Attempt, "statements": audits,
		})
	})
}

// Failure is why an execution failed. Nothing it attempted is committed,
// except where Code says the outcome is unknown.
type Failure struct {
	Code    string // FailDrift, FailTimeout, FailError, FailInvalid
	Message string
	Drift   *Drift
}

// RecordFailed records a final failure of claim's change. A failed change
// is terminal; the author creates a new change to try again.
func (s *Service) RecordFailed(ctx context.Context, claim Claim, f Failure) error {
	detail, err := encodeOpt(f.Drift)
	if err != nil {
		return err
	}
	return s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		q := changesdb.New(tx)
		cur, err := q.GetChange(ctx, claim.ChangeID)
		if err != nil {
			return fmt.Errorf("changes: read change: %w", err)
		}
		to, err := transition(Status(cur.Status), EventFail)
		if err != nil {
			return err
		}
		now := msOf(s.now())
		if err := q.FinishExecution(ctx, changesdb.FinishExecutionParams{
			FinishedAt: someInt(now), Outcome: OutcomeFailed, ErrorCode: f.Code, ErrorMessage: f.Message,
			Detail: detail, ID: claim.ExecutionID,
		}); err != nil {
			return fmt.Errorf("changes: record execution: %w", err)
		}
		kind := ""
		if f.Drift != nil {
			kind = string(f.Drift.Kind)
		}
		if err := q.FinishChange(ctx, changesdb.FinishChangeParams{
			Status: string(to), StatusCode: f.Code, StatusDetail: kind, StatusMessage: f.Message,
			UpdatedAt: now, ID: cur.ID,
		}); err != nil {
			return fmt.Errorf("changes: mark failed: %w", err)
		}
		payload := map[string]any{
			"number": cur.Number, "attempt": claim.Attempt, "code": f.Code, "message": f.Message,
		}
		if f.Drift != nil {
			payload["drift"] = f.Drift
		}
		return s.record(ctx, tx, audit.ExecutionFailed, cur.ID, payload)
	})
}

// RecordDeferred records that claim's change could not reach its target
// before the transaction began, so nothing ran (PLAN.md 5.6). The change
// returns to approved and waits for the next attempt by the backoff
// schedule. It returns when that is.
func (s *Service) RecordDeferred(ctx context.Context, claim Claim, cause string) (time.Time, error) {
	var next time.Time
	err := s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		q := changesdb.New(tx)
		cur, err := q.GetChange(ctx, claim.ChangeID)
		if err != nil {
			return fmt.Errorf("changes: read change: %w", err)
		}
		if _, err := transition(Status(cur.Status), EventDefer); err != nil {
			return err
		}
		now := s.now()
		attempts := cur.Attempts + 1
		next = now.Add(Backoff(int(attempts)))
		if err := q.FinishExecution(ctx, changesdb.FinishExecutionParams{
			FinishedAt: someInt(msOf(now)), Outcome: OutcomeNotStarted, ErrorCode: CodeUnavailable,
			ErrorMessage: cause, ID: claim.ExecutionID,
		}); err != nil {
			return fmt.Errorf("changes: record execution: %w", err)
		}
		if err := q.DeferChange(ctx, changesdb.DeferChangeParams{
			Attempts: attempts, NextAttemptAt: someInt(msOf(next)), LastAttemptError: cause,
			UpdatedAt: msOf(now), ID: cur.ID,
		}); err != nil {
			return fmt.Errorf("changes: defer: %w", err)
		}
		return s.record(ctx, tx, audit.ExecutionDeferred, cur.ID, map[string]any{
			"number": cur.Number, "attempt": claim.Attempt, "attempts": attempts,
			"next_attempt_at": next, "error": cause,
		})
	})
	return next, err
}
