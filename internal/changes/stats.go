package changes

import (
	"context"
	"fmt"

	"github.com/alvaroibarguen/dbairlock/internal/changes/changesdb"
)

// Count returns how many changes exist, in every status, for the System
// page. Authorize before calling.
func (s *Service) Count(ctx context.Context) (int64, error) {
	n, err := changesdb.New(s.opts.Store.Reader()).CountChanges(ctx)
	if err != nil {
		return 0, fmt.Errorf("changes: count: %w", err)
	}
	return n, nil
}
