//go:build load

package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"testing"
	"testing/fstest"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/identity"
	"github.com/alvaroibarguen/dbairlock/internal/platform/clock"
	"github.com/alvaroibarguen/dbairlock/internal/platform/ids"
	"github.com/alvaroibarguen/dbairlock/internal/platform/logx"
	"github.com/alvaroibarguen/dbairlock/internal/store"
)

// Load sanity (phase 11): 50,000 changes and 500,000 audit events, then the
// list endpoints, the audit page API and audit verification are timed.
//
//	go test -tags load -run TestLoad -timeout 60m -v ./internal/app
//
// (make load). The numbers are recorded in docs/report-notes/robust.md.

const (
	loadChanges = 50_000
	loadEvents  = 500_000
	// Budget for one API request on this data set. A list page that takes
	// longer is not "responsive".
	loadBudget = 250 * time.Millisecond
)

func TestLoad(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2025, 11, 1, 0, 0, 0, 0, time.UTC)
	c := clock.NewFake(start)
	dir := t.TempDir()
	a, err := New(Options{
		DataDir: dir, Listen: "127.0.0.1:0", Dev: true, Version: "test", Logger: logx.Discard(), Clock: c,
		WebUI: fstest.MapFS{"index.html": {Data: []byte("x")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()

	// Users: an admin who reviews, and twenty authors.
	type user struct {
		id, email, token string
	}
	var users []user
	for i := range 21 {
		role := identity.RoleMember
		if i == 0 {
			role = identity.RoleAdmin
		}
		email := fmt.Sprintf("user%02d@example.com", i)
		u, sess, err := a.identity.DevLogin(ctx, email, role, identity.Meta{IP: "127.0.0.1"})
		if err != nil {
			t.Fatal(err)
		}
		users = append(users, user{id: u.ID, email: email, token: sess.Token})
	}
	admin := users[0]

	elapsed := stopwatch()
	connIDs := seedConnections(t, a.store, 3)
	changeIDs := seedChanges(t, a.store, connIDs, func(i int) string { return users[1+i%20].id }, admin.id, start)
	t.Logf("seeded %d changes in %s", loadChanges, elapsed().Round(time.Millisecond))

	elapsed = stopwatch()
	// A clock of its own: the sessions above must not expire.
	seedAudit(t, a.store, clock.NewFake(start), changeIDs, users[0].id, users[0].email, func(i int) (string, string) {
		u := users[1+i%20]
		return u.id, u.email
	})
	t.Logf("seeded %d audit events in %s", loadEvents, elapsed().Round(time.Millisecond))
	size, _ := a.store.Size()
	t.Logf("database size: %.1f MB", float64(size)/(1<<20))

	h := a.Handler()
	get := func(token, path string) (int, time.Duration) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "127.0.0.1:8080"
		req.RemoteAddr = "127.0.0.1:50000"
		req.AddCookie(&http.Cookie{Name: "dbairlock_session", Value: token})
		req.AddCookie(&http.Cookie{Name: "__Host-dbairlock_session", Value: token})
		rec := httptest.NewRecorder()
		took := stopwatch()
		h.ServeHTTP(rec, req)
		return rec.Code, took()
	}
	author := users[5]
	mid := start.Add(180 * 24 * time.Hour).Format(time.RFC3339)
	midEnd := start.Add(181 * 24 * time.Hour).Format(time.RFC3339)
	aChange := changeIDs[loadChanges/2]
	cases := []struct {
		name, token, path string
	}{
		{"changes: all, first page", admin.token, "/api/changes?view=all"},
		{"changes: all, page 100", admin.token, "/api/changes?view=all&before=" + strconv.Itoa(loadChanges-100*50)},
		{"changes: needs my review", admin.token, "/api/changes?view=needs_my_review"},
		{"changes: mine (author)", author.token, "/api/changes?view=mine"},
		{"changes: status=pending_review", admin.token, "/api/changes?view=all&status=pending_review"},
		{"changes: status=failed", admin.token, "/api/changes?view=all&status=failed"},
		{"changes: open", admin.token, "/api/changes?view=all&open=true"},
		{"changes: by connection", admin.token, "/api/changes?view=all&connection_id=" + connIDs[2]},
		{"changes: by author", admin.token, "/api/changes?view=all&author_id=" + author.id},
		{"change: one", admin.token, "/api/changes/" + aChange},
		{"change: timeline", admin.token, "/api/changes/" + aChange + "/events"},
		{"audit: first page", admin.token, "/api/audit"},
		{"audit: page 1000", admin.token, "/api/audit?before=" + strconv.Itoa(loadEvents-1000*50)},
		{"audit: action=change.approved", admin.token, "/api/audit?action=change.approved"},
		{"audit: action prefix setting.", admin.token, "/api/audit?action=setting."},
		{"audit: actor email", admin.token, "/api/audit?actor=" + url.QueryEscape(author.email)},
		{"audit: subject one change", admin.token, "/api/audit?subject_type=change&subject_id=" + aChange},
		{"audit: via=cli", admin.token, "/api/audit?via=cli"},
		{"audit: one day mid-year", admin.token, "/api/audit?from=" + url.QueryEscape(mid) + "&to=" + url.QueryEscape(midEnd)},
		{"audit: day + action", admin.token, "/api/audit?action=execution.&from=" + url.QueryEscape(mid) + "&to=" + url.QueryEscape(midEnd)},
		{"system page", admin.token, "/api/system"},
	}
	var slow []string
	for _, tc := range cases {
		var times []time.Duration
		for range 5 {
			code, d := get(tc.token, tc.path)
			if code != http.StatusOK {
				t.Fatalf("%s: %d", tc.name, code)
			}
			times = append(times, d)
		}
		slices.Sort(times)
		med := times[len(times)/2]
		t.Logf("%-34s median %8s  max %8s", tc.name, med.Round(10*time.Microsecond), times[len(times)-1].Round(10*time.Microsecond))
		if med > loadBudget {
			slow = append(slow, tc.name)
		}
	}

	elapsed = stopwatch()
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	res, err := VerifyAudit(ctx, dir)
	if err != nil || !res.OK() || res.Checked < loadEvents {
		t.Fatalf("verify: %+v %v", res, err)
	}
	t.Logf("audit verify of %d events: %s", res.Checked, elapsed().Round(time.Millisecond))
	if len(slow) > 0 {
		t.Errorf("over the %s budget: %v", loadBudget, slow)
	}
}

// stopwatch measures real elapsed time; the instance itself runs on a fake
// clock.
func stopwatch() func() time.Duration {
	t0 := time.Now() //nolint:forbidigo // measures real time
	return func() time.Duration {
		return time.Since(t0) //nolint:forbidigo // measures real time
	}
}

func seedConnections(t *testing.T, st *store.Store, n int) []string {
	t.Helper()
	ctx := context.Background()
	var out []string
	err := st.Tx(ctx, func(tx *store.Tx) error {
		for i := range n {
			id := fmt.Sprintf("conn-%d", i)
			_, err := tx.ExecContext(ctx, `INSERT INTO connections (id, name, connector, config, required_approvals,
				max_rows, store_row_images, auto_execute, approval_ttl_hours, allowed_functions, created_at, updated_at)
				VALUES (?, ?, 'postgres', '{"host":"db.internal","port":5432,"database":"app","user":"u","sslmode":"require","schema":"public"}',
				1, 100, 1, 1, 72, '["now","lower"]', 0, 0)`, id, fmt.Sprintf("db-%d", i))
			if err != nil {
				return err
			}
			out = append(out, id)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// seedChanges inserts loadChanges changes, numbered 1..loadChanges, most of
// them executed, with one statement and one approval each.
func seedChanges(t *testing.T, st *store.Store, conns []string, authorOf func(int) string, reviewer string, start time.Time) []string {
	t.Helper()
	ctx := context.Background()
	gen := ids.UUIDv7{}
	rng := rand.New(rand.NewPCG(1, 2))
	out := make([]string, loadChanges)
	const batch = 5000
	for b := 0; b < loadChanges; b += batch {
		err := st.Tx(ctx, func(tx *store.Tx) error {
			for i := b; i < min(b+batch, loadChanges); i++ {
				id := gen.NewID()
				out[i] = id
				n := i + 1
				status, code := "executed", ""
				switch r := rng.IntN(100); {
				case r < 2:
					status = "pending_review"
				case r < 3:
					status = "approved"
				case r < 6:
					status, code = "failed", "drift"
				case r < 8:
					status = "rejected"
				case r < 10:
					status, code = "cancelled", "author"
				case r < 11:
					status = "draft"
				}
				// Spread over a year, in number order.
				at := start.Add(time.Duration(i) * (365 * 24 * time.Hour / loadChanges)).UnixMilli()
				sqlText := fmt.Sprintf("UPDATE users SET status = 'inactive' WHERE id = %d", n)
				sum := sha256.Sum256([]byte(sqlText))
				author := authorOf(i)
				via := "web"
				if i%3 == 0 {
					via = "mcp"
				}
				var decided, executed, submitted, nextAttempt any
				if status != "draft" {
					submitted = at
				}
				if status != "draft" && status != "pending_review" {
					decided = at + 60_000
				}
				if status == "executed" {
					executed = at + 61_000
				}
				if status == "approved" {
					// Due far in the future: the executor is not under test.
					nextAttempt = at + 365*24*3600*1000
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO change_requests (id, number, connection_id, author_id, via,
					title, reason, status, status_code, sql_hash, preview_hash, previewed_at, revision, next_attempt_at,
					created_at, updated_at, submitted_at, decided_at, executed_at, expires_at)
					VALUES (?, ?, ?, ?, ?, ?, 'Load test', ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?)`,
					id, n, conns[i%len(conns)], author, via, fmt.Sprintf("Deactivate user %d", n), status, code,
					hex.EncodeToString(sum[:]), hex.EncodeToString(sum[:]), at, nextAttempt, at, at+61_000,
					submitted, decided, executed, at+4*365*24*3600*1000); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO change_statements (change_id, revision, position, sql_text,
					kind, schema_name, table_name, expected_rows, expected_fingerprint, has_primary_key, preview)
					VALUES (?, 1, 0, ?, 'update', 'public', 'users', 1, ?, 1,
					'{"columns":["id","status"],"before":[["1","active"]],"after":[["1","inactive"]]}')`,
					id, sqlText, hex.EncodeToString(sum[:])); err != nil {
					return err
				}
				if decided != nil && status != "cancelled" {
					decision := "approve"
					if status == "rejected" {
						decision = "reject"
					}
					if _, err := tx.ExecContext(ctx, `INSERT INTO reviews (id, change_id, reviewer_id, decision, sql_hash,
						preview_hash, revision, created_at) VALUES (?, ?, ?, ?, ?, ?, 1, ?)`,
						gen.NewID(), id, reviewer, decision, hex.EncodeToString(sum[:]), hex.EncodeToString(sum[:]), at+60_000); err != nil {
						return err
					}
				}
				if status == "executed" || status == "failed" {
					outcome := "executed"
					if status == "failed" {
						outcome = "failed"
					}
					if _, err := tx.ExecContext(ctx, `INSERT INTO executions (id, change_id, attempt, started_at, finished_at, outcome)
						VALUES (?, ?, 1, ?, ?, ?)`, gen.NewID(), id, at+60_500, at+61_000, outcome); err != nil {
						return err
					}
				}
			}
			_, err := tx.ExecContext(ctx, `UPDATE change_counter SET last = ?`, min(b+batch, loadChanges))
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// seedAudit records loadEvents events through the real Recorder (so the
// hash chain is genuine), spread over a year: four per change (submitted,
// approved, started, succeeded) and the rest sign-ins, settings changes and
// token use.
func seedAudit(t *testing.T, st *store.Store, c *clock.Fake, changeIDs []string, adminID, adminEmail string, authorOf func(int) (string, string)) {
	t.Helper()
	rec := audit.NewRecorder(c, ids.UUIDv7{})
	step := 365 * 24 * time.Hour / loadEvents
	const batch = 10_000
	perChange := []audit.Action{audit.ChangeSubmitted, audit.ChangeApproved, audit.ExecutionStarted, audit.ExecutionSucceeded}
	n := 0
	for n < loadEvents {
		err := st.Tx(context.Background(), func(tx *store.Tx) error {
			for end := min(n+batch, loadEvents); n < end; n++ {
				c.Advance(step)
				var (
					ctx context.Context
					ev  audit.Event
				)
				k := n / 10 // ten events per slot: four for a change, six others
				switch j := n % 10; {
				case j < 4 && k < len(changeIDs):
					uid, email := authorOf(k)
					via := audit.ViaWeb
					if k%3 == 0 {
						via = audit.ViaMCP
					}
					actor := audit.Actor{UserID: uid, Email: email, Via: via, IP: "10.0.0.7"}
					if j == 1 {
						actor = audit.Actor{UserID: adminID, Email: adminEmail, Via: audit.ViaWeb, IP: "10.0.0.8"}
					}
					if j >= 2 {
						actor = audit.SystemActor
					}
					ctx = audit.WithActor(context.Background(), actor)
					ev = audit.Event{Action: perChange[j], SubjectType: "change", SubjectID: changeIDs[k],
						Payload: map[string]any{"number": k + 1, "sql_hash": "ab12", "connection": "db-1"}}
				case j == 9 && k%50 == 0:
					ctx = audit.WithActor(context.Background(), audit.Actor{Via: audit.ViaCLI})
					ev = audit.Event{Action: audit.AuditVerified, Payload: map[string]any{"checked": n}}
				case j == 8 && k%20 == 0:
					ctx = audit.WithActor(context.Background(), audit.Actor{UserID: adminID, Email: adminEmail, Via: audit.ViaWeb})
					ev = audit.Event{Action: audit.SettingChanged, SubjectType: "setting", SubjectID: "policy.default_max_rows",
						Payload: map[string]any{"key": "policy.default_max_rows", "old": 100, "new": 200}}
				default:
					uid, email := authorOf(n)
					ctx = audit.WithActor(context.Background(), audit.Actor{UserID: uid, Email: email, Via: audit.ViaWeb, IP: "10.0.0.9"})
					ev = audit.Event{Action: audit.LoginSucceeded, SubjectType: "user", SubjectID: uid,
						Payload: map[string]any{"ip": "10.0.0.9"}}
				}
				if err := rec.Record(ctx, tx, ev); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
