package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseServeFlags(t *testing.T) {
	f, err := parseServeFlags([]string{"--data-dir", "/d", "--listen", "127.0.0.1:1", "--dev"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if f.dataDir != "/d" || f.listen != "127.0.0.1:1" || !f.dev {
		t.Fatalf("flags = %+v", f)
	}
	f, err = parseServeFlags(nil, &bytes.Buffer{})
	if err != nil || f.dataDir != "./data" || f.listen != ":8080" || f.dev {
		t.Fatalf("defaults = %+v, %v", f, err)
	}
}

func TestRejectsUnknownFlagsAndArgs(t *testing.T) {
	for _, args := range [][]string{{"--config", "x"}, {"extra"}, {"--data-dir"}} {
		if _, err := parseServeFlags(args, &bytes.Buffer{}); err == nil {
			t.Errorf("args %v accepted", args)
		}
	}
	if code := run([]string{"serve", "--nope"}, &bytes.Buffer{}, &bytes.Buffer{}); code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if code := run([]string{"frobnicate"}, &bytes.Buffer{}, &bytes.Buffer{}); code != 2 {
		t.Errorf("unknown command exit code = %d, want 2", code)
	}
}

func TestVersionCommand(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"version"}, &out, &bytes.Buffer{}); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if strings.TrimSpace(out.String()) == "" {
		t.Fatal("empty version")
	}
}
