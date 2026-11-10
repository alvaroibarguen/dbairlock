package notify_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/notify"
	"github.com/alvaroibarguen/dbairlock/internal/platform/clock"
	"github.com/alvaroibarguen/dbairlock/internal/platform/ids"
	"github.com/alvaroibarguen/dbairlock/internal/secrets"
	"github.com/alvaroibarguen/dbairlock/internal/settings"
	"github.com/alvaroibarguen/dbairlock/internal/store"
)

// webhookToken stands in for the credential a chat webhook URL embeds.
const webhookToken = "T0KEN-must-never-leak"

type receiver struct {
	srv *httptest.Server

	mu     sync.Mutex
	bodies []map[string]any
	// status answers each request in turn; the last one repeats.
	status []int
}

func newReceiver(t *testing.T, status ...int) *receiver {
	t.Helper()
	r := &receiver{status: status}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		r.mu.Lock()
		r.bodies = append(r.bodies, body)
		code := http.StatusOK
		if len(r.status) > 0 {
			code = r.status[0]
			if len(r.status) > 1 {
				r.status = r.status[1:]
			}
		}
		r.mu.Unlock()
		if code >= 300 && code < 400 {
			w.Header().Set("Location", "/elsewhere")
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) url() string { return r.srv.URL + "/hooks/" + webhookToken }

func (r *receiver) received() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]any(nil), r.bodies...)
}

type env struct {
	t     *testing.T
	st    *store.Store
	set   *settings.Service
	clock *clock.Fake
	n     *notify.Service
	logs  *bytes.Buffer
}

var admin = audit.WithActor(context.Background(), audit.Actor{UserID: "u1", Email: "admin@example.com", Via: audit.ViaWeb})

func newEnv(t *testing.T, allowLoopback bool) *env {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	ctx := context.Background()
	st, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	c := clock.NewFake(time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC))
	rec := audit.NewRecorder(c, &ids.Sequential{Prefix: "evt"})
	logs := &bytes.Buffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	sec, err := secrets.Load(ctx, secrets.Options{DataDir: dir, Store: st, Audit: rec, Clock: c, IDs: ids.UUIDv7{}, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	set, err := settings.New(ctx, settings.Options{Store: st, Audit: rec, Secrets: sec, Clock: c, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	n, err := notify.New(notify.Options{
		Store: st, Audit: rec, Secrets: sec, Settings: set, Clock: c, IDs: &ids.Sequential{Prefix: "ntf"},
		Logger: log, AllowLoopback: allowLoopback,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, st: st, set: set, clock: c, n: n, logs: logs}
}

// configure enables notifications for events, posting to url.
func (e *env) configure(url string, events ...string) {
	e.t.Helper()
	if events == nil {
		events = []string{}
	}
	err := e.set.Update(admin, settings.Changes{
		Values:  map[string]any{"notify.enabled": true, "notify.events": events},
		Secrets: map[string]settings.SecretChange{"notify.webhook_url": {Value: secrets.NewValue(url)}},
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) enqueue(ev notify.Event) {
	e.t.Helper()
	if err := e.st.Tx(admin, func(tx *store.Tx) error { return e.n.Enqueue(admin, tx, ev) }); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) recent() []notify.Delivery {
	e.t.Helper()
	list, err := e.n.Recent(context.Background(), 50)
	if err != nil {
		e.t.Fatal(err)
	}
	return list
}

func event(typ string) notify.Event {
	return notify.Event{
		Type: typ, Text: "Change #7 " + typ, URL: "https://dbairlock.test/changes/7",
		Fields: []notify.Field{{Key: "change", Label: "Change", Value: 7}, {Key: "title", Label: "Title", Value: "Fix names"}},
	}
}

func (e *env) assertNoURL(extra ...string) {
	e.t.Helper()
	all := append([]string{e.logs.String()}, extra...)
	for _, d := range e.recent() {
		all = append(all, d.LastError)
	}
	for _, s := range all {
		if strings.Contains(s, webhookToken) {
			e.t.Fatalf("webhook URL leaked: %s", s)
		}
	}
}

func TestDeliversOnlySelectedEvents(t *testing.T) {
	e := newEnv(t, true)
	r := newReceiver(t)
	e.configure(r.url(), "change.submitted", "change.approved", "change.failed")
	for _, typ := range settings.NotifyEventNames {
		e.enqueue(event(typ))
	}
	e.n.RunOnce(context.Background())
	got := r.received()
	var types []string
	for _, b := range got {
		types = append(types, b["event"].(string))
		if b["url"] != "https://dbairlock.test/changes/7" || b["title"] != "Fix names" || b["change"] != float64(7) {
			t.Errorf("body = %v", b)
		}
	}
	if strings.Join(types, ",") != "change.submitted,change.approved,change.failed" {
		t.Fatalf("delivered %v", types)
	}
	for _, d := range e.recent() {
		if d.Status != "delivered" || d.Attempts != 1 || d.LastStatus != 200 {
			t.Errorf("delivery %+v", d)
		}
	}
	e.assertNoURL()
}

func TestDisabledQueuesNothing(t *testing.T) {
	e := newEnv(t, true)
	r := newReceiver(t)
	e.configure(r.url(), settings.NotifyEventNames...)
	if err := e.set.Update(admin, settings.Changes{Values: map[string]any{"notify.enabled": false}}); err != nil {
		t.Fatal(err)
	}
	e.enqueue(event("change.submitted"))
	e.n.RunOnce(context.Background())
	if len(r.received()) != 0 || len(e.recent()) != 0 {
		t.Fatal("a disabled notifier queued or sent something")
	}
}

func TestRetryAfterServerError(t *testing.T) {
	e := newEnv(t, true)
	r := newReceiver(t, 500, 200)
	e.configure(r.url(), "change.approved")
	e.enqueue(event("change.approved"))
	ctx := context.Background()

	e.n.RunOnce(ctx)
	d := e.recent()[0]
	if d.Status != "pending" || d.Attempts != 1 || d.LastStatus != 500 || d.LastError != "The receiver answered 500." {
		t.Fatalf("after a 500: %+v", d)
	}
	if !d.NextAttemptAt.Equal(e.clock.Now().Add(notify.FirstRetry)) {
		t.Fatalf("next attempt at %v", d.NextAttemptAt)
	}
	e.n.RunOnce(ctx) // not due yet
	if len(r.received()) != 1 {
		t.Fatal("retried before the backoff elapsed")
	}
	e.clock.Advance(notify.FirstRetry)
	e.n.RunOnce(ctx)
	d = e.recent()[0]
	if d.Status != "delivered" || d.Attempts != 2 || len(r.received()) != 2 {
		t.Fatalf("after the retry: %+v (%d requests)", d, len(r.received()))
	}
	e.assertNoURL()
}

func TestGivesUpAfterDeadline(t *testing.T) {
	e := newEnv(t, true)
	r := newReceiver(t, 503)
	e.configure(r.url(), "change.failed")
	start := e.clock.Now()
	e.enqueue(event("change.failed"))
	ctx := context.Background()
	var waits []time.Duration
	for i := 0; i < 100; i++ {
		e.n.RunOnce(ctx)
		d := e.recent()[0]
		if d.Status != "pending" {
			break
		}
		wait := d.NextAttemptAt.Sub(e.clock.Now())
		waits = append(waits, wait)
		e.clock.Set(d.NextAttemptAt)
	}
	d := e.recent()[0]
	if d.Status != "gave_up" {
		t.Fatalf("still %s after %d attempts", d.Status, d.Attempts)
	}
	if e.clock.Now().Sub(start) > notify.GiveUpAfter {
		t.Fatalf("kept trying for %v", e.clock.Now().Sub(start))
	}
	if int(d.Attempts) != len(r.received()) || d.Attempts < 10 {
		t.Fatalf("attempts %d, requests %d", d.Attempts, len(r.received()))
	}
	// Exponential backoff, capped.
	if waits[0] != notify.FirstRetry || waits[1] != 2*notify.FirstRetry || waits[len(waits)-1] != notify.MaxRetryWait {
		t.Fatalf("waits = %v", waits)
	}
	// Giving up is audited, without the URL.
	page, err := audit.NewReader(e.st.Reader()).List(ctx, audit.Filter{ActionPrefix: string(audit.NotificationGaveUp)})
	if err != nil || len(page.Entries) != 1 {
		t.Fatalf("gave_up events: %v %v", page.Entries, err)
	}
	e.assertNoURL(string(page.Entries[0].Payload))
	// Nothing more is sent.
	e.clock.Advance(48 * time.Hour)
	n := len(r.received())
	e.n.RunOnce(ctx)
	if len(r.received()) != n {
		t.Fatal("sent after giving up")
	}
}

func TestRolledBackTransactionSendsNothing(t *testing.T) {
	e := newEnv(t, true)
	r := newReceiver(t)
	e.configure(r.url(), settings.NotifyEventNames...)
	boom := errors.New("the change failed")
	err := e.st.Tx(admin, func(tx *store.Tx) error {
		if err := e.n.Enqueue(admin, tx, event("change.approved")); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	e.n.RunOnce(context.Background())
	if len(r.received()) != 0 || len(e.recent()) != 0 {
		t.Fatal("a rolled-back event was queued or sent")
	}
}

func TestRefusesLoopbackAndRedirects(t *testing.T) {
	ctx := context.Background()
	t.Run("loopback outside dev", func(t *testing.T) {
		e := newEnv(t, false)
		r := newReceiver(t)
		e.configure(r.url(), "change.submitted")
		e.enqueue(event("change.submitted"))
		e.n.RunOnce(ctx)
		d := e.recent()[0]
		if len(r.received()) != 0 || d.Status != "pending" || !strings.Contains(d.LastError, "blocked address") {
			t.Fatalf("%+v, %d requests", d, len(r.received()))
		}
		res, err := e.n.SendTest(admin)
		if err != nil || res.OK || !strings.Contains(res.Message, "blocked address") {
			t.Fatalf("test: %+v %v", res, err)
		}
		e.assertNoURL()
	})
	t.Run("redirects are not followed", func(t *testing.T) {
		e := newEnv(t, true)
		r := newReceiver(t, http.StatusFound)
		e.configure(r.url(), "change.submitted")
		res, err := e.n.SendTest(admin)
		if err != nil || res.OK || res.StatusCode != http.StatusFound || len(r.received()) != 1 {
			t.Fatalf("%+v %v, %d requests", res, err, len(r.received()))
		}
	})
}

func TestSendTest(t *testing.T) {
	e := newEnv(t, true)
	if _, err := e.n.SendTest(admin); !errors.Is(err, notify.ErrNoWebhook) {
		t.Fatalf("without a URL: %v", err)
	}
	r := newReceiver(t)
	e.configure(r.url())
	// Sent even though no event is selected, in the chosen format.
	if err := e.set.Update(admin, settings.Changes{Values: map[string]any{"notify.format": "slack"}}); err != nil {
		t.Fatal(err)
	}
	res, err := e.n.SendTest(admin)
	if err != nil || !res.OK || res.StatusCode != 200 {
		t.Fatalf("%+v %v", res, err)
	}
	got := r.received()
	if len(got) != 1 || got[0]["blocks"] == nil || !strings.HasPrefix(got[0]["text"].(string), "Test notification") {
		t.Fatalf("received %v", got)
	}
	page, err := audit.NewReader(e.st.Reader()).List(context.Background(), audit.Filter{ActionPrefix: string(audit.NotificationTestSent)})
	if err != nil || len(page.Entries) != 1 {
		t.Fatalf("test_sent events: %v %v", page.Entries, err)
	}
	e.assertNoURL(string(page.Entries[0].Payload))
}

func TestRender(t *testing.T) {
	ev := notify.Event{
		Type: "change.approved", Text: "Change #3 approved: <!channel> & <https://evil.example|click>",
		URL: "https://dbairlock.test/changes/3", OccurredAt: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		Fields: []notify.Field{{Key: "change", Label: "Change", Value: 3}, {Key: "event", Label: "Shadow", Value: "x"}},
	}
	raw, err := notify.Render("generic", ev)
	if err != nil {
		t.Fatal(err)
	}
	var g map[string]any
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	if g["event"] != "change.approved" || g["change"] != float64(3) || g["occurred_at"] != "2026-05-01T12:00:00Z" || g["url"] != ev.URL {
		t.Fatalf("generic = %v", g)
	}
	raw, err = notify.Render("slack", ev)
	if err != nil {
		t.Fatal(err)
	}
	var sl struct {
		Text   string `json:"text"`
		Blocks []struct {
			Type     string `json:"type"`
			Text     *struct{ Type, Text string }
			Elements []struct{ Type, Text string }
		} `json:"blocks"`
	}
	if err := json.Unmarshal(raw, &sl); err != nil {
		t.Fatal(err)
	}
	// Text written by people stays plain: in mrkdwn it is escaped, and the
	// summary block is plain_text, so no link or mention can form.
	if strings.Contains(sl.Text, "<!channel>") || strings.Contains(sl.Text, "<https://evil") ||
		!strings.HasSuffix(sl.Text, "<"+ev.URL+"|Open in dbairlock>") {
		t.Fatalf("slack fallback text = %q", sl.Text)
	}
	if len(sl.Blocks) != 3 || sl.Blocks[0].Text.Type != "plain_text" || sl.Blocks[2].Elements[0].Text != "<"+ev.URL+"|Open in dbairlock>" {
		t.Fatalf("slack blocks = %s", raw)
	}
	if _, err := notify.Render("teams", ev); err == nil {
		t.Fatal("unknown format rendered")
	}
}
