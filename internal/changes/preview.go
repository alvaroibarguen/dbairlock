package changes

import (
	"regexp"

	"github.com/alvaroibarguen/dbairlock/internal/identity"
)

// visiblePreview is the one function every row image passes through on its
// way to a viewer: change details, the editor's preview, refreshed previews
// and execution results. Table and column restrictions will be applied here
// (PLAN.md section 2, "Accepted for v1").
//
// In v1 a person who may review changes sees everything stored. An agent
// (a viewer acting through an API token) sees counts and column names
// only: row data never enters model context.
func visiblePreview(viewer identity.Principal, p *Preview) *Preview {
	if p == nil {
		return nil
	}
	out := *p
	if isAgent(viewer) || identity.Can(viewer, identity.ActionChangeReview, identity.Instance) != nil {
		out.Before, out.After, out.Session = nil, nil, nil
		out.RowImages = false
		out.Truncated = false
	}
	return &out
}

var sqlState = regexp.MustCompile(`SQLSTATE ([0-9A-Z]{5})|Error ([0-9]+)`)

// visibleIssue is how an issue reaches a viewer. Database error text can
// quote values from the target ("duplicate entry 'ann@example.com'"), so
// agents get the error class instead of the text.
func visibleIssue(viewer identity.Principal, iss *Issue) *Issue {
	if iss == nil {
		return nil
	}
	out := *iss
	if isAgent(viewer) && out.Code == CodeDatabase {
		out.Message = "The database refused this statement."
		if m := sqlState.FindStringSubmatch(iss.Message); m != nil {
			code := m[1]
			if code == "" {
				code = m[2]
			}
			out.Message = "The database refused this statement (error " + code + "). Check the column names and values."
		}
	}
	return &out
}

// visible applies visiblePreview and visibleIssue to everything in c.
func visible(viewer identity.Principal, c Change) Change {
	stmts := make([]Statement, len(c.Statements))
	for i, st := range c.Statements {
		st.Preview = visiblePreview(viewer, st.Preview)
		st.PreviousPreview = visiblePreview(viewer, st.PreviousPreview)
		st.Issue = visibleIssue(viewer, st.Issue)
		stmts[i] = st
	}
	c.Statements = stmts
	execs := make([]Execution, len(c.Executions))
	for i, e := range c.Executions {
		res := make([]StatementOutcome, len(e.Results))
		for j, r := range e.Results {
			if p := visiblePreview(viewer, &r.Preview); p != nil {
				r.Preview = *p
			}
			res[j] = r
		}
		e.Results = res
		execs[i] = e
	}
	c.Executions = execs
	return c
}

// visibleResults applies the same rules to the results of Preview.
func visibleResults(viewer identity.Principal, rs []PreviewStatement) []PreviewStatement {
	out := make([]PreviewStatement, len(rs))
	for i, r := range rs {
		r.Preview = visiblePreview(viewer, r.Preview)
		r.Issue = visibleIssue(viewer, r.Issue)
		out[i] = r
	}
	return out
}
