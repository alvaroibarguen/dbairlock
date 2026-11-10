package changes

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
)

// The sequence state machine runs against real targets in
// internal/app/sequences_test.go; these are its pure parts.

func TestStopMessage(t *testing.T) {
	for code, want := range map[string]string{
		StopFailed:    "Stopped: step 2 failed.",
		StopRejected:  "Stopped: step 2 was rejected.",
		StopExpired:   "Stopped: step 2 expired.",
		StopCancelled: "Stopped: step 2 was cancelled.",
	} {
		if got := stopMessage(code, 1); got != want {
			t.Errorf("%s: %q, want %q", code, got, want)
		}
	}
}

// Every step event that ends a step without running it stops the sequence;
// nothing else does.
func TestStopCodes(t *testing.T) {
	want := map[audit.Action]string{
		audit.ChangeRejected: StopRejected, audit.ChangeCancelled: StopCancelled, audit.ChangeExpired: StopExpired,
		audit.ExecutionFailed: StopFailed, audit.ExecutionInterrupted: StopFailed,
	}
	if len(stopCodes) != len(want) {
		t.Fatalf("stopCodes = %v", stopCodes)
	}
	for a, code := range want {
		if stopCodes[a] != code {
			t.Errorf("%s stops with %q, want %q", a, stopCodes[a], code)
		}
	}
	for _, a := range []audit.Action{audit.ExecutionDeferred, audit.ChangeReturnedToReview, audit.ChangeEdited, audit.ExecutionSucceeded} {
		if _, ok := stopCodes[a]; ok {
			t.Errorf("%s stops a sequence", a)
		}
	}
}

func TestStepTitle(t *testing.T) {
	if got := stepTitle("Move a customer", 1, 3); got != "Move a customer · step 2 of 3" {
		t.Errorf("got %q", got)
	}
	long := strings.Repeat("é", MaxTitle)
	got := stepTitle(long, 9, 10)
	if n := utf8.RuneCountInString(got); n > MaxTitle || !strings.HasSuffix(got, "… · step 10 of 10") {
		t.Errorf("long title: %d runes, %q", n, got[len(got)-30:])
	}
}

func TestStepsHash(t *testing.T) {
	a := stepsHash([]string{"c1", "c2"}, []string{SQLHash([]string{"x"}), SQLHash([]string{"y"})})
	if a != stepsHash([]string{"c1", "c2"}, []string{SQLHash([]string{"x"}), SQLHash([]string{"y"})}) {
		t.Fatal("not deterministic")
	}
	for _, other := range []string{
		stepsHash([]string{"c2", "c1"}, []string{SQLHash([]string{"x"}), SQLHash([]string{"y"})}), // other connections order
		stepsHash([]string{"c1", "c2"}, []string{SQLHash([]string{"y"}), SQLHash([]string{"x"})}), // other step order
		stepsHash([]string{"c1"}, []string{SQLHash([]string{"x"})}),                               // fewer steps
	} {
		if other == a {
			t.Error("different steps share a hash")
		}
	}
}

func TestStepErrorWrapsTheDryRun(t *testing.T) {
	err := error(&StepError{Step: 1, Err: &DryRunError{Problem: &Issue{Code: CodeUnavailable, Message: "Could not connect."}}})
	if !errors.Is(err, ErrInvalid) || !errors.Is(err, ErrTargetUnavailable) {
		t.Errorf("errors.Is: %v", err)
	}
	if err.Error() != "changes: step 2: Could not connect." {
		t.Errorf("message = %q", err.Error())
	}
}
