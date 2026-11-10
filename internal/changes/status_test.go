package changes

import (
	"errors"
	"testing"
)

// TestTransitionTable covers every (status, event) pair: the transitions of
// PLAN.md section 6, and every other pair refused.
func TestTransitionTable(t *testing.T) {
	allowed := map[Status]map[Event]Status{
		StatusDraft: {
			EventSubmit: StatusPendingReview,
			EventCancel: StatusCancelled,
		},
		StatusPendingReview: {
			EventApprove: StatusApproved,
			EventReject:  StatusRejected,
			EventCancel:  StatusCancelled,
			EventExpire:  StatusExpired,
		},
		StatusApproved: {
			EventClaim:        StatusExecuting,
			EventCancel:       StatusCancelled,
			EventExpire:       StatusExpired,
			EventApprovalLost: StatusPendingReview,
		},
		StatusExecuting: {
			EventSucceed: StatusExecuted,
			EventFail:    StatusFailed,
			EventDefer:   StatusApproved,
		},
		// Terminal statuses accept no event.
		StatusExecuted:  {},
		StatusFailed:    {},
		StatusRejected:  {},
		StatusExpired:   {},
		StatusCancelled: {},
	}
	if len(allowed) != len(Statuses()) {
		t.Fatalf("the table covers %d statuses, there are %d", len(allowed), len(Statuses()))
	}
	pairs := 0
	for _, from := range Statuses() {
		for _, ev := range Events() {
			pairs++
			to, err := transition(from, ev)
			want, ok := allowed[from][ev]
			switch {
			case ok && err != nil:
				t.Errorf("transition(%s, %s) = %v, want %s", from, ev, err, want)
			case ok && to != want:
				t.Errorf("transition(%s, %s) = %s, want %s", from, ev, to, want)
			case !ok && err == nil:
				t.Errorf("transition(%s, %s) = %s, want refused", from, ev, to)
			case !ok:
				var te *TransitionError
				if !errors.As(err, &te) || !errors.Is(err, ErrInvalidState) || to != from {
					t.Errorf("transition(%s, %s) = %s, %v; want %s and a TransitionError", from, ev, to, err, from)
				}
			}
		}
	}
	if pairs != 9*10 {
		t.Errorf("checked %d pairs, want 90", pairs)
	}
	// Unknown statuses and events are refused too.
	if _, err := transition("weird", EventSubmit); err == nil {
		t.Error("unknown status accepted")
	}
	if _, err := transition(StatusDraft, "jump"); err == nil {
		t.Error("unknown event accepted")
	}
}

func TestOpen(t *testing.T) {
	for _, s := range Statuses() {
		want := s == StatusDraft || s == StatusPendingReview || s == StatusApproved
		if s.Open() != want {
			t.Errorf("%s.Open() = %v", s, s.Open())
		}
	}
}

func TestHashes(t *testing.T) {
	a := SQLHash([]string{"UPDATE t SET a = 1 WHERE id = 1", "DELETE FROM t WHERE id = 2"})
	if len(a) != 64 {
		t.Fatalf("hash %q", a)
	}
	// Order, boundaries and count all matter.
	for _, other := range [][]string{
		{"DELETE FROM t WHERE id = 2", "UPDATE t SET a = 1 WHERE id = 1"},
		{"UPDATE t SET a = 1 WHERE id = 1DELETE FROM t WHERE id = 2"},
		{"UPDATE t SET a = 1 WHERE id = 1", "DELETE FROM t WHERE id = 2", ""},
		{"UPDATE t SET a = 2 WHERE id = 1", "DELETE FROM t WHERE id = 2"},
	} {
		if SQLHash(other) == a {
			t.Errorf("SQLHash(%q) collides", other)
		}
	}
	if SQLHash([]string{"UPDATE t SET a = 1 WHERE id = 1", "DELETE FROM t WHERE id = 2"}) != a {
		t.Error("SQLHash is not deterministic")
	}
	p := previewHash([]expectation{{Rows: 1, Fingerprint: "ab"}, {Rows: 2}})
	for _, other := range [][]expectation{
		{{Rows: 1, Fingerprint: "ab"}, {Rows: 3}},
		{{Rows: 1, Fingerprint: "ac"}, {Rows: 2}},
		{{Rows: 2}, {Rows: 1, Fingerprint: "ab"}},
		{{Rows: 1, Fingerprint: "ab"}},
	} {
		if previewHash(other) == p {
			t.Errorf("previewHash(%v) collides", other)
		}
	}
}

func TestBackoff(t *testing.T) {
	want := []string{"30s", "1m0s", "2m0s", "5m0s", "5m0s", "5m0s"}
	for i, w := range want {
		if got := Backoff(i + 1).String(); got != w {
			t.Errorf("Backoff(%d) = %s, want %s", i+1, got, w)
		}
	}
}
