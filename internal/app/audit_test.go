package app

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Phase 9 checks over HTTP: the audit page's API, chain verification, the
// export, the System page, and that the webhook URL never comes back.

const hookSecret = "hooks-secret-T0KEN"

func events(t *testing.T, r response) []map[string]any {
	t.Helper()
	if r.Code != http.StatusOK {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	list, _ := r.body["events"].([]any)
	out := make([]map[string]any, len(list))
	for i, e := range list {
		out[i] = e.(map[string]any)
	}
	return out
}

func TestAuditAPI(t *testing.T) {
	hs := newHarness(t)
	ada := hs.completeSetup()
	var bodies []string
	keep := func(r response) response { bodies = append(bodies, r.Body.String()); return r }

	// A few settings changes, one with the webhook URL.
	for _, name := range []string{"one", "two", "three"} {
		if r := keep(ada.do("PATCH", "/api/settings", map[string]any{"values": map[string]any{"instance.name": name}})); r.Code != http.StatusOK {
			t.Fatalf("patch: %d %s", r.Code, r.Body)
		}
	}
	r := keep(ada.do("PATCH", "/api/settings", map[string]any{
		"values":  map[string]any{"notify.enabled": true, "notify.format": "slack", "notify.events": []string{"change.approved"}},
		"secrets": map[string]any{"notify.webhook_url": "https://hooks.example.com/services/" + hookSecret},
	}))
	if r.Code != http.StatusOK || r.body["values"].(map[string]any)["notify.format"] != "slack" {
		t.Fatalf("notify settings: %d %s", r.Code, r.Body)
	}

	all := events(t, keep(ada.do("GET", "/api/audit", nil)))
	if len(all) < 6 {
		t.Fatalf("%d events", len(all))
	}
	first := all[0]
	if len(first["hash"].(string)) != 64 || len(first["prev_hash"].(string)) != 64 || first["actor"].(map[string]any)["via"] != "web" {
		t.Fatalf("event = %v", first)
	}
	for i := 1; i < len(all); i++ {
		if all[i-1]["seq"].(float64) <= all[i]["seq"].(float64) {
			t.Fatal("not newest first")
		}
	}

	// Filters.
	q := url.Values{"action": {"setting."}, "actor": {"ada@example.com"}, "via": {"web"}}
	settingEvents := events(t, keep(ada.do("GET", "/api/audit?"+q.Encode(), nil)))
	if len(settingEvents) < 6 {
		t.Fatalf("%d setting events", len(settingEvents))
	}
	for _, e := range settingEvents {
		if !strings.HasPrefix(e["action"].(string), "setting.") || e["actor"].(map[string]any)["email"] != "ada@example.com" {
			t.Fatalf("filtered event %v", e)
		}
	}
	if got := events(t, ada.do("GET", "/api/audit?via=mcp", nil)); len(got) != 0 {
		t.Fatalf("mcp events: %v", got)
	}
	if got := events(t, ada.do("GET", "/api/audit?from=2099-01-01T00:00:00Z", nil)); len(got) != 0 {
		t.Fatalf("future events: %v", got)
	}
	if r := ada.do("GET", "/api/audit?via=carrier-pigeon", nil); r.Code != http.StatusBadRequest {
		t.Fatalf("bad channel: %d", r.Code)
	}

	// Paging walks the same events.
	var paged []float64
	path := "/api/audit?limit=2"
	for range 100 {
		r := ada.do("GET", path, nil)
		for _, e := range events(t, r) {
			paged = append(paged, e["seq"].(float64))
		}
		next, ok := r.body["next_before"].(float64)
		if !ok {
			break
		}
		path = "/api/audit?limit=2&before=" + itoa(int(next))
	}
	// Reading the audit log records nothing, so the listing is unchanged.
	if len(paged) != len(all) || paged[0] != all[0]["seq"] || paged[len(paged)-1] != 1 {
		t.Fatalf("paged %v, listed %d", paged, len(all))
	}

	// A member reads the log but cannot verify, export or see the system.
	bob := hs.browser("198.51.100.2")
	bob.signIn(bobMember)
	if got := events(t, bob.do("GET", "/api/audit?limit=1", nil)); len(got) != 1 {
		t.Fatal("member cannot read the audit log")
	}
	for _, rq := range [][2]string{{"POST", "/api/audit/verify"}, {"GET", "/api/audit/verify"}, {"GET", "/api/audit/export"}, {"GET", "/api/system"}, {"GET", "/api/notifications"}, {"POST", "/api/notifications/test"}} {
		if r := bob.do(rq[0], rq[1], nil); r.Code != http.StatusForbidden {
			t.Errorf("member %s %s: %d", rq[0], rq[1], r.Code)
		}
	}

	// Verification.
	if r := ada.do("GET", "/api/audit/verify", nil); r.body["state"] != "none" {
		t.Fatalf("before any run: %s", r.Body)
	}
	r = keep(ada.do("POST", "/api/audit/verify", nil))
	if r.Code != http.StatusOK || r.body["state"] != "done" || r.body["ok"] != true || r.body["last_success"] == nil {
		t.Fatalf("verify: %d %s", r.Code, r.Body)
	}

	// System.
	r = keep(ada.do("GET", "/api/system", nil))
	counts, _ := r.body["counts"].(map[string]any)
	mig, _ := r.body["migration"].(map[string]any)
	if r.Code != http.StatusOK || r.body["version"] != "test" || counts["users"] != float64(2) || counts["connections"] != float64(0) ||
		mig["pending"] != false || mig["current"] != mig["latest"] || r.body["store_bytes"].(float64) <= 0 ||
		r.body["last_verification"] == nil || !strings.HasSuffix(r.body["data_dir"].(string), "data") {
		t.Fatalf("system: %d %s", r.Code, r.Body)
	}

	// Notifications: nothing queued; the test send fails (no receiver), and
	// says so without the URL.
	keep(ada.do("GET", "/api/notifications", nil))
	r = keep(ada.do("POST", "/api/notifications/test", nil))
	if r.Code != http.StatusOK || r.body["ok"] != false {
		t.Fatalf("test notification: %d %s", r.Code, r.Body)
	}

	// Export: valid NDJSON covering every event.
	exp := ada.do("GET", "/api/audit/export", nil)
	bodies = append(bodies, exp.Body.String())
	if exp.Code != http.StatusOK || exp.Header().Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("export: %d %s", exp.Code, exp.Header())
	}
	sc := bufio.NewScanner(strings.NewReader(exp.Body.String()))
	lines := 0
	for sc.Scan() {
		var line map[string]any
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatalf("export line %d: %v", lines+1, err)
		}
		lines++
		if line["seq"] != float64(lines) {
			t.Fatalf("export line %d has seq %v", lines, line["seq"])
		}
	}
	if lines < len(all) {
		t.Fatalf("export has %d lines, the log at least %d", lines, len(all))
	}

	// The webhook URL never came back.
	keep(ada.do("GET", "/api/settings", nil))
	keep(ada.do("GET", "/api/audit?limit=500", nil))
	for _, b := range bodies {
		if strings.Contains(b, hookSecret) {
			t.Fatalf("webhook URL in a response: %s", b)
		}
	}

	// Tampering is found and named by sequence number.
	db, err := sql.Open("sqlite", hs.app.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, stmt := range []string{"DROP TRIGGER audit_events_no_update", `UPDATE audit_events SET payload = '{"forged":true}' WHERE seq = 3`} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	r = ada.do("POST", "/api/audit/verify", nil)
	problem, _ := r.body["problem"].(map[string]any)
	if r.body["state"] != "done" || r.body["ok"] != false || problem["seq"] != float64(3) {
		t.Fatalf("tampered: %s", r.Body)
	}
}
