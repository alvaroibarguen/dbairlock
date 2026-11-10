package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/alvaroibarguen/dbairlock/internal/identity/identitydb"
	"github.com/alvaroibarguen/dbairlock/internal/store"
)

// Users returns the users with the given ids, removed ones included, read
// through db: a store transaction when the caller must see the state its
// own transaction is about to commit (for example whether a reviewer was
// removed), or the store's reader. Unknown ids are left out. It makes no
// authorization decision; callers use it to display authors and reviewers
// and to refuse removed ones.
func (s *Service) Users(ctx context.Context, db store.DBTX, ids []string) (map[string]User, error) {
	if db == nil {
		db = s.opts.Store.Reader()
	}
	q := identitydb.New(db)
	out := make(map[string]User, len(ids))
	for _, id := range ids {
		if _, done := out[id]; done || id == "" {
			continue
		}
		row, err := q.GetUser(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("identity: load user: %w", err)
		}
		out[id] = userFromRow(row)
	}
	return out, nil
}

// TokenName returns the name of the agent credential id, revoked or not:
// an API token's name, or for an OAuth grant the name of its client. It
// returns "" if there is no such credential.
func (s *Service) TokenName(ctx context.Context, db store.DBTX, id string) (string, error) {
	if id == "" {
		return "", nil
	}
	if db == nil {
		db = s.opts.Store.Reader()
	}
	q := identitydb.New(db)
	row, err := q.GetToken(ctx, id)
	if err == nil {
		return row.Name, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("identity: load token: %w", err)
	}
	g, err := q.GetOAuthGrant(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("identity: load grant: %w", err)
	}
	return g.ClientName, nil
}
