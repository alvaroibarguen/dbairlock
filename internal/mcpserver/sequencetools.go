package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/alvaroibarguen/dbairlock/internal/changes"
)

// Sequence tools: ordered changes across connections. Thin adapters like
// the change tools: the changes module applies every rule, and outputs
// carry identifiers, counts and statuses only.

type stepIn struct {
	Connection string   `json:"connection" jsonschema:"Connection name from list_connections; steps may use different connections and databases (Postgres and MySQL mixed)"`
	Title      string   `json:"title,omitempty" jsonschema:"Optional one-line title of this step; defaults to the sequence title with the step number"`
	Statements []string `json:"statements" jsonschema:"SQL statements of this step, one per entry, run in order in one transaction on this step's connection"`
}

type submitSequenceIn struct {
	Title  string   `json:"title" jsonschema:"One line saying what the whole sequence does, for the reviewer"`
	Reason string   `json:"reason" jsonschema:"Why the sequence is needed: the request, ticket or context the reviewer should know"`
	Steps  []stepIn `json:"steps" jsonschema:"2 to 10 steps, in the order they run. Each step runs only after the step before it has executed"`
}

// StepInfo is one step of a sequence.
type StepInfo struct {
	Step       int              `json:"step" jsonschema:"Position in the sequence, from 1"`
	Number     int64            `json:"number" jsonschema:"The step's change number; get_change shows its reviews and execution"`
	Title      string           `json:"title"`
	Connection string           `json:"connection"`
	Status     string           `json:"status" jsonschema:"The step's change status. A step waiting for an earlier step is a draft with no row counts yet"`
	StatusCode string           `json:"status_code,omitempty" jsonschema:"Why the step is in its status, for example sequence_stopped or drift"`
	Statements []StatementCount `json:"statements" jsonschema:"Row counts are known only for steps that have been previewed"`
}

type sequenceOut struct {
	Number         int64      `json:"number" jsonschema:"Sequence number"`
	Title          string     `json:"title"`
	Status         string     `json:"status" jsonschema:"running, completed (every step executed) or stopped (a step failed, was rejected, expired or was cancelled)"`
	StatusCode     string     `json:"status_code,omitempty" jsonschema:"For a stopped sequence: failed, rejected, expired or cancelled"`
	Message        string     `json:"message" jsonschema:"What the status means, which steps were applied, and what happens next"`
	ReviewURL      string     `json:"review_url" jsonschema:"Dashboard page of the sequence; give it to the user"`
	CurrentStep    int        `json:"current_step" jsonschema:"The step (from 1) that is open now, or the one that ended the sequence"`
	AlreadyExisted bool       `json:"already_existed,omitempty" jsonschema:"True when a running sequence with the same steps already existed and was returned instead of creating another"`
	Steps          []StepInfo `json:"steps"`
}

type cancelSequenceIn struct {
	Number int64  `json:"number" jsonschema:"Sequence number"`
	Reason string `json:"reason,omitempty" jsonschema:"Why it is cancelled (optional)"`
}

func (s *Server) registerSequences() {
	addTool(s, tool[submitSequenceIn, sequenceOut]{
		def: &mcp.Tool{
			Name: "submit_sequence",
			Description: "Submits a sequence for human review: 2 to 10 steps that run in order, each a change on its own connection (the same or different " +
				"databases, Postgres and MySQL mixed). Use it when a later change depends on an earlier one having run, for example copy a row to one " +
				"database, then update another. Each step is atomic on its own database; the sequence as a whole is NOT: if a later step fails, is " +
				"rejected, expires or is cancelled, the sequence stops and the steps already run stay applied (nothing is rolled back). " +
				"Every step's SQL is validated now; only step 1 is dry-run and goes to review now. Each later step is dry-run and goes to review " +
				"when the step before it has executed, and a person other than the token's owner approves every step; you cannot approve anything. " +
				"If validation or step 1's dry run fails, nothing is created and the error names the step. Calling it again with the same steps while " +
				"that sequence runs returns it (already_existed true). Give the user the review URL. " + allowedSQL + " UPDATE and DELETE need a WHERE clause.",
			Annotations: &mcp.ToolAnnotations{Title: "Submit a sequence for review", DestructiveHint: boolPtr(false), IdempotentHint: true, OpenWorldHint: boolPtr(true)},
		},
		kind: limitSubmit,
		args: func(in submitSequenceIn) map[string]any {
			conns := make([]string, len(in.Steps))
			for i, st := range in.Steps {
				conns[i] = st.Connection
			}
			return map[string]any{"steps": len(in.Steps), "connections": conns}
		},
		run: s.submitSequence,
	})
	addTool(s, tool[numberIn, sequenceOut]{
		def: &mcp.Tool{
			Name: "get_sequence",
			Description: "Returns the status of one of your sequences: every step's change number and status, which step is open now, and, if it " +
				"stopped, why and which steps were applied. Use get_change with a step's number for its reviews and execution. Sequences wait for " +
				"people, so check back when the user asks, not in a tight loop.",
			Annotations: readOnly("Get a sequence"),
		},
		args: func(in numberIn) map[string]any { return map[string]any{"number": in.Number} },
		run:  s.getSequence,
	})
	addTool(s, tool[cancelSequenceIn, sequenceOut]{
		def: &mcp.Tool{
			Name: "cancel_sequence",
			Description: "Cancels one of your running sequences: the open step and every later step are cancelled. Steps that already ran stay " +
				"applied; nothing is rolled back. It cannot be undone.",
			Annotations: &mcp.ToolAnnotations{Title: "Cancel a sequence", DestructiveHint: boolPtr(true), IdempotentHint: true, OpenWorldHint: boolPtr(false)},
		},
		args: func(in cancelSequenceIn) map[string]any { return map[string]any{"number": in.Number} },
		run:  s.cancelSequence,
	})
}

// sequenceErr names a missing sequence as a sequence; other errors are
// mapped like a change's.
func sequenceErr(err error) error {
	if errors.Is(err, changes.ErrNotFound) {
		return agentErr("No such sequence among the sequences authored by this token's owner. A sequence's number is in submit_sequence's result, " +
			"or as \"sequence\" in list_changes.")
	}
	return err
}

// sequenceURL is the dashboard page of sequence number.
func (s *Server) sequenceURL(number int64) string {
	return strings.Replace(s.reviewURL(number), "/changes/", "/sequences/", 1)
}

func (s *Server) submitSequence(ctx context.Context, c call[submitSequenceIn]) (result[sequenceOut], error) {
	in := changes.SequenceDraft{Title: c.in.Title, Reason: c.in.Reason}
	for i, st := range c.in.Steps {
		conn, err := s.connection(ctx, st.Connection)
		if err != nil {
			return result[sequenceOut]{}, userErrorf("Step %d: %s", i+1, err.Error())
		}
		in.Steps = append(in.Steps, changes.StepDraft{ConnectionID: conn.ID, Title: st.Title, Statements: st.Statements})
	}
	q, err := s.opts.Changes.CreateSequence(ctx, c.p, in)
	if err != nil {
		return result[sequenceOut]{}, err
	}
	out := s.sequenceOut(q)
	if q.AlreadyExisted {
		out.Message = fmt.Sprintf("You already had a running sequence with exactly these steps, so no new one was created: sequence #%d. %s Give the user the review URL: %s",
			q.Number, out.Message, out.ReviewURL)
	} else {
		out.Message = fmt.Sprintf("Sequence #%d was submitted. Step 1 (change #%d) is in review now; each later step is previewed and reviewed after the step before it has run. "+
			"Each step is atomic on its own database, but steps already run are not undone if a later one stops. Give the user the review URL: %s",
			q.Number, q.Steps[0].Number, out.ReviewURL)
	}
	return result[sequenceOut]{out: out, summary: out.Message}, nil
}

func (s *Server) getSequence(ctx context.Context, c call[numberIn]) (result[sequenceOut], error) {
	if err := checkNumber(c.in.Number); err != nil {
		return result[sequenceOut]{}, err
	}
	q, err := s.opts.Changes.GetSequence(ctx, c.p, strconv.FormatInt(c.in.Number, 10))
	if err != nil {
		return result[sequenceOut]{}, sequenceErr(err)
	}
	out := s.sequenceOut(q)
	return result[sequenceOut]{out: out, summary: fmt.Sprintf("Sequence #%d: %s", q.Number, out.Message)}, nil
}

func (s *Server) cancelSequence(ctx context.Context, c call[cancelSequenceIn]) (result[sequenceOut], error) {
	if err := checkNumber(c.in.Number); err != nil {
		return result[sequenceOut]{}, err
	}
	q, err := s.opts.Changes.CancelSequence(ctx, c.p, strconv.FormatInt(c.in.Number, 10), c.in.Reason)
	if err != nil {
		return result[sequenceOut]{}, sequenceErr(err)
	}
	out := s.sequenceOut(q)
	return result[sequenceOut]{out: out, summary: fmt.Sprintf("Sequence #%d: %s", q.Number, out.Message)}, nil
}

func (s *Server) sequenceOut(q changes.Sequence) sequenceOut {
	out := sequenceOut{
		Number: q.Number, Title: q.Title, Status: string(q.Status), StatusCode: q.StatusCode,
		ReviewURL: s.sequenceURL(q.Number), CurrentStep: q.CurrentStep + 1, AlreadyExisted: q.AlreadyExisted,
		Steps: []StepInfo{}, Message: describeSequence(q),
	}
	for i, st := range q.Steps {
		out.Steps = append(out.Steps, StepInfo{
			Step: i + 1, Number: st.Number, Title: st.Title, Connection: st.ConnectionName,
			Status: string(st.Status), StatusCode: st.StatusCode, Statements: statementCounts(st),
		})
	}
	return out
}

// describeSequence says where q stands, naming the steps that were applied.
func describeSequence(q changes.Sequence) string {
	var applied []string
	for i, st := range q.Steps {
		if st.Status == changes.StatusExecuted {
			applied = append(applied, strconv.Itoa(i+1))
		}
	}
	appliedText := "No step was applied."
	if len(applied) > 0 {
		appliedText = "Applied and not rolled back: step " + strings.Join(applied, ", ") + "."
	}
	switch q.Status {
	case changes.SequenceCompleted:
		return fmt.Sprintf("Completed: all %d steps executed.", len(q.Steps))
	case changes.SequenceStopped:
		return q.StatusMessage + " " + appliedText + " The later steps were cancelled. Submit a new change or sequence for what is left."
	}
	if q.CurrentStep < len(q.Steps) {
		cur := q.Steps[q.CurrentStep]
		return fmt.Sprintf("Running: step %d of %d (change #%d) is %s. %s", q.CurrentStep+1, len(q.Steps), cur.Number, statusLabel(cur.Status), appliedText)
	}
	return "Running."
}
