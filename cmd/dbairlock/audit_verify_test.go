package main

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/alvaroibarguen/dbairlock/internal/app"
	"github.com/alvaroibarguen/dbairlock/internal/platform/logx"
)

func TestAuditVerifyExitCodes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	verify := func(args ...string) (int, string) {
		t.Helper()
		var out, errOut bytes.Buffer
		code := run(append([]string{"audit", "verify"}, args...), &out, &errOut)
		return code, out.String() + errOut.String()
	}

	if code, out := verify("--data-dir", dir); code != exitVerifyError || !strings.Contains(out, "not found") {
		t.Fatalf("missing database: %d %q", code, out)
	}
	if code, _ := verify("--data-dir", dir, "extra"); code != exitUsage {
		t.Fatalf("extra args: %d", code)
	}
	if code := run([]string{"audit"}, &bytes.Buffer{}, &bytes.Buffer{}); code != exitUsage {
		t.Fatalf("bare audit: %d", code)
	}

	// A freshly created database has an intact chain (two events: the master
	// key was initialized and a setup code issued).
	a, err := app.New(app.Options{DataDir: dir, Listen: "127.0.0.1:0", Logger: logx.Discard()})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if code, out := verify("--data-dir", dir); code != exitVerifyOK || !strings.Contains(out, "intact") {
		t.Fatalf("good database: %d %q", code, out)
	}

	// A row forged with a raw connection does not chain.
	db, err := sql.Open("sqlite", filepath.Join(dir, "dbairlock.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO audit_events (seq, id, occurred_at, via, action, payload, prev_hash, hash)
		 VALUES (3, 'forged', 0, 'system', 'setting.changed', '{}', zeroblob(32), zeroblob(32))`); err != nil {
		t.Fatal(err)
	}
	if code, out := verify("--data-dir", dir); code != exitVerifyBroken || !strings.Contains(out, "seq 3") {
		t.Fatalf("tampered database: %d %q", code, out)
	}
}

func TestRecoverCommand(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	recoverCmd := func(args ...string) (int, string) {
		t.Helper()
		var out, errOut bytes.Buffer
		code := run(append([]string{"recover"}, args...), &out, &errOut)
		return code, out.String() + errOut.String()
	}
	if code, out := recoverCmd("--data-dir", dir); code != 1 || !strings.Contains(out, "not found") {
		t.Fatalf("missing database: %d %q", code, out)
	}
	if code, _ := recoverCmd("--data-dir", dir, "extra"); code != exitUsage {
		t.Fatalf("extra args: %d", code)
	}
	a, err := app.New(app.Options{DataDir: dir, Listen: "127.0.0.1:0", Logger: logx.Discard()})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	code, out := recoverCmd("--data-dir", dir, "--listen", "127.0.0.1:9000")
	if code != 0 || !strings.Contains(out, "http://127.0.0.1:9000/recover#code=") || !strings.Contains(out, "No public URL") {
		t.Fatalf("recover: %d %q", code, out)
	}
}

func TestRotateKeyCommand(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	rotate := func(args ...string) (int, string) {
		t.Helper()
		var out, errOut bytes.Buffer
		code := run(append([]string{"rotate-key"}, args...), &out, &errOut)
		return code, out.String() + errOut.String()
	}
	if code, out := rotate("--data-dir", dir); code != 1 || !strings.Contains(out, "not found") {
		t.Fatalf("missing database: %d %q", code, out)
	}
	if code, _ := rotate("--data-dir", dir, "extra"); code != exitUsage {
		t.Fatalf("extra args: %d", code)
	}
	a, err := app.New(app.Options{DataDir: dir, Listen: "127.0.0.1:0", Logger: logx.Discard()})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if code, out := rotate("--data-dir", dir); code != 0 || !strings.Contains(out, "version 2") {
		t.Fatalf("rotate: %d %q", code, out)
	}
	// The instance starts with the rotated key.
	a, err = app.New(app.Options{DataDir: dir, Listen: "127.0.0.1:0", Logger: logx.Discard()})
	if err != nil {
		t.Fatal(err)
	}
	_ = a.Close()
}
