package targets

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/connector"
	"github.com/alvaroibarguen/dbairlock/internal/platform/clock"
	"github.com/alvaroibarguen/dbairlock/internal/platform/ids"
	"github.com/alvaroibarguen/dbairlock/internal/platform/logx"
	"github.com/alvaroibarguen/dbairlock/internal/secrets"
	"github.com/alvaroibarguen/dbairlock/internal/settings"
	"github.com/alvaroibarguen/dbairlock/internal/store"
)

// fakeConnector records every Open and the password it was given.
type fakeConnector struct {
	mu     sync.Mutex
	opened []fakeOpen
	conns  []*fakeConn
	// excess, when set, is reported as an excess privilege.
	excess bool
	major  int
}

type fakeOpen struct {
	host     string
	password string
}

func (f *fakeConnector) Info() connector.Info {
	return connector.Info{ID: "fake", DisplayName: "Fake", Dialect: "sql", DefaultPort: 1}
}

func (f *fakeConnector) ConfigFields() []connector.Field {
	return []connector.Field{
		{Key: "host", Label: "Host", Type: connector.FieldString, Required: true},
		{Key: "port", Label: "Port", Type: connector.FieldInt, Default: 1},
		{Key: "password", Label: "Password", Type: connector.FieldSecret, Required: true},
	}
}

func (f *fakeConnector) ValidateConfig(cfg connector.Config) (connector.Config, error) {
	host, _ := cfg["host"].(string)
	if host == "" {
		return nil, errors.New("host is required")
	}
	if host == "169.254.169.254" {
		return nil, errors.New("connections to link-local address 169.254.169.254 are not allowed")
	}
	port := cfg.Int("port")
	if port == 0 {
		port = 1
	}
	for k := range cfg {
		if k != "host" && k != "port" {
			return nil, errors.New("unknown field " + k)
		}
	}
	return connector.Config{"host": host, "port": port}, nil
}

func (f *fakeConnector) Validate(connector.Config, string, connector.Policy) (connector.Statement, error) {
	return connector.Statement{}, errors.New("not used")
}

func (f *fakeConnector) Open(_ context.Context, cfg connector.Config, password []byte) (connector.Conn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, fakeOpen{host: cfg.String("host"), password: string(password)})
	c := &fakeConn{f: f, host: cfg.String("host"), password: string(password)}
	f.conns = append(f.conns, c)
	return c, nil
}

func (f *fakeConnector) opens() []fakeOpen {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeOpen(nil), f.opened...)
}

type fakeConn struct {
	f        *fakeConnector
	host     string
	password string
	mu       sync.Mutex
	closed   bool
	schemaN  int
}

func (c *fakeConn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *fakeConn) Ping(context.Context) error {
	if c.password != "right" {
		return connector.Unavailable(errors.New("password authentication failed for user \"app\""))
	}
	return nil
}

func (c *fakeConn) ServerInfo(context.Context) (connector.ServerInfo, error) {
	major := c.f.major
	if major == 0 {
		major = 18
	}
	return connector.ServerInfo{Product: "Fake", Version: "x", Major: major, Supported: major >= 14}, nil
}

func (c *fakeConn) Privileges(context.Context) (connector.PrivilegeReport, error) {
	r := connector.PrivilegeReport{User: "app", CanSelect: true, CanInsert: true, CanUpdate: true, CanDelete: true}
	if c.f.excess {
		r.Excess = []connector.ExcessPrivilege{{Privilege: "SUPERUSER", Detail: "d", Fix: "ALTER ROLE app NOSUPERUSER;"}}
	}
	return r, nil
}

func (c *fakeConn) Schema(context.Context) (connector.Schema, error) {
	c.mu.Lock()
	c.schemaN++
	c.mu.Unlock()
	return connector.Schema{Namespaces: []connector.Namespace{{Name: "public", Tables: []connector.Table{
		{Name: "users", Columns: []connector.Column{{Name: "id", Type: "integer"}, {Name: "email", Type: "text"}}, PrimaryKey: []string{"id"}},
		{Name: "orders", Columns: []connector.Column{{Name: "user_id", Type: "integer"}}},
	}}}}, nil
}

func (c *fakeConn) DryRun(context.Context, []connector.Statement, connector.Limits, connector.RunInfo) ([]connector.Result, error) {
	return nil, nil
}

func (c *fakeConn) Execute(context.Context, []connector.Statement, []connector.Expectation, connector.Limits, connector.RunInfo) ([]connector.Result, error) {
	return nil, nil
}

func (c *fakeConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

type env struct {
	svc   *Service
	fake  *fakeConnector
	st    *store.Store
	clock *clock.Fake
	ctx   context.Context
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := audit.WithActor(context.Background(), audit.Actor{UserID: "u1", Email: "ada@example.com", Via: audit.ViaWeb})
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	c := clock.NewFake(time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC))
	g := &ids.Sequential{Prefix: "id"}
	rec := audit.NewRecorder(c, g)
	sec, err := secrets.Load(ctx, secrets.Options{DataDir: dir, Store: st, Audit: rec, Clock: c, IDs: g, Logger: logx.Discard()})
	if err != nil {
		t.Fatal(err)
	}
	set, err := settings.New(ctx, settings.Options{Store: st, Audit: rec, Secrets: sec, Clock: c, Logger: logx.Discard()})
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeConnector{}
	reg := connector.NewRegistry()
	if err := reg.Register(fake); err != nil {
		t.Fatal(err)
	}
	svc, err := New(Options{Store: st, Audit: rec, Secrets: sec, Settings: set, Connectors: reg, Clock: c, IDs: g, Logger: logx.Discard()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return &env{svc: svc, fake: fake, st: st, clock: c, ctx: ctx}
}

func (e *env) create(t *testing.T, name, host, password string) Connection {
	t.Helper()
	c, err := e.svc.Create(e.ctx, CreateInput{
		Name: name, Connector: "fake", Config: connector.Config{"host": host}, Password: secrets.NewValue(password),
	})
	if err != nil {
		t.Fatalf("Create(%s): %v", name, err)
	}
	return c
}

func (e *env) auditActions(t *testing.T) []string {
	t.Helper()
	page, err := audit.NewReader(e.st.Reader()).List(e.ctx, audit.Filter{Limit: audit.MaxPageSize})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, en := range page.Entries {
		out = append(out, string(en.Action))
		if strings.Contains(string(en.Payload), "right") || strings.Contains(string(en.Payload), "s3cret") {
			t.Errorf("audit payload of %s contains a password: %s", en.Action, en.Payload)
		}
	}
	return out
}

func TestCreateUsesDefaultsAndNeverExposesThePassword(t *testing.T) {
	e := newEnv(t)
	c := e.create(t, "prod-app", "db.internal", "right")
	if c.Policy.RequiredApprovals != 1 || c.Policy.MaxRows != 100 || c.Policy.ApprovalTTLHours != 72 ||
		!c.Policy.StoreRowImages || !c.Policy.AutoExecute || len(c.Policy.AllowedFunctions) != len(connector.DefaultAllowedFunctions()) {
		t.Errorf("policy = %+v, want the policy.default_* settings", c.Policy)
	}
	if !c.Password.IsSet || c.Password.SetBy != "ada@example.com" {
		t.Errorf("password meta = %+v", c.Password)
	}
	if c.Config.Int("port") != 1 || c.CreatedBy != "ada@example.com" {
		t.Errorf("connection = %+v", c)
	}
	raw, _ := json.Marshal(c)
	if strings.Contains(string(raw), "right") {
		t.Fatalf("connection carries the password: %s", raw)
	}
	acts := e.auditActions(t)
	if !strings.Contains(strings.Join(acts, ","), "connection.created") || !strings.Contains(strings.Join(acts, ","), "secret.set") {
		t.Errorf("audit = %v", acts)
	}
}

func TestValidation(t *testing.T) {
	e := newEnv(t)
	bad := 0
	cases := []struct {
		name  string
		in    CreateInput
		field string
	}{
		{"bad name", CreateInput{Name: "Prod App", Connector: "fake", Config: connector.Config{"host": "h"}, Password: secrets.NewValue("p")}, "name"},
		{"unknown connector", CreateInput{Name: "a", Connector: "oracle", Config: connector.Config{"host": "h"}, Password: secrets.NewValue("p")}, "connector"},
		{"no password", CreateInput{Name: "a", Connector: "fake", Config: connector.Config{"host": "h"}}, "password"},
		{"password in config", CreateInput{Name: "a", Connector: "fake", Config: connector.Config{"host": "h", "password": "x"}, Password: secrets.NewValue("p")}, "config.password"},
		{"link-local host", CreateInput{Name: "a", Connector: "fake", Config: connector.Config{"host": "169.254.169.254"}, Password: secrets.NewValue("p")}, "config"},
		{"long description", CreateInput{Name: "a", Description: strings.Repeat("x", 501), Connector: "fake", Config: connector.Config{"host": "h"}, Password: secrets.NewValue("p")}, "description"},
		{"policy bounds", CreateInput{Name: "a", Connector: "fake", Config: connector.Config{"host": "h"}, Password: secrets.NewValue("p"), Policy: PolicyPatch{MaxRows: &bad}}, "policy.max_rows"},
		{"bad function", CreateInput{Name: "a", Connector: "fake", Config: connector.Config{"host": "h"}, Password: secrets.NewValue("p"), Policy: PolicyPatch{AllowedFunctions: &[]string{"pg_sleep(1)"}}}, "policy.allowed_functions"},
	}
	for _, tc := range cases {
		_, err := e.svc.Create(e.ctx, tc.in)
		var ve *ValidationError
		if !errors.As(err, &ve) || !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want a ValidationError", tc.name, err)
			continue
		}
		found := false
		for _, f := range ve.Fields {
			found = found || f.Field == tc.field
		}
		if !found {
			t.Errorf("%s: fields = %+v, want %s", tc.name, ve.Fields, tc.field)
		}
	}
	if list, _ := e.svc.List(e.ctx); len(list) != 0 {
		t.Errorf("invalid creates were stored: %+v", list)
	}
}

func TestNamesAreUniqueAcrossActiveConnections(t *testing.T) {
	e := newEnv(t)
	a := e.create(t, "app", "h1", "right")
	if _, err := e.svc.Create(e.ctx, CreateInput{Name: "app", Connector: "fake", Config: connector.Config{"host": "h2"}, Password: secrets.NewValue("x")}); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("duplicate name: %v", err)
	}
	b := e.create(t, "other", "h2", "right")
	name := "app"
	if _, err := e.svc.Update(e.ctx, b.ID, UpdateInput{Name: &name}); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("rename to a taken name: %v", err)
	}
	// Archiving hides the connection, frees its name and removes its password.
	arch, err := e.svc.Archive(e.ctx, a.ID)
	if err != nil || arch.ArchivedAt == nil || arch.Password.IsSet || arch.ArchivedBy != "ada@example.com" {
		t.Fatalf("Archive = %+v, %v", arch, err)
	}
	list, _ := e.svc.List(e.ctx)
	if len(list) != 1 || list[0].ID != b.ID {
		t.Errorf("List after archive = %+v", list)
	}
	if _, err := e.svc.Update(e.ctx, a.ID, UpdateInput{Name: &name}); !errors.Is(err, ErrArchived) {
		t.Errorf("update archived: %v", err)
	}
	if _, err := e.svc.Test(e.ctx, a.ID); !errors.Is(err, ErrArchived) {
		t.Errorf("test archived: %v", err)
	}
	if err := e.svc.WithConn(e.ctx, a.ID, func(context.Context, Connection, connector.Conn) error { return nil }); !errors.Is(err, ErrArchived) {
		t.Errorf("WithConn archived: %v", err)
	}
	e.create(t, "app", "h3", "right")
	acts := strings.Join(e.auditActions(t), ",")
	if !strings.Contains(acts, "connection.archived") || !strings.Contains(acts, "secret.cleared") {
		t.Errorf("audit = %s", acts)
	}
}

func TestPasswordChangeTakesEffectOnTheNextConnection(t *testing.T) {
	e := newEnv(t)
	a := e.create(t, "a", "host-a", "wrong")
	b := e.create(t, "b", "host-b", "right")

	res, err := e.svc.Test(e.ctx, a.ID)
	if err != nil || res.OK || !strings.Contains(res.Message, "Could not connect") || strings.Contains(res.Message, "wrong") {
		t.Fatalf("Test(a) = %+v, %v", res, err)
	}
	if res, err := e.svc.Test(e.ctx, b.ID); err != nil || !res.OK {
		t.Fatalf("Test(b) = %+v, %v", res, err)
	}
	var connB *fakeConn
	_ = e.svc.WithConn(e.ctx, b.ID, func(_ context.Context, _ Connection, c connector.Conn) error {
		connB = c.(*fakeConn)
		return nil
	})

	pw := secrets.NewValue("right")
	e.clock.Advance(time.Second)
	if _, err := e.svc.Update(e.ctx, a.ID, UpdateInput{Password: &pw}); err != nil {
		t.Fatal(err)
	}
	res, err = e.svc.Test(e.ctx, a.ID)
	if err != nil || !res.OK {
		t.Fatalf("Test(a) after the new password = %+v, %v", res, err)
	}
	got, _ := e.svc.Get(e.ctx, a.ID)
	if got.LastTest == nil || !got.LastTest.OK {
		t.Errorf("last test = %+v", got.LastTest)
	}
	// b's pool was not touched.
	_ = e.svc.WithConn(e.ctx, b.ID, func(_ context.Context, _ Connection, c connector.Conn) error {
		if c.(*fakeConn) != connB || connB.isClosed() {
			t.Error("changing a's password replaced b's pool")
		}
		return nil
	})
	var hostA []string
	for _, o := range e.fake.opens() {
		if o.host == "host-a" {
			hostA = append(hostA, o.password)
		}
	}
	if strings.Join(hostA, ",") != "wrong,right" {
		t.Errorf("opens of a = %v", hostA)
	}
	for _, c := range e.fake.conns {
		if c.host == "host-a" && c.password == "wrong" && !c.isClosed() {
			t.Error("the pool with the old password was not closed")
		}
	}
	acts := strings.Join(e.auditActions(t), ",")
	if !strings.Contains(acts, "secret.replaced") || !strings.Contains(acts, "connection.tested") {
		t.Errorf("audit = %s", acts)
	}
}

func TestRunMutexIsPerConnection(t *testing.T) {
	e := newEnv(t)
	a := e.create(t, "a", "host-a", "right")
	b := e.create(t, "b", "host-b", "right")

	holding := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- e.svc.WithConn(e.ctx, a.ID, func(context.Context, Connection, connector.Conn) error {
			close(holding)
			<-release
			return nil
		})
	}()
	<-holding

	// A slow run on a does not block b.
	ctx, cancel := context.WithTimeout(e.ctx, 2*time.Second)
	defer cancel()
	if err := e.svc.WithConn(ctx, b.ID, func(_ context.Context, c Connection, _ connector.Conn) error {
		if c.ID != b.ID {
			t.Errorf("WithConn(b) got %s", c.Name)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithConn(b) while a is busy: %v", err)
	}
	// A second run on a waits.
	short, cancel2 := context.WithTimeout(e.ctx, 50*time.Millisecond)
	defer cancel2()
	if err := e.svc.WithConn(short, a.ID, func(context.Context, Connection, connector.Conn) error { return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second WithConn(a) = %v, want to wait", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := e.svc.WithConn(e.ctx, a.ID, func(context.Context, Connection, connector.Conn) error { return nil }); err != nil {
		t.Fatalf("WithConn(a) after release: %v", err)
	}
}

func TestPoolInUseClosesAfterItsLastUser(t *testing.T) {
	e := newEnv(t)
	a := e.create(t, "a", "host-a", "right")
	err := e.svc.WithConn(e.ctx, a.ID, func(_ context.Context, _ Connection, c connector.Conn) error {
		desc := "new"
		if _, err := e.svc.Update(e.ctx, a.ID, UpdateInput{Description: &desc}); err != nil {
			return err
		}
		if c.(*fakeConn).isClosed() {
			t.Error("the pool was closed while in use")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !e.fake.conns[0].isClosed() {
		t.Error("the retired pool was not closed after use")
	}
}

func TestTestReportsWarnings(t *testing.T) {
	e := newEnv(t)
	e.fake.excess = true
	e.fake.major = 9
	res, err := e.svc.TestUnsaved(e.ctx, TestInput{Connector: "fake", Config: connector.Config{"host": "h"}, Password: ptr(secrets.NewValue("right"))})
	if err != nil || !res.OK {
		t.Fatalf("TestUnsaved = %+v, %v", res, err)
	}
	if len(res.Warnings) != 2 || res.Privileges == nil || len(res.Privileges.Excess) != 1 {
		t.Errorf("warnings = %v, privileges = %+v", res.Warnings, res.Privileges)
	}
	// Unsaved tests store nothing.
	if list, _ := e.svc.List(e.ctx); len(list) != 0 {
		t.Error("TestUnsaved stored a connection")
	}
	// With a saved connection's id and no password, its password is used.
	c := e.create(t, "a", "h", "right")
	res, err = e.svc.TestUnsaved(e.ctx, TestInput{Connector: "fake", Config: connector.Config{"host": "other"}, ConnectionID: c.ID})
	if err != nil || !res.OK {
		t.Fatalf("TestUnsaved with the saved password = %+v, %v", res, err)
	}
	if _, err := e.svc.TestUnsaved(e.ctx, TestInput{Connector: "fake", Config: connector.Config{"host": "h"}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("TestUnsaved without a password = %v", err)
	}
}

func TestSchemaCacheAndSearch(t *testing.T) {
	e := newEnv(t)
	a := e.create(t, "a", "host-a", "right")
	e.create(t, "b", "host-b", "right")
	for range 3 {
		if _, err := e.svc.Schema(e.ctx, a.ID); err != nil {
			t.Fatal(err)
		}
	}
	if n := e.fake.conns[0].schemaN; n != 1 {
		t.Errorf("schema read %d times within the TTL, want 1", n)
	}
	e.clock.Advance(SchemaTTL + time.Second)
	if _, err := e.svc.Schema(e.ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if n := e.fake.conns[0].schemaN; n != 2 {
		t.Errorf("schema read %d times after the TTL, want 2", n)
	}
	m, err := e.svc.SearchSchema(e.ctx, "USER", "")
	if err != nil {
		t.Fatal(err)
	}
	// users (table) and orders.user_id (column), in both connections.
	if len(m) != 4 {
		t.Errorf("matches = %+v", m)
	}
	m, err = e.svc.SearchSchema(e.ctx, "email", a.ID)
	if err != nil || len(m) != 1 || m[0].Column != "email" || m[0].ConnectionName != "a" {
		t.Errorf("matches = %+v, %v", m, err)
	}
}

func ptr[T any](v T) *T { return &v }
