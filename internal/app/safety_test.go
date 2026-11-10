package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/changes"
	"github.com/alvaroibarguen/dbairlock/internal/identity"
	"github.com/alvaroibarguen/dbairlock/internal/settings"
	"github.com/alvaroibarguen/dbairlock/internal/targets"
)

// Phase 11 safety audit: attempts to break the review and execution rules
// (PLAN.md 5.5 and 5.6) by every route the product offers. Outcomes are
// recorded in docs/report-notes/safety.md and docs/security.md.

// restClient calls the REST API as a dashboard would: with a session
// cookie, a bearer header, or neither.
type restClient struct {
	e      *chgEnv
	cookie string // session token for the dev cookie
	bearer string // Authorization: Bearer value
}

// session signs p in again (a new, independent session) with role and
// returns a client for it. Dev sign-in sets the role, so pass p's role.
func (e *chgEnv) session(p identity.Principal, role identity.Role) restClient {
	e.t.Helper()
	_, sess, err := e.a.identity.DevLogin(context.Background(), p.Email, role, identity.Meta{IP: "127.0.0.1"})
	if err != nil {
		e.t.Fatal(err)
	}
	return restClient{e: e, cookie: sess.Token}
}

func (c restClient) do(method, path string, body any) (int, map[string]any) {
	c.e.t.Helper()
	var rd *strings.Reader
	if body == nil {
		rd = strings.NewReader("")
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			c.e.t.Fatal(err)
		}
		rd = strings.NewReader(string(raw))
	}
	req := httptest.NewRequest(method, path, rd)
	req.Host = "127.0.0.1:8080"
	req.RemoteAddr = "127.0.0.1:40000"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.cookie != "" {
		req.AddCookie(&http.Cookie{Name: devCookie, Value: c.cookie})
	}
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	rec := httptest.NewRecorder()
	c.e.a.Handler().ServeHTTP(rec, req)
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func errCodeOf(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

// approveBody is the REST body that approves c as last loaded.
func approveBody(c changes.Change) map[string]any {
	return map[string]any{"decision": "approve", "sql_hash": c.SQLHash, "preview_hash": c.PreviewHash}
}

// sqlOf returns the statements of c's current revision.
func sqlOf(c changes.Change) []string {
	out := make([]string, len(c.Statements))
	for i, s := range c.Statements {
		out[i] = s.SQL
	}
	return out
}

// TestSafetyApprovedChangeIsFrozen: once approved, a change's SQL cannot be
// changed by any route, and what runs is what was approved.
func TestSafetyApprovedChangeIsFrozen(t *testing.T) {
	e := newChgEnv(t, postgresTargets(t), targets.PolicyPatch{AutoExecute: boolp(false)})
	const approved = "UPDATE users SET name = 'Approved' WHERE id = 1"
	const smuggled = "UPDATE users SET name = 'Smuggled' WHERE id = 1"
	c := e.submit(e.bob, approved)
	// A stale revision is refused while the change is still in review.
	if _, err := e.a.changes.Edit(e.ctx(e.bob), e.bob, c.ID, changes.EditInput{Revision: c.Revision + 1, Statements: []string{smuggled}}); !errors.Is(err, changes.ErrConflict) {
		t.Fatalf("edit with a future revision: %v", err)
	}
	c = e.mustApprove(e.cat, c)
	if c.Status != changes.StatusApproved {
		t.Fatalf("status %s", c.Status)
	}

	bob := e.session(e.bob, identity.RoleMember)
	ada := e.session(e.ada, identity.RoleAdmin)
	cat := e.session(e.cat, identity.RoleMember)
	path := "/api/changes/" + c.ID
	for _, tc := range []struct {
		who  restClient
		name string
		body map[string]any
	}{
		{bob, "author, current revision", map[string]any{"revision": c.Revision, "statements": []string{smuggled}}},
		{bob, "author, future revision", map[string]any{"revision": c.Revision + 1, "statements": []string{smuggled}}},
		{bob, "author, title only", map[string]any{"revision": c.Revision, "title": "Renamed after approval"}},
		{ada, "admin", map[string]any{"revision": c.Revision, "statements": []string{smuggled}}},
		{cat, "approver", map[string]any{"revision": c.Revision, "statements": []string{smuggled}}},
	} {
		code, body := tc.who.do("PATCH", path, tc.body)
		if code != http.StatusConflict && code != http.StatusForbidden {
			t.Errorf("%s: PATCH = %d %v", tc.name, code, body)
		}
	}
	// Re-submitting or refreshing an approved change is refused too.
	if code, body := bob.do("POST", path+"/submit", map[string]any{"revision": c.Revision}); code != http.StatusConflict {
		t.Errorf("submit: %d %v", code, body)
	}
	if code, body := cat.do("POST", path+"/refresh-preview", nil); code != http.StatusConflict {
		t.Errorf("refresh: %d %v", code, body)
	}
	if _, err := e.a.changes.Edit(e.ctx(e.bob), e.bob, c.ID, changes.EditInput{Revision: c.Revision, Statements: []string{smuggled}}); !errors.Is(err, changes.ErrInvalidState) {
		t.Errorf("service edit: %v", err)
	}

	after := e.status(c)
	if after.Status != changes.StatusApproved || after.Revision != c.Revision || after.SQLHash != c.SQLHash ||
		strings.Join(sqlOf(after), ";") != approved || after.Title != c.Title {
		t.Fatalf("approved change changed: %+v", after)
	}
	if code, body := bob.do("POST", path+"/execute", nil); code != http.StatusOK {
		t.Fatalf("execute: %d %v", code, body)
	}
	e.run()
	e.expectStatus(c, changes.StatusExecuted)
	if e.name(1) != "Approved" {
		t.Errorf("name = %s", e.name(1))
	}
}

// TestSafetyEditApproveExecuteRace races the author's edit against a
// reviewer's approval, a preview refresh, manual execution requests and
// the executor. Exactly one of the edit and the approval wins, and the
// target only ever receives SQL that was approved.
func TestSafetyEditApproveExecuteRace(t *testing.T) {
	e := newChgEnv(t, postgresTargets(t), targets.PolicyPatch{})
	for round := 1; round <= 5; round++ {
		approved := fmt.Sprintf("UPDATE users SET name = 'Approved%d' WHERE id = %d", round, round)
		edited := fmt.Sprintf("UPDATE users SET name = 'Edited%d' WHERE id = %d", round, round)
		original := e.name(round)
		c := e.submit(e.bob, approved)
		seen := e.get(e.cat, c)

		var wg sync.WaitGroup
		var editErr, approveErr error
		start := make(chan struct{})
		run := func(fn func()) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				fn()
			}()
		}
		run(func() {
			_, editErr = e.a.changes.Edit(e.ctx(e.bob), e.bob, c.ID, changes.EditInput{Revision: c.Revision, Statements: []string{edited}})
		})
		run(func() {
			_, approveErr = e.a.changes.Review(e.ctx(e.cat), e.cat, c.ID, changes.ReviewInput{
				Decision: changes.DecisionApprove, SQLHash: seen.SQLHash, PreviewHash: seen.PreviewHash,
			})
		})
		run(func() { _, _, _ = e.a.changes.RefreshPreview(e.ctx(e.dan), e.dan, c.ID) })
		for range 3 {
			run(func() { e.run() })
			run(func() { _, _ = e.a.changes.ExecuteNow(e.ctx(e.ada), e.ada, c.ID) })
		}
		close(start)
		wg.Wait()
		e.run()

		if (editErr == nil) == (approveErr == nil) {
			t.Fatalf("round %d: edit error %v, approve error %v; exactly one must win", round, editErr, approveErr)
		}
		got := e.status(c)
		if approveErr == nil {
			if got.Status != changes.StatusExecuted || got.SQLHash != seen.SQLHash || e.name(round) != "Approved"+itoa(round) {
				t.Fatalf("round %d: approval won but the change is %s with name %q", round, got.Status, e.name(round))
			}
			if n := e.countAction(c, audit.ExecutionStarted); n != 1 {
				t.Fatalf("round %d: %d executions", round, n)
			}
		} else {
			if !errors.Is(approveErr, changes.ErrSQLChanged) && !errors.Is(approveErr, changes.ErrPreviewChanged) {
				t.Errorf("round %d: approval error %v", round, approveErr)
			}
			if got.Status != changes.StatusPendingReview || e.name(round) != original {
				t.Fatalf("round %d: the edit won but the change is %s with name %q", round, got.Status, e.name(round))
			}
			if _, err := e.a.changes.Cancel(e.ctx(e.bob), e.bob, c.ID, "race round done"); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// TestSafetySelfApproval tries to approve one's own change by every route.
func TestSafetySelfApproval(t *testing.T) {
	e := newChgEnv(t, postgresTargets(t), targets.PolicyPatch{})
	if err := e.a.settings.Update(e.ctx(e.ada), settings.Changes{Values: map[string]any{"instance.public_url": publicURL}}); err != nil {
		t.Fatal(err)
	}
	bob := e.session(e.bob, identity.RoleMember)
	code, body := bob.do("POST", "/api/changes", map[string]any{
		"connection_id": e.conn, "title": "Rename Ann", "reason": "Ticket 1",
		"statements": []string{"UPDATE users SET name = 'Self' WHERE id = 1"}, "submit": true,
	})
	if code != http.StatusCreated {
		t.Fatalf("submit: %d %v", code, body)
	}
	c := e.get(e.bob, changes.Change{ID: body["id"].(string)})
	reviews := "/api/changes/" + c.ID + "/reviews"
	refused := func(name string, client restClient, wantCode int, wantErr string) {
		t.Helper()
		code, body := client.do("POST", reviews, approveBody(e.get(e.ada, c)))
		if code != wantCode || (wantErr != "" && errCodeOf(body) != wantErr) {
			t.Errorf("%s: %d %v; want %d %s", name, code, body, wantCode, wantErr)
		}
	}

	// The same session, a second session, and a rejection.
	refused("own session", bob, http.StatusForbidden, "self_review")
	refused("second session", e.session(e.bob, identity.RoleMember), http.StatusForbidden, "self_review")
	if code, body := bob.do("POST", reviews, map[string]any{"decision": "reject", "comment": "x"}); code != http.StatusForbidden {
		t.Errorf("self-reject: %d %v", code, body)
	}

	// API tokens, two of them: not accepted by the dashboard API at all,
	// not as a bearer header and not as a session cookie.
	var pats []string
	for _, name := range []string{"agent-one", "agent-two"} {
		code, body := bob.do("POST", "/api/tokens", map[string]any{"name": name})
		if code != http.StatusCreated {
			t.Fatalf("token: %d %v", code, body)
		}
		pats = append(pats, body["secret"].(string))
	}
	for i, pat := range pats {
		refused(fmt.Sprintf("token %d as bearer", i+1), restClient{e: e, bearer: pat}, http.StatusUnauthorized, "")
		refused(fmt.Sprintf("token %d as session cookie", i+1), restClient{e: e, cookie: pat}, http.StatusUnauthorized, "")
		agent, err := e.a.identity.AuthenticateToken(context.Background(), pat)
		if err != nil {
			t.Fatal(err)
		}
		_, err = e.a.changes.Review(e.ctx(agent), agent, c.ID, changes.ReviewInput{
			Decision: changes.DecisionApprove, SQLHash: c.SQLHash, PreviewHash: c.PreviewHash,
		})
		if !errors.Is(err, changes.ErrAgentReview) {
			t.Errorf("token %d through the service: %v", i+1, err)
		}
	}

	// MCP: no tool reviews, and a change an agent submits is its owner's.
	srv := httptest.NewServer(e.a.Handler())
	t.Cleanup(srv.Close)
	for _, pat := range pats {
		mc := connectMCP(t, srv.URL, pat)
		tools, err := mc.sess.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, tl := range tools.Tools {
			if strings.Contains(tl.Name, "approv") || strings.HasPrefix(tl.Name, "review") || strings.Contains(tl.Name, "reject") || strings.Contains(tl.Name, "execute") {
				t.Errorf("MCP tool %s", tl.Name)
			}
		}
		for _, name := range []string{"approve_change", "review_change", "execute_change"} {
			r, err := mc.sess.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"number": c.Number}})
			if err == nil && !r.IsError {
				t.Errorf("MCP %s succeeded", name)
			}
		}
	}
	mc := connectMCP(t, srv.URL, pats[0])
	out := mc.ok("submit_change", map[string]any{
		"connection": "target", "title": "Agent change", "reason": "Ticket 2",
		"statements": []string{"UPDATE users SET name = 'Agent' WHERE id = 2"},
	})
	agentChange := e.get(e.ada, changes.Change{ID: itoa(int(num(out["number"])))})
	if agentChange.Author.ID != e.bob.UserID {
		t.Fatalf("agent change author = %s", agentChange.Author.Email)
	}
	if code, body := bob.do("POST", "/api/changes/"+agentChange.ID+"/reviews", approveBody(agentChange)); code != http.StatusForbidden || errCodeOf(body) != "self_review" {
		t.Errorf("approving own agent's change: %d %v", code, body)
	}

	// A role change mid-review: promoted to admin, the author still cannot
	// approve; admins have no bypass.
	ada := e.session(e.ada, identity.RoleAdmin)
	if code, body := ada.do("PATCH", "/api/users/"+e.bob.UserID, map[string]any{"role": "admin"}); code != http.StatusOK {
		t.Fatalf("promote: %d %v", code, body)
	}
	refused("author promoted to admin, old session", bob, http.StatusForbidden, "self_review")
	refused("author promoted to admin, new session", e.session(e.bob, identity.RoleAdmin), http.StatusForbidden, "self_review")
	// And demoting an approver does not void or duplicate an approval:
	// the change needs one approval from someone other than the author.
	cat := e.session(e.cat, identity.RoleMember)
	if code, body := cat.do("POST", reviews, approveBody(e.get(e.cat, c))); code != http.StatusOK || body["status"] != "approved" {
		t.Fatalf("cat approves: %d %v", code, body)
	}
	if n := e.count("SELECT count(*) FROM reviews WHERE reviewer_id = '" + e.bob.UserID + "'"); n != 0 {
		t.Errorf("the author has %d reviews on record", n)
	}
	e.run()
	e.expectStatus(c, changes.StatusExecuted)
}

// TestSafetyDoubleExecution tries to run one change twice: concurrent
// manual triggers and executor cycles, a restart after the claim, and
// clock changes.
func TestSafetyDoubleExecution(t *testing.T) {
	srv := postgresTargets(t)
	sys := audit.WithActor(context.Background(), audit.SystemActor)

	t.Run("concurrent manual triggers and executor cycles", func(t *testing.T) {
		e := newChgEnv(t, srv, targets.PolicyPatch{AutoExecute: boolp(false)})
		c := e.submit(e.bob, "UPDATE users SET name = concat(name, '!') WHERE id = 1")
		e.mustApprove(e.cat, c)
		bob, ada := e.session(e.bob, identity.RoleMember), e.session(e.ada, identity.RoleAdmin)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range 12 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				switch i % 4 {
				case 0:
					bob.do("POST", "/api/changes/"+c.ID+"/execute", nil)
				case 1:
					ada.do("POST", "/api/changes/"+c.ID+"/execute", nil)
				case 2:
					_, _ = e.a.changes.ExecuteNow(e.ctx(e.bob), e.bob, c.ID)
				default:
					e.run()
				}
			}()
		}
		close(start)
		wg.Wait()
		e.run()
		e.clock.Advance(time.Hour)
		e.run()
		e.expectStatus(c, changes.StatusExecuted)
		if e.name(1) != "Ann!" {
			t.Errorf("name = %q, want Ann! (one execution)", e.name(1))
		}
		if n := e.countAction(c, audit.ExecutionStarted); n != 1 {
			t.Errorf("%d execution.started events", n)
		}
		if n := e.countAction(c, audit.ChangeExecuteRequested); n != 1 {
			t.Errorf("%d change.execute_requested events", n)
		}
		if n := e.count("SELECT count(*) FROM executions WHERE change_id = '" + c.ID + "'"); n != 1 {
			t.Errorf("%d execution rows", n)
		}
	})

	t.Run("restart after the claim never re-runs, however often", func(t *testing.T) {
		e := newChgEnv(t, srv, targets.PolicyPatch{})
		c := e.submit(e.bob, "UPDATE users SET name = concat(name, '!') WHERE id = 1")
		e.mustApprove(e.cat, c)
		claim, err := e.a.changes.ClaimNext(sys)
		if err != nil || claim == nil {
			t.Fatalf("claim: %v %v", claim, err)
		}
		// The claimed change runs on the target (as if the executor got
		// that far) and the process dies before recording the outcome.
		e.db.exec(t, "UPDATE users SET name = concat(name, '!') WHERE id = 1")
		for range 3 {
			dir := e.a.opts.DataDir
			_ = e.a.Close()
			b, err := New(Options{DataDir: dir, Listen: "127.0.0.1:0", Dev: true, Version: "test", Logger: e.a.log, Clock: e.clock, WebUI: e.a.opts.WebUI})
			if err != nil {
				t.Fatal(err)
			}
			e.a = b
			if err := b.executor.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			b.executor.RunOnce(context.Background())
			e.clock.Advance(time.Hour)
			b.executor.RunOnce(context.Background())
		}
		got := e.expectStatus(c, changes.StatusFailed)
		if got.StatusCode != changes.FailInterrupted {
			t.Errorf("status code %s", got.StatusCode)
		}
		if e.name(1) != "Ann!" {
			t.Errorf("name = %q: the interrupted change ran again", e.name(1))
		}
		if n := e.countAction(c, audit.ExecutionInterrupted); n != 1 {
			t.Errorf("%d execution.interrupted events", n)
		}
		// A failed change cannot be executed or retried by hand either.
		if _, err := e.a.changes.ExecuteNow(e.ctx(e.bob), e.bob, c.ID); !errors.Is(err, changes.ErrInvalidState) {
			t.Errorf("execute a failed change: %v", err)
		}
		if _, err := e.a.changes.RetryNow(e.ctx(e.ada), e.ada, c.ID); !errors.Is(err, changes.ErrInvalidState) {
			t.Errorf("retry a failed change: %v", err)
		}
	})

	t.Run("the clock moving back after execution runs nothing again", func(t *testing.T) {
		e := newChgEnv(t, srv, targets.PolicyPatch{})
		c := e.submit(e.bob, "UPDATE users SET name = concat(name, '!') WHERE id = 1")
		e.mustApprove(e.cat, c)
		e.run()
		e.expectStatus(c, changes.StatusExecuted)
		e.clock.Advance(-48 * time.Hour)
		e.run()
		e.clock.Advance(96 * time.Hour)
		e.run()
		if e.name(1) != "Ann!" || e.countAction(c, audit.ExecutionStarted) != 1 {
			t.Errorf("name = %q after clock changes", e.name(1))
		}
	})

	t.Run("an unreachable target retried across clock jumps runs once", func(t *testing.T) {
		e := newChgEnv(t, srv, targets.PolicyPatch{})
		c := e.submit(e.bob, "UPDATE users SET name = concat(name, '!') WHERE id = 1")
		e.px.setDown(true)
		e.mustApprove(e.cat, c)
		e.run()
		e.expectStatus(c, changes.StatusApproved)
		// Back in time: the retry is not due earlier, and nothing runs twice.
		e.clock.Advance(-time.Hour)
		e.run()
		e.px.setDown(false)
		e.clock.Advance(time.Hour + time.Minute)
		e.run()
		e.run()
		e.expectStatus(c, changes.StatusExecuted)
		if e.name(1) != "Ann!" {
			t.Errorf("name = %q", e.name(1))
		}
	})

	t.Run("a change past its approval lifetime is never claimed", func(t *testing.T) {
		e := newChgEnv(t, srv, targets.PolicyPatch{AutoExecute: boolp(false)})
		c := e.submit(e.bob, "UPDATE users SET name = concat(name, '!') WHERE id = 1")
		c = e.mustApprove(e.cat, c)
		if _, err := e.a.changes.ExecuteNow(e.ctx(e.bob), e.bob, c.ID); err != nil {
			t.Fatal(err)
		}
		// The clock jumps past the expiry between the executor's expiry
		// pass and its claim.
		if c.ExpiresAt == nil {
			t.Fatal("no expiry")
		}
		e.clock.Set(c.ExpiresAt.Add(time.Second))
		claim, err := e.a.changes.ClaimNext(sys)
		if err != nil || claim != nil {
			t.Fatalf("claimed an expired change: %+v %v", claim, err)
		}
		e.run()
		e.expectStatus(c, changes.StatusExpired)
		if e.name(1) != "Ann" {
			t.Errorf("name = %q", e.name(1))
		}
	})
}

// TestSafetyRevalidatedAtExecution: the executor validates every statement
// again with the connection's policy at the time it runs. A function an
// admin removed from the allowlist after the approval stops the change
// before anything reaches the target.
func TestSafetyRevalidatedAtExecution(t *testing.T) {
	e := newChgEnv(t, postgresTargets(t), targets.PolicyPatch{AutoExecute: boolp(false)})
	c := e.submit(e.bob, "UPDATE users SET name = upper(name) WHERE id = 1")
	e.mustApprove(e.cat, c)
	only := []string{"lower"}
	if _, err := e.a.targets.Update(e.ctx(e.ada), e.conn, targets.UpdateInput{Policy: targets.PolicyPatch{AllowedFunctions: &only}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.a.changes.ExecuteNow(e.ctx(e.bob), e.bob, c.ID); err != nil {
		t.Fatal(err)
	}
	e.run()
	got := e.expectStatus(c, changes.StatusFailed)
	if got.StatusCode != changes.FailInvalid || !strings.Contains(got.StatusMessage, "upper") {
		t.Errorf("failure = %s %q", got.StatusCode, got.StatusMessage)
	}
	if e.name(1) != "Ann" {
		t.Errorf("name = %q", e.name(1))
	}
}

// TestSafetyRowImagesOff: with row images turned off for a connection,
// only column names and counts are kept (PLAN.md 5.4): no row value
// reaches the store, the audit log or a reviewer.
func TestSafetyRowImagesOff(t *testing.T) {
	forEachTarget(t, func(t *testing.T, srv targetServer) {
		e := newChgEnv(t, srv, targets.PolicyPatch{StoreRowImages: boolp(false)})
		e.db.exec(t, "UPDATE users SET name = 'zqxrow-name', email = 'zqxrow@example.com' WHERE id IN (1, 2)")
		c := e.submit(e.bob, "UPDATE users SET status = 'inactive' WHERE id IN (1, 2)")
		p := e.get(e.cat, c).Statements[0].Preview
		if p == nil || p.RowImages || p.Before != nil || p.After != nil || p.RowsAffected != 2 || len(p.Columns) != 5 {
			t.Fatalf("reviewer preview = %+v", p)
		}
		e.clock.Advance(time.Minute)
		if _, _, err := e.a.changes.RefreshPreview(e.ctx(e.cat), e.cat, c.ID); err != nil {
			t.Fatal(err)
		}
		e.mustApprove(e.cat, c)
		e.run()
		e.expectStatus(c, changes.StatusExecuted)
		for _, q := range []string{
			"SELECT count(*) FROM change_statements WHERE coalesce(preview, '') || coalesce(previous_preview, '') LIKE '%zqxrow%'",
			"SELECT count(*) FROM executions WHERE coalesce(statement_results, '') LIKE '%zqxrow%'",
			"SELECT count(*) FROM audit_events WHERE payload LIKE '%zqxrow%'",
		} {
			if n := e.count(q); n != 0 {
				t.Errorf("%s: %d", q, n)
			}
		}
	})
}

// TestSafetyPreviewFreshnessAndClock: the fresh-preview rule holds when the
// clock moves, in either direction.
func TestSafetyPreviewFreshnessAndClock(t *testing.T) {
	e := newChgEnv(t, postgresTargets(t), targets.PolicyPatch{})
	c := e.submit(e.bob, "UPDATE users SET name = 'Zed' WHERE id = 1")

	// Forward past the limit: stale.
	e.clock.Advance(6 * time.Minute)
	_, err := e.approve(e.cat, c)
	wantErr(t, err, changes.ErrPreviewStale)

	// Refresh, then the clock steps back three minutes: the preview now
	// looks taken in the future. Its age is unknown, so it is not fresh.
	// (Stepping back past the change's creation makes every write to it
	// fail the store's updated_at >= created_at check: fail closed.)
	if _, _, err := e.a.changes.RefreshPreview(e.ctx(e.cat), e.cat, c.ID); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(-3 * time.Minute)
	_, err = e.approve(e.cat, c)
	wantErr(t, err, changes.ErrPreviewStale)

	// A refresh is not blocked until the clock catches up.
	if _, _, err := e.a.changes.RefreshPreview(e.ctx(e.cat), e.cat, c.ID); err != nil {
		t.Fatalf("refresh after the clock went back: %v", err)
	}
	e.mustApprove(e.cat, c)
	e.run()
	e.expectStatus(c, changes.StatusExecuted)
}
