package audit

import (
	"bytes"
	"context"
	"fmt"

	"github.com/alvaroibarguen/dbairlock/internal/audit/auditdb"
	"github.com/alvaroibarguen/dbairlock/internal/store"
)

// verifyBatch is how many rows Verify reads per query.
const verifyBatch = 1000

// Problem is the first broken link Verify found.
type Problem struct {
	Seq    int64
	Reason string
}

func (p Problem) String() string { return fmt.Sprintf("seq %d: %s", p.Seq, p.Reason) }

// Result is the outcome of Verify.
type Result struct {
	Checked int64    // rows whose hash was recomputed
	LastSeq int64    // seq of the last row read
	Problem *Problem // nil when the chain is intact
}

// OK reports whether the chain is intact.
func (r Result) OK() bool { return r.Problem == nil }

// Verifier walks the audit chain.
type Verifier struct {
	repo Repository
}

// NewVerifier returns a Verifier over db (normally store.Store.Reader()).
func NewVerifier(db store.DBTX) *Verifier {
	return &Verifier{repo: NewRepository(db)}
}

// Verify streams every row in seq order, recomputes each hash and returns
// the first mismatch or gap. A non-nil error means the check could not run;
// a broken chain is reported in Result.Problem with a nil error.
func (v *Verifier) Verify(ctx context.Context) (Result, error) {
	return v.VerifyProgress(ctx, nil)
}

// VerifyProgress is Verify, calling progress (if not nil) with the rows
// checked so far after every batch.
func (v *Verifier) VerifyProgress(ctx context.Context, progress func(checked int64)) (Result, error) {
	var res Result
	prev := make([]byte, hashSize)
	expect := int64(1)
	for {
		rows, err := v.repo.After(ctx, res.LastSeq, verifyBatch)
		if err != nil {
			return res, fmt.Errorf("audit: read events: %w", err)
		}
		for _, row := range rows {
			if p := checkRow(row, expect, prev); p != nil {
				res.Problem = p
				return res, nil
			}
			res.Checked++
			res.LastSeq = row.Seq
			prev = row.Hash
			expect = row.Seq + 1
		}
		if progress != nil {
			progress(res.Checked)
		}
		if len(rows) < verifyBatch {
			break
		}
	}
	// Rows deleted from the end leave no gap between survivors, but
	// AUTOINCREMENT remembers the highest seq it handed out.
	high, err := v.repo.SequenceHigh(ctx)
	if err != nil {
		return res, fmt.Errorf("audit: read sequence: %w", err)
	}
	if high > res.LastSeq {
		res.Problem = &Problem{Seq: res.LastSeq + 1, Reason: fmt.Sprintf("missing: events %d to %d were deleted from the end of the log", res.LastSeq+1, high)}
	}
	return res, nil
}

func checkRow(row auditdb.AuditEvent, expect int64, prev []byte) *Problem {
	if row.Seq != expect {
		return &Problem{Seq: expect, Reason: fmt.Sprintf("missing: next stored event is %d", row.Seq)}
	}
	if !bytes.Equal(row.PrevHash, prev) {
		return &Problem{Seq: row.Seq, Reason: "prev_hash does not match the previous event's hash"}
	}
	want, err := chainHash(prev, record{
		Seq:         row.Seq,
		ID:          row.ID,
		OccurredAt:  row.OccurredAt,
		ActorUserID: row.ActorUserID,
		ActorEmail:  row.ActorEmail,
		Via:         row.Via,
		APITokenID:  row.ApiTokenID,
		IP:          row.Ip,
		Action:      row.Action,
		SubjectType: row.SubjectType,
		SubjectID:   row.SubjectID,
		Payload:     row.Payload,
	})
	if err != nil {
		return &Problem{Seq: row.Seq, Reason: "payload cannot be read: " + err.Error()}
	}
	if !bytes.Equal(row.Hash, want) {
		return &Problem{Seq: row.Seq, Reason: "hash mismatch: the event was modified"}
	}
	return nil
}
