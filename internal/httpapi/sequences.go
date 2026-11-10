package httpapi

import (
	"context"

	"github.com/alvaroibarguen/dbairlock/internal/changes"
	"github.com/alvaroibarguen/dbairlock/internal/httpapi/gen"
)

// CreateSequence validates and submits a sequence.
func (s *Server) CreateSequence(ctx context.Context, req gen.CreateSequenceRequestObject) (gen.CreateSequenceResponseObject, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	b := req.Body
	in := changes.SequenceDraft{Title: b.Title}
	if b.Reason != nil {
		in.Reason = *b.Reason
	}
	for _, st := range b.Steps {
		d := changes.StepDraft{ConnectionID: st.ConnectionId, Statements: st.Statements}
		if st.Title != nil {
			d.Title = *st.Title
		}
		in.Steps = append(in.Steps, d)
	}
	q, err := s.deps.Changes.CreateSequence(ctx, p, in)
	if err != nil {
		return nil, err
	}
	if q.AlreadyExisted {
		return gen.CreateSequence200JSONResponse(sequenceBody(q)), nil
	}
	return gen.CreateSequence201JSONResponse(sequenceBody(q)), nil
}

// GetSequence returns one sequence.
func (s *Server) GetSequence(ctx context.Context, req gen.GetSequenceRequestObject) (gen.GetSequenceResponseObject, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	q, err := s.deps.Changes.GetSequence(ctx, p, req.Id)
	if err != nil {
		return nil, err
	}
	return gen.GetSequence200JSONResponse(sequenceBody(q)), nil
}

// CancelSequence stops a running sequence.
func (s *Server) CancelSequence(ctx context.Context, req gen.CancelSequenceRequestObject) (gen.CancelSequenceResponseObject, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	reason := ""
	if req.Body != nil && req.Body.Reason != nil {
		reason = *req.Body.Reason
	}
	q, err := s.deps.Changes.CancelSequence(ctx, p, req.Id, reason)
	if err != nil {
		return nil, err
	}
	return gen.CancelSequence200JSONResponse(sequenceBody(q)), nil
}

// ListSequenceEvents returns a sequence's own timeline.
func (s *Server) ListSequenceEvents(ctx context.Context, req gen.ListSequenceEventsRequestObject) (gen.ListSequenceEventsResponseObject, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	entries, err := s.deps.Changes.SequenceEvents(ctx, p, req.Id)
	if err != nil {
		return nil, err
	}
	list, err := eventList(entries)
	if err != nil {
		return nil, err
	}
	return gen.ListSequenceEvents200JSONResponse(list), nil
}

// sequenceRefBody numbers steps from 1, as people count them.
func sequenceRefBody(r *changes.SequenceRef) *gen.SequenceRef {
	if r == nil {
		return nil
	}
	return &gen.SequenceRef{
		Id: r.ID, Number: r.Number, Title: r.Title, Status: gen.SequenceStatus(r.Status),
		Step: r.Step + 1, Steps: r.Steps, CurrentStep: r.CurrentStep + 1,
	}
}

func sequenceBody(q changes.Sequence) gen.Sequence {
	via := gen.SequenceVia("web")
	if q.ByAgent() {
		via = "mcp"
	}
	out := gen.Sequence{
		Id: q.ID, Number: q.Number, Author: personBody(q.Author), Via: via, AgentName: optStr(q.AgentName),
		Title: q.Title, Reason: q.Reason, Status: gen.SequenceStatus(q.Status), StatusMessage: optStr(q.StatusMessage),
		CurrentStep: q.CurrentStep + 1, Steps: []gen.ChangeSummary{}, Actions: gen.SequenceActions{Cancel: q.Actions.Cancel},
		AlreadyExisted: q.AlreadyExisted, CreatedAt: q.CreatedAt, UpdatedAt: q.UpdatedAt, FinishedAt: q.FinishedAt,
		ServerTime: q.ServerTime,
	}
	if q.StatusCode != "" {
		code := gen.SequenceStatusCode(q.StatusCode)
		out.StatusCode = &code
	}
	for _, st := range q.Steps {
		out.Steps = append(out.Steps, summaryBody(st))
	}
	return out
}
