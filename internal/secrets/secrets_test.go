package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/platform/clock"
	"github.com/alvaroibarguen/dbairlock/internal/platform/ids"
	"github.com/alvaroibarguen/dbairlock/internal/store"
)

// env is a data directory with an open store, shared by a sequence of Loads.
type env struct {
	t    *testing.T
	dir  string
	st   *store.Store
	logs *bytes.Buffer
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	st, err := store.Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &env{t: t, dir: dir, st: st, logs: &bytes.Buffer{}}
}

func (e *env) opts() Options {
	return Options{
		DataDir: e.dir,
		Store:   e.st,
		Audit:   audit.NewRecorder(clock.Real{}, ids.UUIDv7{}),
		Clock:   clock.Real{},
		IDs:     ids.UUIDv7{},
		Logger:  slog.New(slog.NewJSONHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
}

func (e *env) load() *Service {
	e.t.Helper()
	s, err := Load(context.Background(), e.opts())
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

func (e *env) keyPath() string { return filepath.Join(e.dir, KeyFileName) }

var adminCtx = audit.WithActor(context.Background(), audit.Actor{UserID: "u1", Email: "admin@example.com", Via: audit.ViaWeb})

func (e *env) set(s *Service, ref Ref, v string) {
	e.t.Helper()
	if err := e.st.Tx(adminCtx, func(tx *store.Tx) error { return s.Set(adminCtx, tx, ref, NewValue(v)) }); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) open(s *Service, ref Ref) string {
	e.t.Helper()
	v, err := s.Open(context.Background(), ref)
	if err != nil {
		e.t.Fatalf("open %s: %v", ref, err)
	}
	return string(v.Reveal())
}

// auditRows returns (action, payload) of every audit event.
func (e *env) auditRows() [][2]string {
	e.t.Helper()
	rows, err := e.st.Reader().QueryContext(context.Background(), `SELECT action, payload FROM audit_events ORDER BY seq`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out [][2]string
	for rows.Next() {
		var a, p string
		if err := rows.Scan(&a, &p); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, [2]string{a, p})
	}
	if err := rows.Err(); err != nil {
		e.t.Fatal(err)
	}
	return out
}

var (
	refA = Ref{Kind: KindTargetPassword, OwnerType: "connection", OwnerID: "c1"}
	refB = Ref{Kind: KindTargetPassword, OwnerType: "connection", OwnerID: "c2"}
	refC = Ref{Kind: KindWebhookURL, OwnerType: "instance", OwnerID: "notify_webhook"}
)

func TestValueRedaction(t *testing.T) {
	const plain = "hunter2-plaintext"
	v := NewValue(plain)
	type wrapper struct {
		Password Value
		Ptr      *Value
	}
	w := wrapper{Password: v, Ptr: &v}

	outputs := map[string]string{
		"fmt":         fmt.Sprintf("%v %+v %s %#v", v, v, v, v),
		"fmt-other":   fmt.Sprintf("%d %x %X %q %T", v, v, v, v, v),
		"fmt-wrapper": fmt.Sprintf("%v %+v %#v", w, w, w),
	}
	for _, x := range []any{v, &v, w, []Value{v}, map[string]Value{"k": v}} {
		b, err := json.Marshal(x)
		if err != nil {
			t.Fatal(err)
		}
		outputs[fmt.Sprintf("json %T", x)] = string(b)
	}
	text, _ := v.MarshalText()
	outputs["text"] = string(text)

	var jsonLog, textLog bytes.Buffer
	for _, h := range []slog.Handler{slog.NewJSONHandler(&jsonLog, nil), slog.NewTextHandler(&textLog, nil)} {
		l := slog.New(h)
		l.Info("msg", "v", v, slog.Any("w", w), slog.Any("ptr", &v))
		l.Info("msg", "group", slog.GroupValue(slog.Any("v", v)))
	}
	outputs["slog-json"] = jsonLog.String()
	outputs["slog-text"] = textLog.String()

	for name, out := range outputs {
		if strings.Contains(out, plain) || strings.Contains(out, "68756e746572") || strings.Contains(out, "104 117") {
			t.Errorf("%s leaked the value: %s", name, out)
		}
		if !strings.Contains(out, Redacted) && name != "fmt-other" {
			t.Errorf("%s does not say %s: %s", name, Redacted, out)
		}
	}
	if got := fmt.Sprintf("%v %+v %s %#v", v, v, v, v); got != "[redacted] [redacted] [redacted] [redacted]" {
		t.Errorf("fmt = %q", got)
	}
	if string(v.Reveal()) != plain {
		t.Fatal("Reveal does not return the value")
	}
}

func TestSetOpenRoundTrip(t *testing.T) {
	e := newEnv(t)
	s := e.load()
	e.set(s, refA, "pw-alpha")
	e.set(s, refB, "pw-bravo")
	if got := e.open(s, refA); got != "pw-alpha" {
		t.Fatalf("open A = %q", got)
	}
	if got := e.open(s, refB); got != "pw-bravo" {
		t.Fatalf("open B = %q", got)
	}

	m, err := s.Describe(context.Background(), refA)
	if err != nil || !m.IsSet || m.SetBy != "admin@example.com" || m.SetAt.IsZero() {
		t.Fatalf("describe = %+v %v", m, err)
	}
	if _, err := s.Open(context.Background(), refC); !errors.Is(err, ErrNotSet) {
		t.Fatalf("unset open err = %v", err)
	}

	e.set(s, refA, "pw-alpha-2")
	if got := e.open(s, refA); got != "pw-alpha-2" {
		t.Fatalf("replaced = %q", got)
	}
	if err := e.st.Tx(adminCtx, func(tx *store.Tx) error { return s.Clear(adminCtx, tx, refA) }); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.Describe(context.Background(), refA); m.IsSet {
		t.Fatal("cleared secret still set")
	}
	// Clearing again changes nothing and records nothing.
	if err := e.st.Tx(adminCtx, func(tx *store.Tx) error { return s.Clear(adminCtx, tx, refA) }); err != nil {
		t.Fatal(err)
	}

	var actions []string
	for _, row := range e.auditRows() {
		actions = append(actions, row[0])
		for _, plain := range []string{"pw-alpha", "pw-bravo"} {
			if strings.Contains(row[1], plain) {
				t.Errorf("audit payload leaked %q: %s", plain, row[1])
			}
		}
	}
	want := "master_key.initialized secret.set secret.set secret.replaced secret.cleared"
	if got := strings.Join(actions, " "); got != want {
		t.Fatalf("audit actions = %q, want %q", got, want)
	}
	if strings.Contains(e.logs.String(), "pw-") {
		t.Fatalf("logs leaked a value: %s", e.logs.String())
	}
	if !strings.Contains(e.logs.String(), "secret opened") {
		t.Fatal("Open is not logged at debug level")
	}
}

func TestSetRejectsBadInput(t *testing.T) {
	e := newEnv(t)
	s := e.load()
	cases := []struct {
		ctx context.Context
		ref Ref
		v   Value
		err error
	}{
		{adminCtx, refA, NewValue(""), ErrInvalidValue},
		{adminCtx, refA, NewValue(strings.Repeat("x", MaxValueBytes+1)), ErrInvalidValue},
		{adminCtx, Ref{Kind: "nope", OwnerType: "x", OwnerID: "y"}, NewValue("v"), nil},
		{context.Background(), refA, NewValue("v"), audit.ErrNoActor},
	}
	for i, tc := range cases {
		err := e.st.Tx(tc.ctx, func(tx *store.Tx) error { return s.Set(tc.ctx, tx, tc.ref, tc.v) })
		if err == nil || (tc.err != nil && !errors.Is(err, tc.err)) {
			t.Errorf("case %d: err = %v, want %v", i, err, tc.err)
		}
	}
}

func TestCiphertextMovedToAnotherRowFailsToOpen(t *testing.T) {
	e := newEnv(t)
	s := e.load()
	e.set(s, refA, "pw-alpha")
	e.set(s, refB, "pw-bravo")
	err := e.st.Tx(adminCtx, func(tx *store.Tx) error {
		_, err := tx.ExecContext(adminCtx, `
			UPDATE secrets SET
				ciphertext = (SELECT ciphertext FROM secrets WHERE owner_id = 'c1'),
				nonce = (SELECT nonce FROM secrets WHERE owner_id = 'c1')
			WHERE owner_id = 'c2'`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(context.Background(), refB); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("moved ciphertext opened: %v", err)
	}
	if got := e.open(s, refA); got != "pw-alpha" {
		t.Fatalf("source row broken: %q", got)
	}
}

func TestAuditRejectsValueInPayload(t *testing.T) {
	e := newEnv(t)
	rec := audit.NewRecorder(clock.Real{}, ids.UUIDv7{})
	type inner struct{ Secret *Value }
	v := NewValue("pw")
	payloads := []any{
		v,
		map[string]any{"password": v},
		struct{ Nested []any }{Nested: []any{1, inner{Secret: &v}}},
		map[string]Value{"k": v},
	}
	for i, p := range payloads {
		err := e.st.Tx(adminCtx, func(tx *store.Tx) error {
			return rec.Record(adminCtx, tx, audit.Event{Action: audit.SettingChanged, Payload: p})
		})
		if !errors.Is(err, audit.ErrSecretInPayload) {
			t.Errorf("payload %d: err = %v", i, err)
		}
	}
	if n := len(e.auditRows()); n != 0 {
		t.Fatalf("%d events written", n)
	}
}

func TestStartupCreatesMissingKey(t *testing.T) {
	e := newEnv(t)
	e.load()
	info, err := os.Stat(e.keyPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 || info.Size() != KeySize {
		t.Fatalf("key file mode %v size %d", info.Mode().Perm(), info.Size())
	}
	// A second start reuses it.
	before, _ := os.ReadFile(e.keyPath())
	e.load()
	after, _ := os.ReadFile(e.keyPath())
	if !bytes.Equal(before, after) {
		t.Fatal("key replaced on second start")
	}
}

func TestStartupRefusesBadKeyFile(t *testing.T) {
	cases := map[string]struct {
		content []byte
		mode    os.FileMode
	}{
		"short":          {bytes.Repeat([]byte{1}, KeySize-1), 0o600},
		"long":           {bytes.Repeat([]byte{1}, KeySize+1), 0o600},
		"group readable": {bytes.Repeat([]byte{1}, KeySize), 0o640},
		"world readable": {bytes.Repeat([]byte{1}, KeySize), 0o604},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			if err := os.WriteFile(e.keyPath(), tc.content, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(e.keyPath(), tc.mode); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(context.Background(), e.opts()); !errors.Is(err, ErrKeyFile) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestStartupRefusesKeyOfAnotherDatabase(t *testing.T) {
	e := newEnv(t)
	e.load()
	other, _ := newRandomKey()
	if err := os.WriteFile(e.keyPath(), other, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(context.Background(), e.opts())
	if !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("err = %v", err)
	}
}

func TestStartupRefusesMissingKeyForExistingDatabase(t *testing.T) {
	e := newEnv(t)
	e.load()
	if err := os.Remove(e.keyPath()); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(context.Background(), e.opts()); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(e.keyPath()); err == nil {
		t.Fatal("a new key was created for a database that already has one")
	}
}

var cliCtx = audit.WithActor(context.Background(), audit.Actor{Via: audit.ViaCLI})

func setThree(e *env, s *Service) map[Ref]string {
	want := map[Ref]string{refA: "pw-alpha", refB: "pw-bravo", refC: "https://hooks.example.com/T0/B0/xyz"}
	for ref, v := range want {
		e.set(s, ref, v)
	}
	return want
}

func (e *env) openAll(s *Service, want map[Ref]string) {
	e.t.Helper()
	for ref, v := range want {
		if got := e.open(s, ref); got != v {
			e.t.Fatalf("%s = %q, want %q", ref, got, v)
		}
	}
}

func TestRotateThenRestart(t *testing.T) {
	e := newEnv(t)
	s := e.load()
	want := setThree(e, s)
	oldKey, _ := os.ReadFile(e.keyPath())

	res, err := Rotate(cliCtx, e.opts())
	if err != nil {
		t.Fatal(err)
	}
	if res.Secrets != 3 || res.KeyVersion != 2 {
		t.Fatalf("result = %+v", res)
	}
	newKey, _ := os.ReadFile(e.keyPath())
	if bytes.Equal(oldKey, newKey) {
		t.Fatal("key not replaced")
	}
	if kept, _ := os.ReadFile(e.keyPath() + oldKeySuffix); !bytes.Equal(kept, oldKey) {
		t.Fatal("old key not kept as master.key.old")
	}
	if _, err := os.Stat(e.keyPath() + newKeySuffix); err == nil {
		t.Fatal("master.key.new left behind")
	}

	s2 := e.load()
	if s2.KeyVersion() != 2 {
		t.Fatalf("key version = %d", s2.KeyVersion())
	}
	e.openAll(s2, want)
	if _, err := os.Stat(e.keyPath() + oldKeySuffix); err == nil {
		t.Fatal("master.key.old not removed by a successful start")
	}
	if strings.Contains(e.logs.String(), "pw-") || strings.Contains(e.logs.String(), "hooks.example.com") {
		t.Fatalf("logs leaked a value: %s", e.logs.String())
	}
	rows := e.auditRows()
	if last := rows[len(rows)-1]; last[0] != string(audit.MasterKeyRotated) {
		t.Fatalf("last audit event = %v", last)
	}
}

func TestRotateInterruptedBeforeCommitKeepsOldKey(t *testing.T) {
	e := newEnv(t)
	s := e.load()
	want := setThree(e, s)
	oldKey, _ := os.ReadFile(e.keyPath())

	rotateHook = func(stage string) error {
		if stage == "before-commit" {
			return errors.New("simulated crash")
		}
		return nil
	}
	t.Cleanup(func() { rotateHook = nil })
	if _, err := Rotate(cliCtx, e.opts()); err == nil {
		t.Fatal("rotation succeeded")
	}
	rotateHook = nil
	if key, _ := os.ReadFile(e.keyPath()); !bytes.Equal(key, oldKey) {
		t.Fatal("master.key changed")
	}
	e.openAll(e.load(), want)
}

func TestStartupDiscardsStaleNewKey(t *testing.T) {
	// A crash during a rotation, before its transaction committed, can leave
	// master.key.new behind. The database still uses master.key.
	e := newEnv(t)
	want := setThree(e, e.load())
	stale, _ := newRandomKey()
	if err := writeKeyFileAtomic(e.keyPath()+newKeySuffix, stale); err != nil {
		t.Fatal(err)
	}
	e.openAll(e.load(), want)
	if _, err := os.Stat(e.keyPath() + newKeySuffix); err == nil {
		t.Fatal("stale master.key.new not removed")
	}
}

func TestRotateInterruptedAfterCommitFinishesOnStart(t *testing.T) {
	e := newEnv(t)
	s := e.load()
	want := setThree(e, s)
	oldKey, _ := os.ReadFile(e.keyPath())

	rotateHook = func(stage string) error {
		if stage == "after-commit" {
			return errors.New("simulated crash")
		}
		return nil
	}
	t.Cleanup(func() { rotateHook = nil })
	if _, err := Rotate(cliCtx, e.opts()); err == nil {
		t.Fatal("rotation succeeded")
	}
	rotateHook = nil
	if key, _ := os.ReadFile(e.keyPath()); !bytes.Equal(key, oldKey) {
		t.Fatal("master.key changed before the rename step")
	}
	pending, err := os.ReadFile(e.keyPath() + newKeySuffix)
	if err != nil {
		t.Fatal(err)
	}

	s2 := e.load()
	e.openAll(s2, want)
	if key, _ := os.ReadFile(e.keyPath()); !bytes.Equal(key, pending) {
		t.Fatal("start-up did not install master.key.new")
	}
}
