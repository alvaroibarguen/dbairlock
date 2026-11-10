package settings

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/platform/clock"
	"github.com/alvaroibarguen/dbairlock/internal/platform/ids"
	"github.com/alvaroibarguen/dbairlock/internal/secrets"
	"github.com/alvaroibarguen/dbairlock/internal/store"
)

type env struct {
	t   *testing.T
	dir string
	st  *store.Store
	sec *secrets.Service
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	st, err := store.Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sec, err := secrets.Load(context.Background(), secrets.Options{
		DataDir: dir, Store: st, Audit: audit.NewRecorder(clock.Real{}, ids.UUIDv7{}),
		Clock: clock.Real{}, IDs: ids.UUIDv7{}, Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, dir: dir, st: st, sec: sec}
}

func (e *env) service() *Service {
	e.t.Helper()
	s, err := New(context.Background(), Options{
		Store: e.st, Audit: audit.NewRecorder(clock.Real{}, ids.UUIDv7{}), Secrets: e.sec,
		Clock: clock.Real{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

type auditRow struct {
	Action, SubjectID string
	Payload           map[string]any
}

func (e *env) events(prefix string) []auditRow {
	e.t.Helper()
	rows, err := e.st.Reader().QueryContext(context.Background(),
		`SELECT action, subject_id, payload FROM audit_events WHERE action LIKE ? ORDER BY seq`, prefix+"%")
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []auditRow
	for rows.Next() {
		var r auditRow
		var p string
		if err := rows.Scan(&r.Action, &r.SubjectID, &p); err != nil {
			e.t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(p), &r.Payload); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		e.t.Fatal(err)
	}
	return out
}

var admin = audit.WithActor(context.Background(), audit.Actor{UserID: "u1", Email: "admin@example.com", Via: audit.ViaWeb})

func TestRegistryIsConsistent(t *testing.T) {
	e := newEnv(t)
	s := e.service()
	seen := map[string]bool{}
	for _, d := range Definitions() {
		if seen[d.Key] {
			t.Errorf("%s registered twice", d.Key)
		}
		seen[d.Key] = true
		// The default must pass its own validation unchanged.
		v, err := d.parse(d.Default)
		if err != nil || !reflect.DeepEqual(v, d.Default) {
			t.Errorf("%s: default %v does not validate: %v %v", d.Key, d.Default, v, err)
		}
		wantType := map[Type]reflect.Type{
			TypeString: reflect.TypeFor[string](), TypeURL: reflect.TypeFor[string](),
			TypeInt: reflect.TypeFor[int](), TypeBool: reflect.TypeFor[bool](), TypeStringList: reflect.TypeFor[[]string](),
		}[d.Type]
		if reflect.TypeOf(d.Default) != wantType {
			t.Errorf("%s: default has type %T for %s", d.Key, d.Default, d.Type)
		}
		if d.Type == TypeInt && (d.Min > d.Max || d.Max == 0) {
			t.Errorf("%s: int without bounds", d.Key)
		}
	}
	// Every typed key resolves without panicking and returns its default.
	if Get(s, InstanceName) != "dbairlock" || Get(s, SessionHours) != 12 || !Get(s, DefaultAutoExecute) ||
		Get(s, NotifyEnabled) || len(Get(s, AllowedDomains)) != 0 || Get(s, InstancePublicURL) != "" ||
		Get(s, GoogleClientID) != "" || Get(s, DefaultRequiredApprovals) != 1 || Get(s, DefaultMaxRows) != 100 ||
		Get(s, DefaultApprovalTTLHours) != 72 || !Get(s, DefaultStoreRowImages) || Get(s, PreviewMaxAgeMinutes) != 5 ||
		Get(s, StatementTimeoutMS) != 10000 || Get(s, LockTimeoutMS) != 3000 ||
		Get(s, OIDCIssuer) != GoogleIssuer || len(Get(s, TrustedProxies)) != 0 ||
		Get(s, MCPCallsPerMinute) != 60 || Get(s, MCPPreviewsPerHour) != 30 || Get(s, MCPSubmissionsPerHour) != 20 {
		t.Fatalf("defaults = %v", s.Values())
	}
	if len(s.Values()) != len(Definitions()) || len(seen) != 19 {
		t.Fatalf("registry has %d settings", len(seen))
	}
	if pub := s.PublicValues(); !reflect.DeepEqual(pub, map[string]any{"instance.name": "dbairlock"}) {
		t.Fatalf("public = %v", pub)
	}
}

func TestUpdateIsVisibleImmediatelyAndAudited(t *testing.T) {
	e := newEnv(t)
	s := e.service()
	var heard []string
	s.OnChange(func(_ context.Context, changed []string) { heard = append(heard, changed...) })

	err := s.Update(admin, Changes{Values: map[string]any{
		"auth.session_hours":   json.RawMessage(`24`),
		"auth.allowed_domains": []string{" Example.COM ", "example.com", "corp.example.org"},
		"instance.name":        "  Acme dbairlock ",
		"instance.public_url":  "https://dbairlock.example.com/",
		"notify.enabled":       false, // unchanged: no event
	}})
	if err != nil {
		t.Fatal(err)
	}
	if Get(s, SessionHours) != 24 || Get(s, InstanceName) != "Acme dbairlock" ||
		Get(s, InstancePublicURL) != "https://dbairlock.example.com" ||
		!slices.Equal(Get(s, AllowedDomains), []string{"example.com", "corp.example.org"}) {
		t.Fatalf("values = %v", s.Values())
	}
	wantChanged := []string{"auth.allowed_domains", "auth.session_hours", "instance.name", "instance.public_url"}
	if !slices.Equal(heard, wantChanged) {
		t.Fatalf("listener heard %v", heard)
	}

	evs := e.events("setting.")
	if len(evs) != 4 {
		t.Fatalf("events = %+v", evs)
	}
	for _, ev := range evs {
		if ev.Payload["key"] != ev.SubjectID {
			t.Errorf("event %+v", ev)
		}
		if ev.SubjectID == "auth.session_hours" && (ev.Payload["old"] != float64(12) || ev.Payload["new"] != float64(24)) {
			t.Errorf("session hours event = %+v", ev.Payload)
		}
	}

	// A fresh service reads the stored values back.
	if s2 := e.service(); Get(s2, SessionHours) != 24 || Get(s2, InstanceName) != "Acme dbairlock" {
		t.Fatalf("reloaded = %v", s2.Values())
	}

	// Callers cannot mutate the snapshot through a returned slice.
	l := Get(s, AllowedDomains)
	l[0] = "evil.example"
	if Get(s, AllowedDomains)[0] != "example.com" {
		t.Fatal("snapshot mutated through Get")
	}
}

func TestInvalidUpdateChangesNothing(t *testing.T) {
	e := newEnv(t)
	s := e.service()
	err := s.Update(admin, Changes{
		Values: map[string]any{
			"auth.session_hours":                0,
			"policy.default_required_approvals": 6,
			"policy.default_max_rows":           "100",
			"instance.name":                     "   ",
			"instance.public_url":               "ftp://example.com",
			"auth.allowed_domains":              []string{"not a domain"},
			"auth.google.client_id":             "has space",
			"limits.lock_timeout_ms":            1.5,
			"notify.enabled":                    "yes",
			"no.such.setting":                   1,
			"policy.preview_max_age_minutes":    30, // valid, but must not apply
		},
		Secrets: map[string]SecretChange{
			"auth.google.client_secret": {Value: secrets.NewValue("")},
			"no.such.secret":            {Clear: true},
		},
	})
	var ve *ValidationError
	if !errors.As(err, &ve) || !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
	got := map[string]string{}
	for _, f := range ve.Fields {
		got[f.Field] = f.Message
	}
	for _, k := range []string{
		"auth.session_hours", "policy.default_required_approvals", "policy.default_max_rows", "instance.name",
		"instance.public_url", "auth.allowed_domains", "auth.google.client_id", "limits.lock_timeout_ms",
		"notify.enabled", "no.such.setting", "auth.google.client_secret", "no.such.secret",
	} {
		if got[k] == "" {
			t.Errorf("no field error for %s", k)
		}
	}
	if _, ok := got["policy.preview_max_age_minutes"]; ok {
		t.Error("valid field reported invalid")
	}
	if got["auth.session_hours"] != "Must be between 1 and 720." {
		t.Errorf("message = %q", got["auth.session_hours"])
	}
	if Get(s, PreviewMaxAgeMinutes) != 5 {
		t.Fatal("a valid change applied although the update was rejected")
	}
	if n := len(e.events("")); n != 1 { // only master_key.initialized
		t.Fatalf("%d audit events", n)
	}
}

func TestIssuerAndProxyValidation(t *testing.T) {
	d, _ := definition(OIDCIssuer.name)
	for _, ok := range []string{GoogleIssuer, GoogleIssuer + "/", "http://127.0.0.1:9999", "http://localhost:1/x", "https://[::1]:8443"} {
		if _, err := d.parse(ok); err != nil {
			t.Errorf("issuer %q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"https://evil.example.com", "http://10.0.0.1", "", "accounts.google.com", "http://127.0.0.1.evil.com"} {
		if _, err := d.parse(bad); err == nil {
			t.Errorf("issuer %q accepted", bad)
		}
	}
	p, _ := definition(TrustedProxies.name)
	v, err := p.parse([]string{"10.0.0.0/8", " 192.168.1.7 ", "10.1.2.3/8", "2001:db8::/32"})
	if err != nil || !reflect.DeepEqual(v, []string{"10.0.0.0/8", "192.168.1.7/32", "2001:db8::/32"}) {
		t.Fatalf("proxies = %v %v", v, err)
	}
	if _, err := p.parse([]string{"10.0.0.0/33"}); err == nil {
		t.Fatal("bad CIDR accepted")
	}
	if got := ParsePrefixes(v.([]string)); len(got) != 3 {
		t.Fatalf("parsed %v", got)
	}
}

func TestSecretsThroughUpdate(t *testing.T) {
	e := newEnv(t)
	s := e.service()
	const plain = "gcp-client-secret-value"
	if err := s.Update(admin, Changes{
		Values:  map[string]any{"auth.google.client_id": "123.apps.googleusercontent.com"},
		Secrets: map[string]SecretChange{"auth.google.client_secret": {Value: secrets.NewValue(plain)}},
	}); err != nil {
		t.Fatal(err)
	}
	st, err := s.SecretStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m := st["auth.google.client_secret"]; !m.IsSet || m.SetBy != "admin@example.com" {
		t.Fatalf("status = %+v", st)
	}
	if st["notify.webhook_url"].IsSet {
		t.Fatal("webhook set")
	}
	v, err := e.sec.Open(context.Background(), GoogleClientSecret)
	if err != nil || v.IsEmpty() {
		t.Fatalf("open: %v", err)
	}

	if err := s.Update(admin, Changes{Secrets: map[string]SecretChange{"auth.google.client_secret": {Clear: true}}}); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.SecretStatus(context.Background()); st["auth.google.client_secret"].IsSet {
		t.Fatal("not cleared")
	}
	var actions []string
	for _, ev := range e.events("") {
		actions = append(actions, ev.Action)
		raw, _ := json.Marshal(ev.Payload)
		if strings.Contains(string(raw), plain) {
			t.Fatalf("audit leaked the secret: %s", raw)
		}
	}
	if got := strings.Join(actions, " "); got != "master_key.initialized setting.changed secret.set secret.cleared" {
		t.Fatalf("actions = %s", got)
	}
}

func TestUpdateNeedsActor(t *testing.T) {
	s := newEnv(t).service()
	err := s.Update(context.Background(), Changes{Values: map[string]any{"auth.session_hours": 2}})
	if !errors.Is(err, audit.ErrNoActor) || Get(s, SessionHours) != 12 {
		t.Fatalf("err = %v", err)
	}
}

func TestInvalidStoredValueFallsBackToDefault(t *testing.T) {
	e := newEnv(t)
	err := e.st.Tx(admin, func(tx *store.Tx) error {
		_, err := tx.ExecContext(admin, `INSERT INTO settings (key, value, updated_by, updated_at) VALUES
			('auth.session_hours', '9999', 'x', 0), ('gone.setting', '1', 'x', 0)`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if s := e.service(); Get(s, SessionHours) != 12 {
		t.Fatalf("session hours = %d", Get(s, SessionHours))
	}
}
