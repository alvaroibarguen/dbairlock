package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// exportBatch is how many rows Export reads per query, so memory stays flat
// however long the log is.
const exportBatch = 500

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
	PrevHash    []byte
	Hash        []byte
}

// Filter selects events for Reader.List. Zero values mean "any".
type Filter struct {
	// Actor matches the actor's user id or email exactly.
	Actor        string
	Via          Via
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
		Actor:        f.Actor,
		Via:          string(f.Via),
		ActionPrefix: f.ActionPrefix,
		SubjectType:  f.SubjectType,
		SubjectID:    f.SubjectID,
		PageLimit:    int64(limit) + 1,
	}
	if p.BeforeSeq <= 0 {
		p.BeforeSeq = math.MaxInt64
	}
	p.FromMs, p.ToMs = rangeMs(f.From, f.To)
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

// rangeMs converts an optional time range to stored milliseconds.
func rangeMs(from, to time.Time) (int64, int64) {
	lo, hi := int64(0), int64(math.MaxInt64)
	if !from.IsZero() {
		lo = from.UnixMilli()
	}
	if !to.IsZero() {
		hi = to.UnixMilli()
	}
	return lo, hi
}

// ExportLine is one line of an export: the stored event with its hashes in
// hex, so the chain can be checked outside dbairlock.
type ExportLine struct {
	Seq         int64           `json:"seq"`
	ID          string          `json:"id"`
	OccurredAt  time.Time       `json:"occurred_at"`
	ActorUserID string          `json:"actor_user_id,omitempty"`
	ActorEmail  string          `json:"actor_email,omitempty"`
	Via         Via             `json:"via"`
	APITokenID  string          `json:"api_token_id,omitempty"`
	IP          string          `json:"ip,omitempty"`
	Action      Action          `json:"action"`
	SubjectType string          `json:"subject_type,omitempty"`
	SubjectID   string          `json:"subject_id,omitempty"`
	Payload     json.RawMessage `json:"payload"`
	PrevHash    string          `json:"prev_hash"`
	Hash        string          `json:"hash"`
}

// Export writes every event with from <= occurred_at < to (zero times mean
// unbounded) to w as newline-delimited JSON, oldest first. It reads the log
// in batches, so memory use does not grow with the size of the log.
func (r *Reader) Export(ctx context.Context, w io.Writer, from, to time.Time) (int64, error) {
	lo, hi := rangeMs(from, to)
	enc := json.NewEncoder(w)
	var after, n int64
	for {
		rows, err := r.repo.Between(ctx, auditdb.EventsBetweenParams{
			AfterSeq: after, FromMs: lo, ToMs: hi, PageLimit: exportBatch,
		})
		if err != nil {
			return n, fmt.Errorf("audit: export: %w", err)
		}
		for _, row := range rows {
			e := toEntry(row)
			if err := enc.Encode(ExportLine{
				Seq: e.Seq, ID: e.ID, OccurredAt: e.OccurredAt,
				ActorUserID: e.Actor.UserID, ActorEmail: e.Actor.Email, Via: e.Actor.Via,
				APITokenID: e.Actor.APITokenID, IP: e.Actor.IP,
				Action: e.Action, SubjectType: e.SubjectType, SubjectID: e.SubjectID,
				Payload: e.Payload, PrevHash: fmt.Sprintf("%x", e.PrevHash), Hash: fmt.Sprintf("%x", e.Hash),
			}); err != nil {
				return n, err
			}
			n++
			after = row.Seq
		}
		if len(rows) < exportBatch {
			return n, nil
		}
		if err := ctx.Err(); err != nil {
			return n, err
		}
	}
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
		PrevHash:    row.PrevHash,
		Hash:        row.Hash,
	}
}
