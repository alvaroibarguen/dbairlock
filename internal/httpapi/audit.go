package httpapi

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/httpapi/gen"
	"github.com/alvaroibarguen/dbairlock/internal/identity"
	"github.com/alvaroibarguen/dbairlock/internal/platform/httpx"
)

// AuditLog reads the audit log (an *audit.Reader).
type AuditLog interface {
	List(ctx context.Context, f audit.Filter) (audit.Page, error)
	Export(ctx context.Context, w io.Writer, from, to time.Time) (int64, error)
}

// AuditChecker verifies the hash chain in the background (an *audit.Checker).
type AuditChecker interface {
	Start(ctx context.Context) <-chan struct{}
	Status() (audit.Run, bool)
	LastSuccess(ctx context.Context) (audit.Verification, bool, error)
}

// verifyWait is how long POST /api/audit/verify waits for the check before
// answering with its progress.
const verifyWait = time.Second

// ListAuditEvents returns one page of the audit log.
func (s *Server) ListAuditEvents(ctx context.Context, req gen.ListAuditEventsRequestObject) (gen.ListAuditEventsResponseObject, error) {
	if _, err := authorize(ctx, identity.ActionAuditRead, identity.Instance); err != nil {
		return nil, err
	}
	q := req.Params
	f := audit.Filter{
		Actor: deref(q.Actor), ActionPrefix: deref(q.Action),
		SubjectType: deref(q.SubjectType), SubjectID: deref(q.SubjectId),
	}
	if q.Via != nil {
		if !q.Via.Valid() {
			return nil, httpx.NewError(http.StatusBadRequest, "bad_request", "via must be web, mcp, cli or system.")
		}
		f.Via = audit.Via(*q.Via)
	}
	if q.From != nil {
		f.From = *q.From
	}
	if q.To != nil {
		f.To = *q.To
	}
	if q.Before != nil {
		f.BeforeSeq = *q.Before
	}
	if q.Limit != nil {
		f.Limit = *q.Limit
	}
	page, err := s.deps.AuditLog.List(ctx, f)
	if err != nil {
		return nil, err
	}
	out := gen.ListAuditEvents200JSONResponse{Events: make([]gen.AuditEvent, 0, len(page.Entries))}
	for _, e := range page.Entries {
		ev, err := auditEventBody(e)
		if err != nil {
			return nil, err
		}
		out.Events = append(out.Events, ev)
	}
	if page.NextBeforeSeq > 0 {
		n := page.NextBeforeSeq
		out.NextBefore = &n
	}
	return out, nil
}

func auditEventBody(e audit.Entry) (gen.AuditEvent, error) {
	payload := map[string]any{}
	if err := json.Unmarshal(e.Payload, &payload); err != nil {
		return gen.AuditEvent{}, fmt.Errorf("httpapi: audit payload of event %d: %w", e.Seq, err)
	}
	return gen.AuditEvent{
		Seq: e.Seq, Id: e.ID, OccurredAt: e.OccurredAt, Action: string(e.Action),
		SubjectType: e.SubjectType, SubjectId: e.SubjectID, Payload: payload,
		PrevHash: hex.EncodeToString(e.PrevHash), Hash: hex.EncodeToString(e.Hash),
		Actor: gen.AuditActor{
			Via: gen.AuditVia(e.Actor.Via), UserId: optStr(e.Actor.UserID), Email: optStr(e.Actor.Email),
			ApiTokenId: optStr(e.Actor.APITokenID), Ip: optStr(e.Actor.IP),
		},
	}, nil
}

// ExportAudit streams the audit log as newline-delimited JSON.
func (s *Server) ExportAudit(ctx context.Context, req gen.ExportAuditRequestObject) (gen.ExportAuditResponseObject, error) {
	if _, err := authorize(ctx, identity.ActionSettingsManage, identity.Instance); err != nil {
		return nil, err
	}
	var from, to time.Time
	if req.Params.From != nil {
		from = *req.Params.From
	}
	if req.Params.To != nil {
		to = *req.Params.To
	}
	// The export is written into a pipe as the response copies from it, a
	// batch at a time. When the request ends (the client went away), closing
	// the reading side stops the writer.
	pr, pw := io.Pipe()
	stop := context.AfterFunc(ctx, func() { _ = pr.CloseWithError(ctx.Err()) })
	go func() {
		defer stop()
		// A failure ends the stream early; the copy then reports it.
		_, err := s.deps.AuditLog.Export(ctx, pw, from, to)
		_ = pw.CloseWithError(err)
	}()
	return gen.ExportAudit200ApplicationxNdjsonResponse{Body: pr}, nil
}

// VerifyAudit starts (or joins) a chain verification and waits up to a
// second for it.
func (s *Server) VerifyAudit(ctx context.Context, _ gen.VerifyAuditRequestObject) (gen.VerifyAuditResponseObject, error) {
	if _, err := authorize(ctx, identity.ActionSettingsManage, identity.Instance); err != nil {
		return nil, err
	}
	done := s.deps.AuditCheck.Start(ctx)
	t := time.NewTimer(verifyWait)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	body, err := s.verificationBody(ctx)
	if err != nil {
		return nil, err
	}
	return gen.VerifyAudit200JSONResponse(body), nil
}

// GetAuditVerification reports the current or last verification.
func (s *Server) GetAuditVerification(ctx context.Context, _ gen.GetAuditVerificationRequestObject) (gen.GetAuditVerificationResponseObject, error) {
	if _, err := authorize(ctx, identity.ActionSettingsManage, identity.Instance); err != nil {
		return nil, err
	}
	body, err := s.verificationBody(ctx)
	if err != nil {
		return nil, err
	}
	return gen.GetAuditVerification200JSONResponse(body), nil
}

func (s *Server) verificationBody(ctx context.Context) (gen.AuditVerification, error) {
	out := gen.AuditVerification{State: gen.AuditVerificationStateNone}
	if run, ok := s.deps.AuditCheck.Status(); ok {
		out.Checked = run.Checked
		out.StartedAt = ptr(run.StartedAt)
		switch {
		case run.Running:
			out.State = gen.AuditVerificationStateRunning
		case run.Err != "":
			out.State = gen.AuditVerificationStateError
			out.Message = ptr(run.Err)
		default:
			out.State = gen.AuditVerificationStateDone
		}
		if !run.FinishedAt.IsZero() {
			out.FinishedAt = ptr(run.FinishedAt)
		}
		if r := run.Result; r != nil {
			out.Ok = ptr(r.OK())
			out.LastSeq = ptr(r.LastSeq)
			if r.Problem != nil {
				out.Problem = &gen.AuditProblem{Seq: r.Problem.Seq, Reason: r.Problem.Reason}
			}
		}
	}
	last, ok, err := s.deps.AuditCheck.LastSuccess(ctx)
	if err != nil {
		return out, err
	}
	if ok {
		out.LastSuccess = &gen.AuditLastSuccess{At: last.At, By: last.By, Checked: last.Checked}
	}
	return out, nil
}
