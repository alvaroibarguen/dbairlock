package logx

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestContextRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, false).With("request_id", "r1")
	ctx := WithLogger(context.Background(), l)
	FromContext(ctx).Info("hello")
	if !strings.Contains(buf.String(), `"request_id":"r1"`) {
		t.Fatalf("log line missing request id: %s", buf.String())
	}
}

func TestFromContextDefault(t *testing.T) {
	if FromContext(context.Background()) == nil {
		t.Fatal("nil default logger")
	}
}
