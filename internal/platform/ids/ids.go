// Package ids generates the identifiers used for every stored entity.
package ids

import (
	"fmt"
	"sync"

	"github.com/google/uuid"
)

// Generator produces new unique identifiers.
type Generator interface {
	NewID() string
}

// UUIDv7 generates time-ordered UUIDv7 identifiers.
type UUIDv7 struct{}

// NewID returns a new UUIDv7 in its canonical string form.
func (UUIDv7) NewID() string {
	// NewV7 only fails if the system random source fails, and since Go 1.24
	// crypto/rand never returns an error (it crashes the program instead).
	return uuid.Must(uuid.NewV7()).String()
}

// Sequential is a deterministic Generator for tests. It returns
// prefix-000001, prefix-000002, and so on.
type Sequential struct {
	Prefix string

	mu sync.Mutex
	n  int
}

// NewID returns the next id in the sequence.
func (s *Sequential) NewID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return fmt.Sprintf("%s-%06d", s.Prefix, s.n)
}
