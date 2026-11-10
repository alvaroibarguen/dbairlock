package audit

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"

	"github.com/alvaroibarguen/dbairlock/internal/audit/auditdb"
	"github.com/alvaroibarguen/dbairlock/internal/store"
)

// Repository is the audit module's storage contract. The SQLite
// implementation wraps the sqlc package auditdb; nothing else in the module
// issues SQL.
type Repository interface {
	// Last returns the seq and hash of the newest row; ok is false when the
	// table is empty.
	Last(ctx context.Context) (seq int64, hash []byte, ok bool, err error)
	// SequenceHigh returns the highest seq ever assigned, including rows
	// that no longer exist.
	SequenceHigh(ctx context.Context) (int64, error)
	Insert(ctx context.Context, row auditdb.InsertEventParams) error
	// After returns up to limit rows with seq > after, in seq order.
	After(ctx context.Context, after, limit int64) ([]auditdb.AuditEvent, error)
	// List returns up to p.Limit rows matching p, newest first.
	List(ctx context.Context, p ListParams) ([]auditdb.AuditEvent, error)
	// Between returns rows after a seq within a time range, in seq order.
	Between(ctx context.Context, p auditdb.EventsBetweenParams) ([]auditdb.AuditEvent, error)
}

// NewRepository returns the SQLite Repository over db (a *store.Tx or the
// store's reader).
func NewRepository(db store.DBTX) Repository {
	return sqlRepo{db: db, q: auditdb.New(db)}
}

type sqlRepo struct {
	db store.DBTX
	q  *auditdb.Queries
}

func (r sqlRepo) Last(ctx context.Context) (int64, []byte, bool, error) {
	row, err := r.q.LastEvent(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, false, nil
	}
	if err != nil {
		return 0, nil, false, err
	}
	return row.Seq, row.Hash, true, nil
}

// sequenceHighSQL is hand-written because sqlc does not model the internal
// sqlite_sequence table that AUTOINCREMENT maintains.
const sequenceHighSQL = `SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'audit_events'), 0)`

func (r sqlRepo) SequenceHigh(ctx context.Context) (int64, error) {
	var high int64
	err := r.db.QueryRowContext(ctx, sequenceHighSQL).Scan(&high)
	return high, err
}

func (r sqlRepo) Insert(ctx context.Context, row auditdb.InsertEventParams) error {
	return r.q.InsertEvent(ctx, row)
}

func (r sqlRepo) After(ctx context.Context, after, limit int64) ([]auditdb.AuditEvent, error) {
	return r.q.EventsAfter(ctx, auditdb.EventsAfterParams{Seq: after, Limit: limit})
}

// ListParams filter one page of List. Empty strings and zero times mean
// no filter.
type ListParams struct {
	BeforeSeq    int64 // only seq < BeforeSeq; <= 0 means no bound
	Actor        string
	Via          string
	ActionPrefix string
	SubjectType  string
	SubjectID    string
	FromMs       int64 // occurred_at >= FromMs; <= 0 means no bound
	ToMs         int64 // occurred_at < ToMs; math.MaxInt64 means no bound
	Limit        int64
}

const listColumns = `seq, id, occurred_at, actor_user_id, actor_email, via, api_token_id, ip,
	action, subject_type, subject_id, payload, prev_hash, hash`

// List is hand-written rather than an sqlc query: a static query with
// "(param is empty OR col = param)" for every optional filter hides them from
// SQLite's planner, which then scanned the whole log (or sorted it by
// occurred_at) for a selective filter such as one change's timeline. Only
// the filters in use are added here, so each can use its index. Every
// value is a bound parameter.
func (r sqlRepo) List(ctx context.Context, p ListParams) ([]auditdb.AuditEvent, error) {
	var (
		where []string
		args  []any
	)
	add := func(cond string, a ...any) {
		where = append(where, cond)
		args = append(args, a...)
	}
	if p.BeforeSeq > 0 {
		add("seq < ?", p.BeforeSeq)
	}
	if p.Actor != "" {
		add("(actor_user_id = ? OR actor_email = ?)", p.Actor, p.Actor)
	}
	if p.Via != "" {
		add("via = ?", p.Via)
	}
	if p.ActionPrefix != "" {
		// A prefix as a range, so the action index applies. Text compares
		// byte-wise (BINARY collation), exactly like the prefix test.
		if hi, ok := prefixUpperBound(p.ActionPrefix); ok {
			add("action >= ? AND action < ?", p.ActionPrefix, hi)
		} else {
			add("action >= ?", p.ActionPrefix)
		}
	}
	if p.SubjectType != "" {
		add("subject_type = ?", p.SubjectType)
	}
	if p.SubjectID != "" {
		add("subject_id = ?", p.SubjectID)
	}
	if p.FromMs > 0 {
		add("occurred_at >= ?", p.FromMs)
	}
	if p.ToMs < math.MaxInt64 {
		add("occurred_at < ?", p.ToMs)
	}
	q := "SELECT " + listColumns + " FROM audit_events"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY seq DESC LIMIT ?"
	args = append(args, p.Limit)

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []auditdb.AuditEvent{}
	for rows.Next() {
		var e auditdb.AuditEvent
		if err := rows.Scan(&e.Seq, &e.ID, &e.OccurredAt, &e.ActorUserID, &e.ActorEmail, &e.Via, &e.ApiTokenID,
			&e.Ip, &e.Action, &e.SubjectType, &e.SubjectID, &e.Payload, &e.PrevHash, &e.Hash); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// prefixUpperBound returns the smallest string greater than every string
// that starts with prefix, byte-wise; ok is false when there is none (the
// prefix is all 0xff bytes).
func prefixUpperBound(prefix string) (string, bool) {
	b := []byte(prefix)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] < 0xff {
			b[i]++
			return string(b[:i+1]), true
		}
	}
	return "", false
}

func (r sqlRepo) Between(ctx context.Context, p auditdb.EventsBetweenParams) ([]auditdb.AuditEvent, error) {
	return r.q.EventsBetween(ctx, p)
}
