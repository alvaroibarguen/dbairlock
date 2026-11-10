package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/audit/auditdb"
	"github.com/alvaroibarguen/dbairlock/internal/store"
)

// Page sizes for Reader.List.
const (
	DefaultPageSize = 50
	MaxPageSize     = 500
)

// Entry is one stored audit event.
type Entry struct {
	Seq         int64
	ID          string
	OccurredAt  time.Time
	Actor       Actor
	Action      Action
	SubjectType string
	SubjectID   string
	Payload     json.RawMessage
}

// Filter selects events for Reader.List. Zero values mean "any".
type Filter struct {
	ActorUserID  string
	ActionPrefix string // e.g. "change." matches change.submitted
	SubjectType  string
	SubjectID    string
	From         time.Time // inclusive
	To           time.Time // exclusive
	// BeforeSeq returns only events with seq < BeforeSeq; pass the previous
	// page's NextBeforeSeq to continue. Zero starts at the newest event.
	BeforeSeq int64
	Limit     int // default DefaultPageSize, capped at MaxPageSize
}

// Page is one page of events, newest first.
type Page struct {
	Entries []Entry
	// NextBeforeSeq continues the listing; zero when there are no more.
	NextBeforeSeq int64
}

// Reader lists audit events from the store's read-only pool.
type Reader struct {
	repo Repository
}

// NewReader returns a Reader over db (normally store.Store.Reader()).
func NewReader(db store.DBTX) *Reader {
	return &Reader{repo: NewRepository(db)}
}

// List returns one page of events matching f, ordered by seq descending.
func (r *Reader) List(ctx context.Context, f Filter) (Page, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultPageSize
	}
	limit = min(limit, MaxPageSize)
	p := auditdb.ListEventsParams{
		BeforeSeq:    f.BeforeSeq,
		ActorUserID:  f.ActorUserID,
		ActionPrefix: f.ActionPrefix,
		SubjectType:  f.SubjectType,
		SubjectID:    f.SubjectID,
		FromMs:       0,
		ToMs:         math.MaxInt64,
		PageLimit:    int64(limit) + 1,
	}
	if p.BeforeSeq <= 0 {
		p.BeforeSeq = math.MaxInt64
	}
	if !f.From.IsZero() {
		p.FromMs = f.From.UnixMilli()
	}
	if !f.To.IsZero() {
		p.ToMs = f.To.UnixMilli()
	}
	rows, err := r.repo.List(ctx, p)
	if err != nil {
		return Page{}, fmt.Errorf("audit: list events: %w", err)
	}
	var page Page
	if len(rows) > limit {
		rows = rows[:limit]
		page.NextBeforeSeq = rows[limit-1].Seq
	}
	page.Entries = make([]Entry, len(rows))
	for i, row := range rows {
		page.Entries[i] = toEntry(row)
	}
	return page, nil
}

func toEntry(row auditdb.AuditEvent) Entry {
	return Entry{
		Seq:        row.Seq,
		ID:         row.ID,
		OccurredAt: time.UnixMilli(row.OccurredAt).UTC(),
		Actor: Actor{
			UserID:     row.ActorUserID,
			Email:      row.ActorEmail,
			Via:        Via(row.Via),
			APITokenID: row.ApiTokenID,
			IP:         row.Ip,
		},
		Action:      Action(row.Action),
		SubjectType: row.SubjectType,
		SubjectID:   row.SubjectID,
		Payload:     json.RawMessage(row.Payload),
	}
}
