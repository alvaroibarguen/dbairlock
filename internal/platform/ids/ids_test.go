package ids

import (
	"testing"

	"github.com/google/uuid"
)

func TestUUIDv7(t *testing.T) {
	var g UUIDv7
	a, b := g.NewID(), g.NewID()
	if a == b {
		t.Fatal("ids are not unique")
	}
	u, err := uuid.Parse(a)
	if err != nil {
		t.Fatalf("parse %q: %v", a, err)
	}
	if u.Version() != 7 {
		t.Fatalf("version = %d, want 7", u.Version())
	}
	if a >= b {
		t.Fatalf("ids not time ordered: %s >= %s", a, b)
	}
}

func TestSequential(t *testing.T) {
	s := &Sequential{Prefix: "x"}
	if got := s.NewID(); got != "x-000001" {
		t.Fatalf("got %q", got)
	}
	if got := s.NewID(); got != "x-000002" {
		t.Fatalf("got %q", got)
	}
}
