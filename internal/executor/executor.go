// Package executor is the background loop that runs approved changes
// (PLAN.md 5.6).
//
// On start it marks every change found executing as failed (interrupted,
// outcome unknown). Then, woken when a change becomes ready and every five
// seconds, it expires changes past their approval lifetime and claims
// approved changes one at a time. A claim is committed in the store before
// anything touches the target, and at most one change per target executes
// at a time. Each execution re-validates every statement with the
// connection's current policy, then runs them in one target transaction
// that compares every statement with what its dry run recorded and commits
// only if nothing drifted.
//
// A target that cannot be reached before the transaction begins is not a
// failure: nothing ran, so the change goes back to approved and is retried
// with backoff until its approval expires. Any error after the transaction
// began is final.
package executor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/changes"
	"github.com/alvaroibarguen/dbairlock/internal/connector"
	"github.com/alvaroibarguen/dbairlock/internal/platform/clock"
	"github.com/alvaroibarguen/dbairlock/internal/targets"
)

// Changes is the execution half of the changes module.
type Changes interface {
	FailInterrupted(ctx context.Context) (int, error)
	ExpireDue(ctx context.Context) (int, error)
	ClaimNext(ctx context.Context) (*changes.Claim, error)
	RecordExecuted(ctx context.Context, claim changes.Claim, results []connector.Result) error
	RecordFailed(ctx context.Context, claim changes.Claim, f changes.Failure) error
	RecordDeferred(ctx context.Context, claim changes.Claim, cause string) (time.Time, error)
}

// Targets is the targets module as the executor uses it.
type Targets interface {
	Get(ctx context.Context, id string) (targets.Connection, error)
	Connector(id string) (connector.Connector, bool)
	Limits(c targets.Connection) connector.Limits
	WithConn(ctx context.Context, id string, fn func(ctx context.Context, c targets.Connection, conn connector.Conn) error) error
}

// Options configure the executor.
type Options struct {
	Changes Changes
	Targets Targets
	Clock   clock.Clock
	Logger  *slog.Logger
	// Interval is how often the loop wakes on its own. Default 5 seconds.
	Interval time.Duration
}

// Executor runs approved changes.
type Executor struct {
	opts Options
	wake chan struct{}
	wg   sync.WaitGroup
}

// New returns an executor.
func New(opts Options) (*Executor, error) {
	if opts.Changes == nil || opts.Targets == nil || opts.Clock == nil || opts.Logger == nil {
		return nil, errors.New("executor: changes, targets, clock and logger are required")
	}
	if opts.Interval <= 0 {
		opts.Interval = 5 * time.Second
	}
	return &Executor{opts: opts, wake: make(chan struct{}, 1)}, nil
}

// Wake makes the loop look for work now. It never blocks.
func (e *Executor) Wake() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func systemCtx(ctx context.Context) context.Context {
	return audit.WithActor(ctx, audit.SystemActor)
}

// Recover marks every change found executing as failed (PLAN.md 5.6). Run
// calls it before claiming anything.
func (e *Executor) Recover(ctx context.Context) error {
	n, err := e.opts.Changes.FailInterrupted(systemCtx(ctx))
	if n > 0 {
		e.opts.Logger.Error("changes were executing when dbairlock stopped; marked failed, verify them manually",
			slog.Int("count", n))
	}
	return err
}

// Run recovers interrupted executions, then runs the loop until ctx is
// cancelled. It then stops claiming and waits for executions in flight,
// which are not cancelled (each is bounded by its own timeouts).
func (e *Executor) Run(ctx context.Context) error {
	for {
		err := e.Recover(ctx)
		if err == nil {
			break
		}
		e.opts.Logger.Error("executor: marking interrupted changes failed did not work; retrying", slog.Any("error", err))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(5 * time.Second):
		}
	}
	ticker := time.NewTicker(e.opts.Interval)
	defer ticker.Stop()
	for {
		e.cycle(ctx)
		select {
		case <-ctx.Done():
			e.wg.Wait()
			return nil
		case <-e.wake:
		case <-ticker.C:
		}
	}
}

// RunOnce expires due changes and runs every change that is ready, until
// none is left, and waits for them. It is the loop's work without the
// loop, for tests and tools.
func (e *Executor) RunOnce(ctx context.Context) {
	for {
		n := e.cycle(ctx)
		e.wg.Wait()
		if n == 0 {
			return
		}
	}
}

// cycle expires due changes and starts every change that can be claimed
// now. It returns how many it started.
func (e *Executor) cycle(ctx context.Context) int {
	sys := systemCtx(context.WithoutCancel(ctx))
	if _, err := e.opts.Changes.ExpireDue(sys); err != nil {
		e.opts.Logger.Error("executor: expiring changes failed", slog.Any("error", err))
	}
	started := 0
	for ctx.Err() == nil {
		claim, err := e.opts.Changes.ClaimNext(sys)
		if err != nil {
			e.opts.Logger.Error("executor: claiming a change failed", slog.Any("error", err))
			return started
		}
		if claim == nil {
			return started
		}
		started++
		e.wg.Add(1)
		go func(c changes.Claim) {
			defer e.wg.Done()
			e.execute(sys, ctx, c)
		}(*claim)
	}
	return started
}

// validationError means a statement no longer passes validation at
// execution. Nothing ran.
type validationError struct {
	index int
	err   error
}

func (v *validationError) Error() string {
	return fmt.Sprintf("statement %d no longer passes validation: %v", v.index+1, v.err)
}

// execute runs one claimed change and records the outcome. ctx carries the
// system actor and is never cancelled; stop is the loop's context, which
// only bounds retries of the outcome's store write.
func (e *Executor) execute(ctx, stop context.Context, claim changes.Claim) {
	log := e.opts.Logger.With(slog.Int64("change", claim.Number), slog.Int("attempt", claim.Attempt))
	timeout := 2 * time.Minute
	if c, err := e.opts.Targets.Get(ctx, claim.ConnectionID); err == nil {
		lim := e.opts.Targets.Limits(c)
		// Wait up to 30s for another run on the target, then every
		// statement's locking select and statement, then the commit.
		timeout = 30*time.Second + time.Duration(2*len(claim.SQL)+1)*lim.StatementTimeout + 15*time.Second
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var results []connector.Result
	started := false
	err := e.opts.Targets.WithConn(tctx, claim.ConnectionID, func(ctx context.Context, c targets.Connection, conn connector.Conn) error {
		cn, ok := e.opts.Targets.Connector(c.Connector)
		if !ok {
			return &validationError{index: 0, err: fmt.Errorf("connector %q is not registered", c.Connector)}
		}
		// Validate again: a parser upgrade, a policy change or a tampered
		// row must not reach the target.
		stmts := make([]connector.Statement, len(claim.SQL))
		for i, text := range claim.SQL {
			st, err := cn.Validate(c.Config, text, c.ConnectorPolicy())
			if err != nil {
				return &validationError{index: i, err: err}
			}
			stmts[i] = st
		}
		started = true
		var err error
		results, err = conn.Execute(ctx, stmts, claim.Expected, e.opts.Targets.Limits(c),
			connector.RunInfo{Label: changes.ExecutionLabel(claim.Number)})
		return err
	})

	var (
		ve    *validationError
		drift *connector.DriftError
	)
	switch {
	case err == nil:
		log.Info("change executed")
		e.record(stop, log, "executed", func() error { return e.opts.Changes.RecordExecuted(ctx, claim, results) })
	case errors.As(err, &ve):
		log.Warn("change failed validation at execution", slog.Any("error", err))
		e.record(stop, log, "failed", func() error {
			return e.opts.Changes.RecordFailed(ctx, claim, changes.Failure{
				Code:    changes.FailInvalid,
				Message: fmt.Sprintf("Statement %d no longer passes validation, so nothing was run: %v", ve.index+1, ve.err),
			})
		})
	case !started || errors.Is(err, connector.ErrUnavailable):
		// Nothing ran on the target: the outcome is certain. Retry later.
		cause := err.Error()
		log.Warn("database unreachable; the change will be retried", slog.Any("error", err))
		e.record(stop, log, "deferred", func() error {
			_, err := e.opts.Changes.RecordDeferred(ctx, claim, cause)
			return err
		})
	case errors.As(err, &drift):
		log.Warn("change drifted; nothing was changed", slog.String("kind", string(drift.Kind)))
		e.record(stop, log, "drift", func() error {
			return e.opts.Changes.RecordFailed(ctx, claim, changes.Failure{
				Code: changes.FailDrift, Message: DriftMessage(drift.Kind, drift.Expected, drift.Actual),
				Drift: &changes.Drift{Kind: drift.Kind, Statement: drift.Statement, Expected: drift.Expected, Actual: drift.Actual},
			})
		})
	case errors.Is(err, connector.ErrTimeout):
		log.Warn("change timed out", slog.Any("error", err))
		e.record(stop, log, "timeout", func() error {
			return e.opts.Changes.RecordFailed(ctx, claim, changes.Failure{
				Code: changes.FailTimeout, Message: "A statement timed out or waited too long for a row lock, so the transaction was rolled back: " + err.Error(),
			})
		})
	default:
		log.Error("change failed", slog.Any("error", err))
		e.record(stop, log, "failed", func() error {
			return e.opts.Changes.RecordFailed(ctx, claim, changes.Failure{Code: changes.FailError, Message: err.Error()})
		})
	}
}

// record writes an execution outcome to the store. The target transaction
// is never re-run: if the write fails it is retried with backoff until it
// succeeds or the loop stops (the start-up rule then covers the change).
func (e *Executor) record(stop context.Context, log *slog.Logger, outcome string, fn func() error) {
	wait := time.Second
	for {
		err := fn()
		if err == nil {
			return
		}
		log.Error("recording the outcome of a change failed; retrying the store write",
			slog.String("outcome", outcome), slog.Any("error", err))
		select {
		case <-stop.Done():
			if err := fn(); err != nil {
				log.Error("could not record the outcome of a change; it will be marked interrupted at the next start",
					slog.String("outcome", outcome), slog.Any("error", err))
			}
			return
		case <-time.After(wait):
		}
		wait = min(2*wait, 30*time.Second)
	}
}

// DriftMessage explains a drift in plain words (phase 7).
func DriftMessage(kind connector.DriftKind, expected, actual string) string {
	if kind == connector.DriftCount {
		return fmt.Sprintf("This change was approved for %s but would now affect %s, so nothing was changed.",
			rows(expected), rows(actual))
	}
	return "The rows this change would affect, or the values it would overwrite, are no longer the ones that were reviewed, so nothing was changed."
}

func rows(n string) string {
	if v, err := strconv.Atoi(n); err == nil && v == 1 {
		return "1 row"
	}
	return n + " rows"
}
