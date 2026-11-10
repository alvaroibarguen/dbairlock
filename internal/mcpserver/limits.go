package mcpserver

import (
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/platform/clock"
	"github.com/alvaroibarguen/dbairlock/internal/platform/ratelimit"
	"github.com/alvaroibarguen/dbairlock/internal/settings"
)

// limitKind selects the per-tool limit a call counts against, on top of
// the limit on every call.
type limitKind int

const (
	limitNone limitKind = iota
	limitPreview
	limitSubmit
)

// limits are the per-token rate limits (phase 8 step 12). Each reads its
// rate from a setting on every call, so a change in Settings applies at
// once (and starts every token afresh).
type limits struct {
	calls, previews, submissions *settingLimiter
}

func newLimits(c clock.Clock, src settings.Source) *limits {
	return &limits{
		calls:       &settingLimiter{clock: c, src: src, key: settings.MCPCallsPerMinute, window: time.Minute, unit: "minute"},
		previews:    &settingLimiter{clock: c, src: src, key: settings.MCPPreviewsPerHour, window: time.Hour, unit: "hour"},
		submissions: &settingLimiter{clock: c, src: src, key: settings.MCPSubmissionsPerHour, window: time.Hour, unit: "hour"},
	}
}

// allow consumes one call of tool by token, or returns an error that says
// which limit was reached and when to try again.
func (l *limits) allow(token, tool string, kind limitKind) error {
	if err := l.calls.allow(token, "tool calls"); err != nil {
		return err
	}
	switch kind {
	case limitPreview:
		return l.previews.allow(token, tool+" calls")
	case limitSubmit:
		return l.submissions.allow(token, tool+" calls")
	default:
		return nil
	}
}

// settingLimiter is a token bucket per API token whose size is a setting:
// n calls at once, refilled at n per window.
type settingLimiter struct {
	clock  clock.Clock
	src    settings.Source
	key    settings.Key[int]
	window time.Duration
	unit   string

	mu  sync.Mutex
	n   int
	lim *ratelimit.Limiter
}

func (l *settingLimiter) allow(token, what string) error {
	n := max(settings.Get(l.src, l.key), 1)
	l.mu.Lock()
	if l.lim == nil || l.n != n {
		l.lim, l.n = ratelimit.New(l.clock, l.window/time.Duration(n), n), n
	}
	lim := l.lim
	l.mu.Unlock()
	ok, wait := lim.Allow(token)
	if ok {
		return nil
	}
	return &rateLimitError{msg: fmt.Sprintf(
		"Rate limit reached: each API token may make %d %s per %s. Try again in %s. Do not retry in a loop; tell the user if this keeps happening.",
		n, what, l.unit, humanWait(wait))}
}

type rateLimitError struct{ msg string }

func (e *rateLimitError) Error() string { return e.msg }

func humanWait(d time.Duration) string {
	if d < time.Minute {
		s := int(math.Ceil(d.Seconds()))
		if s <= 1 {
			return "1 second"
		}
		return fmt.Sprintf("%d seconds", s)
	}
	m := int(math.Ceil(d.Minutes()))
	if m == 1 {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", m)
}
