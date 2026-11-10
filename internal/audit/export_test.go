package audit_test

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/platform/logx"
	"github.com/alvaroibarguen/dbairlock/internal/store"
)

// heapProbe is an io.Writer that checks every line is a JSON object and,
// every sample lines, measures the live heap.
type heapProbe struct {
	t       *testing.T
	buf     []byte
	lines   int
	sample  int
	maxHeap uint64
	lastSeq int64
}

func liveHeap() uint64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

func (p *heapProbe) Write(b []byte) (int, error) {
	p.buf = append(p.buf, b...)
	for {
		i := strings.IndexByte(string(p.buf), '\n')
		if i < 0 {
			break
		}
		var line audit.ExportLine
		if err := json.Unmarshal(p.buf[:i], &line); err != nil {
			p.t.Fatalf("line %d is not JSON: %v", p.lines+1, err)
		}
		if line.Seq != p.lastSeq+1 || len(line.Hash) != 64 || len(line.PrevHash) != 64 {
			p.t.Fatalf("line %d: seq %d after %d, hash %q", p.lines+1, line.Seq, p.lastSeq, line.Hash)
		}
		p.lastSeq = line.Seq
		p.buf = p.buf[i+1:]
		p.lines++
		if p.lines%p.sample == 0 {
			p.maxHeap = max(p.maxHeap, liveHeap())
		}
	}
	return len(b), nil
}

func TestExportStreamsWithoutGrowingMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("records 10,000 events")
	}
	e := newEnv(t)
	ctx := actorCtx()
	pad := strings.Repeat("x", 2000) // 10,000 events of ~2 KB: about 20 MB in all
	const total, perTx = 10_000, 1_000
	for b := 0; b < total/perTx; b++ {
		err := e.store.Tx(ctx, func(tx *store.Tx) error {
			for i := range perTx {
				ev := audit.Event{Action: audit.SettingChanged, SubjectID: fmt.Sprint(b*perTx + i), Payload: map[string]any{"pad": pad}}
				if err := e.rec.Record(ctx, tx, ev); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	r := audit.NewReader(e.store.Reader())
	base := liveHeap()
	p := &heapProbe{t: t, sample: 1000}
	n, err := r.Export(context.Background(), p, time.Time{}, time.Time{})
	if err != nil || n != total || p.lines != total {
		t.Fatalf("exported %d (%d lines): %v", n, p.lines, err)
	}
	if grew := int64(p.maxHeap) - int64(base); grew > 8<<20 {
		t.Fatalf("heap grew by %d bytes while streaming about 20 MB", grew)
	}

	// A time range selects exactly the events inside it.
	var out strings.Builder
	n, err = r.Export(context.Background(), &out, t0, t0.Add(time.Millisecond))
	if err != nil || n != total { // the fake clock did not move
		t.Fatalf("range export: %d %v", n, err)
	}
	n, err = r.Export(context.Background(), io.Discard, t0.Add(time.Millisecond), time.Time{})
	if err != nil || n != 0 {
		t.Fatalf("empty range: %d %v", n, err)
	}
	sc := bufio.NewScanner(strings.NewReader(out.String()))
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	lines := 0
	for sc.Scan() {
		lines++
	}
	if lines != total {
		t.Fatalf("%d lines", lines)
	}
}

func TestCheckerReportsFirstBrokenLink(t *testing.T) {
	e := newEnv(t)
	ctx := actorCtx()
	for range 5 {
		e.record(ctx, t, audit.Event{Action: audit.SettingChanged})
	}
	c := audit.NewChecker(e.store.Reader(), e.store, e.rec, e.clock, logx.Discard())
	if _, ok := c.Status(); ok {
		t.Fatal("status before any run")
	}
	<-c.Start(ctx)
	run, ok := c.Status()
	if !ok || run.Running || run.Result == nil || !run.Result.OK() || run.Checked != 5 {
		t.Fatalf("intact chain: %+v", run)
	}
	last, ok, err := c.LastSuccess(context.Background())
	if err != nil || !ok || last.By != "alice@example.com" || last.Checked != 5 {
		t.Fatalf("last success: %+v %v %v", last, ok, err)
	}

	db, err := sql.Open("sqlite", e.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, q := range []string{"DROP TRIGGER audit_events_no_update", `UPDATE audit_events SET payload = '{"x":1}' WHERE seq = 3`} {
		if _, err := db.ExecContext(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	e.clock.Advance(time.Minute)
	<-c.Start(ctx)
	run, _ = c.Status()
	if run.Result == nil || run.Result.OK() || run.Result.Problem.Seq != 3 {
		t.Fatalf("tampered chain: %+v", run)
	}
	// The failed run is recorded but the last success stays the earlier one.
	again, ok, err := c.LastSuccess(context.Background())
	if err != nil || !ok || !again.At.Equal(last.At) {
		t.Fatalf("last success after a failure: %+v %v %v", again, ok, err)
	}
	page, err := audit.NewReader(e.store.Reader()).List(context.Background(), audit.Filter{ActionPrefix: string(audit.AuditVerified)})
	if err != nil || len(page.Entries) != 2 || !strings.Contains(string(page.Entries[0].Payload), `"problem_seq":3`) {
		t.Fatalf("audit.verified events: %+v %v", page.Entries, err)
	}
}
