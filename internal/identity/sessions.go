package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/identity/identitydb"
	"github.com/alvaroibarguen/dbairlock/internal/settings"
	"github.com/alvaroibarguen/dbairlock/internal/store"
)

const (
	// SessionAbsoluteMax caps a session however active it is. Not a
	// setting: the idle length (auth.session_hours) is the knob, and this
	// cap guarantees every sign-in is re-checked against Google (removed
	// accounts, changed domains) at least monthly whatever it is set to.
	SessionAbsoluteMax = 30 * 24 * time.Hour
	// touchEvery limits how often activity extends a session (one write per
	// session per minute at most).
	touchEvery   = time.Minute
	maxUserAgent = 256
)

// NewSession is a session just created. Token goes into the cookie and is
// not stored anywhere.
type NewSession struct {
	Token     string
	ExpiresAt time.Time
}

// RandomToken returns 32 random bytes, base64url without padding.
func RandomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error since Go 1.24
	return base64.RawURLEncoding.EncodeToString(b)
}

// HashToken is the SHA-256 of a token, which is all that is stored.
func HashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

func (s *Service) sessionLength() time.Duration {
	return time.Duration(settings.Get(s.opts.Settings, settings.SessionHours)) * time.Hour
}

// IssueSession creates a session for userID inside the caller's transaction.
// It is used by sign-in and by setup when the first admin signs in.
func (s *Service) IssueSession(ctx context.Context, tx *store.Tx, userID string, meta Meta) (NewSession, error) {
	token := RandomToken()
	now := s.now()
	exp := now.Add(min(s.sessionLength(), SessionAbsoluteMax))
	ua := meta.UserAgent
	if len(ua) > maxUserAgent {
		ua = ua[:maxUserAgent]
	}
	err := identitydb.New(tx).InsertSession(ctx, identitydb.InsertSessionParams{
		IDHash: HashToken(token), UserID: userID, CreatedAt: ms(now), ExpiresAt: ms(exp),
		LastSeenAt: ms(now), Ip: meta.IP, UserAgent: ua,
	})
	if err != nil {
		return NewSession{}, fmt.Errorf("identity: create session: %w", err)
	}
	return NewSession{Token: token, ExpiresAt: exp}, nil
}

// AuthenticateSession returns the principal of a session cookie value. It
// fails with ErrUnauthenticated if the session does not exist or expired, or
// its user was removed. Activity slides the expiry forward by the session
// length, up to SessionAbsoluteMax after the sign-in.
func (s *Service) AuthenticateSession(ctx context.Context, token string) (Principal, error) {
	if token == "" || len(token) > 128 {
		return Principal{}, ErrUnauthenticated
	}
	hash := HashToken(token)
	q := identitydb.New(s.opts.Store.Reader())
	sess, err := q.GetSession(ctx, hash)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, fmt.Errorf("identity: load session: %w", err)
	}
	if subtle.ConstantTimeCompare(sess.IDHash, hash) != 1 {
		return Principal{}, ErrUnauthenticated
	}
	now := s.now()
	if ms(now) >= sess.ExpiresAt {
		_ = s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
			_, err := identitydb.New(tx).DeleteSession(ctx, hash)
			return err
		})
		return Principal{}, ErrUnauthenticated
	}
	u, err := q.GetUser(ctx, sess.UserID)
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	if u.DisabledAt.Valid {
		return Principal{}, ErrUnauthenticated
	}
	if now.Sub(time.UnixMilli(sess.LastSeenAt)) >= touchEvery {
		created := time.UnixMilli(sess.CreatedAt)
		exp := now.Add(s.sessionLength())
		if limit := created.Add(SessionAbsoluteMax); exp.After(limit) {
			exp = limit
		}
		// Not audited: activity is not a state change anyone reviews, and
		// it would flood the log (as with secrets.Open).
		if err := s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
			return identitydb.New(tx).TouchSession(ctx, identitydb.TouchSessionParams{
				LastSeenAt: ms(now), ExpiresAt: max(ms(exp), sess.ExpiresAt), IDHash: hash,
			})
		}); err != nil {
			return Principal{}, fmt.Errorf("identity: extend session: %w", err)
		}
	}
	return principalOf(u, audit.ViaWeb, ""), nil
}

// SignOut ends the session with this cookie value. Signing out of a session
// that no longer exists succeeds.
func (s *Service) SignOut(ctx context.Context, p Principal, token string) error {
	return s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		n, err := identitydb.New(tx).DeleteSession(ctx, HashToken(token))
		if err != nil {
			return fmt.Errorf("identity: sign out: %w", err)
		}
		if n == 0 || p.UserID == "" {
			return nil
		}
		return s.opts.Audit.Record(ctx, tx, audit.Event{
			Action: audit.SignedOut, SubjectType: "user", SubjectID: p.UserID,
		})
	})
}

// SignOutEverywhere ends every session of the principal. API tokens keep
// working; they are revoked separately.
func (s *Service) SignOutEverywhere(ctx context.Context, p Principal) error {
	if p.UserID == "" {
		return ErrUnauthenticated
	}
	return s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		n, err := identitydb.New(tx).DeleteUserSessions(ctx, p.UserID)
		if err != nil {
			return fmt.Errorf("identity: sign out everywhere: %w", err)
		}
		return s.opts.Audit.Record(ctx, tx, audit.Event{
			Action: audit.SignedOutEverywhere, SubjectType: "user", SubjectID: p.UserID,
			Payload: map[string]any{"sessions_ended": n},
		})
	})
}
