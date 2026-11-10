package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/alvaroibarguen/dbairlock/internal/changes"
	"github.com/alvaroibarguen/dbairlock/internal/connector"
	"github.com/alvaroibarguen/dbairlock/internal/identity"
	"github.com/alvaroibarguen/dbairlock/internal/targets"
)

// allowedSQL is the reminder appended to validation errors.
const allowedSQL = "Only single-table INSERT ... VALUES, UPDATE ... SET ... WHERE and DELETE FROM ... WHERE statements are accepted, one statement per list entry."

// userError is a tool error whose message is written for the agent.
type userError struct{ msg string }

func (e *userError) Error() string { return e.msg }

func agentErr(msg string) error { return &userError{msg: msg} }

func userErrorf(format string, args ...any) error {
	return &userError{msg: fmt.Sprintf(format, args...)}
}

var errInternal = agentErr("dbairlock could not complete this call because of an internal error. Try again later; if it keeps failing, tell the user.")

// toolError turns an error from a module into the text an agent reads.
// Error text is the agent's only feedback, so it says what was wrong and
// what to do instead. Unknown errors are logged and reported generically.
func (s *Server) toolError(ctx context.Context, tool string, err error) error {
	var (
		ue  *userError
		rl  *rateLimitError
		dre *changes.DryRunError
		ie  *changes.InputError
		se  *changes.StateError
		dup *changes.DuplicateError
	)
	switch {
	case errors.As(err, &ue), errors.As(err, &rl):
		return err
	case errors.As(err, &dre):
		return agentErr(dryRunMessage(dre))
	case errors.As(err, &ie):
		return agentErr(inputMessage(ie))
	case errors.As(err, &dup):
		return userErrorf("You already have an open change with exactly this SQL on this connection: #%d. Use get_change with number %d.", dup.Number, dup.Number)
	case errors.As(err, &se):
		return agentErr(se.Message)
	case errors.Is(err, changes.ErrNotFound):
		return agentErr("No such change among the changes authored by this token's owner. Call list_changes to see them.")
	case errors.Is(err, changes.ErrInvalidState):
		return agentErr("That is not possible in the change's current status. Call get_change to see its status.")
	case errors.Is(err, changes.ErrAgentReview), errors.Is(err, changes.ErrSelfReview):
		return agentErr("Agents cannot review or approve changes; a person approves them in the dashboard.")
	case errors.Is(err, changes.ErrTargetUnavailable):
		return agentErr("The database could not be reached, so nothing was checked or created. Try again in a few minutes and tell the user if it persists.")
	case errors.Is(err, targets.ErrArchived):
		return agentErr("This connection is archived and cannot be used. Call list_connections for the active ones.")
	case errors.Is(err, targets.ErrNotFound):
		return agentErr("That connection does not exist. Call list_connections for the active ones.")
	case errors.Is(err, identity.ErrUnauthenticated):
		return agentErr("The API token is no longer valid (revoked, expired, or its owner was removed). Ask the user for a new token.")
	case errors.Is(err, identity.ErrForbidden):
		return agentErr("The owner of this API token is not allowed to do this.")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return agentErr("The call was cancelled before it finished. Nothing was created unless get_change or list_changes shows it.")
	}
	s.opts.Logger.ErrorContext(ctx, "mcp tool failed", slog.String("tool", tool), slog.Any("error", err))
	return errInternal
}

// statementLabel names statement position (0-based) for an agent:
// "Statement 2".
func statementLabel(position int) string { return "Statement " + strconv.Itoa(position+1) }

// issueText is one problem with its position in the statement text.
func issueText(iss *changes.Issue) string {
	msg := strings.TrimSpace(iss.Message)
	if iss.Position >= 0 {
		msg += fmt.Sprintf(" (at character %d)", iss.Position+1)
	}
	return msg
}

// dryRunMessage lists every statement's problem, so the agent can fix all
// of them in one pass.
func dryRunMessage(e *changes.DryRunError) string {
	var b strings.Builder
	b.WriteString("The change was not submitted and nothing was created.")
	if e.Problem != nil {
		b.WriteString(" " + strings.TrimSpace(e.Problem.Message))
		if e.Problem.Code == changes.CodeUnavailable {
			b.WriteString(" Try again in a few minutes.")
		}
		return b.String()
	}
	b.WriteString(" Fix these problems and try again (preview_change checks without submitting):")
	invalid := false
	for _, st := range e.Statements {
		if st.Issue == nil {
			continue
		}
		b.WriteString("\n- " + statementLabel(st.Position))
		if st.Statement != nil {
			b.WriteString(" (" + kindName(st.Statement.Kind) + " " + qualified(st.Statement.Schema, st.Statement.Table) + ")")
		}
		b.WriteString(": " + issueText(st.Issue))
		if st.Statement == nil {
			invalid = true
		}
	}
	if invalid {
		b.WriteString("\n" + allowedSQL + " UPDATE and DELETE need a WHERE clause that is not always true.")
	}
	return b.String()
}

// inputMessage lists every invalid argument.
func inputMessage(e *changes.InputError) string {
	var b strings.Builder
	b.WriteString("The request is invalid and nothing was created:")
	for _, f := range e.Fields {
		b.WriteString("\n- " + fieldLabel(f.Field) + ": " + f.Message)
	}
	return b.String()
}

// fieldLabel converts a changes field name to the tool argument it came
// from.
func fieldLabel(field string) string {
	if rest, ok := strings.CutPrefix(field, "statements["); ok {
		if n, err := strconv.Atoi(strings.TrimSuffix(rest, "]")); err == nil {
			return statementLabel(n)
		}
	}
	if field == "connection_id" {
		return "connection"
	}
	return field
}

// kindName is a statement kind as SQL spells it: INSERT, UPDATE, DELETE.
func kindName(k connector.Kind) string { return strings.ToUpper(string(k)) }

func qualified(schema, table string) string {
	if schema == "" {
		return table
	}
	return schema + "." + table
}
