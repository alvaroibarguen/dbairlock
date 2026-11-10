package mcpserver

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/changes"
	"github.com/alvaroibarguen/dbairlock/internal/connector"
	"github.com/alvaroibarguen/dbairlock/internal/platform/clock"
	"github.com/alvaroibarguen/dbairlock/internal/platform/logx"
	"github.com/alvaroibarguen/dbairlock/internal/settings"
)

type fixedSettings map[string]any

func (f fixedSettings) Value(key string) any { return f[key] }

func TestLimits(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	src := fixedSettings{
		settings.MCPCallsPerMinute.Name(): 3, settings.MCPPreviewsPerHour.Name(): 1, settings.MCPSubmissionsPerHour.Name(): 1,
	}
	l := newLimits(c, src)
	if err := l.allow("a", "preview_change", limitPreview); err != nil {
		t.Fatal(err)
	}
	err := l.allow("a", "preview_change", limitPreview)
	if err == nil || !strings.Contains(err.Error(), "1 preview_change calls per hour") || !strings.Contains(err.Error(), "60 minutes") {
		t.Fatalf("second preview: %v", err)
	}
	// Another token has its own budget.
	if err := l.allow("b", "preview_change", limitPreview); err != nil {
		t.Fatal(err)
	}
	// Every call counts against the per-minute limit, refused ones too.
	if err := l.allow("a", "get_change", limitNone); err != nil {
		t.Fatalf("third call: %v", err)
	}
	if err := l.allow("a", "get_change", limitNone); err == nil || !strings.Contains(err.Error(), "3 tool calls per minute") {
		t.Fatalf("fourth call: %v", err)
	}
	c.Advance(time.Hour)
	if err := l.allow("a", "preview_change", limitPreview); err != nil {
		t.Fatalf("after an hour: %v", err)
	}
	// A new setting applies at once.
	src[settings.MCPPreviewsPerHour.Name()] = 5
	for i := range 2 {
		if err := l.allow("a", "preview_change", limitPreview); err != nil {
			t.Fatalf("preview %d after raising the limit: %v", i, err)
		}
	}
}

func TestToolErrors(t *testing.T) {
	s := &Server{opts: Options{Logger: logx.Discard()}}
	ctx := context.Background()
	dre := &changes.DryRunError{Statements: []changes.StatementResult{
		{Position: 0, Statement: &connector.Statement{Kind: connector.KindUpdate, Schema: "public", Table: "users"}},
		{Position: 1, Issue: &changes.Issue{Code: "missing_where", Message: "UPDATE needs a WHERE clause.", Position: 0}},
	}}
	msg := s.toolError(ctx, "submit_change", dre).Error()
	for _, want := range []string{"nothing was created", "Statement 2", "WHERE clause", "at character 1"} {
		if !strings.Contains(msg, want) {
			t.Errorf("dry run error %q lacks %q", msg, want)
		}
	}
	if strings.Contains(msg, "Statement 1") {
		t.Errorf("a valid statement is reported: %q", msg)
	}
	ie := &changes.InputError{Fields: []changes.FieldError{{Field: "statements[0]", Message: "Empty."}, {Field: "title", Message: "Required."}}}
	if msg := s.toolError(ctx, "submit_change", ie).Error(); !strings.Contains(msg, "Statement 1: Empty.") || !strings.Contains(msg, "title: Required.") {
		t.Errorf("input error = %q", msg)
	}
	if msg := s.toolError(ctx, "get_change", changes.ErrNotFound).Error(); !strings.Contains(msg, "No such change") {
		t.Errorf("not found = %q", msg)
	}
	// Unknown errors are not shown to the agent.
	if err := s.toolError(ctx, "get_change", errors.New("disk on fire at /var/lib")); !errors.Is(err, errInternal) {
		t.Errorf("internal error = %v", err)
	}
}
