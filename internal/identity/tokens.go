package identity

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
	"unicode"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/identity/identitydb"
	"github.com/alvaroibarguen/dbairlock/internal/store"
)

const (
	// TokenPrefix starts every API token, so secret scanners can find leaked
	// ones.
	TokenPrefix = "dbal_pat_" //nolint:gosec // a public prefix, not a credential
	// tokenBodyLen is 32 random bytes in base62 (ceil(256 / log2 62)).
	tokenBodyLen = 43
	// displayChars of the random part are shown in lists, never used to look
	// a token up.
	displayChars = 4
	maxTokenName = 64
	// tokenTouchEvery limits last_used_at writes to one a minute per token.
	tokenTouchEvery = time.Minute
)

// TokenExpiries are the lifetimes a token can be created with, in days; 0
// means it does not expire.
var TokenExpiries = []int{0, 30, 90, 365}

// Token describes an API token. It never contains the token itself.
type Token struct {
	ID         string
	UserID     string
	OwnerEmail string
	Name       string
	Prefix     string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
}

// CreatedToken is a token just created. Secret is the full token: it is in
// the creation response only and cannot be retrieved again.
type CreatedToken struct {
	Token
	Secret string
}

func newTokenSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	body := new(big.Int).SetBytes(b).Text(62)
	return TokenPrefix + strings.Repeat("0", tokenBodyLen-len(body)) + body
}

// CreateToken creates an API token owned by the principal.
func (s *Service) CreateToken(ctx context.Context, p Principal, name string, expiresInDays int) (CreatedToken, error) {
	if p.UserID == "" || p.APITokenID != "" {
		// A token cannot mint further tokens.
		return CreatedToken{}, ErrUnauthenticated
	}
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > maxTokenName || strings.ContainsFunc(name, unicode.IsControl) {
		return CreatedToken{}, &InvalidError{Message: fmt.Sprintf("Name must be 1 to %d characters.", maxTokenName)}
	}
	valid := false
	for _, d := range TokenExpiries {
		valid = valid || d == expiresInDays
	}
	if !valid {
		return CreatedToken{}, &InvalidError{Message: "Expiry must be 30, 90 or 365 days, or none."}
	}
	secret := newTokenSecret()
	now := s.now()
	t := Token{
		ID: s.opts.IDs.NewID(), UserID: p.UserID, OwnerEmail: p.Email, Name: name,
		Prefix: secret[:len(TokenPrefix)+displayChars], CreatedAt: now.UTC(),
	}
	var exp sql.NullInt64
	if expiresInDays > 0 {
		e := now.Add(time.Duration(expiresInDays) * 24 * time.Hour).UTC()
		t.ExpiresAt = &e
		exp = sql.NullInt64{Int64: ms(e), Valid: true}
	}
	err := s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		if err := identitydb.New(tx).InsertToken(ctx, identitydb.InsertTokenParams{
			ID: t.ID, UserID: t.UserID, Name: t.Name, Prefix: t.Prefix,
			TokenHash: HashToken(secret), CreatedAt: ms(now), ExpiresAt: exp,
		}); err != nil {
			return fmt.Errorf("identity: create token: %w", err)
		}
		return s.opts.Audit.Record(ctx, tx, audit.Event{
			Action: audit.APITokenCreated, SubjectType: "api_token", SubjectID: t.ID,
			Payload: map[string]any{"name": t.Name, "prefix": t.Prefix, "expires_in_days": expiresInDays},
		})
	})
	if err != nil {
		return CreatedToken{}, err
	}
	return CreatedToken{Token: t, Secret: secret}, nil
}

func tokenFromRow(id, userID, owner, name, prefix string, created int64, used, exp sql.NullInt64) Token {
	return Token{
		ID: id, UserID: userID, OwnerEmail: owner, Name: name, Prefix: prefix,
		CreatedAt: time.UnixMilli(created).UTC(), LastUsedAt: optTime(used), ExpiresAt: optTime(exp),
	}
}

// ListTokens returns the principal's active tokens, or with all every user's
// (which requires user.manage). Revoked tokens are not listed.
func (s *Service) ListTokens(ctx context.Context, p Principal, all bool) ([]Token, error) {
	if p.UserID == "" {
		return nil, ErrUnauthenticated
	}
	q := identitydb.New(s.opts.Store.Reader())
	var out []Token
	if all {
		if err := Can(p, ActionUserManage, Instance); err != nil {
			return nil, err
		}
		rows, err := q.ListAllTokens(ctx)
		if err != nil {
			return nil, fmt.Errorf("identity: list tokens: %w", err)
		}
		for _, r := range rows {
			out = append(out, tokenFromRow(r.ID, r.UserID, r.OwnerEmail, r.Name, r.Prefix, r.CreatedAt, r.LastUsedAt, r.ExpiresAt))
		}
		return out, nil
	}
	rows, err := q.ListTokensOfUser(ctx, p.UserID)
	if err != nil {
		return nil, fmt.Errorf("identity: list tokens: %w", err)
	}
	for _, r := range rows {
		out = append(out, tokenFromRow(r.ID, r.UserID, r.OwnerEmail, r.Name, r.Prefix, r.CreatedAt, r.LastUsedAt, r.ExpiresAt))
	}
	return out, nil
}

// RevokeToken revokes a token permanently. Its owner may revoke it, and so
// may anyone allowed user.manage. Another user's token reads as not found to
// anyone else.
func (s *Service) RevokeToken(ctx context.Context, p Principal, id string) error {
	if p.UserID == "" {
		return ErrUnauthenticated
	}
	return s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		q := identitydb.New(tx)
		t, err := q.GetToken(ctx, id)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && t.RevokedAt.Valid) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("identity: load token: %w", err)
		}
		if t.UserID != p.UserID && Can(p, ActionUserManage, Instance) != nil {
			return ErrNotFound
		}
		if _, err := q.RevokeToken(ctx, identitydb.RevokeTokenParams{
			RevokedAt: sql.NullInt64{Int64: ms(s.now()), Valid: true}, ID: t.ID,
		}); err != nil {
			return fmt.Errorf("identity: revoke token: %w", err)
		}
		return s.opts.Audit.Record(ctx, tx, audit.Event{
			Action: audit.APITokenRevoked, SubjectType: "api_token", SubjectID: t.ID,
			Payload: map[string]any{"name": t.Name, "prefix": t.Prefix, "owner_id": t.UserID},
		})
	})
}

// AuthenticateToken returns the principal of a bearer token: the token's
// owner, via mcp, with the token id. It fails with ErrUnauthenticated if the
// token is unknown, revoked or expired, or its owner was removed. Lookup is
// by hash of the whole token; the display prefix is never used.
func (s *Service) AuthenticateToken(ctx context.Context, raw string) (Principal, error) {
	if !strings.HasPrefix(raw, TokenPrefix) || len(raw) != len(TokenPrefix)+tokenBodyLen {
		return Principal{}, ErrUnauthenticated
	}
	hash := HashToken(raw)
	q := identitydb.New(s.opts.Store.Reader())
	t, err := q.GetTokenByHash(ctx, hash)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, fmt.Errorf("identity: load token: %w", err)
	}
	if subtle.ConstantTimeCompare(t.TokenHash, hash) != 1 || t.RevokedAt.Valid {
		return Principal{}, ErrUnauthenticated
	}
	now := s.now()
	if t.ExpiresAt.Valid && ms(now) >= t.ExpiresAt.Int64 {
		return Principal{}, ErrUnauthenticated
	}
	u, err := q.GetUser(ctx, t.UserID)
	if err != nil || u.DisabledAt.Valid {
		return Principal{}, ErrUnauthenticated
	}
	if !t.LastUsedAt.Valid || now.Sub(time.UnixMilli(t.LastUsedAt.Int64)) >= tokenTouchEvery {
		// Not audited, like session activity.
		if err := s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
			return identitydb.New(tx).TouchToken(ctx, identitydb.TouchTokenParams{
				LastUsedAt: sql.NullInt64{Int64: ms(now), Valid: true}, ID: t.ID,
			})
		}); err != nil {
			return Principal{}, fmt.Errorf("identity: record token use: %w", err)
		}
	}
	return principalOf(u, audit.ViaMCP, t.ID), nil
}
