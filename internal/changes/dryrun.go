package changes

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/alvaroibarguen/dbairlock/internal/connector"
	"github.com/alvaroibarguen/dbairlock/internal/targets"
)

// StatementResult is the outcome of validating and dry-running one
// statement. Statement is set when it passed validation; Result when the dry
// run succeeded; Issue when either failed.
type StatementResult struct {
	Position  int
	SQL       string
	Statement *connector.Statement
	Issue     *Issue
	Result    *connector.Result
}

// runOutcome is the outcome of one dry run of a list of statements.
type runOutcome struct {
	Statements []StatementResult
	// Problem is set when the run as a whole failed: the target could not
	// be reached, the connection is archived, and so on.
	Problem *Issue
	// Conn is the connection as it was when the dry run ran.
	Conn targets.Connection
}

// ok reports whether every statement is valid and the dry run succeeded.
func (o runOutcome) ok() bool {
	if o.Problem != nil {
		return false
	}
	for _, st := range o.Statements {
		if st.Issue != nil || st.Result == nil {
			return false
		}
	}
	return true
}

// valid reports whether every statement passed validation.
func (o runOutcome) valid() bool {
	for _, st := range o.Statements {
		if st.Statement == nil {
			return false
		}
	}
	return true
}

func (o runOutcome) err() error {
	return &DryRunError{Statements: o.Statements, Problem: o.Problem}
}

// summary is a one-line description of what went wrong, for preview_error.
func (o runOutcome) summary() string {
	if o.Problem != nil {
		return o.Problem.Message
	}
	for _, st := range o.Statements {
		if st.Issue != nil {
			return fmt.Sprintf("Statement %d: %s", st.Position+1, st.Issue.Message)
		}
	}
	return ""
}

func (o runOutcome) expectations() []expectation {
	out := make([]expectation, len(o.Statements))
	for i, st := range o.Statements {
		if st.Result != nil {
			out[i] = expectation{Rows: st.Result.RowsAffected, Fingerprint: st.Result.Fingerprint}
		}
	}
	return out
}

// checkTitle validates and normalizes a title: one line, 1 to 200
// characters.
func checkTitle(fe *fieldErrs, title string) string {
	title = strings.TrimSpace(title)
	switch {
	case !utf8.ValidString(title):
		fe.add("title", "The title is not valid text.")
	case title == "":
		fe.add("title", "Enter a title.")
	case utf8.RuneCountInString(title) > MaxTitle:
		fe.add("title", "Use at most %d characters.", MaxTitle)
	case strings.ContainsFunc(title, unicode.IsControl):
		fe.add("title", "The title must be one line of plain text.")
	}
	return title
}

// checkText validates free text that may span lines: at most limit
// characters, no control characters other than newlines and tabs.
func checkText(fe *fieldErrs, field, text string, limit int) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	switch {
	case !utf8.ValidString(text):
		fe.add(field, "This is not valid text.")
	case utf8.RuneCountInString(text) > limit:
		fe.add(field, "Use at most %d characters.", limit)
	case strings.ContainsFunc(text, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }):
		fe.add(field, "Use plain text without control characters.")
	}
	return text
}

// checkStatements validates the statement list's shape (not the SQL): 1 to
// 20 statements, none empty, each at most 64 KB. Surrounding whitespace is
// trimmed; the result is exactly the text that is hashed and executed.
func checkStatements(fe *fieldErrs, in []string) []string {
	if len(in) == 0 {
		fe.add("statements", "Add at least one statement.")
		return nil
	}
	if len(in) > MaxStatements {
		fe.add("statements", "A change can have at most %d statements.", MaxStatements)
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		s = strings.TrimSpace(s)
		field := fmt.Sprintf("statements[%d]", i)
		switch {
		case !utf8.ValidString(s):
			fe.add(field, "The statement is not valid UTF-8 text.")
		case s == "":
			fe.add(field, "This statement is empty.")
		case len(s) > MaxStatementBytes:
			fe.add(field, "A statement can be at most %d KB.", MaxStatementBytes>>10)
		}
		out[i] = s
	}
	return out
}

// validate checks every statement against the connector's rules and the
// connection's policy, as the connection is configured now.
func (s *Service) validate(c targets.Connection, sqls []string) ([]StatementResult, error) {
	cn, ok := s.opts.Targets.Connector(c.Connector)
	if !ok {
		return nil, fmt.Errorf("changes: connection %s uses connector %q, which is not registered", c.Name, c.Connector)
	}
	out := make([]StatementResult, len(sqls))
	for i, text := range sqls {
		out[i] = StatementResult{Position: i, SQL: text}
		st, err := cn.Validate(c.Config, text, c.ConnectorPolicy())
		if err != nil {
			out[i].Issue = rejectionIssue(err)
			continue
		}
		out[i].Statement = &st
	}
	return out, nil
}

func rejectionIssue(err error) *Issue {
	var rej *connector.RejectedError
	if errors.As(err, &rej) {
		return &Issue{Code: string(rej.Code), Message: sentence(rej.Reason), Position: rej.Position}
	}
	return &Issue{Code: string(connector.RejectParse), Message: sentence(err.Error()), Position: -1}
}

// sentence capitalizes msg and ends it with a full stop.
func sentence(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return msg
	}
	r, n := utf8.DecodeRuneInString(msg)
	msg = string(unicode.ToUpper(r)) + msg[n:]
	if !strings.HasSuffix(msg, ".") {
		msg += "."
	}
	return msg
}

// errRevalidated stops a dry run whose statements no longer pass
// validation against the connection as it is inside the run.
var errRevalidated = errors.New("changes: statements no longer valid")

// dryRun validates sqls and, if all are valid, runs them in one rolled-back
// transaction on the target (PLAN.md 5.4). Validation uses the connection's
// policy as it is at the moment of the run. It never touches the store.
func (s *Service) dryRun(ctx context.Context, c targets.Connection, sqls []string) (runOutcome, error) {
	out := runOutcome{Conn: c}
	res, err := s.validate(c, sqls)
	if err != nil {
		return out, err
	}
	out.Statements = res
	if !out.valid() {
		return out, nil
	}
	if c.ArchivedAt != nil {
		out.Problem = &Issue{Code: CodeArchived, Message: "This connection is archived; changes cannot run on it.", Position: -1}
		return out, nil
	}

	lim := s.opts.Targets.Limits(c)
	// Room to wait for another run on this target, plus every statement's
	// timeout twice (the locking select and the statement).
	timeout := 30*time.Second + time.Duration(2*len(sqls))*lim.StatementTimeout + 10*time.Second
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ran := false
	err = s.opts.Targets.WithConn(rctx, c.ID, func(ctx context.Context, cur targets.Connection, conn connector.Conn) error {
		ran = true
		out.Conn = cur
		// Validate again with the connection as it is now: the policy may
		// have changed since the first check.
		again, err := s.validate(cur, sqls)
		if err != nil {
			return err
		}
		out.Statements = again
		if !out.valid() {
			return errRevalidated
		}
		stmts := make([]connector.Statement, len(again))
		for i, st := range again {
			stmts[i] = *st.Statement
		}
		results, err := conn.DryRun(ctx, stmts, s.opts.Targets.Limits(cur), connector.RunInfo{Label: PreviewLabel})
		if err != nil {
			return err
		}
		if len(results) != len(stmts) {
			return fmt.Errorf("changes: the connector returned %d results for %d statements", len(results), len(stmts))
		}
		for i := range results {
			r := results[i]
			out.Statements[i].Result = &r
		}
		return nil
	})
	switch {
	case err == nil, errors.Is(err, errRevalidated):
		return out, nil
	case !ran:
		out.Problem = beforeRunIssue(err)
		return out, nil
	}
	if errors.Is(err, connector.ErrUnavailable) {
		out.Problem = &Issue{Code: CodeUnavailable, Message: "Could not connect to the database: " + err.Error(), Position: -1}
		return out, nil
	}
	var se *connector.StatementError
	if errors.As(err, &se) && se.Index >= 0 && se.Index < len(out.Statements) {
		out.Statements[se.Index].Issue = runIssue(se.Err, lim)
		for i := range out.Statements {
			if i != se.Index && out.Statements[i].Issue == nil {
				out.Statements[i].Issue = &Issue{Code: CodeNotRun, Message: fmt.Sprintf("Not counted: statement %d failed, so the dry run stopped.", se.Index+1), Position: -1}
			}
		}
		return out, nil
	}
	iss := runIssue(err, lim)
	out.Problem = iss
	return out, nil
}

// beforeRunIssue describes a failure before the dry run could start.
func beforeRunIssue(err error) *Issue {
	switch {
	case errors.Is(err, targets.ErrArchived):
		return &Issue{Code: CodeArchived, Message: "This connection is archived; changes cannot run on it.", Position: -1}
	case errors.Is(err, targets.ErrNoPassword):
		return &Issue{Code: CodeUnavailable, Message: "This connection has no password. An admin has to set one.", Position: -1}
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return &Issue{Code: CodeBusy, Message: "Another preview or execution is running on this database. Try again in a moment.", Position: -1}
	default:
		return &Issue{Code: CodeUnavailable, Message: "Could not connect to the database: " + err.Error(), Position: -1}
	}
}

// runIssue describes why a statement failed in a dry run.
func runIssue(err error, lim connector.Limits) *Issue {
	switch {
	case errors.Is(err, connector.ErrTooManyRows):
		return &Issue{Code: CodeTooManyRows, Message: fmt.Sprintf(
			"This statement affects more than %d rows, the most this connection allows in one change. Narrow the WHERE clause or split the change.", lim.MaxRows), Position: -1}
	case errors.Is(err, connector.ErrNoRows):
		return &Issue{Code: CodeNoRows, Message: "This statement matches no rows. Check the WHERE clause.", Position: -1}
	case errors.Is(err, connector.ErrNotTransactional):
		return &Issue{Code: CodeNotTransactional, Message: "This table cannot be rolled back, so dbairlock will not change it: " + err.Error() + ".", Position: -1}
	case errors.Is(err, connector.ErrTimeout):
		return &Issue{Code: CodeTimeout, Message: "This statement timed out, or waited too long for a row lock. Try again, or narrow it.", Position: -1}
	}
	var rej *connector.RejectedError
	if errors.As(err, &rej) {
		return rejectionIssue(rej)
	}
	return &Issue{Code: CodeDatabase, Message: "The database refused this statement: " + err.Error(), Position: -1}
}
