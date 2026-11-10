package changes

import (
	"context"
	"fmt"
	"math"
	"slices"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/changes/changesdb"
	"github.com/alvaroibarguen/dbairlock/internal/identity"
	"github.com/alvaroibarguen/dbairlock/internal/targets"
)

// Get returns change ref (its id or its number) as viewer p sees it.
// Every signed-in person who may review sees every change and its row
// images (PLAN.md section 2); agents see counts only.
func (s *Service) Get(ctx context.Context, p identity.Principal, ref string) (Change, error) {
	row, err := load(ctx, s.opts.Store.Reader(), ref)
	if err != nil {
		return Change{}, err
	}
	if err := s.canRead(p, row); err != nil {
		return Change{}, err
	}
	return s.assemble(ctx, p, row, true)
}

// canRead authorizes viewing a change. In v1 every signed-in person may see
// every change; an agent may see only changes its token's owner authored.
func (s *Service) canRead(p identity.Principal, row changesdb.ChangeRequest) error {
	if err := identity.Can(p, identity.ActionChangeSubmit, connectionResource(row.ConnectionID)); err != nil {
		return err
	}
	if isAgent(p) && row.AuthorID != p.UserID {
		return ErrNotFound
	}
	return nil
}

// assemble builds a Change from its row. With detail it includes reviews,
// executions and full previews; without, statements carry counts only.
func (s *Service) assemble(ctx context.Context, p identity.Principal, row changesdb.ChangeRequest, detail bool) (Change, error) {
	db := s.opts.Store.Reader()
	q := changesdb.New(db)
	c := fromRow(row)
	stmts, err := q.ListStatements(ctx, changesdb.ListStatementsParams{ChangeID: row.ID, Revision: row.Revision})
	if err != nil {
		return Change{}, fmt.Errorf("changes: read statements: %w", err)
	}
	reviews := []changesdb.Review{}
	if detail {
		if reviews, err = q.ListReviews(ctx, row.ID); err != nil {
			return Change{}, fmt.Errorf("changes: read reviews: %w", err)
		}
	}
	ids := []string{row.AuthorID}
	for _, r := range reviews {
		ids = append(ids, r.ReviewerID)
	}
	users, err := s.opts.Directory.Users(ctx, db, ids)
	if err != nil {
		return Change{}, err
	}
	conns := map[string]targets.Connection{}
	if err := s.fill(ctx, &c, row, stmts, reviews, users, conns, detail); err != nil {
		return Change{}, err
	}
	if detail {
		execs, err := q.ListExecutions(ctx, row.ID)
		if err != nil {
			return Change{}, fmt.Errorf("changes: read executions: %w", err)
		}
		for _, e := range execs {
			ex, err := executionFromRow(e)
			if err != nil {
				return Change{}, err
			}
			c.Executions = append(c.Executions, ex)
		}
	}
	c.Actions = s.actions(p, c, reviews)
	return visible(p, c), nil
}

// fill completes c with its connection, people, statements and reviews.
func (s *Service) fill(ctx context.Context, c *Change, row changesdb.ChangeRequest, stmts []changesdb.ChangeStatement,
	reviews []changesdb.Review, users map[string]identity.User, conns map[string]targets.Connection, detail bool) error {
	conn, ok := conns[row.ConnectionID]
	if !ok {
		var err error
		if conn, err = s.opts.Targets.Get(ctx, row.ConnectionID); err != nil {
			return err
		}
		conns[row.ConnectionID] = conn
	}
	c.ConnectionName, c.Connector = conn.Name, conn.Connector
	c.RequiredApprovals, c.AutoExecute, c.StoreRowImages = conn.Policy.RequiredApprovals, conn.Policy.AutoExecute, conn.Policy.StoreRowImages
	c.Author = person(users, row.AuthorID)
	c.PreviewMaxAge = s.PreviewMaxAge()
	c.ServerTime = s.now()
	for _, st := range stmts {
		out, err := statementFromRow(st)
		if err != nil {
			return err
		}
		if !detail {
			out.Preview, out.PreviousPreview = nil, nil
		}
		c.Statements = append(c.Statements, out)
	}
	counted := map[string]bool{}
	for _, r := range reviews {
		rv := Review{
			ID: r.ID, Reviewer: person(users, r.ReviewerID), Decision: r.Decision, Comment: r.Comment,
			SQLHash: r.SqlHash, PreviewHash: r.PreviewHash, Revision: int(r.Revision),
			CreatedAt: timeOf(r.CreatedAt), Counts: approvalCounts(r, row, users), Outdated: outdated(r, row, users),
		}
		if rv.Counts {
			counted[r.ReviewerID] = true
		}
		c.Reviews = append(c.Reviews, rv)
	}
	c.Approvals = len(counted)
	return nil
}

func person(users map[string]identity.User, id string) Person {
	u, ok := users[id]
	if !ok {
		return Person{ID: id}
	}
	return Person{ID: u.ID, Email: u.Email, Name: u.Name, Removed: u.Removed()}
}

// actions works out what p may do with c now. The service checks every
// action again when it is taken.
func (s *Service) actions(p identity.Principal, c Change, reviews []changesdb.Review) Actions {
	res := connectionResource(c.ConnectionID)
	author := p.UserID == c.Author.ID
	submit := identity.Can(p, identity.ActionChangeSubmit, res) == nil
	review := identity.Can(p, identity.ActionChangeReview, res) == nil && !isAgent(p)
	admin := identity.Can(p, identity.ActionChangeAdminister, res) == nil
	approvedAlready := slices.ContainsFunc(reviews, func(r changesdb.Review) bool {
		return r.ReviewerID == p.UserID && r.Decision == DecisionApprove && !r.VoidedAt.Valid &&
			r.SqlHash == c.SQLHash && r.PreviewHash == c.PreviewHash
	})
	pending := c.Status == StatusPendingReview
	return Actions{
		Edit:           author && submit && (c.Status == StatusDraft || pending),
		Submit:         author && submit && c.Status == StatusDraft,
		Cancel:         ((author && submit) || admin) && c.Status.Open(),
		Approve:        review && !author && pending && !approvedAlready,
		Reject:         review && !author && pending,
		RefreshPreview: identity.Can(p, identity.ActionChangeReview, res) == nil && pending,
		ExecuteNow:     ((author && submit) || admin) && c.Status == StatusApproved && !c.AutoExecute && !c.ExecuteRequested,
		RetryNow:       admin && c.Status == StatusApproved && c.NextAttemptAt != nil,
	}
}

// ListFilter selects changes for List. Zero values mean "any".
type ListFilter struct {
	// NeedsReviewBy lists changes in review that this user did not author
	// and has not yet approved (for its current SQL and preview) or
	// rejected.
	NeedsReviewBy string
	AuthorID      string
	ConnectionID  string
	Status        Status
	// Open lists only drafts, changes in review, approved and executing.
	Open bool
	// Before continues a listing: only changes numbered below it.
	Before int64
	Limit  int
}

// Page sizes for List.
const (
	DefaultPageSize = 50
	MaxPageSize     = 200
)

// Page is one page of changes, newest first.
type Page struct {
	Changes []Change
	// NextBefore continues the listing; zero when there are no more.
	NextBefore int64
	// NeedsMyReview counts the changes waiting for the viewer's review.
	NeedsMyReview int
}

// List returns one page of changes matching f, newest first. Statements
// carry kinds, tables and counts, never row images.
func (s *Service) List(ctx context.Context, p identity.Principal, f ListFilter) (Page, error) {
	if err := identity.Can(p, identity.ActionChangeSubmit, identity.Resource{Type: "connection"}); err != nil {
		return Page{}, err
	}
	if isAgent(p) {
		f.AuthorID = p.UserID // agents list their owner's changes only
	}
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultPageSize
	}
	limit = min(limit, MaxPageSize)
	before := f.Before
	if before <= 0 {
		before = math.MaxInt64
	}
	db := s.opts.Store.Reader()
	q := changesdb.New(db)
	open := int64(0)
	if f.Open {
		open = 1
	}
	rows, err := q.ListChanges(ctx, changesdb.ListChangesParams{
		BeforeNumber: before, Status: string(f.Status), ConnectionID: f.ConnectionID, AuthorID: f.AuthorID,
		OpenOnly: open, ReviewerID: f.NeedsReviewBy, PageLimit: int64(limit) + 1,
	})
	if err != nil {
		return Page{}, fmt.Errorf("changes: list: %w", err)
	}
	var page Page
	if len(rows) > limit {
		rows = rows[:limit]
		page.NextBefore = rows[limit-1].Number
	}
	ids := make([]string, len(rows))
	authors := make([]string, len(rows))
	for i, r := range rows {
		ids[i], authors[i] = r.ID, r.AuthorID
	}
	stmts := []changesdb.ChangeStatement{}
	if len(ids) > 0 {
		if stmts, err = q.ListStatementsOfChanges(ctx, ids); err != nil {
			return Page{}, fmt.Errorf("changes: list statements: %w", err)
		}
	}
	byChange := map[string][]changesdb.ChangeStatement{}
	for _, st := range stmts {
		byChange[st.ChangeID] = append(byChange[st.ChangeID], st)
	}
	users, err := s.opts.Directory.Users(ctx, db, authors)
	if err != nil {
		return Page{}, err
	}
	conns := map[string]targets.Connection{}
	for _, r := range rows {
		c := fromRow(r)
		if err := s.fill(ctx, &c, r, byChange[r.ID], nil, users, conns, false); err != nil {
			return Page{}, err
		}
		c.Approvals = -1 // not computed in lists
		page.Changes = append(page.Changes, visible(p, c))
	}
	if !isAgent(p) {
		n, err := q.CountNeedsReview(ctx, p.UserID)
		if err != nil {
			return Page{}, fmt.Errorf("changes: count: %w", err)
		}
		page.NeedsMyReview = int(n)
	}
	return page, nil
}

// Events returns the timeline of change ref: its audit events, oldest
// first. It is a filtered view of the audit log, not a separate record.
func (s *Service) Events(ctx context.Context, p identity.Principal, ref string) ([]audit.Entry, error) {
	row, err := load(ctx, s.opts.Store.Reader(), ref)
	if err != nil {
		return nil, err
	}
	if err := s.canRead(p, row); err != nil {
		return nil, err
	}
	page, err := s.opts.AuditLog.List(ctx, audit.Filter{SubjectType: "change", SubjectID: row.ID, Limit: audit.MaxPageSize})
	if err != nil {
		return nil, err
	}
	out := page.Entries
	slices.Reverse(out)
	return out, nil
}
