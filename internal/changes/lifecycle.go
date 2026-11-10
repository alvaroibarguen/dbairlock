package changes

import (
	"context"
	"errors"
	"fmt"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/changes/changesdb"
	"github.com/alvaroibarguen/dbairlock/internal/identity"
	"github.com/alvaroibarguen/dbairlock/internal/store"
)

// Status codes of cancelled, expired and returned changes.
const (
	CancelledByAuthor     = "cancelled_by_author"
	CancelledByAdmin      = "cancelled_by_admin"
	CancelledAuthorGone   = "author_removed"
	CancelledArchived     = "connection_archived"
	ExpiredCode           = "expired"
	ReturnedApprovalsLost = "approvals_lost"
)

// mayActFor reports whether p may act on row as its author or as an admin.
func mayActFor(p identity.Principal, row changesdb.ChangeRequest) bool {
	if row.AuthorID == p.UserID && identity.Can(p, identity.ActionChangeSubmit, connectionResource(row.ConnectionID)) == nil {
		return true
	}
	return identity.Can(p, identity.ActionChangeAdminister, connectionResource(row.ConnectionID)) == nil
}

// Cancel cancels a draft, a change in review or an approved change. The
// author or an admin may cancel; a change already executing cannot be.
func (s *Service) Cancel(ctx context.Context, p identity.Principal, ref, reason string) (Change, error) {
	row, err := load(ctx, s.opts.Store.Reader(), ref)
	if err != nil {
		return Change{}, err
	}
	if p.UserID == "" {
		return Change{}, identity.ErrUnauthenticated
	}
	if !mayActFor(p, row) {
		return Change{}, identity.ErrForbidden
	}
	var fe fieldErrs
	reason = checkText(&fe, "reason", reason, MaxComment)
	if err := fe.err(); err != nil {
		return Change{}, err
	}
	code := CancelledByAuthor
	if row.AuthorID != p.UserID {
		code = CancelledByAdmin
	}
	err = s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		q := changesdb.New(tx)
		cur, err := q.GetChange(ctx, row.ID)
		if err != nil {
			return fmt.Errorf("changes: read change: %w", err)
		}
		if err := s.requireActive(ctx, tx, p.UserID); err != nil {
			return err
		}
		if _, err := transition(Status(cur.Status), EventCancel); err != nil {
			return stateErr("A change that is %s cannot be cancelled.", label(Status(cur.Status)))
		}
		return s.cancelTx(ctx, tx, cur, code, reason)
	})
	if err != nil {
		return Change{}, err
	}
	return s.Get(ctx, p, row.ID)
}

// cancelTx cancels row inside tx with a status code and message.
func (s *Service) cancelTx(ctx context.Context, tx *store.Tx, row changesdb.ChangeRequest, code, message string) error {
	to, err := transition(Status(row.Status), EventCancel)
	if err != nil {
		return err
	}
	if err := changesdb.New(tx).SetStatus(ctx, changesdb.SetStatusParams{
		Status: string(to), StatusCode: code, StatusMessage: message, UpdatedAt: msOf(s.now()), ID: row.ID,
	}); err != nil {
		return fmt.Errorf("changes: cancel: %w", err)
	}
	return s.record(ctx, tx, audit.ChangeCancelled, row.ID, map[string]any{
		"number": row.Number, "from": row.Status, "code": code, "reason": message,
	})
}

// ExecuteNow asks the executor to run an approved change on a connection
// that does not execute automatically. The author or an admin may.
func (s *Service) ExecuteNow(ctx context.Context, p identity.Principal, ref string) (Change, error) {
	row, err := load(ctx, s.opts.Store.Reader(), ref)
	if err != nil {
		return Change{}, err
	}
	if p.UserID == "" {
		return Change{}, identity.ErrUnauthenticated
	}
	if !mayActFor(p, row) {
		return Change{}, identity.ErrForbidden
	}
	c, err := s.opts.Targets.Get(ctx, row.ConnectionID)
	if err != nil {
		return Change{}, err
	}
	if c.Policy.AutoExecute {
		return Change{}, stateErr("This connection runs approved changes automatically.")
	}
	err = s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		q := changesdb.New(tx)
		cur, err := q.GetChange(ctx, row.ID)
		if err != nil {
			return fmt.Errorf("changes: read change: %w", err)
		}
		if Status(cur.Status) != StatusApproved {
			return stateErr("Only an approved change can be executed; this one is %s.", label(Status(cur.Status)))
		}
		if cur.ExecuteRequestedAt.Valid {
			return nil
		}
		if err := s.requireActive(ctx, tx, p.UserID); err != nil {
			return err
		}
		now := msOf(s.now())
		if err := q.RequestExecution(ctx, changesdb.RequestExecutionParams{
			ExecuteRequestedAt: someInt(now), UpdatedAt: now, ID: cur.ID,
		}); err != nil {
			return fmt.Errorf("changes: request execution: %w", err)
		}
		return s.record(ctx, tx, audit.ChangeExecuteRequested, cur.ID, map[string]any{"number": cur.Number})
	})
	if err != nil {
		return Change{}, err
	}
	s.wakeExecutor()
	return s.Get(ctx, p, row.ID)
}

// RetryNow clears the wait of an approved change whose target could not be
// reached, so the executor tries again at once. Admins only.
func (s *Service) RetryNow(ctx context.Context, p identity.Principal, ref string) (Change, error) {
	row, err := load(ctx, s.opts.Store.Reader(), ref)
	if err != nil {
		return Change{}, err
	}
	if err := identity.Can(p, identity.ActionChangeAdminister, connectionResource(row.ConnectionID)); err != nil {
		return Change{}, err
	}
	err = s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		q := changesdb.New(tx)
		cur, err := q.GetChange(ctx, row.ID)
		if err != nil {
			return fmt.Errorf("changes: read change: %w", err)
		}
		if Status(cur.Status) != StatusApproved || !cur.NextAttemptAt.Valid {
			return stateErr("Only an approved change that is waiting for its database can be retried.")
		}
		now := msOf(s.now())
		if err := q.ClearNextAttempt(ctx, changesdb.ClearNextAttemptParams{UpdatedAt: now, ID: cur.ID}); err != nil {
			return fmt.Errorf("changes: retry: %w", err)
		}
		return s.record(ctx, tx, audit.ChangeRetryRequested, cur.ID, map[string]any{
			"number": cur.Number, "attempts": cur.Attempts,
		})
	})
	if err != nil {
		return Change{}, err
	}
	s.wakeExecutor()
	return s.Get(ctx, p, row.ID)
}

// OnUserRemoved runs inside the transaction that removes a user (phase 4).
// It cancels every change the user authored that is a draft, in review or
// approved, with the reason "author removed"; a change already executing
// is left to finish. It voids the user's reviews on open changes, so their
// approvals stop counting even if the user is restored later; an approved
// change that falls below its required approvals returns to review.
func (s *Service) OnUserRemoved(ctx context.Context, tx *store.Tx, ev identity.UserRemoved) error {
	q := changesdb.New(tx)
	own, err := q.ListOpenByAuthor(ctx, ev.UserID)
	if err != nil {
		return fmt.Errorf("changes: list changes of removed user: %w", err)
	}
	for _, row := range own {
		if err := s.cancelTx(ctx, tx, row, CancelledAuthorGone, "The author was removed."); err != nil {
			return err
		}
	}
	now := msOf(s.now())
	voided, err := q.VoidReviewsOf(ctx, changesdb.VoidReviewsOfParams{
		VoidedAt: someInt(now), VoidReason: OutdatedRemoved, ReviewerID: ev.UserID,
	})
	if err != nil {
		return fmt.Errorf("changes: void reviews: %w", err)
	}
	seen := map[string]bool{}
	for _, id := range voided {
		if seen[id] {
			continue
		}
		seen[id] = true
		row, err := q.GetChange(ctx, id)
		if err != nil {
			return fmt.Errorf("changes: read change: %w", err)
		}
		if err := s.record(ctx, tx, audit.ChangeApprovalsVoided, row.ID, map[string]any{
			"number": row.Number, "reviewer_id": ev.UserID, "reason": OutdatedRemoved,
		}); err != nil {
			return err
		}
		if Status(row.Status) != StatusApproved {
			continue
		}
		if _, err := s.stillApproved(ctx, tx, row); err != nil {
			return err
		}
	}
	return nil
}

// stillApproved returns an approved change to review if its counting
// approvals are now below the connection's required number. It reports
// whether the change still stands approved.
func (s *Service) stillApproved(ctx context.Context, tx *store.Tx, row changesdb.ChangeRequest) (bool, error) {
	c, err := s.opts.Targets.Get(ctx, row.ConnectionID)
	if err != nil {
		return false, err
	}
	count, err := s.countApprovals(ctx, tx, row)
	if err != nil {
		return false, err
	}
	if count >= c.Policy.RequiredApprovals {
		return true, nil
	}
	to, err := transition(Status(row.Status), EventApprovalLost)
	if err != nil {
		return false, err
	}
	msg := fmt.Sprintf("Back in review: %d of %d required approvals still count.", count, c.Policy.RequiredApprovals)
	if err := changesdb.New(tx).ReturnToReview(ctx, changesdb.ReturnToReviewParams{
		StatusCode: ReturnedApprovalsLost, StatusMessage: msg, UpdatedAt: msOf(s.now()), ID: row.ID,
	}); err != nil {
		return false, fmt.Errorf("changes: return to review: %w", err)
	}
	return false, s.record(ctx, tx, audit.ChangeReturnedToReview, row.ID, map[string]any{
		"number": row.Number, "to": to, "approvals": count, "required": c.Policy.RequiredApprovals,
	})
}

// ExpireDue expires every change in review or approved whose approval
// lifetime has ended. ctx must carry the system actor.
func (s *Service) ExpireDue(ctx context.Context) (int, error) {
	now := s.now()
	due, err := changesdb.New(s.opts.Store.Reader()).ListDueForExpiry(ctx, someInt(msOf(now)))
	if err != nil {
		return 0, fmt.Errorf("changes: list due changes: %w", err)
	}
	n := 0
	for _, d := range due {
		err := s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
			q := changesdb.New(tx)
			cur, err := q.GetChange(ctx, d.ID)
			if err != nil {
				return fmt.Errorf("changes: read change: %w", err)
			}
			if !cur.ExpiresAt.Valid || cur.ExpiresAt.Int64 > msOf(now) {
				return nil
			}
			to, err := transition(Status(cur.Status), EventExpire)
			if err != nil {
				return nil // it moved on (for example it is executing)
			}
			msg := "The approval lifetime ended before the change ran."
			if Status(cur.Status) == StatusPendingReview {
				msg = "The approval lifetime ended before the change was approved."
			}
			if err := q.SetStatus(ctx, changesdb.SetStatusParams{
				Status: string(to), StatusCode: ExpiredCode, StatusMessage: msg, UpdatedAt: msOf(now), ID: cur.ID,
			}); err != nil {
				return fmt.Errorf("changes: expire: %w", err)
			}
			n++
			return s.record(ctx, tx, audit.ChangeExpired, cur.ID, map[string]any{
				"number": cur.Number, "from": cur.Status, "expires_at": optTime(cur.ExpiresAt), "attempts": cur.Attempts,
			})
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			return n, err
		}
	}
	return n, nil
}
