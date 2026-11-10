package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/platform/clock"
	"github.com/alvaroibarguen/dbairlock/internal/store"
)

// Transactor runs fn in a store write transaction.
type Transactor interface {
	Tx(ctx context.Context, fn func(tx *store.Tx) error) error
}

// Run is one verification started from the dashboard.
type Run struct {
	StartedAt  time.Time
	FinishedAt time.Time // zero while running
	Running    bool
	Checked    int64   // rows checked so far
	Result     *Result // set when the check ran to the end
	Err        string  // set when the check could not run
}

// Checker runs the chain verification in the background, one run at a
// time, reports progress, and records each outcome as audit.verified.
type Checker struct {
	verifier *Verifier
	reader   *Reader
	store    Transactor
	rec      *Recorder
	clock    clock.Clock
	log      *slog.Logger

	mu   sync.Mutex
	run  *Run          // the current or most recent run
	done chan struct{} // closed when run finishes
}

// NewChecker returns a Checker. reads is the store's read-only handle.
func NewChecker(reads store.DBTX, st Transactor, rec *Recorder, c clock.Clock, log *slog.Logger) *Checker {
	return &Checker{
		verifier: NewVerifier(reads), reader: NewReader(reads),
		store: st, rec: rec, clock: c, log: log,
	}
}

// Start begins a verification as the actor in ctx, or joins the one
// already running. It returns a channel closed when that run finishes.
func (c *Checker) Start(ctx context.Context) <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.run != nil && c.run.Running {
		return c.done
	}
	c.run = &Run{StartedAt: c.clock.Now(), Running: true}
	c.done = make(chan struct{})
	run, done := c.run, c.done
	// The request that started it may end first; the actor stays in ctx.
	bg := context.WithoutCancel(ctx)
	go c.verify(bg, run, done)
	return done
}

// Status returns the current or most recent run; ok is false if none ran
// since the process started.
func (c *Checker) Status() (Run, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.run == nil {
		return Run{}, false
	}
	r := *c.run
	return r, true
}

func (c *Checker) verify(ctx context.Context, run *Run, done chan struct{}) {
	defer close(done)
	res, err := c.verifier.VerifyProgress(ctx, func(n int64) {
		c.mu.Lock()
		run.Checked = n
		c.mu.Unlock()
	})
	payload := map[string]any{"ok": err == nil && res.OK(), "checked": res.Checked, "last_seq": res.LastSeq}
	if err != nil {
		payload["error"] = "the check could not run"
	} else if res.Problem != nil {
		payload["problem_seq"] = res.Problem.Seq
		payload["problem"] = res.Problem.Reason
	}
	if rerr := c.store.Tx(ctx, func(tx *store.Tx) error {
		return c.rec.Record(ctx, tx, Event{Action: AuditVerified, Payload: payload})
	}); rerr != nil {
		c.log.Error("audit: record verification", slog.Any("error", rerr))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	run.Running = false
	run.FinishedAt = c.clock.Now()
	run.Checked = res.Checked
	if err != nil {
		c.log.Error("audit: verification could not run", slog.Any("error", err))
		run.Err = "The check could not run. See the server log."
		return
	}
	run.Result = &res
}

// Verification is a recorded audit.verified event.
type Verification struct {
	At      time.Time
	By      string
	Checked int64
}

// lastSuccessScan bounds how many audit.verified events LastSuccess reads.
const lastSuccessScan = 500

// LastSuccess returns the newest verification that found the chain intact;
// ok is false if there is none among the recent runs.
func (c *Checker) LastSuccess(ctx context.Context) (Verification, bool, error) {
	page, err := c.reader.List(ctx, Filter{ActionPrefix: string(AuditVerified), Limit: lastSuccessScan})
	if err != nil {
		return Verification{}, false, err
	}
	for _, e := range page.Entries {
		if e.Action != AuditVerified {
			continue
		}
		var p struct {
			OK      bool  `json:"ok"`
			Checked int64 `json:"checked"`
		}
		if json.Unmarshal(e.Payload, &p) == nil && p.OK {
			return Verification{At: e.OccurredAt, By: e.Actor.Label(), Checked: p.Checked}, true, nil
		}
	}
	return Verification{}, false, nil
}
