package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/alvaroibarguen/dbairlock/internal/changes"
	"github.com/alvaroibarguen/dbairlock/internal/secrets"
	"github.com/alvaroibarguen/dbairlock/internal/settings"
	"github.com/alvaroibarguen/dbairlock/internal/targets"
)

// Phase 8 checks: the MCP server, driven by the SDK's own client over
// streamable HTTP against the real handler, with a bearer token.

// marker is seeded into target rows. It must never appear in anything the
// MCP server sends.
const marker = "zqxmarker"

// recorder is an HTTP transport that adds the bearer token and keeps a copy
// of every response body.
type recorder struct {
	token string

	mu     sync.Mutex
	bodies []string
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.bodies = append(r.bodies, string(raw))
	r.mu.Unlock()
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	return resp, nil
}

func (r *recorder) all() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.bodies, "\n")
}

type mcpClient struct {
	t    *testing.T
	sess *mcp.ClientSession
	rec  *recorder
}

func connectMCP(t *testing.T, url, token string) *mcpClient {
	t.Helper()
	rec := &recorder{token: token}
	client := mcp.NewClient(&mcp.Implementation{Name: "dbairlock-test", Version: "1"}, nil)
	sess, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: url + "/mcp", HTTPClient: &http.Client{Transport: rec}, DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return &mcpClient{t: t, sess: sess, rec: rec}
}

// toolResult is one tool call's outcome: its structured output, its text
// and whether it is a tool error.
type toolResult struct {
	out   map[string]any
	text  string
	isErr bool
}

func (c *mcpClient) call(name string, args map[string]any) toolResult {
	c.t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	res, err := c.sess.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		c.t.Fatalf("%s: %v", name, err)
	}
	var tr toolResult
	tr.isErr = res.IsError
	var texts []string
	for _, ct := range res.Content {
		if tc, ok := ct.(*mcp.TextContent); ok {
			texts = append(texts, tc.Text)
		}
	}
	tr.text = strings.Join(texts, "\n")
	if res.StructuredContent != nil {
		raw, _ := json.Marshal(res.StructuredContent)
		_ = json.Unmarshal(raw, &tr.out)
	}
	return tr
}

func (c *mcpClient) ok(name string, args map[string]any) map[string]any {
	c.t.Helper()
	r := c.call(name, args)
	if r.isErr || r.out == nil {
		c.t.Fatalf("%s(%v): error %v %q", name, args, r.isErr, r.text)
	}
	return r.out
}

func (c *mcpClient) fails(name string, args map[string]any, wantText ...string) string {
	c.t.Helper()
	r := c.call(name, args)
	if !r.isErr {
		c.t.Fatalf("%s(%v) succeeded: %v", name, args, r.out)
	}
	for _, w := range wantText {
		if !strings.Contains(r.text, w) {
			c.t.Errorf("%s error %q does not mention %q", name, r.text, w)
		}
	}
	return r.text
}

func list(v any) []any         { l, _ := v.([]any); return l }
func obj(v any) map[string]any { m, _ := v.(map[string]any); return m }
func num(v any) int64          { f, _ := v.(float64); return int64(f) }

// rawReply is the status and headers of a raw /mcp request.
type rawReply struct {
	StatusCode int
	Header     http.Header
}

// rawMCP posts a request without the SDK.
func rawMCP(t *testing.T, url, auth string, body string) rawReply {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return rawReply{StatusCode: resp.StatusCode, Header: resp.Header}
}

const initializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"raw","version":"1"}}}`

func (e *chgEnv) count(q string) int64 {
	e.t.Helper()
	var n int64
	if err := e.a.store.Reader().QueryRowContext(context.Background(), q).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestMCP(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}
	e := newChgEnv(t, postgresTargets(t), targets.PolicyPatch{})
	if err := e.a.settings.Update(e.ctx(e.ada), settings.Changes{Values: map[string]any{"instance.public_url": publicURL}}); err != nil {
		t.Fatal(err)
	}
	// Rows carrying the marker, and a display_name column on both
	// connections.
	e.db.exec(t, "UPDATE users SET name = '"+marker+"-ann', email = '"+marker+"@example.com' WHERE id IN (1, 2)")
	e.db.exec(t, "UPDATE users SET email = 'bob@example.com' WHERE id = 2")
	e.db.exec(t, "CREATE TABLE profiles (user_id int PRIMARY KEY, display_name varchar(100))")
	e.db.exec(t, "GRANT SELECT, INSERT, UPDATE, DELETE ON profiles TO "+tgtAppUser)
	my := mysqlTargets(t).newDB(t)
	my.exec(t, "CREATE TABLE customers (id int PRIMARY KEY, display_name varchar(100))")
	host, portText, err := net.SplitHostPort(my.backend())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portText)
	if _, err := e.a.targets.Create(e.ctx(e.ada), targets.CreateInput{
		Name: "billing", Description: "Invoices and customers", Connector: "mysql", Config: my.config(host, port),
		Password: secrets.NewValue(tgtAppPassword),
	}); err != nil {
		t.Fatal(err)
	}

	tok, err := e.a.identity.CreateToken(e.ctx(e.dan), e.dan, "claude-code", 0)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(e.a.Handler())
	t.Cleanup(srv.Close)
	c := connectMCP(t, srv.URL, tok.Secret)

	t.Run("tools", func(t *testing.T) {
		res, err := c.sess.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, tl := range res.Tools {
			names = append(names, tl.Name)
			if tl.Description == "" || tl.InputSchema == nil || tl.OutputSchema == nil {
				t.Errorf("tool %s lacks a description or schema", tl.Name)
			}
		}
		slices.Sort(names)
		want := []string{"cancel_change", "cancel_sequence", "find_tables", "get_change", "get_schema", "get_sequence", "list_changes",
			"list_connections", "list_tables", "preview_change", "submit_change", "submit_sequence"}
		if !slices.Equal(names, want) {
			t.Errorf("tools = %v", names)
		}
		// Agents cannot approve: there is no tool for it.
		r, err := c.sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "approve_change", Arguments: map[string]any{"number": 1}})
		if err == nil && !r.IsError {
			t.Error("approve_change exists")
		}
		if init := c.sess.InitializeResult(); init == nil || !strings.Contains(init.Instructions, "preview_change") {
			t.Errorf("instructions = %+v", init)
		}
	})

	t.Run("discovery", func(t *testing.T) {
		out := c.ok("list_connections", nil)
		conns := list(out["connections"])
		if len(conns) != 2 {
			t.Fatalf("connections = %v", out)
		}
		byName := map[string]map[string]any{}
		for _, x := range conns {
			byName[obj(x)["name"].(string)] = obj(x)
		}
		tg, bl := byName["target"], byName["billing"]
		if tg["connector"] != "postgres" || tg["schema_status"] != "available" || num(tg["approvals_required"]) != 1 || num(tg["max_rows_per_change"]) != 100 {
			t.Errorf("target = %v", tg)
		}
		if bl["connector"] != "mysql" || bl["description"] != "Invoices and customers" || len(list(bl["schemas"])) != 1 {
			t.Errorf("billing = %v", bl)
		}
		if s := obj(list(tg["schemas"])[0]); s["name"] != "public" || num(s["tables"]) != 3 {
			t.Errorf("target schemas = %v", tg["schemas"])
		}

		out = c.ok("list_tables", map[string]any{"connection": "target"})
		tables := list(obj(list(out["schemas"])[0])["tables"])
		found := map[string]map[string]any{}
		for _, x := range tables {
			found[obj(x)["name"].(string)] = obj(x)
		}
		if u := found["users"]; num(u["columns"]) != 5 || len(list(u["primary_key"])) != 1 {
			t.Errorf("users = %v", u)
		}
		if n := found["audit_notes"]; len(list(n["primary_key"])) != 0 {
			t.Errorf("audit_notes = %v", n)
		}

		out = c.ok("find_tables", map[string]any{"query": "display_name"})
		matches := list(out["matches"])
		var where []string
		for _, m := range matches {
			mm := obj(m)
			where = append(where, mm["connection"].(string)+"."+mm["table"].(string))
			if !slices.Contains(list(mm["matched_columns"]), any("display_name")) {
				t.Errorf("match = %v", mm)
			}
		}
		slices.Sort(where)
		if !slices.Equal(where, []string{"billing.customers", "target.profiles"}) {
			t.Errorf("find display_name = %v", where)
		}

		out = c.ok("get_schema", map[string]any{"connection": "target", "table": "users"})
		tbl := obj(list(obj(list(out["schemas"])[0])["tables"])[0])
		cols := list(tbl["columns"])
		if tbl["name"] != "users" || len(cols) != 5 || obj(cols[0])["name"] != "id" || obj(cols[0])["nullable"] != false {
			t.Errorf("get_schema users = %v", tbl)
		}
		out = c.ok("get_schema", map[string]any{"connection": "billing"})
		if out["truncated"] != false || len(list(obj(list(out["schemas"])[0])["tables"])) != 3 {
			t.Errorf("get_schema billing = %v", out)
		}
		c.fails("get_schema", map[string]any{"connection": "target", "table": "nope"}, "find_tables")
		c.fails("list_tables", map[string]any{"connection": "nope"}, `"target"`, `"billing"`)
	})

	const update = "UPDATE users SET status = 'inactive' WHERE id IN (1, 2)"
	var previewRows int64

	t.Run("preview_change", func(t *testing.T) {
		changesBefore := e.count("SELECT count(*) FROM change_requests")
		auditBefore := e.count("SELECT count(*) FROM audit_events")
		out := c.ok("preview_change", map[string]any{"connection": "target", "statements": []string{update}})
		st := obj(list(out["statements"])[0])
		if out["ok"] != true || st["valid"] != true || st["kind"] != "UPDATE" || st["table"] != "public.users" || st["has_primary_key"] != true {
			t.Errorf("preview = %v", out)
		}
		previewRows = num(st["rows_affected"])
		if previewRows != 2 {
			t.Errorf("rows_affected = %d", previewRows)
		}
		// A mix of valid and invalid statements reports each one.
		out = c.ok("preview_change", map[string]any{"connection": "target", "statements": []string{
			"UPDATE users SET name = 'Q' WHERE id = 3", "DELETE FROM users", "DROP TABLE users",
		}})
		sts := list(out["statements"])
		if out["ok"] != false || len(sts) != 3 || obj(sts[0])["valid"] != true || obj(sts[1])["valid"] != false || obj(sts[2])["valid"] != false {
			t.Fatalf("mixed preview = %v", out)
		}
		if obj(obj(sts[1])["error"])["message"] == "" || obj(sts[2])["error"] == nil {
			t.Errorf("mixed preview errors = %v", sts)
		}
		// Nothing was written except the audit events, and the target is
		// unchanged.
		if n := e.count("SELECT count(*) FROM change_requests"); n != changesBefore {
			t.Errorf("preview created changes: %d -> %d", changesBefore, n)
		}
		if n := e.count("SELECT count(*) FROM audit_events"); n != auditBefore+2 {
			t.Errorf("audit events %d -> %d", auditBefore, n)
		}
		if e.count("SELECT count(*) FROM audit_events WHERE action = 'mcp.tool_called' AND via = 'mcp'") < 2 {
			t.Error("no mcp.tool_called events")
		}
		if v := e.db.value(t, "SELECT status FROM users WHERE id = 1"); v != "active" {
			t.Errorf("target changed: %s", v)
		}
		if e.name(3) != "Cat" {
			t.Error("target changed by the mixed preview")
		}
	})

	var number int64
	t.Run("submit_change", func(t *testing.T) {
		args := map[string]any{
			"connection": "target", "title": "Deactivate two users", "reason": "Requested in ticket 7.", "statements": []string{update},
		}
		out := c.ok("submit_change", args)
		number = num(out["number"])
		st := obj(list(out["statements"])[0])
		if number == 0 || out["status"] != "pending_review" || out["already_existed"] != false ||
			out["review_url"] != publicURL+"/changes/"+itoa(int(number)) || num(st["rows_affected"]) != previewRows {
			t.Fatalf("submit = %v", out)
		}
		// The preview's count is what the change records.
		ch, err := e.a.changes.Get(e.ctx(e.bob), e.bob, itoa(int(number)))
		if err != nil {
			t.Fatal(err)
		}
		if ch.Statements[0].ExpectedRows == nil || *ch.Statements[0].ExpectedRows != previewRows {
			t.Errorf("expected_rows = %v", ch.Statements[0].ExpectedRows)
		}
		// Authored by the token's owner, with the agent badge.
		if ch.Author.ID != e.dan.UserID || !ch.ByAgent() || ch.AgentName != "claude-code" {
			t.Errorf("author %s via %s agent %q", ch.Author.Email, ch.Via, ch.AgentName)
		}
		// A retry returns the same change.
		again := c.ok("submit_change", args)
		if num(again["number"]) != number || again["already_existed"] != true || !strings.Contains(again["message"].(string), "already") {
			t.Errorf("retry = %v", again)
		}

		// Invalid SQL: a tool error naming the statement and the reason, and
		// no change.
		before := e.count("SELECT count(*) FROM change_requests")
		c.fails("submit_change", map[string]any{
			"connection": "target", "title": "Bad", "reason": "x",
			"statements": []string{"UPDATE users SET name = 'ok' WHERE id = 4", "UPDATE users SET name = 'x'"},
		}, "Statement 2", "WHERE", "nothing was created")
		if n := e.count("SELECT count(*) FROM change_requests"); n != before {
			t.Errorf("invalid submit created a change")
		}
	})

	t.Run("get, list and cancel", func(t *testing.T) {
		out := c.ok("get_change", map[string]any{"number": number})
		if out["status"] != "pending_review" || num(out["approvals_required"]) != 1 || out["connection"] != "target" ||
			!strings.Contains(out["message"].(string), "0 of 1") {
			t.Errorf("get_change = %v", out)
		}
		out = c.ok("list_changes", nil)
		if l := list(out["changes"]); len(l) != 1 || num(obj(l[0])["number"]) != number {
			t.Errorf("list_changes = %v", out)
		}
		c.fails("list_changes", map[string]any{"status": "bogus"}, "pending_review")

		// Another person's change is invisible to the token.
		bobs := e.submit(e.bob, "UPDATE users SET name = 'Bobby' WHERE id = 2")
		c.fails("get_change", map[string]any{"number": bobs.Number}, "No such change")
		c.fails("cancel_change", map[string]any{"number": bobs.Number}, "No such change")
		// Not even through an admin's token: an agent acts only as author.
		adaTok, err := e.a.identity.CreateToken(e.ctx(e.ada), e.ada, "admin-agent", 0)
		if err != nil {
			t.Fatal(err)
		}
		connectMCP(t, srv.URL, adaTok.Secret).fails("cancel_change", map[string]any{"number": bobs.Number}, "No such change")
		if e.status(bobs).Status != changes.StatusPendingReview {
			t.Error("the token cancelled another person's change")
		}
		if l := list(c.ok("list_changes", nil)["changes"]); len(l) != 1 {
			t.Errorf("list_changes shows others' changes: %v", l)
		}

		out = c.ok("cancel_change", map[string]any{"number": number, "reason": "Not needed after all."})
		if out["status"] != "cancelled" {
			t.Errorf("cancel = %v", out)
		}
		c.fails("cancel_change", map[string]any{"number": number}, "cancelled")
	})

	t.Run("an agent signed in with OAuth acts like an API token", func(t *testing.T) {
		oc := connectMCP(t, srv.URL, oauthAccess(e.ctx(e.dan), t, e.a, e.dan))
		bobs := e.submit(e.bob, "UPDATE users SET name = 'Bobbie' WHERE id = 2")
		oc.fails("get_change", map[string]any{"number": bobs.Number}, "No such change")
		oc.fails("cancel_change", map[string]any{"number": bobs.Number}, "No such change")
		out := oc.ok("submit_change", map[string]any{
			"connection": "target", "title": "Via OAuth", "reason": "r", "statements": []string{"UPDATE users SET name = 'Oz' WHERE id = 3"},
		})
		ch, err := e.a.changes.Get(e.ctx(e.bob), e.bob, itoa(int(num(out["number"]))))
		if err != nil {
			t.Fatal(err)
		}
		if ch.Author.ID != e.dan.UserID || ch.Via != "mcp" || ch.AgentName != "Claude Code (dbairlock)" || ch.APITokenID == "" {
			t.Errorf("change by the OAuth agent: author %s via %s agent %q", ch.Author.ID, ch.Via, ch.AgentName)
		}
		if strings.Contains(oc.rec.all(), marker) {
			t.Error("row data reached the OAuth agent")
		}
	})

	t.Run("waiting for an unreachable target", func(t *testing.T) {
		out := c.ok("submit_change", map[string]any{
			"connection": "target", "title": "Rename", "reason": "Typo.", "statements": []string{"UPDATE users SET name = 'Zed' WHERE id = 4"},
		})
		ch, err := e.a.changes.Get(e.ctx(e.bob), e.bob, itoa(int(num(out["number"]))))
		if err != nil {
			t.Fatal(err)
		}
		e.mustApprove(e.bob, ch)
		e.px.setDown(true)
		e.run()
		got := c.ok("get_change", map[string]any{"number": ch.Number})
		ex := obj(got["execution"])
		if got["status"] != "approved" || ex["waiting_for_database"] != true || num(ex["attempts"]) != 1 ||
			!strings.Contains(got["message"].(string), "could not be reached") {
			t.Errorf("waiting change = %v", got)
		}
		// A connection that cannot be reached is still listed.
		e.clock.Advance(2 * targets.SchemaTTL)
		lc := c.ok("list_connections", nil)
		for _, x := range list(lc["connections"]) {
			want := map[string]string{"target": "unavailable", "billing": "available"}[obj(x)["name"].(string)]
			if obj(x)["schema_status"] != want {
				t.Errorf("with target down: %v", x)
			}
		}
		e.px.setDown(false)
	})

	t.Run("sequences", func(t *testing.T) {
		args := map[string]any{
			"title": "Copy Eve to billing", "reason": "Ticket 9.",
			"steps": []map[string]any{
				{"connection": "target", "statements": []string{"UPDATE users SET status = 'migrated' WHERE id = 5"}},
				{"connection": "billing", "title": "Create the customer", "statements": []string{"INSERT INTO customers (id, display_name) VALUES (5, 'Eve')"}},
			},
		}
		out := c.ok("submit_sequence", args)
		seqNo := num(out["number"])
		steps := list(out["steps"])
		if seqNo == 0 || out["status"] != "running" || num(out["current_step"]) != 1 || len(steps) != 2 ||
			out["review_url"] != publicURL+"/sequences/"+itoa(int(seqNo)) || !strings.Contains(out["message"].(string), "not undone") {
			t.Fatalf("submit_sequence = %v", out)
		}
		s1, s2 := obj(steps[0]), obj(steps[1])
		if s1["status"] != "pending_review" || num(obj(list(s1["statements"])[0])["rows_affected"]) != 1 ||
			s2["status"] != "draft" || s2["title"] != "Create the customer" || obj(list(s2["statements"])[0])["rows_affected"] != nil {
			t.Errorf("steps = %v", steps)
		}
		// A retry returns the same sequence.
		if again := c.ok("submit_sequence", args); num(again["number"]) != seqNo || again["already_existed"] != true {
			t.Errorf("retry = %v", again)
		}
		// The waiting step says what it waits for.
		got := c.ok("get_change", map[string]any{"number": num(s2["number"])})
		if num(got["sequence"]) != seqNo || num(got["step"]) != 2 || !strings.Contains(got["message"].(string), "after step 1 has run") {
			t.Errorf("get_change of step 2 = %v", got)
		}
		// list_changes shows the sequence once, as its current step.
		found := 0
		for _, x := range list(c.ok("list_changes", nil)["changes"]) {
			if num(obj(x)["sequence"]) == seqNo {
				found++
				if num(obj(x)["step"]) != 1 || num(obj(x)["steps"]) != 2 {
					t.Errorf("listed step = %v", x)
				}
			}
		}
		if found != 1 {
			t.Errorf("sequence listed %d times", found)
		}

		// A bad later step: the error names it, and nothing is created.
		before := e.count("SELECT count(*) FROM change_requests")
		c.fails("submit_sequence", map[string]any{"title": "Bad", "reason": "x", "steps": []map[string]any{
			{"connection": "target", "statements": []string{"UPDATE users SET name = 'ok' WHERE id = 4"}},
			{"connection": "billing", "statements": []string{"DELETE FROM customers"}},
		}}, "Step 2", "WHERE", "nothing was created")
		c.fails("submit_sequence", map[string]any{"title": "Bad", "reason": "x", "steps": []map[string]any{
			{"connection": "target", "statements": []string{"UPDATE users SET name = 'ok' WHERE id = 4"}},
			{"connection": "nowhere", "statements": []string{"DELETE FROM customers WHERE id = 1"}},
		}}, "Step 2", "nowhere")
		if n := e.count("SELECT count(*) FROM change_requests"); n != before {
			t.Error("a refused sequence created changes")
		}

		// A person approves step 1; step 2 is then previewed for review.
		first, err := e.a.changes.Get(e.ctx(e.bob), e.bob, itoa(int(num(s1["number"]))))
		if err != nil {
			t.Fatal(err)
		}
		e.mustApprove(e.bob, first)
		e.run()
		got = c.ok("get_sequence", map[string]any{"number": seqNo})
		if num(got["current_step"]) != 2 || obj(list(got["steps"])[1])["status"] != "pending_review" ||
			!strings.Contains(got["message"].(string), "Applied and not rolled back: step 1") {
			t.Errorf("after step 1 = %v", got)
		}

		// Cancelling stops it; step 1 stays applied.
		got = c.ok("cancel_sequence", map[string]any{"number": seqNo, "reason": "Done by hand."})
		if got["status"] != "stopped" || got["status_code"] != "cancelled" || !strings.Contains(got["message"].(string), "step 1") {
			t.Errorf("cancel_sequence = %v", got)
		}
		c.fails("cancel_sequence", map[string]any{"number": seqNo}, "stopped")
		// Another person's sequence does not exist for the agent.
		bobs, err := e.a.changes.CreateSequence(e.ctx(e.bob), e.bob, changes.SequenceDraft{Title: "Bob's", Steps: []changes.StepDraft{
			{ConnectionID: e.conn, Statements: []string{"UPDATE users SET name = 'B1' WHERE id = 4"}},
			{ConnectionID: e.conn, Statements: []string{"UPDATE users SET name = 'B2' WHERE id = 4"}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		c.fails("get_sequence", map[string]any{"number": bobs.Number}, "No such")
		c.fails("cancel_sequence", map[string]any{"number": bobs.Number}, "No such")
	})

	t.Run("rate limits", func(t *testing.T) {
		if err := e.a.settings.Update(e.ctx(e.ada), settings.Changes{Values: map[string]any{"limits.mcp_previews_per_hour": 2}}); err != nil {
			t.Fatal(err)
		}
		args := map[string]any{"connection": "target", "statements": []string{"UPDATE users SET name = 'Q' WHERE id = 3"}}
		c.ok("preview_change", args)
		c.ok("preview_change", args)
		c.fails("preview_change", args, "Rate limit", "2 preview_change calls per hour", "30 minutes")
		c.ok("list_connections", nil) // other tools are unaffected
		e.clock.Advance(31 * time.Minute)
		c.ok("preview_change", args)
	})

	t.Run("no row data in any response", func(t *testing.T) {
		all := strings.ToLower(c.rec.all())
		if !strings.Contains(all, "public.users") || !strings.Contains(all, "rows_affected") {
			t.Fatal("the recorded responses are not the tool results")
		}
		// The rows the agent's change touched do carry the marker: a person
		// sees it in the dashboard.
		ch, err := e.a.changes.Get(e.ctx(e.bob), e.bob, itoa(int(number)))
		if err != nil {
			t.Fatal(err)
		}
		if p := ch.Statements[0].Preview; p == nil || !strings.Contains(strings.ToLower(jsonOf(t, p.Before)), marker) {
			t.Fatalf("the seeded rows do not carry the marker: %+v", p)
		}
		if strings.Contains(all, marker) {
			i := strings.Index(all, marker)
			t.Errorf("marker leaked: ...%s...", all[max(0, i-200):min(len(all), i+100)])
		}
		// The audit events summarize the arguments, never the SQL.
		for _, a := range e.auditRows() {
			if strings.HasPrefix(a, "mcp.tool_called") && strings.Contains(a, "UPDATE users") {
				t.Errorf("SQL in the tool audit event: %s", a)
			}
		}
	})

	t.Run("authentication", func(t *testing.T) {
		if r := rawMCP(t, srv.URL, "", initializeBody); r.StatusCode != http.StatusUnauthorized || !strings.HasPrefix(r.Header.Get("WWW-Authenticate"), "Bearer") {
			t.Errorf("no token: %d %q", r.StatusCode, r.Header.Get("WWW-Authenticate"))
		}
		if r := rawMCP(t, srv.URL, "Bearer dbal_pat_nope", initializeBody); r.StatusCode != http.StatusUnauthorized {
			t.Errorf("unknown token: %d", r.StatusCode)
		}
		if r := rawMCP(t, srv.URL, "Bearer "+tok.Secret, initializeBody); r.StatusCode != http.StatusOK {
			t.Errorf("valid token: %d", r.StatusCode)
		}
		big := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"x":"` + strings.Repeat("a", 300<<10) + `"}}`
		if r := rawMCP(t, srv.URL, "Bearer "+tok.Secret, big); r.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("large body: %d", r.StatusCode)
		}

		revoked, err := e.a.identity.CreateToken(e.ctx(e.dan), e.dan, "old", 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.a.identity.RevokeToken(e.ctx(e.dan), e.dan, revoked.ID); err != nil {
			t.Fatal(err)
		}
		if r := rawMCP(t, srv.URL, "Bearer "+revoked.Secret, initializeBody); r.StatusCode != http.StatusUnauthorized {
			t.Errorf("revoked token: %d", r.StatusCode)
		}

		// Removing the owner rejects the token at once.
		if _, err := e.a.identity.RemoveUser(e.ctx(e.ada), e.ada, e.dan.UserID); err != nil {
			t.Fatal(err)
		}
		if r := rawMCP(t, srv.URL, "Bearer "+tok.Secret, initializeBody); r.StatusCode != http.StatusUnauthorized {
			t.Errorf("removed owner's token: %d", r.StatusCode)
		}
		res, err := c.sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_connections", Arguments: map[string]any{}})
		if err == nil && !res.IsError {
			t.Error("a removed owner's session still works")
		}
	})
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// auditRows returns every audit row as "action via payload".
func (e *chgEnv) auditRows() []string {
	e.t.Helper()
	rows, err := e.a.store.Reader().QueryContext(context.Background(), `SELECT action || ' ' || via || ' ' || payload FROM audit_events ORDER BY seq`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// TestMCPSetupMode: /mcp answers 503 until setup is complete.
func TestMCPSetupMode(t *testing.T) {
	hs := newHarness(t)
	srv := httptest.NewServer(hs.h)
	t.Cleanup(srv.Close)
	if r := rawMCP(t, srv.URL, "Bearer dbal_pat_x", initializeBody); r.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("setup mode: %d", r.StatusCode)
	}
}
