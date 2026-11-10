package ratelimit

import (
	"testing"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/platform/clock"
)

func TestLimiter(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	l := New(c, 12*time.Second, 5) // 5 per minute
	for i := range 5 {
		if ok, _ := l.Allow("a"); !ok {
			t.Fatalf("attempt %d refused", i+1)
		}
	}
	ok, wait := l.Allow("a")
	if ok || wait <= 0 || wait > 12*time.Second {
		t.Fatalf("6th attempt: %v %v", ok, wait)
	}
	if ok, _ := l.Allow("b"); !ok {
		t.Fatal("keys are independent")
	}
	c.Advance(12 * time.Second)
	if ok, _ := l.Allow("a"); !ok {
		t.Fatal("refilled token refused")
	}
	if ok, _ := l.Allow("a"); ok {
		t.Fatal("only one token refilled")
	}
}

func TestBackoff(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	b := NewBackoff(c, 3, time.Second, 10*time.Second)
	for range 3 {
		if d := b.Fail("ip"); d != 0 {
			t.Fatalf("free failure locked out for %v", d)
		}
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second, 10 * time.Second}
	for i, w := range want {
		if d := b.Fail("ip"); d != w {
			t.Fatalf("failure %d: %v, want %v", i+4, d, w)
		}
	}
	if d := b.Wait("ip"); d != 10*time.Second {
		t.Fatalf("wait %v", d)
	}
	c.Advance(10 * time.Second)
	if d := b.Wait("ip"); d != 0 {
		t.Fatalf("still locked: %v", d)
	}
	b.Reset("ip")
	if d := b.Fail("ip"); d != 0 {
		t.Fatal("reset did not forget failures")
	}
}
