package audit

import (
	"context"
	"database/sql"
	"errors"

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
	List(ctx context.Context, p auditdb.ListEventsParams) ([]auditdb.AuditEvent, error)
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

func (r sqlRepo) List(ctx context.Context, p auditdb.ListEventsParams) ([]auditdb.AuditEvent, error) {
	return r.q.ListEvents(ctx, p)
}

func (r sqlRepo) Between(ctx context.Context, p auditdb.EventsBetweenParams) ([]auditdb.AuditEvent, error) {
	return r.q.EventsBetween(ctx, p)
}
