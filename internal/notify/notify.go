// Package notify sends outbound webhook notifications. It knows about events
// and destinations, not about changes: a module that wants to notify builds
// an Event and calls Enqueue inside the transaction of the state change it
// reports.
//
// Delivery uses an outbox. Enqueue writes a row in the caller's
// transaction, so a rolled-back change sends nothing, and a slow or failing
// webhook never delays or fails the change. A background worker (Run)
// delivers due rows with exponential backoff and gives up after
// GiveUpAfter. Notifications are a convenience, not the audit trail: they
// may be delayed or dropped.
//
// The webhook URL is a secret (chat webhook URLs embed a credential). It is
// opened at the moment of delivery and never logged, stored elsewhere or
// returned: delivery errors are reduced to fixed sentences that cannot carry
// it.
package notify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/notify/notifydb"
	"github.com/alvaroibarguen/dbairlock/internal/platform/clock"
	"github.com/alvaroibarguen/dbairlock/internal/platform/ids"
	"github.com/alvaroibarguen/dbairlock/internal/secrets"
	"github.com/alvaroibarguen/dbairlock/internal/settings"
	"github.com/alvaroibarguen/dbairlock/internal/store"
)

// Delivery limits. Compile-time constants: they bound how much an outside
// receiver can cost the instance, which is not something to tune per site.
const (
	// Timeout bounds one delivery attempt, connection included.
	Timeout = 5 * time.Second
	// GiveUpAfter is how long an event is retried before it is dropped.
	GiveUpAfter = 24 * time.Hour
	// FirstRetry is the wait after the first failure; it doubles after each
	// further failure up to MaxRetryWait.
	FirstRetry   = 30 * time.Second
	MaxRetryWait = time.Hour
	// PollInterval is how often the worker looks for due events when
	// nothing woke it.
	PollInterval = 2 * time.Second
	// KeepFor is how long delivered and dropped events stay listed.
	KeepFor = 30 * 24 * time.Hour
	// batch is how many due events one pass delivers.
	batch = 20
)

// Event is something worth telling people about.
type Event struct {
	// Type names the event, for example "change.approved". Only types
	// selected in the notify.events setting are sent.
	Type string `json:"event"`
	// Text is a one-line summary for people.
	Text string `json:"text"`
	// URL opens the subject in the dashboard; empty when no public URL is
	// configured.
	URL string `json:"url,omitempty"`
	// Fields are the event's details, in order.
	Fields []Field `json:"fields"`
	// OccurredAt is set by Enqueue.
	OccurredAt time.Time `json:"occurred_at"`
}

// Field is one detail of an event. Value is a string or a number.
type Field struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Value any    `json:"value"`
}

// Transactor is the store surface notify needs.
type Transactor interface {
	Tx(ctx context.Context, fn func(tx *store.Tx) error) error
	Reader() store.DBTX
}

// Auditor records audit events inside the caller's transaction.
type Auditor interface {
	Record(ctx context.Context, tx *store.Tx, ev audit.Event) error
}

// Options configure the notify module.
type Options struct {
	Store    Transactor
	Audit    Auditor
	Secrets  secrets.Opener
	Settings settings.Source
	Clock    clock.Clock
	IDs      ids.Generator
	Logger   *slog.Logger
	// AllowLoopback permits delivery to loopback and link-local addresses.
	// Only --dev (and tests) set it.
	AllowLoopback bool
}

// Service is the notify module.
type Service struct {
	opts   Options
	client *http.Client
	wake   chan struct{}

	mu        sync.Mutex
	lastPrune time.Time
}

// ErrNoWebhook means no webhook URL is set.
var ErrNoWebhook = errors.New("notify: no webhook URL is set")

// New returns the notify module.
func New(opts Options) (*Service, error) {
	if opts.Store == nil || opts.Audit == nil || opts.Secrets == nil || opts.Settings == nil ||
		opts.Clock == nil || opts.IDs == nil || opts.Logger == nil {
		return nil, errors.New("notify: store, audit, secrets, settings, clock, ids and logger are required")
	}
	return &Service{opts: opts, client: newClient(opts.AllowLoopback), wake: make(chan struct{}, 1)}, nil
}

// Enqueue records ev for delivery inside the caller's transaction, if
// notifications are enabled and ev's type is selected. It does nothing
// otherwise. The row commits or rolls back with the caller's change.
func (s *Service) Enqueue(ctx context.Context, tx *store.Tx, ev Event) error {
	if !settings.Get(s.opts.Settings, settings.NotifyEnabled) ||
		!slices.Contains(settings.Get(s.opts.Settings, settings.NotifyEvents), ev.Type) {
		return nil
	}
	now := s.opts.Clock.Now()
	ev.OccurredAt = now
	if ev.Fields == nil {
		ev.Fields = []Field{}
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("notify: encode event: %w", err)
	}
	err = notifydb.New(tx).InsertOutbox(ctx, notifydb.InsertOutboxParams{
		ID: s.opts.IDs.NewID(), Event: ev.Type, Payload: string(raw),
		CreatedAt: now.UnixMilli(), NextAttemptAt: now.UnixMilli(), GiveUpAt: now.Add(GiveUpAfter).UnixMilli(),
	})
	if err != nil {
		return fmt.Errorf("notify: queue event: %w", err)
	}
	s.Wake()
	return nil
}

// Wake makes the worker look for due events now. The event may not be
// committed yet; the next poll then finds it.
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run delivers due events until ctx is cancelled.
func (s *Service) Run(ctx context.Context) error {
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		case <-s.wake:
			// Give the transaction that enqueued the event time to commit.
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(50 * time.Millisecond):
			}
		}
		s.RunOnce(ctx)
		t.Reset(PollInterval)
	}
}

// RunOnce delivers every event that is due now.
func (s *Service) RunOnce(ctx context.Context) {
	for ctx.Err() == nil {
		rows, err := notifydb.New(s.opts.Store.Reader()).DueOutbox(ctx, notifydb.DueOutboxParams{
			NextAttemptAt: s.opts.Clock.Now().UnixMilli(), Limit: batch,
		})
		if err != nil {
			s.opts.Logger.Error("notify: read outbox", slog.Any("error", err))
			return
		}
		for _, row := range rows {
			s.deliverRow(ctx, row)
		}
		if len(rows) < batch {
			break
		}
	}
	s.prune(ctx)
}

func (s *Service) deliverRow(ctx context.Context, row notifydb.NotifyOutbox) {
	var ev Event
	if err := json.Unmarshal([]byte(row.Payload), &ev); err != nil {
		s.opts.Logger.Error("notify: unreadable event", slog.String("id", row.ID), slog.Any("error", err))
		return
	}
	status, failure := s.send(ctx, ev)
	now := s.opts.Clock.Now()
	attempt := row.Attempts + 1
	q := func(tx *store.Tx) *notifydb.Queries { return notifydb.New(tx) }
	log := s.opts.Logger.With(slog.String("id", row.ID), slog.String("event", row.Event), slog.Int64("attempt", attempt))
	next := now.Add(retryWait(attempt))
	var err error
	switch {
	case failure == "":
		err = s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
			return q(tx).MarkDelivered(ctx, notifydb.MarkDeliveredParams{
				FinishedAt: sql.NullInt64{Int64: now.UnixMilli(), Valid: true}, LastStatus: int64(status), ID: row.ID,
			})
		})
		log.Info("notification delivered", slog.Int("status", status))
	case next.UnixMilli() <= row.GiveUpAt:
		err = s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
			return q(tx).MarkRetry(ctx, notifydb.MarkRetryParams{
				NextAttemptAt: next.UnixMilli(), LastStatus: int64(status), LastError: failure, ID: row.ID,
			})
		})
		log.Warn("notification failed; will retry", slog.String("reason", failure), slog.Time("next", next))
	default:
		actx := audit.WithActor(ctx, audit.SystemActor)
		err = s.opts.Store.Tx(actx, func(tx *store.Tx) error {
			if err := q(tx).MarkGaveUp(actx, notifydb.MarkGaveUpParams{
				FinishedAt: sql.NullInt64{Int64: now.UnixMilli(), Valid: true}, LastStatus: int64(status),
				LastError: failure, ID: row.ID,
			}); err != nil {
				return err
			}
			return s.opts.Audit.Record(actx, tx, audit.Event{
				Action: audit.NotificationGaveUp, SubjectType: "notification", SubjectID: row.ID,
				Payload: map[string]any{"event": row.Event, "attempts": attempt, "reason": failure},
			})
		})
		log.Warn("notification dropped after retries", slog.String("reason", failure))
	}
	if err != nil {
		log.Error("notify: record delivery outcome", slog.Any("error", err))
	}
}

// retryWait is the wait after the attempt-th failure: FirstRetry, doubling,
// at most MaxRetryWait.
func retryWait(attempt int64) time.Duration {
	d := FirstRetry
	for i := int64(1); i < attempt && d < MaxRetryWait; i++ {
		d *= 2
	}
	return min(d, MaxRetryWait)
}

func (s *Service) prune(ctx context.Context) {
	now := s.opts.Clock.Now()
	s.mu.Lock()
	due := now.Sub(s.lastPrune) >= time.Hour
	if due {
		s.lastPrune = now
	}
	s.mu.Unlock()
	if !due {
		return
	}
	if err := s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		return notifydb.New(tx).PruneOutbox(ctx, now.Add(-KeepFor).UnixMilli())
	}); err != nil {
		s.opts.Logger.Error("notify: prune outbox", slog.Any("error", err))
	}
}

// send renders ev in the configured format and posts it. It returns the
// HTTP status (0 if there was none) and, on failure, a fixed sentence that
// never contains the URL.
func (s *Service) send(ctx context.Context, ev Event) (int, string) {
	target, err := s.opts.Secrets.Open(ctx, settings.NotifyWebhookURL)
	if errors.Is(err, secrets.ErrNotSet) {
		return 0, "No webhook URL is set."
	}
	if err != nil {
		s.opts.Logger.Error("notify: open webhook URL", slog.Any("error", err))
		return 0, "The webhook URL could not be read."
	}
	body, err := Render(settings.Get(s.opts.Settings, settings.NotifyFormat), ev)
	if err != nil {
		return 0, "The event could not be encoded."
	}
	return post(ctx, s.client, target, body)
}

// Delivery is one queued notification and its outcome.
type Delivery struct {
	ID            string
	Event         string
	Status        string // pending, delivered, gave_up
	Attempts      int64
	CreatedAt     time.Time
	NextAttemptAt time.Time
	FinishedAt    time.Time // zero while pending
	LastStatus    int
	LastError     string
}

// Recent returns the newest limit notifications.
func (s *Service) Recent(ctx context.Context, limit int) ([]Delivery, error) {
	rows, err := notifydb.New(s.opts.Store.Reader()).RecentOutbox(ctx, int64(limit))
	if err != nil {
		return nil, fmt.Errorf("notify: list outbox: %w", err)
	}
	out := make([]Delivery, len(rows))
	for i, r := range rows {
		d := Delivery{
			ID: r.ID, Event: r.Event, Status: r.Status, Attempts: r.Attempts,
			CreatedAt: time.UnixMilli(r.CreatedAt).UTC(), NextAttemptAt: time.UnixMilli(r.NextAttemptAt).UTC(),
			LastStatus: int(r.LastStatus), LastError: r.LastError,
		}
		if r.FinishedAt.Valid {
			d.FinishedAt = time.UnixMilli(r.FinishedAt.Int64).UTC()
		}
		out[i] = d
	}
	return out, nil
}

// TestResult is the outcome of SendTest.
type TestResult struct {
	OK         bool
	StatusCode int
	Message    string
}

// SendTest posts a test notification now, in the configured format, to the
// saved webhook URL, and records notification.test_sent with the outcome.
// It ignores whether notifications are enabled. Authorize before calling.
func (s *Service) SendTest(ctx context.Context) (TestResult, error) {
	ev := Event{
		Type: "test", Text: "Test notification from " + settings.Get(s.opts.Settings, settings.InstanceName),
		URL: settings.Get(s.opts.Settings, settings.InstancePublicURL), Fields: []Field{},
		OccurredAt: s.opts.Clock.Now(),
	}
	status, failure := s.send(ctx, ev)
	if failure == "No webhook URL is set." {
		return TestResult{}, ErrNoWebhook
	}
	res := TestResult{OK: failure == "", StatusCode: status, Message: failure}
	if res.OK {
		res.Message = fmt.Sprintf("Delivered: the receiver answered %d.", status)
	}
	err := s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		return s.opts.Audit.Record(ctx, tx, audit.Event{
			Action: audit.NotificationTestSent, SubjectType: "notification",
			Payload: map[string]any{"ok": res.OK, "status": status, "reason": failure},
		})
	})
	return res, err
}
