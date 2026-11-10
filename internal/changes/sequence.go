package changes

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/changes/changesdb"
	"github.com/alvaroibarguen/dbairlock/internal/identity"
	"github.com/alvaroibarguen/dbairlock/internal/store"
	"github.com/alvaroibarguen/dbairlock/internal/targets"
)

// Sequences: ordered changes ("steps") that may target different
// connections and run one after another (migration 00009).
//
// Cross-database atomicity is impossible, so a sequence is honest about it:
// each step is atomic on its own database; the sequence as a whole is not.
//
//   - Every step is a full change with the usual rules: its own preview,
//     fingerprint, approvals, freshness window, drift guards and audit
//     events. Nothing here bypasses them.
//   - Only the current step is in review. The steps after it wait as drafts
//     with no preview, because their rows may depend on the steps before.
//     When the current step executes, the next one is dry-run right then
//     and submitted for review (advanceSequence).
//   - When a step fails, is rejected, expires or is cancelled, the sequence
//     stops: every other open step is cancelled with the reason, and the
//     steps that already ran stay applied (stopTx). Nothing is retried that
//     a single change would not retry.
//
// The sequence moves on or stops inside the transaction that records the
// step's event (onStepEvent, called from record), so its state and its
// audit events commit together with the step's.

// Size of a sequence.
const (
	MinSteps = 2
	MaxSteps = 10
)

// SequenceStatus is where a sequence is.
type SequenceStatus string

// Sequence statuses.
const (
	SequenceRunning   SequenceStatus = "running"
	SequenceCompleted SequenceStatus = "completed"
	SequenceStopped   SequenceStatus = "stopped"
)

// Why a sequence stopped (Sequence.StatusCode).
const (
	StopFailed    = "failed"
	StopRejected  = "rejected"
	StopExpired   = "expired"
	StopCancelled = "cancelled"
)

// CancelledSequenceStopped is the status code of a step cancelled because
// its sequence stopped.
const CancelledSequenceStopped = "sequence_stopped"

// StepDraft is one step of a new sequence. Title is optional.
type StepDraft struct {
	ConnectionID string
	Title        string
	Statements   []string
}

// SequenceDraft is a new sequence. It is always submitted at once.
type SequenceDraft struct {
	Title  string
	Reason string
	Steps  []StepDraft
}

// SequenceRef places a change in its sequence. Step and CurrentStep are
// 0-based.
type SequenceRef struct {
	ID          string
	Number      int64
	Title       string
	Status      SequenceStatus
	Step        int
	Steps       int
	CurrentStep int
}

// SequenceActions says what the viewer may do with a sequence now.
type SequenceActions struct {
	Cancel bool
}

// Sequence is a sequence as a viewer sees it. Steps are in order, with
// counts but no row images (each step's own detail has those).
type Sequence struct {
	ID            string
	Number        int64
	Author        Person
	Via           audit.Via
	APITokenID    string
	AgentName     string
	Title         string
	Reason        string
	Status        SequenceStatus
	StatusCode    string
	StatusMessage string
	CurrentStep   int
	CreatedAt     time.Time
	UpdatedAt     time.Time
	FinishedAt    *time.Time
	Steps         []Change
	Actions       SequenceActions
	// AlreadyExisted is true when a submission matched a running sequence
	// of the same author with the same steps, which is returned instead.
	AlreadyExisted bool
	ServerTime     time.Time
}

// ByAgent reports whether an agent submitted the sequence.
func (q Sequence) ByAgent() bool { return q.Via == audit.ViaMCP || q.APITokenID != "" }

// StepError is an error of one step of a sequence (0-based). Nothing was
// created.
type StepError struct {
	Step int
	Err  error
}

func (e *StepError) Error() string {
	return fmt.Sprintf("changes: step %d: %s", e.Step+1, strings.TrimPrefix(e.Err.Error(), "changes: "))
}

// Unwrap returns the step's error (a *DryRunError).
func (e *StepError) Unwrap() error { return e.Err }

const stepsHashVersion = "dbairlock/sequence/v1\x00"

// stepsHash identifies a sequence's steps: every step's connection and SQL
// hash, in order.
func stepsHash(conns, sqlHashes []string) string {
	h := sha256.New()
	h.Write([]byte(stepsHashVersion))
	h.Write(u64(uint64(len(conns))))
	for i := range conns {
		h.Write(u64(uint64(len(conns[i]))))
		h.Write([]byte(conns[i]))
		h.Write([]byte(sqlHashes[i]))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// stepTitle is the title of a step without one: the sequence's, with the
// step, within MaxTitle characters.
func stepTitle(title string, step, steps int) string {
	suffix := fmt.Sprintf(" · step %d of %d", step+1, steps)
	room := MaxTitle - utf8.RuneCountInString(suffix)
	if r := []rune(title); len(r) > room {
		title = strings.TrimSpace(string(r[:room-1])) + "…"
	}
	return title + suffix
}

// preparedStep is a validated step of a new sequence.
type preparedStep struct {
	conn  targets.Connection
	title string
	sqls  []string
	out   runOutcome
}

// CreateSequence validates and submits a sequence. Every step's statements
// are validated against its connection; only the first step is dry-run,
// since the later steps' rows may depend on the earlier ones. The first
// step is submitted for review; the others wait. If the author already has
// a running sequence with the same steps, it is returned with
// AlreadyExisted set and nothing is created.
func (s *Service) CreateSequence(ctx context.Context, p identity.Principal, in SequenceDraft) (Sequence, error) {
	if err := identity.Can(p, identity.ActionChangeSubmit, identity.Resource{Type: "connection"}); err != nil {
		return Sequence{}, err
	}
	var fe fieldErrs
	title := checkTitle(&fe, in.Title)
	reason := checkText(&fe, "reason", in.Reason, MaxReason)
	if len(in.Steps) < MinSteps || len(in.Steps) > MaxSteps {
		fe.add("steps", "A sequence has %d to %d steps.", MinSteps, MaxSteps)
		return Sequence{}, fe.err()
	}
	steps := make([]preparedStep, len(in.Steps))
	for i, st := range in.Steps {
		if err := identity.Can(p, identity.ActionChangeSubmit, connectionResource(st.ConnectionID)); err != nil {
			return Sequence{}, err
		}
		prefix := fmt.Sprintf("steps[%d]", i)
		steps[i].title = stepTitle(title, i, len(in.Steps))
		if strings.TrimSpace(st.Title) != "" {
			steps[i].title = checkTitleAt(&fe, prefix+".title", st.Title)
		}
		steps[i].sqls = checkStatementsAt(&fe, prefix+".statements", st.Statements)
		c, err := s.connectionForAt(ctx, &fe, prefix+".connection_id", st.ConnectionID)
		if err != nil {
			return Sequence{}, err
		}
		steps[i].conn = c
	}
	if err := fe.err(); err != nil {
		return Sequence{}, err
	}
	conns, hashes := make([]string, len(steps)), make([]string, len(steps))
	for i, st := range steps {
		conns[i], hashes[i] = st.conn.ID, SQLHash(st.sqls)
	}
	hash := stepsHash(conns, hashes)
	if dup, err := findOpenSequence(ctx, s.opts.Store.Reader(), p.UserID, hash); err != nil || dup != nil {
		if err != nil {
			return Sequence{}, err
		}
		return s.existingSequence(ctx, p, dup.ID)
	}
	// The later steps first: they are cheap to check and need no database.
	for i := 1; i < len(steps); i++ {
		out, err := s.validateOnly(steps[i].conn, steps[i].sqls)
		if err != nil {
			return Sequence{}, err
		}
		if !out.valid() || out.Problem != nil {
			return Sequence{}, &StepError{Step: i, Err: out.err()}
		}
		steps[i].out = out
	}
	first, err := s.dryRun(ctx, steps[0].conn, steps[0].sqls)
	if err != nil {
		return Sequence{}, err
	}
	if !first.ok() {
		return Sequence{}, &StepError{Step: 0, Err: first.err()}
	}
	steps[0].out, steps[0].conn = first, first.Conn

	seqID := s.opts.IDs.NewID()
	var dupID string
	err = s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		q := changesdb.New(tx)
		if err := s.requireActive(ctx, tx, p.UserID); err != nil {
			return err
		}
		dup, err := findOpenSequence(ctx, tx, p.UserID, hash)
		if err != nil {
			return err
		}
		if dup != nil {
			dupID = dup.ID
			return nil
		}
		agent := ""
		if isAgent(p) {
			if agent, err = s.opts.Directory.TokenName(ctx, tx, p.APITokenID); err != nil {
				return err
			}
		}
		number, err := q.NextNumber(ctx)
		if err != nil {
			return fmt.Errorf("changes: next number: %w", err)
		}
		now := s.now()
		if err := q.InsertSequence(ctx, changesdb.InsertSequenceParams{
			ID: seqID, Number: number, AuthorID: p.UserID, Via: viaOf(p), ApiTokenID: p.APITokenID,
			AgentName: agent, Title: title, Reason: reason, Steps: int64(len(steps)), StepsHash: hash,
			CreatedAt: msOf(now), UpdatedAt: msOf(now),
		}); err != nil {
			return fmt.Errorf("changes: insert sequence: %w", err)
		}
		stepAudit := make([]map[string]any, len(steps))
		ids := make([]string, len(steps))
		numbers := make([]int64, len(steps))
		for i, st := range steps {
			if numbers[i], err = q.NextNumber(ctx); err != nil {
				return fmt.Errorf("changes: next number: %w", err)
			}
			ids[i] = s.opts.IDs.NewID()
			stepAudit[i] = map[string]any{
				"step": i + 1, "number": numbers[i], "connection_id": st.conn.ID, "connection": st.conn.Name,
				"title": st.title, "statements": len(st.sqls), "sql_hash": hashes[i],
			}
		}
		payload := map[string]any{
			"number": number, "title": title, "reason": reason, "via": viaOf(p), "steps": stepAudit, "steps_hash": hash,
		}
		if agent != "" {
			payload["agent"] = agent
		}
		if err := s.recordSequence(ctx, tx, audit.SequenceCreated, seqID, payload); err != nil {
			return err
		}
		var expires time.Time
		for i, st := range steps {
			params := changesdb.InsertChangeParams{
				ID: ids[i], Number: numbers[i], ConnectionID: st.conn.ID, AuthorID: p.UserID, Via: viaOf(p),
				ApiTokenID: p.APITokenID, AgentName: agent, Title: st.title, Reason: reason,
				Status: string(StatusDraft), SqlHash: hashes[i], Revision: 1,
				CreatedAt: msOf(now), UpdatedAt: msOf(now),
				SequenceID: sql.NullString{String: seqID, Valid: true}, Step: someInt(int64(i)),
			}
			if i == 0 {
				to, err := transition(StatusDraft, EventSubmit)
				if err != nil {
					return err
				}
				expires = now.Add(time.Duration(st.conn.Policy.ApprovalTTLHours) * time.Hour)
				params.Status = string(to)
				params.PreviewHash = previewHash(st.out.expectations())
				params.PreviewedAt = someInt(msOf(now))
				params.SubmittedAt = someInt(msOf(now))
				params.ExpiresAt = someInt(msOf(expires))
			}
			if err := q.InsertChange(ctx, params); err != nil {
				return fmt.Errorf("changes: insert step: %w", err)
			}
			rows, err := statementParams(ids[i], 1, st.out, st.conn.Policy.StoreRowImages)
			if err != nil {
				return err
			}
			for _, r := range rows {
				if err := q.InsertStatement(ctx, r); err != nil {
					return fmt.Errorf("changes: insert statement: %w", err)
				}
			}
			created := map[string]any{
				"number": numbers[i], "connection_id": st.conn.ID, "connection": st.conn.Name, "title": st.title,
				"reason": reason, "statements": statementAudit(st.out), "sql_hash": hashes[i],
				"preview_hash": params.PreviewHash, "via": params.Via, "sequence": number, "step": i + 1,
			}
			if agent != "" {
				created["agent"] = agent
			}
			if err := s.record(ctx, tx, audit.ChangeCreated, ids[i], created); err != nil {
				return err
			}
			if i == 0 {
				if err := s.record(ctx, tx, audit.ChangeSubmitted, ids[i], map[string]any{
					"number": numbers[i], "sql_hash": hashes[i], "preview_hash": params.PreviewHash,
					"expires_at": expires, "sequence": number, "step": 1,
				}); err != nil {
					return err
				}
			}
		}
		return s.recordSequence(ctx, tx, audit.SequenceSubmitted, seqID, map[string]any{
			"number": number, "step": 1, "change": numbers[0], "expires_at": expires,
		})
	})
	if err != nil {
		return Sequence{}, err
	}
	if dupID != "" {
		return s.existingSequence(ctx, p, dupID)
	}
	return s.GetSequence(ctx, p, seqID)
}

func findOpenSequence(ctx context.Context, db store.DBTX, authorID, hash string) (*changesdb.ChangeSequence, error) {
	row, err := changesdb.New(db).FindOpenSequence(ctx, changesdb.FindOpenSequenceParams{AuthorID: authorID, StepsHash: hash})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("changes: find open sequence: %w", err)
	}
	return &row, nil
}

func (s *Service) existingSequence(ctx context.Context, p identity.Principal, id string) (Sequence, error) {
	q, err := s.GetSequence(ctx, p, id)
	if err != nil {
		return Sequence{}, err
	}
	q.AlreadyExisted = true
	return q, nil
}

// recordSequence writes the audit event of a sequence and, for the events
// that notify, queues the notification in the same transaction.
func (s *Service) recordSequence(ctx context.Context, tx *store.Tx, action audit.Action, id string, payload map[string]any) error {
	if err := s.opts.Audit.Record(ctx, tx, audit.Event{Action: action, SubjectType: "sequence", SubjectID: id, Payload: payload}); err != nil {
		return err
	}
	return s.notifySequence(ctx, tx, action, id)
}

// loadSequence reads sequence ref (its id or its number) through db.
func loadSequence(ctx context.Context, db store.DBTX, ref string) (changesdb.ChangeSequence, error) {
	q := changesdb.New(db)
	var (
		row changesdb.ChangeSequence
		err error
	)
	if n, perr := strconv.ParseInt(ref, 10, 64); perr == nil && n > 0 {
		row, err = q.GetSequenceByNumber(ctx, n)
	} else {
		row, err = q.GetSequence(ctx, ref)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return row, ErrNotFound
	}
	if err != nil {
		return row, fmt.Errorf("changes: read sequence: %w", err)
	}
	return row, nil
}

// canReadSequence authorizes viewing a sequence: every signed-in person;
// an agent only its token owner's.
func canReadSequence(p identity.Principal, row changesdb.ChangeSequence) error {
	if err := identity.Can(p, identity.ActionChangeSubmit, identity.Resource{Type: "connection"}); err != nil {
		return err
	}
	if isAgent(p) && row.AuthorID != p.UserID {
		return ErrNotFound
	}
	return nil
}

func refOf(q changesdb.ChangeSequence, step int64) *SequenceRef {
	return &SequenceRef{
		ID: q.ID, Number: q.Number, Title: q.Title, Status: SequenceStatus(q.Status),
		Step: int(step), Steps: int(q.Steps), CurrentStep: int(q.CurrentStep),
	}
}

// sequenceRefFor returns the sequence of change row, or nil.
func sequenceRefFor(ctx context.Context, db store.DBTX, row changesdb.ChangeRequest) (*SequenceRef, error) {
	if !row.SequenceID.Valid {
		return nil, nil
	}
	q, err := changesdb.New(db).GetSequence(ctx, row.SequenceID.String)
	if err != nil {
		return nil, fmt.Errorf("changes: read sequence: %w", err)
	}
	return refOf(q, row.Step.Int64), nil
}

// GetSequence returns sequence ref (its id or number) as viewer p sees it.
func (s *Service) GetSequence(ctx context.Context, p identity.Principal, ref string) (Sequence, error) {
	db := s.opts.Store.Reader()
	row, err := loadSequence(ctx, db, ref)
	if err != nil {
		return Sequence{}, err
	}
	if err := canReadSequence(p, row); err != nil {
		return Sequence{}, err
	}
	steps, err := changesdb.New(db).ListSteps(ctx, sql.NullString{String: row.ID, Valid: true})
	if err != nil {
		return Sequence{}, fmt.Errorf("changes: read steps: %w", err)
	}
	users, err := s.opts.Directory.Users(ctx, db, []string{row.AuthorID})
	if err != nil {
		return Sequence{}, err
	}
	out := Sequence{
		ID: row.ID, Number: row.Number, Author: person(users, row.AuthorID), Via: audit.Via(row.Via),
		APITokenID: row.ApiTokenID, AgentName: row.AgentName, Title: row.Title, Reason: row.Reason,
		Status: SequenceStatus(row.Status), StatusCode: row.StatusCode, StatusMessage: row.StatusMessage,
		CurrentStep: int(row.CurrentStep), CreatedAt: timeOf(row.CreatedAt), UpdatedAt: timeOf(row.UpdatedAt),
		FinishedAt: optTime(row.FinishedAt), ServerTime: s.now(),
	}
	for _, st := range steps {
		c, err := s.assemble(ctx, p, st, false)
		if err != nil {
			return Sequence{}, err
		}
		c.Approvals = -1 // not computed without reviews; each step's detail has it
		out.Steps = append(out.Steps, c)
	}
	res := identity.Resource{Type: "connection"}
	author := row.AuthorID == p.UserID && identity.Can(p, identity.ActionChangeSubmit, res) == nil
	admin := !isAgent(p) && identity.Can(p, identity.ActionChangeAdminister, res) == nil
	out.Actions.Cancel = out.Status == SequenceRunning && (author || admin)
	return out, nil
}

// SequenceEvents returns the sequence's own audit events, oldest first. Each
// step's events are in that step's timeline.
func (s *Service) SequenceEvents(ctx context.Context, p identity.Principal, ref string) ([]audit.Entry, error) {
	row, err := loadSequence(ctx, s.opts.Store.Reader(), ref)
	if err != nil {
		return nil, err
	}
	if err := canReadSequence(p, row); err != nil {
		return nil, err
	}
	page, err := s.opts.AuditLog.List(ctx, audit.Filter{SubjectType: "sequence", SubjectID: row.ID, Limit: audit.MaxPageSize})
	if err != nil {
		return nil, err
	}
	out := page.Entries
	slices.Reverse(out)
	return out, nil
}

// CancelSequence stops a running sequence: its current step is cancelled
// (which cancels the steps after it), or, if the current step is executing
// right now, the sequence stops after it and the later steps are
// cancelled. Steps already executed stay applied. The author or an admin.
func (s *Service) CancelSequence(ctx context.Context, p identity.Principal, ref, reason string) (Sequence, error) {
	row, err := loadSequence(ctx, s.opts.Store.Reader(), ref)
	if err != nil {
		return Sequence{}, err
	}
	if p.UserID == "" {
		return Sequence{}, identity.ErrUnauthenticated
	}
	if err := canReadSequence(p, row); err != nil {
		return Sequence{}, err
	}
	res := identity.Resource{Type: "connection"}
	author := row.AuthorID == p.UserID && identity.Can(p, identity.ActionChangeSubmit, res) == nil
	admin := !isAgent(p) && identity.Can(p, identity.ActionChangeAdminister, res) == nil
	if !author && !admin {
		return Sequence{}, identity.ErrForbidden
	}
	var fe fieldErrs
	reason = checkText(&fe, "reason", reason, MaxComment)
	if err := fe.err(); err != nil {
		return Sequence{}, err
	}
	code := CancelledByAuthor
	if row.AuthorID != p.UserID {
		code = CancelledByAdmin
	}
	err = s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		q := changesdb.New(tx)
		cur, err := q.GetSequence(ctx, row.ID)
		if err != nil {
			return fmt.Errorf("changes: read sequence: %w", err)
		}
		if err := s.requireActive(ctx, tx, p.UserID); err != nil {
			return err
		}
		if SequenceStatus(cur.Status) != SequenceRunning {
			return stateErr("This sequence has already %s.", cur.Status)
		}
		steps, err := q.ListSteps(ctx, sql.NullString{String: cur.ID, Valid: true})
		if err != nil {
			return fmt.Errorf("changes: read steps: %w", err)
		}
		if int(cur.CurrentStep) >= len(steps) {
			return fmt.Errorf("changes: sequence #%d has no step %d", cur.Number, cur.CurrentStep+1)
		}
		step := steps[cur.CurrentStep]
		if _, err := transition(Status(step.Status), EventCancel); err == nil {
			// The step's cancellation stops the sequence (onStepEvent).
			return s.cancelTx(ctx, tx, step, code, reason)
		}
		// The current step is executing: it finishes, and nothing after it
		// runs.
		return s.stopTx(ctx, tx, cur, step, StopCancelled, reason)
	})
	if err != nil {
		return Sequence{}, err
	}
	return s.GetSequence(ctx, p, row.ID)
}

// waitingStepErr explains why a step that waits for an earlier step cannot
// be reviewed or previewed yet, or returns nil if row is not such a step.
func waitingStepErr(ctx context.Context, db store.DBTX, row changesdb.ChangeRequest) error {
	if !row.SequenceID.Valid || Status(row.Status) != StatusDraft {
		return nil
	}
	q, err := changesdb.New(db).GetSequence(ctx, row.SequenceID.String)
	if err != nil {
		return fmt.Errorf("changes: read sequence: %w", err)
	}
	if SequenceStatus(q.Status) != SequenceRunning {
		return stateErr("This step of sequence #%d will not run: the sequence has %s.", q.Number, q.Status)
	}
	return stateErr("This is step %d of sequence #%d. It is previewed and reviewed after step %d has run.",
		row.Step.Int64+1, q.Number, row.Step.Int64)
}

// stopCodes maps the step events that stop a sequence to the reason.
var stopCodes = map[audit.Action]string{
	audit.ChangeRejected:       StopRejected,
	audit.ChangeCancelled:      StopCancelled,
	audit.ChangeExpired:        StopExpired,
	audit.ExecutionFailed:      StopFailed,
	audit.ExecutionInterrupted: StopFailed,
}

// onStepEvent moves the sequence of change id on, or stops it, after the
// step's event action. It runs inside the transaction recording the event.
func (s *Service) onStepEvent(ctx context.Context, tx *store.Tx, action audit.Action, id string) error {
	code, stops := stopCodes[action]
	if !stops && action != audit.ExecutionSucceeded {
		return nil
	}
	q := changesdb.New(tx)
	row, err := q.GetChange(ctx, id)
	if err != nil {
		return fmt.Errorf("changes: read change: %w", err)
	}
	if !row.SequenceID.Valid {
		return nil
	}
	seq, err := q.GetSequence(ctx, row.SequenceID.String)
	if err != nil {
		return fmt.Errorf("changes: read sequence: %w", err)
	}
	if SequenceStatus(seq.Status) != SequenceRunning {
		return nil // already stopped: this is one of the steps it cancels
	}
	if stops {
		return s.stopTx(ctx, tx, seq, row, code, "")
	}
	now := msOf(s.now())
	step := row.Step.Int64
	if step >= seq.Steps-1 {
		if err := q.FinishSequence(ctx, changesdb.FinishSequenceParams{
			Status: string(SequenceCompleted), CurrentStep: step, FinishedAt: someInt(now), UpdatedAt: now, ID: seq.ID,
		}); err != nil {
			return fmt.Errorf("changes: complete sequence: %w", err)
		}
		return s.recordSequence(ctx, tx, audit.SequenceCompleted, seq.ID, map[string]any{
			"number": seq.Number, "steps": seq.Steps, "last_change": row.Number,
		})
	}
	if err := q.SetSequenceStep(ctx, changesdb.SetSequenceStepParams{CurrentStep: step + 1, UpdatedAt: now, ID: seq.ID}); err != nil {
		return fmt.Errorf("changes: advance sequence: %w", err)
	}
	return s.recordSequence(ctx, tx, audit.SequenceAdvanced, seq.ID, map[string]any{
		"number": seq.Number, "executed_step": step + 1, "executed_change": row.Number, "next_step": step + 2,
	})
}

// stopMessage says why a sequence stopped, naming the step (0-based).
func stopMessage(code string, step int64) string {
	what := map[string]string{
		StopFailed: "failed", StopRejected: "was rejected", StopExpired: "expired", StopCancelled: "was cancelled",
	}[code]
	if what == "" {
		what = code
	}
	return fmt.Sprintf("Stopped: step %d %s.", step+1, what)
}

// stopTx stops running sequence seq because of step trigger: it records why,
// cancels every other open step, and leaves executed steps as they are
// (they are not rolled back). note is an optional comment from the person
// who stopped it.
func (s *Service) stopTx(ctx context.Context, tx *store.Tx, seq changesdb.ChangeSequence, trigger changesdb.ChangeRequest, code, note string) error {
	q := changesdb.New(tx)
	msg := stopMessage(code, trigger.Step.Int64)
	now := msOf(s.now())
	if err := q.FinishSequence(ctx, changesdb.FinishSequenceParams{
		Status: string(SequenceStopped), StatusCode: code, StatusMessage: msg, CurrentStep: trigger.Step.Int64,
		FinishedAt: someInt(now), UpdatedAt: now, ID: seq.ID,
	}); err != nil {
		return fmt.Errorf("changes: stop sequence: %w", err)
	}
	steps, err := q.ListSteps(ctx, sql.NullString{String: seq.ID, Valid: true})
	if err != nil {
		return fmt.Errorf("changes: read steps: %w", err)
	}
	applied, cancelled := []int64{}, []int64{}
	for _, st := range steps {
		if st.ID == trigger.ID {
			continue
		}
		switch {
		case Status(st.Status) == StatusExecuted:
			applied = append(applied, st.Step.Int64+1)
		case Status(st.Status).Open():
			if err := s.cancelTx(ctx, tx, st, CancelledSequenceStopped, msg); err != nil {
				return err
			}
			cancelled = append(cancelled, st.Step.Int64+1)
		}
	}
	payload := map[string]any{
		"number": seq.Number, "step": trigger.Step.Int64 + 1, "change": trigger.Number, "code": code,
		"message": msg, "applied_steps": applied, "cancelled_steps": cancelled,
	}
	if note != "" {
		payload["reason"] = note
	}
	return s.recordSequence(ctx, tx, audit.SequenceStopped, seq.ID, payload)
}

// AdvanceSequences previews and submits the current step of every running
// sequence whose previous step executed but whose current step is still
// waiting: normally done right after the execution (RecordExecuted), and
// here again by the executor's cycle in case that did not happen. ctx must
// carry the system actor. It returns how many steps it submitted.
func (s *Service) AdvanceSequences(ctx context.Context) (int, error) {
	due, err := changesdb.New(s.opts.Store.Reader()).ListSequencesToAdvance(ctx)
	if err != nil {
		return 0, fmt.Errorf("changes: list sequences to advance: %w", err)
	}
	n := 0
	var first error
	for _, q := range due {
		if err := s.advanceSequence(ctx, q.ID); err != nil {
			s.opts.Logger.Error("changes: previewing the next step of a sequence failed", "sequence", q.Number, "error", err)
			if first == nil {
				first = err
			}
			continue
		}
		n++
	}
	return n, first
}

// advanceSequence dry-runs the current step of sequence id, now that the
// step before it has executed, and submits it for review with the normal
// approval policy and freshness window. If the dry run fails (no rows match
// any more, the database cannot be reached), the step is still submitted,
// with the failure stored: like a failed preview update, it cannot be
// approved until a later preview succeeds or the author edits it. A step on
// a connection archived meanwhile is cancelled, which stops the sequence.
func (s *Service) advanceSequence(ctx context.Context, id string) error {
	db := s.opts.Store.Reader()
	q := changesdb.New(db)
	seq, err := q.GetSequence(ctx, id)
	if err != nil {
		return fmt.Errorf("changes: read sequence: %w", err)
	}
	if SequenceStatus(seq.Status) != SequenceRunning {
		return nil
	}
	steps, err := q.ListSteps(ctx, sql.NullString{String: seq.ID, Valid: true})
	if err != nil {
		return fmt.Errorf("changes: read steps: %w", err)
	}
	if int(seq.CurrentStep) >= len(steps) {
		return fmt.Errorf("changes: sequence #%d has no step %d", seq.Number, seq.CurrentStep+1)
	}
	row := steps[seq.CurrentStep]
	if Status(row.Status) != StatusDraft {
		return nil
	}
	c, err := s.opts.Targets.Get(ctx, row.ConnectionID)
	if err != nil {
		return err
	}
	stmts, err := s.statementsOf(ctx, db, row)
	if err != nil {
		return err
	}
	out, err := s.dryRun(ctx, c, sqlTexts(stmts))
	if err != nil {
		return err
	}
	return s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		q := changesdb.New(tx)
		curSeq, err := q.GetSequence(ctx, seq.ID)
		if err != nil {
			return fmt.Errorf("changes: read sequence: %w", err)
		}
		cur, err := q.GetChange(ctx, row.ID)
		if err != nil {
			return fmt.Errorf("changes: read change: %w", err)
		}
		if SequenceStatus(curSeq.Status) != SequenceRunning || curSeq.CurrentStep != seq.CurrentStep ||
			Status(cur.Status) != StatusDraft || cur.Revision != row.Revision {
			return nil // it moved on (stopped, or edited: the next cycle previews the new revision)
		}
		if out.Problem != nil && out.Problem.Code == CodeArchived {
			return s.cancelTx(ctx, tx, cur, CancelledArchived, "The connection was archived before this step ran.")
		}
		now := s.now()
		hash, _, err := saveRun(ctx, q, cur, out, msOf(now), cur.LastRefreshAt)
		if err != nil {
			return err
		}
		if _, err := transition(Status(cur.Status), EventSubmit); err != nil {
			return err
		}
		expires := now.Add(time.Duration(out.Conn.Policy.ApprovalTTLHours) * time.Hour)
		if err := q.MarkSubmitted(ctx, changesdb.MarkSubmittedParams{
			SubmittedAt: someInt(msOf(now)), ExpiresAt: someInt(msOf(expires)), UpdatedAt: msOf(now), ID: cur.ID,
		}); err != nil {
			return fmt.Errorf("changes: submit step: %w", err)
		}
		payload := map[string]any{
			"number": cur.Number, "sql_hash": cur.SqlHash, "preview_hash": hash, "statements": statementAudit(out),
			"expires_at": expires, "sequence": curSeq.Number, "step": cur.Step.Int64 + 1, "automatic": true,
		}
		if !out.ok() {
			payload["preview_error"] = out.summary()
		}
		return s.record(ctx, tx, audit.ChangeSubmitted, cur.ID, payload)
	})
}
