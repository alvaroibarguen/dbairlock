package changes

import (
	"context"
	"fmt"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/changes/changesdb"
	"github.com/alvaroibarguen/dbairlock/internal/identity"
	"github.com/alvaroibarguen/dbairlock/internal/store"
)

// ReviewInput is a reviewer's decision. An approval must carry the
// sql_hash and preview_hash of the change as the reviewer saw it; a
// rejection must carry a comment saying why.
type ReviewInput struct {
	Decision    string
	Comment     string
	SQLHash     string
	PreviewHash string
}

// Review records a decision on a change in review (PLAN.md 5.5).
//
// An approval is refused when the reviewer is the author (ErrSelfReview),
// acts through an API token (ErrAgentReview), has been removed, already
// approved this SQL and preview (ErrAlreadyApproved), or when the change's
// last preview failed (ErrPreviewFailed), is older than
// policy.preview_max_age_minutes (ErrPreviewStale), or is not the one the
// reviewer saw (ErrSQLChanged, ErrPreviewChanged). The approval records the
// current sql_hash and preview_hash. When the approvals matching both from
// distinct, not removed reviewers reach the connection's required number,
// the change is approved.
//
// A rejection needs neither a fresh preview nor matching hashes, and moves
// the change to rejected.
func (s *Service) Review(ctx context.Context, p identity.Principal, ref string, in ReviewInput) (Change, error) {
	row, err := load(ctx, s.opts.Store.Reader(), ref)
	if err != nil {
		return Change{}, err
	}
	if err := identity.Can(p, identity.ActionChangeReview, connectionResource(row.ConnectionID)); err != nil {
		return Change{}, err
	}
	if isAgent(p) {
		return Change{}, ErrAgentReview
	}
	var fe fieldErrs
	comment := checkText(&fe, "comment", in.Comment, MaxComment)
	switch in.Decision {
	case DecisionApprove:
	case DecisionReject:
		if comment == "" {
			fe.add("comment", "Say why you are rejecting the change, so the author can fix it.")
		}
	default:
		fe.add("decision", "Choose approve or reject.")
	}
	if err := fe.err(); err != nil {
		return Change{}, err
	}
	c, err := s.opts.Targets.Get(ctx, row.ConnectionID)
	if err != nil {
		return Change{}, err
	}
	maxAge := s.PreviewMaxAge()
	approved := false
	err = s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		q := changesdb.New(tx)
		cur, err := q.GetChange(ctx, row.ID)
		if err != nil {
			return fmt.Errorf("changes: read change: %w", err)
		}
		if cur.AuthorID == p.UserID {
			return ErrSelfReview
		}
		if Status(cur.Status) != StatusPendingReview {
			return stateErr("This change is no longer in review; it is %s.", label(Status(cur.Status)))
		}
		if err := s.requireActive(ctx, tx, p.UserID); err != nil {
			return err
		}
		now := s.now()
		if in.Decision == DecisionApprove {
			if err := checkApprovable(cur, in, now, maxAge); err != nil {
				return err
			}
			reviews, err := q.ListReviews(ctx, cur.ID)
			if err != nil {
				return fmt.Errorf("changes: read reviews: %w", err)
			}
			for _, r := range reviews {
				if r.ReviewerID == p.UserID && r.Decision == DecisionApprove && !r.VoidedAt.Valid &&
					r.SqlHash == cur.SqlHash && r.PreviewHash == cur.PreviewHash {
					return ErrAlreadyApproved
				}
			}
		}
		reviewID := s.opts.IDs.NewID()
		if err := q.InsertReview(ctx, changesdb.InsertReviewParams{
			ID: reviewID, ChangeID: cur.ID, ReviewerID: p.UserID, Decision: in.Decision, Comment: comment,
			SqlHash: cur.SqlHash, PreviewHash: cur.PreviewHash, Revision: cur.Revision, CreatedAt: msOf(now),
		}); err != nil {
			return fmt.Errorf("changes: insert review: %w", err)
		}
		if err := s.record(ctx, tx, audit.ChangeReviewed, cur.ID, map[string]any{
			"number": cur.Number, "review_id": reviewID, "decision": in.Decision, "comment": comment,
			"sql_hash": cur.SqlHash, "preview_hash": cur.PreviewHash, "revision": cur.Revision,
		}); err != nil {
			return err
		}
		if in.Decision == DecisionReject {
			to, err := transition(Status(cur.Status), EventReject)
			if err != nil {
				return err
			}
			if err := q.MarkDecided(ctx, changesdb.MarkDecidedParams{
				Status: string(to), StatusCode: "rejected", StatusMessage: comment,
				DecidedAt: someInt(msOf(now)), UpdatedAt: msOf(now), ID: cur.ID,
			}); err != nil {
				return fmt.Errorf("changes: reject: %w", err)
			}
			return s.record(ctx, tx, audit.ChangeRejected, cur.ID, map[string]any{"number": cur.Number, "comment": comment})
		}
		count, err := s.countApprovals(ctx, tx, cur)
		if err != nil {
			return err
		}
		if count < c.Policy.RequiredApprovals {
			return nil
		}
		to, err := transition(Status(cur.Status), EventApprove)
		if err != nil {
			return err
		}
		if err := q.MarkDecided(ctx, changesdb.MarkDecidedParams{
			Status: string(to), DecidedAt: someInt(msOf(now)), UpdatedAt: msOf(now), ID: cur.ID,
		}); err != nil {
			return fmt.Errorf("changes: approve: %w", err)
		}
		approved = true
		return s.record(ctx, tx, audit.ChangeApproved, cur.ID, map[string]any{
			"number": cur.Number, "approvals": count, "required": c.Policy.RequiredApprovals,
			"sql_hash": cur.SqlHash, "preview_hash": cur.PreviewHash,
		})
	})
	if err != nil {
		return Change{}, err
	}
	if approved {
		s.wakeExecutor()
	}
	return s.Get(ctx, p, row.ID)
}

// checkApprovable applies the fresh-preview rule and the hash binding.
func checkApprovable(cur changesdb.ChangeRequest, in ReviewInput, now time.Time, maxAge time.Duration) error {
	switch {
	case cur.PreviewError != "":
		return ErrPreviewFailed
	case !cur.PreviewedAt.Valid || cur.PreviewHash == "":
		return ErrPreviewStale
	case now.Sub(time.UnixMilli(cur.PreviewedAt.Int64)) > maxAge:
		return ErrPreviewStale
	case in.SQLHash != cur.SqlHash:
		return ErrSQLChanged
	case in.PreviewHash != cur.PreviewHash:
		return ErrPreviewChanged
	}
	return nil
}

// countApprovals counts the approvals that count towards row's required
// number now: decision approve, not voided, matching the current sql_hash
// and preview_hash, from distinct reviewers who are neither the author nor
// removed. db is the caller's transaction, so a removal in the same
// transaction is seen.
func (s *Service) countApprovals(ctx context.Context, db store.DBTX, row changesdb.ChangeRequest) (int, error) {
	reviews, err := changesdb.New(db).ListReviews(ctx, row.ID)
	if err != nil {
		return 0, fmt.Errorf("changes: read reviews: %w", err)
	}
	ids := make([]string, 0, len(reviews))
	for _, r := range reviews {
		ids = append(ids, r.ReviewerID)
	}
	users, err := s.opts.Directory.Users(ctx, db, ids)
	if err != nil {
		return 0, err
	}
	counted := map[string]bool{}
	for _, r := range reviews {
		if approvalCounts(r, row, users) {
			counted[r.ReviewerID] = true
		}
	}
	return len(counted), nil
}

// approvalCounts reports whether review r counts for row now.
func approvalCounts(r changesdb.Review, row changesdb.ChangeRequest, users map[string]identity.User) bool {
	u, ok := users[r.ReviewerID]
	return r.Decision == DecisionApprove && !r.VoidedAt.Valid && ok && !u.Removed() &&
		r.ReviewerID != row.AuthorID && row.PreviewHash != "" &&
		r.SqlHash == row.SqlHash && r.PreviewHash == row.PreviewHash
}

// outdated says why an approval does not count, or "".
func outdated(r changesdb.Review, row changesdb.ChangeRequest, users map[string]identity.User) string {
	if r.Decision != DecisionApprove {
		return ""
	}
	u, ok := users[r.ReviewerID]
	switch {
	case r.VoidedAt.Valid || !ok || u.Removed():
		return OutdatedRemoved
	case r.SqlHash != row.SqlHash:
		return OutdatedSQL
	case r.PreviewHash != row.PreviewHash:
		return OutdatedPreview
	}
	return ""
}
