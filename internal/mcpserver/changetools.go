package mcpserver

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/alvaroibarguen/dbairlock/internal/changes"
)

// Change tools are thin adapters over the changes module, which applies
// every rule: agents see counts and never row images, see only their token
// owner's changes, and cannot review. Outputs below copy identifiers,
// counts, statuses and codes only; nothing here carries a value from a
// target database.

// Page sizes of list_changes.
const (
	defaultListLimit = 20
	maxListLimit     = 50
)

// StatementError is why a statement is invalid or failed its dry run.
type StatementError struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	Position int    `json:"position,omitempty" jsonschema:"1-based character position in the statement the message refers to, when known"`
}

// StatementCheck is one statement's result in preview_change.
type StatementCheck struct {
	Statement     int             `json:"statement" jsonschema:"1-based position in the statements list"`
	Valid         bool            `json:"valid" jsonschema:"True when the statement passed validation"`
	Kind          string          `json:"kind,omitempty" jsonschema:"INSERT, UPDATE or DELETE"`
	Table         string          `json:"table,omitempty" jsonschema:"Fully qualified table"`
	RowsAffected  *int64          `json:"rows_affected,omitempty" jsonschema:"Rows the statement would affect (from a rolled-back dry run)"`
	HasPrimaryKey bool            `json:"has_primary_key" jsonschema:"Whether the table has a primary key"`
	Error         *StatementError `json:"error,omitempty"`
}

// StatementCount is one statement of a change and the rows it affects.
type StatementCount struct {
	Statement    int    `json:"statement" jsonschema:"1-based position in the statements list"`
	Kind         string `json:"kind"`
	Table        string `json:"table"`
	RowsAffected *int64 `json:"rows_affected,omitempty" jsonschema:"Rows the statement affects, as found by the dry run"`
}

type previewIn struct {
	Connection string   `json:"connection" jsonschema:"Connection name from list_connections"`
	Statements []string `json:"statements" jsonschema:"SQL statements, one per entry, run in order in one transaction"`
}

type previewOut struct {
	OK               bool             `json:"ok" jsonschema:"True when every statement is valid and the dry run succeeded: submit_change would accept them as they are"`
	Problem          string           `json:"problem,omitempty" jsonschema:"Why the dry run as a whole could not run"`
	Statements       []StatementCheck `json:"statements"`
	AlreadySubmitted int64            `json:"already_submitted,omitempty" jsonschema:"Number of your open change with exactly this SQL; submit_change would return it"`
}

type submitIn struct {
	Connection string   `json:"connection" jsonschema:"Connection name from list_connections"`
	Title      string   `json:"title" jsonschema:"One line saying what the change does, for the reviewer"`
	Reason     string   `json:"reason" jsonschema:"Why the change is needed: the request, ticket or context the reviewer should know"`
	Statements []string `json:"statements" jsonschema:"SQL statements, one per entry, run in order in one transaction"`
}

type submitOut struct {
	Number            int64            `json:"number" jsonschema:"Change number"`
	Status            string           `json:"status"`
	ReviewURL         string           `json:"review_url" jsonschema:"Dashboard page where a person reviews the change; give it to the user"`
	AlreadyExisted    bool             `json:"already_existed" jsonschema:"True when an open change with the same SQL already existed and was returned instead of creating another"`
	ApprovalsRequired int              `json:"approvals_required"`
	Statements        []StatementCount `json:"statements"`
	Message           string           `json:"message"`
}

type numberIn struct {
	Number int64 `json:"number" jsonschema:"Change number"`
}

// ReviewInfo is one review of a change.
type ReviewInfo struct {
	Reviewer  string    `json:"reviewer"`
	Decision  string    `json:"decision" jsonschema:"approve or reject"`
	Comment   string    `json:"comment,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Counts    bool      `json:"counts" jsonschema:"True when this approval counts towards the approvals required now"`
	Outdated  string    `json:"outdated,omitempty" jsonschema:"Why an approval no longer counts: sql_changed, preview_changed or reviewer_removed"`
}

// ExecutionInfo is how running a change went.
type ExecutionInfo struct {
	Attempts           int        `json:"attempts" jsonschema:"Times dbairlock has tried to run the change"`
	WaitingForDatabase bool       `json:"waiting_for_database" jsonschema:"True when the database could not be reached and dbairlock will try again"`
	NextAttemptAt      *time.Time `json:"next_attempt_at,omitempty"`
	Outcome            string     `json:"outcome,omitempty" jsonschema:"Outcome of the latest attempt: running, executed, failed, not_started or interrupted"`
	ErrorCode          string     `json:"error_code,omitempty" jsonschema:"Why it failed: drift, timeout, error, invalid or interrupted"`
	DriftKind          string     `json:"drift_kind,omitempty" jsonschema:"For drift: rows (different rows match now) or count"`
}

type changeOut struct {
	Number            int64            `json:"number"`
	Title             string           `json:"title"`
	Connection        string           `json:"connection"`
	Status            string           `json:"status" jsonschema:"draft, pending_review, approved, executing, executed, failed, rejected, expired or cancelled"`
	StatusCode        string           `json:"status_code,omitempty" jsonschema:"Why the change is in its status, for example drift or author_removed"`
	Message           string           `json:"message" jsonschema:"What the status means and what happens next"`
	ReviewURL         string           `json:"review_url"`
	ApprovalsRequired int              `json:"approvals_required"`
	Approvals         int              `json:"approvals" jsonschema:"Approvals that count now"`
	Statements        []StatementCount `json:"statements"`
	Reviews           []ReviewInfo     `json:"reviews"`
	Execution         ExecutionInfo    `json:"execution"`
	CreatedAt         time.Time        `json:"created_at"`
	SubmittedAt       *time.Time       `json:"submitted_at,omitempty"`
	ExpiresAt         *time.Time       `json:"expires_at,omitempty" jsonschema:"When an unexecuted change stops being runnable"`
	ExecutedAt        *time.Time       `json:"executed_at,omitempty"`
}

type listIn struct {
	Status string `json:"status,omitempty" jsonschema:"Only changes in this status (optional)"`
	Limit  int    `json:"limit,omitempty" jsonschema:"Most changes to return, newest first (default 20, at most 50)"`
}

// ChangeSummary is one change in list_changes.
type ChangeSummary struct {
	Number     int64     `json:"number"`
	Title      string    `json:"title"`
	Connection string    `json:"connection"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	ReviewURL  string    `json:"review_url"`
}

type listOut struct {
	Changes []ChangeSummary `json:"changes"`
	More    bool            `json:"more" jsonschema:"True when there are older changes than those listed"`
}

type cancelIn struct {
	Number int64  `json:"number" jsonschema:"Change number"`
	Reason string `json:"reason,omitempty" jsonschema:"Why it is cancelled (optional)"`
}

type cancelOut struct {
	Number int64  `json:"number"`
	Status string `json:"status"`
}

func boolPtr(b bool) *bool { return &b }

func (s *Server) registerChanges() {
	addTool(s, tool[previewIn, previewOut]{
		def: &mcp.Tool{
			Name: "preview_change",
			Description: "Checks SQL before you submit it, and creates nothing. Every statement is validated; if all are valid they are dry-run in one " +
				"transaction on the database that is then rolled back, and you get each statement's kind, table and the number of rows it would affect, " +
				"or what is wrong with it and where. Every statement's result is reported, so fix them all in one pass. " +
				allowedSQL + " UPDATE and DELETE need a WHERE clause. You get counts only, never row data. " +
				"A dry run takes brief row locks on the database, so this tool has a lower rate limit; do not call it in a loop.",
			Annotations: &mcp.ToolAnnotations{Title: "Preview a change", ReadOnlyHint: true, OpenWorldHint: boolPtr(true)},
		},
		kind: limitPreview,
		args: func(in previewIn) map[string]any {
			return map[string]any{"connection": in.Connection, "statements": len(in.Statements)}
		},
		run: s.preview,
	})
	addTool(s, tool[submitIn, submitOut]{
		def: &mcp.Tool{
			Name: "submit_change",
			Description: "Submits a change for human review: creates it and submits it in one call. A person other than the token's owner must approve it " +
				"in the dashboard before dbairlock runs it; you cannot approve it. Returns the change number, the rows each statement affects and the review URL: " +
				"give the review URL to the user. Check the SQL with preview_change first. If validation or the dry run fails, nothing is created and the error " +
				"says what to fix. Calling it again with the same SQL while that change is open returns the same change (already_existed true), " +
				"so retrying after a timeout is safe. " + allowedSQL + " UPDATE and DELETE need a WHERE clause.",
			Annotations: &mcp.ToolAnnotations{Title: "Submit a change for review", DestructiveHint: boolPtr(false), IdempotentHint: true, OpenWorldHint: boolPtr(true)},
		},
		kind: limitSubmit,
		args: func(in submitIn) map[string]any {
			return map[string]any{"connection": in.Connection, "statements": len(in.Statements)}
		},
		run: s.submit,
	})
	addTool(s, tool[numberIn, changeOut]{
		def: &mcp.Tool{
			Name: "get_change",
			Description: "Returns the status of one of your changes: its reviews and comments, the approvals it still needs, and how execution went " +
				"(attempts, outcome, error code). Changes wait for people, so check back when the user asks, not in a tight loop.",
			Annotations: readOnly("Get a change"),
		},
		args: func(in numberIn) map[string]any { return map[string]any{"number": in.Number} },
		run:  s.getChange,
	})
	addTool(s, tool[listIn, listOut]{
		def: &mcp.Tool{
			Name:        "list_changes",
			Description: "Lists the changes authored by this token's owner, newest first, optionally only those in one status.",
			Annotations: readOnly("List my changes"),
		},
		args: func(in listIn) map[string]any { return map[string]any{"status": in.Status} },
		run:  s.listChanges,
	})
	addTool(s, tool[cancelIn, cancelOut]{
		def: &mcp.Tool{
			Name:        "cancel_change",
			Description: "Cancels one of your changes that has not run yet (a draft, a change in review or an approved change). It cannot be undone.",
			Annotations: &mcp.ToolAnnotations{Title: "Cancel a change", DestructiveHint: boolPtr(true), IdempotentHint: true, OpenWorldHint: boolPtr(false)},
		},
		args: func(in cancelIn) map[string]any { return map[string]any{"number": in.Number} },
		run:  s.cancel,
	})
}

func (s *Server) preview(ctx context.Context, c call[previewIn]) (result[previewOut], error) {
	conn, err := s.connection(ctx, c.in.Connection)
	if err != nil {
		return result[previewOut]{}, err
	}
	res, err := s.opts.Changes.Preview(ctx, c.p, conn.ID, c.in.Statements)
	if err != nil {
		return result[previewOut]{}, err
	}
	out := previewOut{OK: res.OK, Statements: []StatementCheck{}}
	if res.Problem != nil {
		out.Problem = res.Problem.Message
	}
	invalid := 0
	for _, st := range res.Statements {
		chk := StatementCheck{Statement: st.Position + 1, Valid: st.Valid, Kind: kindName(st.Kind), HasPrimaryKey: st.HasPrimaryKey}
		if st.Valid {
			chk.Table = qualified(st.Schema, st.Table)
		}
		if st.Preview != nil {
			n := st.Preview.RowsAffected
			chk.RowsAffected = &n
		}
		if st.Issue != nil {
			chk.Error = statementError(st.Issue)
			invalid++
		}
		out.Statements = append(out.Statements, chk)
	}
	if res.Duplicate != nil {
		out.AlreadySubmitted = res.Duplicate.Number
	}
	var summary string
	switch {
	case res.OK && res.Duplicate != nil:
		summary = fmt.Sprintf("All %d statements are valid. You already have open change #%d with exactly this SQL; submit_change would return it.", len(out.Statements), res.Duplicate.Number)
	case res.OK:
		summary = fmt.Sprintf("All %d statements are valid and the dry run succeeded. Nothing was saved; submit_change would accept them.", len(out.Statements))
	case out.Problem != "":
		summary = "The dry run could not run: " + out.Problem
	default:
		summary = fmt.Sprintf("%d of %d statements have problems; see each statement's error. Nothing was saved.", invalid, len(out.Statements))
	}
	return result[previewOut]{out: out, summary: summary}, nil
}

func statementError(iss *changes.Issue) *StatementError {
	e := &StatementError{Code: iss.Code, Message: iss.Message}
	if iss.Position >= 0 {
		e.Position = iss.Position + 1
	}
	return e
}

func (s *Server) submit(ctx context.Context, c call[submitIn]) (result[submitOut], error) {
	conn, err := s.connection(ctx, c.in.Connection)
	if err != nil {
		return result[submitOut]{}, err
	}
	ch, err := s.opts.Changes.Create(ctx, c.p, changes.Draft{
		ConnectionID: conn.ID, Title: c.in.Title, Reason: c.in.Reason, Statements: c.in.Statements,
	}, true)
	if err != nil {
		return result[submitOut]{}, err
	}
	out := submitOut{
		Number: ch.Number, Status: string(ch.Status), ReviewURL: s.reviewURL(ch.Number),
		AlreadyExisted: ch.AlreadyExisted, ApprovalsRequired: ch.RequiredApprovals, Statements: statementCounts(ch),
	}
	if ch.AlreadyExisted {
		out.Message = fmt.Sprintf("You already had an open change with exactly this SQL, so no new change was created: #%d is %s. "+
			"Give the user the review URL: %s", ch.Number, statusLabel(ch.Status), out.ReviewURL)
	} else {
		out.Message = fmt.Sprintf("Change #%d was submitted for review and needs %d approval(s) from someone other than its author before it runs. "+
			"Give the user the review URL: %s", ch.Number, ch.RequiredApprovals, out.ReviewURL)
	}
	return result[submitOut]{out: out, summary: out.Message}, nil
}

func statementCounts(ch changes.Change) []StatementCount {
	out := make([]StatementCount, len(ch.Statements))
	for i, st := range ch.Statements {
		out[i] = StatementCount{
			Statement: st.Position + 1, Kind: kindName(st.Kind), Table: qualified(st.Schema, st.Table), RowsAffected: st.ExpectedRows,
		}
	}
	return out
}

func checkNumber(n int64) error {
	if n <= 0 {
		return agentErr("Pass the change number, a positive integer (for example 42 for change #42).")
	}
	return nil
}

func (s *Server) getChange(ctx context.Context, c call[numberIn]) (result[changeOut], error) {
	if err := checkNumber(c.in.Number); err != nil {
		return result[changeOut]{}, err
	}
	ch, err := s.opts.Changes.Get(ctx, c.p, strconv.FormatInt(c.in.Number, 10))
	if err != nil {
		return result[changeOut]{}, err
	}
	out := changeOut{
		Number: ch.Number, Title: ch.Title, Connection: ch.ConnectionName, Status: string(ch.Status),
		StatusCode: ch.StatusCode, Message: describe(ch), ReviewURL: s.reviewURL(ch.Number),
		ApprovalsRequired: ch.RequiredApprovals, Approvals: ch.Approvals, Statements: statementCounts(ch),
		Reviews: []ReviewInfo{}, CreatedAt: ch.CreatedAt, SubmittedAt: ch.SubmittedAt, ExpiresAt: ch.ExpiresAt,
		ExecutedAt: ch.ExecutedAt, Execution: execution(ch),
	}
	for _, r := range ch.Reviews {
		who := r.Reviewer.Name
		if who == "" {
			who = r.Reviewer.Email
		}
		out.Reviews = append(out.Reviews, ReviewInfo{
			Reviewer: who, Decision: r.Decision, Comment: r.Comment, CreatedAt: r.CreatedAt, Counts: r.Counts, Outdated: r.Outdated,
		})
	}
	return result[changeOut]{out: out, summary: fmt.Sprintf("Change #%d: %s", ch.Number, out.Message)}, nil
}

// execution summarizes the attempts to run ch. Error messages from the
// database are left out: they can quote values from the target.
func execution(ch changes.Change) ExecutionInfo {
	e := ExecutionInfo{Attempts: ch.Attempts, NextAttemptAt: ch.NextAttemptAt}
	e.WaitingForDatabase = ch.Status == changes.StatusApproved && ch.NextAttemptAt != nil
	if n := len(ch.Executions); n > 0 {
		last := ch.Executions[n-1]
		e.Outcome, e.ErrorCode = last.Outcome, last.ErrorCode
		if last.Drift != nil {
			e.DriftKind = string(last.Drift.Kind)
		}
	}
	return e
}

func statusLabel(st changes.Status) string {
	return strings.ReplaceAll(string(st), "_", " ")
}

// describe says in one or two sentences what ch's status means and what
// happens next.
func describe(ch changes.Change) string {
	switch ch.Status {
	case changes.StatusDraft:
		return "A draft: it is not submitted for review yet. The user can submit it from the dashboard."
	case changes.StatusPendingReview:
		return fmt.Sprintf("Waiting for review: %d of %d required approvals. A person approves or rejects it in the dashboard.",
			max(ch.Approvals, 0), ch.RequiredApprovals)
	case changes.StatusApproved:
		switch {
		case ch.NextAttemptAt != nil:
			return fmt.Sprintf("Approved and waiting to run: the database could not be reached (%d attempts so far). dbairlock tries again at %s, until the approval expires.",
				ch.Attempts, ch.NextAttemptAt.UTC().Format(time.RFC3339))
		case !ch.AutoExecute && !ch.ExecuteRequested:
			return "Approved. On this connection someone runs approved changes from the dashboard."
		default:
			return "Approved; dbairlock runs it shortly."
		}
	case changes.StatusExecuting:
		return "Running on the database now."
	case changes.StatusExecuted:
		return "Executed: the change was applied to the database."
	case changes.StatusFailed:
		msg := "Failed: nothing was changed on the database"
		switch ch.StatusCode {
		case changes.FailDrift:
			msg += " because the data changed since it was reviewed (" + ch.StatusDetail + " drift)"
		case changes.FailInterrupted:
			msg = "Failed: dbairlock stopped while it was running and the outcome is unknown; a person must verify the data"
		case changes.FailTimeout:
			msg += " because a statement timed out"
		}
		return msg + ". A failed change is final; submit a new change to try again."
	case changes.StatusRejected:
		return "Rejected by a reviewer; see the review comments. Submit a new change if it should be done differently."
	case changes.StatusExpired:
		return "Expired: it was not executed before its approval lifetime ended. Submit it again if it is still needed."
	case changes.StatusCancelled:
		return "Cancelled. It will not run."
	}
	return statusLabel(ch.Status)
}

func (s *Server) listChanges(ctx context.Context, c call[listIn]) (result[listOut], error) {
	f := changes.ListFilter{Limit: defaultListLimit}
	if c.in.Status != "" {
		st := changes.Status(strings.TrimSpace(c.in.Status))
		if !slices.Contains(changes.Statuses(), st) {
			names := make([]string, 0, len(changes.Statuses()))
			for _, v := range changes.Statuses() {
				names = append(names, string(v))
			}
			return result[listOut]{}, userErrorf("Unknown status %q. Use one of: %s.", c.in.Status, strings.Join(names, ", "))
		}
		f.Status = st
	}
	if c.in.Limit > 0 {
		f.Limit = min(c.in.Limit, maxListLimit)
	}
	page, err := s.opts.Changes.List(ctx, c.p, f)
	if err != nil {
		return result[listOut]{}, err
	}
	out := listOut{Changes: []ChangeSummary{}, More: page.NextBefore != 0}
	for _, ch := range page.Changes {
		out.Changes = append(out.Changes, ChangeSummary{
			Number: ch.Number, Title: ch.Title, Connection: ch.ConnectionName, Status: string(ch.Status),
			CreatedAt: ch.CreatedAt, ReviewURL: s.reviewURL(ch.Number),
		})
	}
	return result[listOut]{out: out, summary: fmt.Sprintf("%d changes.", len(out.Changes))}, nil
}

func (s *Server) cancel(ctx context.Context, c call[cancelIn]) (result[cancelOut], error) {
	if err := checkNumber(c.in.Number); err != nil {
		return result[cancelOut]{}, err
	}
	ch, err := s.opts.Changes.Cancel(ctx, c.p, strconv.FormatInt(c.in.Number, 10), c.in.Reason)
	if err != nil {
		return result[cancelOut]{}, err
	}
	out := cancelOut{Number: ch.Number, Status: string(ch.Status)}
	return result[cancelOut]{out: out, summary: fmt.Sprintf("Change #%d is %s.", ch.Number, statusLabel(ch.Status))}, nil
}
