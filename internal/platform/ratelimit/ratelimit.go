// Package ratelimit limits how often a key (usually a client IP) may do
// something. Limiter is a per-key token bucket; Backoff adds a growing delay
// after repeated failures, for endpoints that check a guessable secret.
// Both read time from an injected clock and keep their state in memory: a
// restart forgets it, which is acceptable for a single instance.
package ratelimit

import (
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/alvaroibarguen/dbairlock/internal/platform/clock"
)

// maxKeys bounds the memory a flood of distinct keys can take. When reached,
// idle entries are pruned; if none are idle the oldest are dropped.
const maxKeys = 10000

// Limiter allows burst events per key, refilled at one event per every.
type Limiter struct {
	clock clock.Clock
	limit rate.Limit
	burst int
	idle  time.Duration

	mu   sync.Mutex
	keys map[string]*bucket
}

type bucket struct {
	lim  *rate.Limiter
	seen time.Time
}

// New returns a Limiter that allows burst events at once per key and one
// more every interval.
func New(c clock.Clock, every time.Duration, burst int) *Limiter {
	return &Limiter{
		clock: c, limit: rate.Every(every), burst: burst,
		// A bucket idle this long is full again, so forgetting it is safe.
		idle: every * time.Duration(burst+1),
		keys: map[string]*bucket{},
	}
}

// Allow consumes one event for key. If none is available it returns false
// and how long until one is.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	now := l.clock.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.keys[key]
	if !ok {
		l.prune(now)
		b = &bucket{lim: rate.NewLimiter(l.limit, l.burst)}
		l.keys[key] = b
	}
	b.seen = now
	r := b.lim.ReserveN(now, 1)
	if !r.OK() {
		return false, l.idle
	}
	if d := r.DelayFrom(now); d > 0 {
		r.CancelAt(now)
		return false, d
	}
	return true, 0
}

func (l *Limiter) prune(now time.Time) {
	if len(l.keys) < maxKeys {
		return
	}
	for k, b := range l.keys {
		if now.Sub(b.seen) > l.idle {
			delete(l.keys, k)
		}
	}
	for k := range l.keys { // still full: drop arbitrary entries
		if len(l.keys) < maxKeys {
			break
		}
		delete(l.keys, k)
	}
}

// Backoff locks a key out for a growing time after repeated failures: the
// first free failures cost nothing, then each further failure doubles the
// lock-out, starting at base and capped at max. A success resets the key.
type Backoff struct {
	clock     clock.Clock
	free      int
	base, max time.Duration

	mu   sync.Mutex
	keys map[string]*failures
}

type failures struct {
	n     int
	until time.Time
	seen  time.Time
}

// NewBackoff returns a Backoff.
func NewBackoff(c clock.Clock, free int, base, maxDelay time.Duration) *Backoff {
	return &Backoff{clock: c, free: free, base: base, max: maxDelay, keys: map[string]*failures{}}
}

// Wait reports how long key is still locked out (0 if it is not).
func (b *Backoff) Wait(key string) time.Duration {
	now := b.clock.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	f, ok := b.keys[key]
	if !ok || !now.Before(f.until) {
		return 0
	}
	return f.until.Sub(now)
}

// Fail records a failure for key and returns the lock-out it now has.
func (b *Backoff) Fail(key string) time.Duration {
	now := b.clock.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	f, ok := b.keys[key]
	if !ok {
		b.prune(now)
		f = &failures{}
		b.keys[key] = f
	}
	f.n++
	f.seen = now
	if f.n <= b.free {
		return 0
	}
	d := b.base
	for i := b.free + 1; i < f.n && d < b.max; i++ {
		d *= 2
	}
	d = min(d, b.max)
	f.until = now.Add(d)
	return d
}

// Reset forgets key's failures.
func (b *Backoff) Reset(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.keys, key)
}

func (b *Backoff) prune(now time.Time) {
	if len(b.keys) < maxKeys {
		return
	}
	for k, f := range b.keys {
		if now.After(f.until) && now.Sub(f.seen) > b.max {
			delete(b.keys, k)
		}
	}
	for k := range b.keys {
		if len(b.keys) < maxKeys {
			break
		}
		delete(b.keys, k)
	}
}
