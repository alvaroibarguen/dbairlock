package connector

import (
	"fmt"
	"slices"
	"strings"
	"sync"
)

// Registry holds the available connectors by id. internal/app creates one
// and registers each concrete connector; connector packages never register
// themselves (no init side effects).
type Registry struct {
	mu   sync.RWMutex
	byID map[string]Connector
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{byID: map[string]Connector{}}
}

// Register adds c. It fails if c's id is empty or already registered.
func (r *Registry) Register(c Connector) error {
	id := c.Info().ID
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("connector: empty id")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.byID[id]; dup {
		return fmt.Errorf("connector: %q already registered", id)
	}
	r.byID[id] = c
	return nil
}

// Get returns the connector with the given id.
func (r *Registry) Get(id string) (Connector, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.byID[id]
	return c, ok
}

// List returns every registered connector, sorted by id.
func (r *Registry) List() []Connector {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Connector, 0, len(r.byID))
	for _, c := range r.byID {
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b Connector) int { return strings.Compare(a.Info().ID, b.Info().ID) })
	return out
}
