package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/changes"
	"github.com/alvaroibarguen/dbairlock/internal/connector"
	"github.com/alvaroibarguen/dbairlock/internal/httpapi/gen"
	"github.com/alvaroibarguen/dbairlock/internal/identity"
	"github.com/alvaroibarguen/dbairlock/internal/platform/httpx"
)

// Changes is the changes module as the REST API uses it. Every rule lives
// in the module; handlers only translate.
type Changes interface {
	List(ctx context.Context, p identity.Principal, f changes.ListFilter) (changes.Page, error)
	Create(ctx context.Context, p identity.Principal, in changes.Draft, submit bool) (changes.Change, error)
	Preview(ctx context.Context, p identity.Principal, connectionID string, statements []string) (changes.PreviewResult, error)
	Get(ctx context.Context, p identity.Principal, ref string) (changes.Change, error)
	Edit(ctx context.Context, p identity.Principal, ref string, in changes.EditInput) (changes.Change, error)
	Submit(ctx context.Context, p identity.Principal, ref string, revision int) (changes.Change, error)
	Cancel(ctx context.Context, p identity.Principal, ref, reason string) (changes.Change, error)
	RefreshPreview(ctx context.Context, p identity.Principal, ref string) (changes.Change, changes.RefreshOutcome, error)
	Review(ctx context.Context, p identity.Principal, ref string, in changes.ReviewInput) (changes.Change, error)
	ExecuteNow(ctx context.Context, p identity.Principal, ref string) (changes.Change, error)
	RetryNow(ctx context.Context, p identity.Principal, ref string) (changes.Change, error)
	Events(ctx context.Context, p identity.Principal, ref string) ([]audit.Entry, error)
}

// ListChanges lists changes.
func (s *Server) ListChanges(ctx context.Context, req gen.ListChangesRequestObject) (gen.ListChangesResponseObject, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	f := changes.ListFilter{}
	q := req.Params
	if q.View != nil {
		switch *q.View {
		case gen.NeedsMyReview:
			f.NeedsReviewBy = p.UserID
		case gen.Mine:
			f.AuthorID = p.UserID
		}
	}
	if q.Status != nil {
		f.Status = changes.Status(*q.Status)
	}
	if q.ConnectionId != nil {
		f.ConnectionID = *q.ConnectionId
	}
	if q.AuthorId != nil && f.AuthorID == "" {
		f.AuthorID = *q.AuthorId
	}
	if q.Open != nil {
		f.Open = *q.Open
	}
	if q.Before != nil {
		f.Before = *q.Before
	}
	if q.Limit != nil {
		f.Limit = *q.Limit
	}
	page, err := s.deps.Changes.List(ctx, p, f)
	if err != nil {
		return nil, err
	}
	out := gen.ListChanges200JSONResponse{Changes: []gen.ChangeSummary{}, NeedsMyReview: page.NeedsMyReview}
	for _, c := range page.Changes {
		out.Changes = append(out.Changes, summaryBody(c))
	}
	if page.NextBefore > 0 {
		n := page.NextBefore
		out.NextBefore = &n
	}
	return out, nil
}

// CreateChange creates a draft, or creates and submits a change.
func (s *Server) CreateChange(ctx context.Context, req gen.CreateChangeRequestObject) (gen.CreateChangeResponseObject, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	b := req.Body
	in := changes.Draft{ConnectionID: b.ConnectionId, Title: b.Title, Statements: b.Statements}
	if b.Reason != nil {
		in.Reason = *b.Reason
	}
	c, err := s.deps.Changes.Create(ctx, p, in, b.Submit != nil && *b.Submit)
	if err != nil {
		return nil, err
	}
	if c.AlreadyExisted {
		return gen.CreateChange200JSONResponse(changeBody(c)), nil
	}
	return gen.CreateChange201JSONResponse(changeBody(c)), nil
}

// PreviewChange dry-runs statements without saving anything.
func (s *Server) PreviewChange(ctx context.Context, req gen.PreviewChangeRequestObject) (gen.PreviewChangeResponseObject, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	res, err := s.deps.Changes.Preview(ctx, p, req.Body.ConnectionId, req.Body.Statements)
	if err != nil {
		return nil, err
	}
	out := gen.PreviewChange200JSONResponse{
		Ok: res.OK, Problem: issueBody(res.Problem), SqlHash: res.SQLHash, Statements: []gen.PreviewStatement{},
	}
	for _, st := range res.Statements {
		ps := gen.PreviewStatement{
			Position: st.Position, Sql: st.SQL, Valid: st.Valid, HasPrimaryKey: st.HasPrimaryKey,
			Issue: issueBody(st.Issue), Preview: previewBody(st.Preview),
		}
		if st.Valid {
			k := gen.StatementKind(st.Kind)
			ps.Kind, ps.Schema, ps.Table = &k, ptr(st.Schema), ptr(st.Table)
		}
		out.Statements = append(out.Statements, ps)
	}
	if res.Duplicate != nil {
		out.Duplicate = &gen.ChangeRef{Id: res.Duplicate.ID, Number: res.Duplicate.Number}
	}
	return out, nil
}

// GetChange returns one change.
func (s *Server) GetChange(ctx context.Context, req gen.GetChangeRequestObject) (gen.GetChangeResponseObject, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	c, err := s.deps.Changes.Get(ctx, p, req.Id)
	if err != nil {
		return nil, err
	}
	return gen.GetChange200JSONResponse(changeBody(c)), nil
}

// UpdateChange edits a change.
func (s *Server) UpdateChange(ctx context.Context, req gen.UpdateChangeRequestObject) (gen.UpdateChangeResponseObject, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	b := req.Body
	in := changes.EditInput{Revision: b.Revision, Title: b.Title, Reason: b.Reason}
	if b.Statements != nil {
		in.Statements = *b.Statements
		if in.Statements == nil {
			in.Statements = []string{}
		}
	}
	c, err := s.deps.Changes.Edit(ctx, p, req.Id, in)
	if err != nil {
		return nil, err
	}
	return gen.UpdateChange200JSONResponse(changeBody(c)), nil
}

// SubmitChange submits a draft.
func (s *Server) SubmitChange(ctx context.Context, req gen.SubmitChangeRequestObject) (gen.SubmitChangeResponseObject, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	c, err := s.deps.Changes.Submit(ctx, p, req.Id, req.Body.Revision)
	if err != nil {
		return nil, err
	}
	return gen.SubmitChange200JSONResponse(changeBody(c)), nil
}

// CancelChange cancels a change.
func (s *Server) CancelChange(ctx context.Context, req gen.CancelChangeRequestObject) (gen.CancelChangeResponseObject, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	reason := ""
	if req.Body != nil && req.Body.Reason != nil {
		reason = *req.Body.Reason
	}
	c, err := s.deps.Changes.Cancel(ctx, p, req.Id, reason)
	if err != nil {
		return nil, err
	}
	return gen.CancelChange200JSONResponse(changeBody(c)), nil
}

// RefreshChangePreview re-runs the dry run of a change in review.
func (s *Server) RefreshChangePreview(ctx context.Context, req gen.RefreshChangePreviewRequestObject) (gen.RefreshChangePreviewResponseObject, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	c, res, err := s.deps.Changes.RefreshPreview(ctx, p, req.Id)
	if err != nil {
		return nil, err
	}
	out := gen.RefreshChangePreview200JSONResponse{Change: changeBody(c), Ok: res.OK, DataChanged: res.DataChanged}
	if res.Problem != "" {
		out.Problem = ptr(res.Problem)
	}
	return out, nil
}

// ReviewChange approves or rejects a change.
func (s *Server) ReviewChange(ctx context.Context, req gen.ReviewChangeRequestObject) (gen.ReviewChangeResponseObject, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	b := req.Body
	in := changes.ReviewInput{Decision: string(b.Decision)}
	if b.Comment != nil {
		in.Comment = *b.Comment
	}
	if b.SqlHash != nil {
		in.SQLHash = *b.SqlHash
	}
	if b.PreviewHash != nil {
		in.PreviewHash = *b.PreviewHash
	}
	c, err := s.deps.Changes.Review(ctx, p, req.Id, in)
	if err != nil {
		return nil, err
	}
	return gen.ReviewChange200JSONResponse(changeBody(c)), nil
}

// ExecuteChange asks for an approved change to run now.
func (s *Server) ExecuteChange(ctx context.Context, req gen.ExecuteChangeRequestObject) (gen.ExecuteChangeResponseObject, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	c, err := s.deps.Changes.ExecuteNow(ctx, p, req.Id)
	if err != nil {
		return nil, err
	}
	return gen.ExecuteChange200JSONResponse(changeBody(c)), nil
}

// RetryChange retries a change waiting for its database now.
func (s *Server) RetryChange(ctx context.Context, req gen.RetryChangeRequestObject) (gen.RetryChangeResponseObject, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	c, err := s.deps.Changes.RetryNow(ctx, p, req.Id)
	if err != nil {
		return nil, err
	}
	return gen.RetryChange200JSONResponse(changeBody(c)), nil
}

// ListChangeEvents returns a change's timeline.
func (s *Server) ListChangeEvents(ctx context.Context, req gen.ListChangeEventsRequestObject) (gen.ListChangeEventsResponseObject, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	entries, err := s.deps.Changes.Events(ctx, p, req.Id)
	if err != nil {
		return nil, err
	}
	out := gen.ListChangeEvents200JSONResponse{Events: []gen.ChangeEvent{}}
	for _, e := range entries {
		payload := map[string]any{}
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			return nil, err
		}
		actor := gen.ChangeEventActor{Via: gen.ChangeEventActorVia(e.Actor.Via)}
		if e.Actor.UserID != "" {
			actor.UserId = ptr(e.Actor.UserID)
		}
		if e.Actor.Email != "" {
			actor.Email = ptr(e.Actor.Email)
		}
		if e.Actor.APITokenID != "" {
			actor.ApiTokenId = ptr(e.Actor.APITokenID)
		}
		out.Events = append(out.Events, gen.ChangeEvent{
			Seq: e.Seq, OccurredAt: e.OccurredAt, Action: string(e.Action), Actor: actor, Payload: payload,
		})
	}
	return out, nil
}

func ptr[T comparable](v T) *T {
	return &v
}

func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func personBody(p changes.Person) gen.ChangePerson {
	return gen.ChangePerson{Id: p.ID, Email: p.Email, Name: p.Name, Removed: p.Removed}
}

func issueBody(i *changes.Issue) *gen.ChangeIssue {
	if i == nil {
		return nil
	}
	return &gen.ChangeIssue{Code: i.Code, Message: i.Message, Position: i.Position}
}

func rowsBody(rows []connector.Row) *[][]gen.Cell {
	if rows == nil {
		return nil
	}
	out := make([][]gen.Cell, len(rows))
	for i, r := range rows {
		out[i] = make([]gen.Cell, len(r))
		copy(out[i], r)
	}
	return &out
}

func previewBody(p *changes.Preview) *gen.RowPreview {
	if p == nil {
		return nil
	}
	out := &gen.RowPreview{
		RowsAffected: p.RowsAffected, Fingerprint: optStr(p.Fingerprint), HasPrimaryKey: p.HasPrimaryKey,
		PrimaryKey: append([]string{}, p.PrimaryKey...), Columns: append([]string{}, p.Columns...),
		Before: rowsBody(p.Before), After: rowsBody(p.After), Truncated: p.Truncated, RowImages: p.RowImages,
	}
	if len(p.Session) > 0 {
		sess := map[string]string{}
		for k, v := range p.Session {
			sess[k] = v
		}
		out.Session = &sess
	}
	return out
}

func statementBody(st changes.Statement) gen.ChangeStatement {
	out := gen.ChangeStatement{
		Position: st.Position, Sql: st.SQL, Schema: st.Schema, Table: st.Table, ExpectedRows: st.ExpectedRows,
		Fingerprint: optStr(st.Fingerprint), HasPrimaryKey: st.HasPrimaryKey, Issue: issueBody(st.Issue),
		Preview: previewBody(st.Preview), PreviousPreview: previewBody(st.PreviousPreview),
	}
	if st.Kind != "" {
		k := gen.StatementKind(st.Kind)
		out.Kind = &k
	}
	return out
}

func viaBody(c changes.Change) string {
	if c.ByAgent() {
		return "mcp"
	}
	return "web"
}

func summaryBody(c changes.Change) gen.ChangeSummary {
	out := gen.ChangeSummary{
		Id: c.ID, Number: c.Number,
		Connection: gen.ChangeConnection{Id: c.ConnectionID, Name: c.ConnectionName, Connector: c.Connector},
		Author:     personBody(c.Author), Via: gen.ChangeSummaryVia(viaBody(c)), AgentName: optStr(c.AgentName),
		Title: c.Title, Status: gen.ChangeStatus(c.Status), StatusCode: optStr(c.StatusCode),
		StatusDetail: optStr(c.StatusDetail), Revision: c.Revision, Attempts: c.Attempts,
		NextAttemptAt: c.NextAttemptAt, Statements: []gen.ChangeStatement{},
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, SubmittedAt: c.SubmittedAt,
	}
	for _, st := range c.Statements {
		out.Statements = append(out.Statements, statementBody(st))
	}
	return out
}

func changeBody(c changes.Change) gen.Change {
	out := gen.Change{
		Id: c.ID, Number: c.Number,
		Connection: gen.ChangeConnection{Id: c.ConnectionID, Name: c.ConnectionName, Connector: c.Connector},
		Author:     personBody(c.Author), Via: gen.ChangeVia(viaBody(c)), AgentName: optStr(c.AgentName),
		Title: c.Title, Reason: c.Reason, Status: gen.ChangeStatus(c.Status),
		StatusCode: optStr(c.StatusCode), StatusDetail: optStr(c.StatusDetail), StatusMessage: optStr(c.StatusMessage),
		Revision: c.Revision, SqlHash: c.SQLHash, PreviewHash: c.PreviewHash, PreviewedAt: c.PreviewedAt,
		PreviewError: optStr(c.PreviewError), PreviewChanged: c.PreviewChanged,
		PreviewMaxAgeSeconds: int(c.PreviewMaxAge.Seconds()), ServerTime: c.ServerTime,
		RequiredApprovals: c.RequiredApprovals, Approvals: c.Approvals, AutoExecute: c.AutoExecute,
		StoreRowImages: c.StoreRowImages, Attempts: c.Attempts, NextAttemptAt: c.NextAttemptAt,
		LastAttemptError: optStr(c.LastAttemptError), ExecuteRequested: c.ExecuteRequested,
		Statements: []gen.ChangeStatement{}, Reviews: []gen.ChangeReview{}, Executions: []gen.ChangeExecution{},
		Actions: gen.ChangeActions{
			Edit: c.Actions.Edit, Submit: c.Actions.Submit, Cancel: c.Actions.Cancel, Approve: c.Actions.Approve,
			Reject: c.Actions.Reject, RefreshPreview: c.Actions.RefreshPreview, ExecuteNow: c.Actions.ExecuteNow,
			RetryNow: c.Actions.RetryNow,
		},
		AlreadyExisted: c.AlreadyExisted, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
		SubmittedAt: c.SubmittedAt, DecidedAt: c.DecidedAt, ExecutedAt: c.ExecutedAt, ExpiresAt: c.ExpiresAt,
	}
	for _, st := range c.Statements {
		out.Statements = append(out.Statements, statementBody(st))
	}
	for _, r := range c.Reviews {
		rv := gen.ChangeReview{
			Id: r.ID, Reviewer: personBody(r.Reviewer), Decision: gen.ChangeReviewDecision(r.Decision),
			Comment: r.Comment, SqlHash: r.SQLHash, PreviewHash: r.PreviewHash, Revision: r.Revision,
			CreatedAt: r.CreatedAt, Counts: r.Counts,
		}
		if r.Outdated != "" {
			o := gen.ChangeReviewOutdated(r.Outdated)
			rv.Outdated = &o
		}
		out.Reviews = append(out.Reviews, rv)
	}
	for _, e := range c.Executions {
		ex := gen.ChangeExecution{
			Id: e.ID, Attempt: e.Attempt, StartedAt: e.StartedAt, FinishedAt: e.FinishedAt,
			Outcome: gen.ChangeExecutionOutcome(e.Outcome), ErrorCode: optStr(e.ErrorCode),
			ErrorMessage: optStr(e.ErrorMessage), Results: []gen.ExecutionResult{},
		}
		if e.Drift != nil {
			ex.Drift = &gen.ChangeDrift{
				Kind: gen.ChangeDriftKind(e.Drift.Kind), Statement: e.Drift.Statement,
				Expected: e.Drift.Expected, Actual: e.Drift.Actual,
			}
		}
		for _, r := range e.Results {
			p := r.Preview
			ex.Results = append(ex.Results, gen.ExecutionResult{
				Position: r.Position, Schema: r.Schema, Table: r.Table, Preview: *previewBody(&p),
			})
		}
		out.Executions = append(out.Executions, ex)
	}
	return out
}

// changeError maps the changes module's structured errors to HTTP errors.
// It returns nil for errors it does not handle.
func changeError(err error) *httpx.Error {
	var (
		inputErr *changes.InputError
		dryErr   *changes.DryRunError
		dupErr   *changes.DuplicateError
		stateE   *changes.StateError
		transErr *changes.TransitionError
		soonErr  *changes.TooSoonError
	)
	switch {
	case errors.As(err, &inputErr):
		fields := make([]httpx.FieldError, len(inputErr.Fields))
		for i, f := range inputErr.Fields {
			fields[i] = httpx.FieldError{Field: f.Field, Message: f.Message}
		}
		return httpx.NewValidationError("Some fields are invalid. Nothing was changed.", fields)
	case errors.As(err, &dryErr):
		e := dryErr
		details := gen.ErrorDetails{Problem: issueBody(e.Problem)}
		list := []gen.StatementIssue{}
		fields := []httpx.FieldError{}
		for _, st := range e.Statements {
			if st.Issue == nil {
				continue
			}
			list = append(list, gen.StatementIssue{
				Statement: st.Position, Code: st.Issue.Code, Message: st.Issue.Message, Position: st.Issue.Position,
			})
			fields = append(fields, httpx.FieldError{Field: fieldOf(st.Position), Message: st.Issue.Message})
		}
		details.Statements = &list
		if e.Problem != nil && e.Problem.Code == changes.CodeUnavailable {
			return &httpx.Error{Status: http.StatusBadGateway, Code: "target_unavailable", Message: e.Problem.Message, Details: details}
		}
		msg := "Some statements cannot run as written. Fix them and try again; nothing was submitted."
		if e.Problem != nil {
			msg = e.Problem.Message
		}
		return &httpx.Error{Status: http.StatusUnprocessableEntity, Code: "statements_invalid", Message: msg, Fields: fields, Details: details}
	case errors.As(err, &dupErr):
		return &httpx.Error{
			Status: http.StatusConflict, Code: "duplicate",
			Message: fmt.Sprintf("You already have an open change with this SQL on this connection: #%d.", dupErr.Number),
			Details: gen.ErrorDetails{ChangeId: ptr(dupErr.ID), ChangeNumber: ptr(dupErr.Number)},
		}
	case errors.As(err, &stateE):
		return httpx.NewError(http.StatusConflict, "invalid_state", stateE.Message)
	case errors.As(err, &transErr):
		return httpx.NewError(http.StatusConflict, "invalid_state", "This is not possible in the change's current status. Reload it.")
	case errors.As(err, &soonErr):
		return httpx.NewError(http.StatusTooManyRequests, "refresh_too_soon",
			"The preview was updated a moment ago. Wait a few seconds and try again.")
	}
	return nil
}

func fieldOf(i int) string { return "statements[" + strconv.Itoa(i) + "]" }
