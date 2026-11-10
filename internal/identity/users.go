package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/identity/identitydb"
	"github.com/alvaroibarguen/dbairlock/internal/store"
)

// User is a person who has signed in at least once.
type User struct {
	ID         string
	Email      string
	Name       string
	PictureURL string
	// Role is for display only. Authorization goes through Can; a lint rule
	// forbids reading this field outside identity (and the one renderer).
	Role         Role
	CreatedAt    time.Time
	LastSignInAt *time.Time
	// RemovedAt is set while the user is removed (stored as disabled_at).
	RemovedAt *time.Time
	RemovedBy string
}

// Removed reports whether the user has been removed.
func (u User) Removed() bool { return u.RemovedAt != nil }

func optTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.UnixMilli(v.Int64).UTC()
	return &t
}

func userFromRow(r identitydb.User) User {
	return User{
		ID: r.ID, Email: r.Email, Name: r.Name, PictureURL: r.PictureUrl, Role: Role(r.Role),
		CreatedAt: time.UnixMilli(r.CreatedAt).UTC(), LastSignInAt: optTime(r.LastSignInAt),
		RemovedAt: optTime(r.DisabledAt), RemovedBy: r.DisabledBy,
	}
}

func principalOf(u identitydb.User, via audit.Via, tokenID string) Principal {
	return Principal{UserID: u.ID, Email: u.Email, Name: u.Name, Via: via, APITokenID: tokenID, role: Role(u.Role)}
}

// Me returns the principal's own user record.
func (s *Service) Me(ctx context.Context, p Principal) (User, error) {
	if p.UserID == "" {
		return User{}, ErrUnauthenticated
	}
	row, err := identitydb.New(s.opts.Store.Reader()).GetUser(ctx, p.UserID)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrUnauthenticated
	}
	if err != nil {
		return User{}, fmt.Errorf("identity: load user: %w", err)
	}
	return userFromRow(row), nil
}

// ListUsers returns every user, including removed ones. Requires user.manage.
func (s *Service) ListUsers(ctx context.Context, p Principal) ([]User, error) {
	if err := Can(p, ActionUserManage, Instance); err != nil {
		return nil, err
	}
	rows, err := identitydb.New(s.opts.Store.Reader()).ListUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("identity: list users: %w", err)
	}
	out := make([]User, len(rows))
	for i, r := range rows {
		out[i] = userFromRow(r)
	}
	return out, nil
}

// mutateUser loads the user inside a transaction, runs fn, and returns the
// user as it is afterwards. Last-admin invariants are checked inside the
// same transaction; the store's single writer serializes concurrent calls.
func (s *Service) mutateUser(ctx context.Context, p Principal, id string, fn func(q *identitydb.Queries, tx *store.Tx, u identitydb.User) error) (User, error) {
	if err := Can(p, ActionUserManage, Instance); err != nil {
		return User{}, err
	}
	var out identitydb.User
	err := s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		q := identitydb.New(tx)
		// The principal was loaded before this transaction; a concurrent
		// removal or demotion of the actor must win, so check again.
		actor, err := q.GetUser(ctx, p.UserID)
		if err != nil || actor.DisabledAt.Valid {
			return ErrUnauthenticated
		}
		if err := Can(principalOf(actor, p.Via, p.APITokenID), ActionUserManage, Instance); err != nil {
			return err
		}
		u, err := q.GetUser(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("identity: load user: %w", err)
		}
		if err := fn(q, tx, u); err != nil {
			return err
		}
		out, err = q.GetUser(ctx, id)
		return err
	})
	if err != nil {
		return User{}, err
	}
	return userFromRow(out), nil
}

// isEnabledAdmin reports whether u counts towards the last-admin invariant.
func isEnabledAdmin(u identitydb.User) bool {
	return Role(u.Role) == RoleAdmin && !u.DisabledAt.Valid
}

func (s *Service) checkNotLastAdmin(ctx context.Context, q *identitydb.Queries, u identitydb.User) error {
	if !isEnabledAdmin(u) {
		return nil
	}
	n, err := q.CountEnabledAdmins(ctx)
	if err != nil {
		return fmt.Errorf("identity: count admins: %w", err)
	}
	if n <= 1 {
		return ErrLastAdmin
	}
	return nil
}

// SetRole changes a user's role. The last enabled admin cannot be demoted.
func (s *Service) SetRole(ctx context.Context, p Principal, id string, role Role) (User, error) {
	if !role.valid() {
		return User{}, &InvalidError{Message: "role must be admin or member"}
	}
	return s.mutateUser(ctx, p, id, func(q *identitydb.Queries, tx *store.Tx, u identitydb.User) error {
		if Role(u.Role) == role {
			return nil
		}
		if role != RoleAdmin {
			if err := s.checkNotLastAdmin(ctx, q, u); err != nil {
				return err
			}
		}
		if err := q.SetRole(ctx, identitydb.SetRoleParams{Role: string(role), ID: u.ID}); err != nil {
			return fmt.Errorf("identity: set role: %w", err)
		}
		return s.opts.Audit.Record(ctx, tx, audit.Event{
			Action: audit.UserRoleChanged, SubjectType: "user", SubjectID: u.ID,
			Payload: map[string]any{"email": u.Email, "old": u.Role, "new": string(role)},
		})
	})
}

// RemoveUser offboards a user. In one transaction it marks them removed,
// deletes their sessions, revokes their API tokens and OAuth grants
// (connected apps) permanently, publishes
// UserRemoved (so their open changes are cancelled) and records
// user.removed. The record is kept. The last enabled admin cannot be removed.
func (s *Service) RemoveUser(ctx context.Context, p Principal, id string) (User, error) {
	return s.mutateUser(ctx, p, id, func(q *identitydb.Queries, tx *store.Tx, u identitydb.User) error {
		if u.DisabledAt.Valid {
			return nil // already removed
		}
		if err := s.checkNotLastAdmin(ctx, q, u); err != nil {
			return err
		}
		now := ms(s.now())
		if err := q.DisableUser(ctx, identitydb.DisableUserParams{
			DisabledAt: sql.NullInt64{Int64: now, Valid: true}, DisabledBy: p.Email, ID: u.ID,
		}); err != nil {
			return fmt.Errorf("identity: remove user: %w", err)
		}
		sessions, err := q.DeleteUserSessions(ctx, u.ID)
		if err != nil {
			return fmt.Errorf("identity: end sessions: %w", err)
		}
		tokens, err := q.RevokeUserTokens(ctx, identitydb.RevokeUserTokensParams{
			RevokedAt: sql.NullInt64{Int64: now, Valid: true}, UserID: u.ID,
		})
		if err != nil {
			return fmt.Errorf("identity: revoke tokens: %w", err)
		}
		grants, err := s.revokeUserGrants(ctx, tx, q, u.ID, s.now())
		if err != nil {
			return err
		}
		s.subMu.Lock()
		subs := append([]RemovalSubscriber(nil), s.subscribers...)
		s.subMu.Unlock()
		for _, fn := range subs {
			if err := fn(ctx, tx, UserRemoved{UserID: u.ID, RemovedBy: p.UserID}); err != nil {
				return fmt.Errorf("identity: user removal: %w", err)
			}
		}
		return s.opts.Audit.Record(ctx, tx, audit.Event{
			Action: audit.UserRemoved, SubjectType: "user", SubjectID: u.ID,
			Payload: map[string]any{
				"email": u.Email, "sessions_ended": sessions, "tokens_revoked": tokens, "oauth_grants_revoked": grants,
			},
		})
	})
}

// RestoreUser lets a removed user sign in again, as a member. Tokens revoked
// by the removal stay revoked.
func (s *Service) RestoreUser(ctx context.Context, p Principal, id string) (User, error) {
	return s.mutateUser(ctx, p, id, func(q *identitydb.Queries, tx *store.Tx, u identitydb.User) error {
		if !u.DisabledAt.Valid {
			return nil
		}
		if err := q.EnableUser(ctx, u.ID); err != nil {
			return fmt.Errorf("identity: restore user: %w", err)
		}
		if err := q.SetRole(ctx, identitydb.SetRoleParams{Role: string(RoleMember), ID: u.ID}); err != nil {
			return fmt.Errorf("identity: restore user: %w", err)
		}
		return s.opts.Audit.Record(ctx, tx, audit.Event{
			Action: audit.UserRestored, SubjectType: "user", SubjectID: u.ID,
			Payload: map[string]any{"email": u.Email},
		})
	})
}
