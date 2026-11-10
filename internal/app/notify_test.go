package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/alvaroibarguen/dbairlock/internal/changes"
	"github.com/alvaroibarguen/dbairlock/internal/secrets"
	"github.com/alvaroibarguen/dbairlock/internal/settings"
	"github.com/alvaroibarguen/dbairlock/internal/targets"
)

// TestChangeNotifications runs changes through review and execution with a
// local webhook receiver (phase 9): each selected event arrives once, with
// the documented fields and never SQL, reasons or row data.
func TestChangeNotifications(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}
	e := newChgEnv(t, postgresTargets(t), targets.PolicyPatch{})

	var mu sync.Mutex
	var got []map[string]any
	var raw []string
	recv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		mu.Lock()
		got, raw = append(got, body), append(raw, string(b))
		mu.Unlock()
	}))
	t.Cleanup(recv.Close)
	err := e.a.settings.Update(e.ctx(e.ada), settings.Changes{
		Values: map[string]any{
			"notify.enabled": true, "instance.public_url": "https://dbairlock.test",
			"notify.events": []string{"change.submitted", "change.approved", "change.rejected", "change.executed", "change.failed"},
		},
		Secrets: map[string]settings.SecretChange{"notify.webhook_url": {Value: secrets.NewValue(recv.URL + "/hook")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	deliver := func() []map[string]any {
		e.a.notify.RunOnce(context.Background())
		mu.Lock()
		defer mu.Unlock()
		out := got
		got = nil
		return out
	}

	// Submitted, approved, executed.
	c := e.submit(e.ada, "UPDATE users SET name = 'Notified' WHERE id = 1")
	e.mustApprove(e.bob, c)
	e.run()
	e.expectStatus(c, changes.StatusExecuted)
	var events []string
	for _, b := range deliver() {
		events = append(events, b["event"].(string))
		if b["change"] != float64(c.Number) || b["title"] != "Test change" || b["database"] != "target" ||
			b["author"] != "ada@example.com" || b["url"] != "https://dbairlock.test/changes/"+itoa(int(c.Number)) {
			t.Errorf("payload %v", b)
		}
		switch b["event"] {
		case "change.approved":
			if b["reviewer"] != "bob@example.com" || b["status"] != "approved" {
				t.Errorf("approved payload %v", b)
			}
		case "change.executed":
			if b["status"] != "executed" || b["reviewer"] != nil {
				t.Errorf("executed payload %v", b)
			}
		}
	}
	if strings.Join(events, ",") != "change.submitted,change.approved,change.executed" {
		t.Fatalf("events = %v", events)
	}

	// Rejected.
	c2 := e.submit(e.ada, "UPDATE users SET name = 'Never' WHERE id = 2")
	deliver()
	if _, err := e.a.changes.Review(e.ctx(e.cat), e.cat, c2.ID, changes.ReviewInput{Decision: changes.DecisionReject, Comment: "secret reason"}); err != nil {
		t.Fatal(err)
	}
	if b := deliver(); len(b) != 1 || b[0]["event"] != "change.rejected" || b[0]["reviewer"] != "cat@example.com" || b[0]["status"] != "rejected" {
		t.Fatalf("rejected: %v", b)
	}

	// Failed (drift).
	c3 := e.submit(e.ada, "UPDATE users SET name = 'X' WHERE status = 'active'")
	e.mustApprove(e.bob, c3)
	deliver()
	e.db.exec(t, "UPDATE users SET status = 'active' WHERE id = 5")
	e.run()
	e.expectStatus(c3, changes.StatusFailed)
	if b := deliver(); len(b) != 1 || b[0]["event"] != "change.failed" || b[0]["status"] != "failed" {
		t.Fatalf("failed: %v", b)
	}

	// No SQL, reasons, comments or row values ever left.
	mu.Lock()
	defer mu.Unlock()
	for _, body := range raw {
		for _, leak := range []string{"UPDATE", "SET name", "Because the test says so", "secret reason", "Notified", "ann"} {
			if strings.Contains(body, leak) {
				t.Errorf("notification contains %q: %s", leak, body)
			}
		}
	}
}
