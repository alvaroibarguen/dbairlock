package targets

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/connector"
	"github.com/alvaroibarguen/dbairlock/internal/secrets"
	"github.com/alvaroibarguen/dbairlock/internal/store"
	"github.com/alvaroibarguen/dbairlock/internal/targets/targetsdb"
)

// testTimeout bounds one connection test. Not a setting: a test connects
// (itself bounded by the connector's 10-second connect timeout), reads the
// server version and checks privileges; anything slower is the answer the
// admin needs, and it keeps the request well inside proxy timeouts.
const testTimeout = 30 * time.Second

// maxMessage bounds a test failure message.
const maxMessage = 1000

// TestInput is a connection that is not saved yet, or an edited version of
// a saved one. Password nil with ConnectionID set uses the saved password.
type TestInput struct {
	Connector    string
	Config       connector.Config
	Password     *secrets.Value
	ConnectionID string
}

// TestUnsaved tests connection settings before they are saved, so an admin
// can check them first. Nothing is stored or audited.
func (s *Service) TestUnsaved(ctx context.Context, in TestInput) (TestResult, error) {
	var e errs
	c, ok := s.connectorFor(in.Connector)
	var cfg connector.Config
	if !ok {
		e.add("connector", "Unknown connector %q.", in.Connector)
	} else {
		cfg = checkConfig(&e, c, in.Config)
	}
	var pw secrets.Value
	switch {
	case in.Password != nil:
		checkPassword(&e, *in.Password)
		pw = *in.Password
	case in.ConnectionID != "":
		saved, err := s.Get(ctx, in.ConnectionID)
		if err != nil {
			return TestResult{}, err
		}
		if saved.ArchivedAt != nil {
			return TestResult{}, ErrArchived
		}
		if saved.Connector != in.Connector {
			e.add("connector", "The connector of a saved connection cannot change.")
			break
		}
		v, err := s.opts.Secrets.Open(ctx, passwordRef(saved.ID))
		if errors.Is(err, secrets.ErrNotSet) {
			e.add("password", "A password is required.")
			break
		}
		if err != nil {
			return TestResult{}, err
		}
		pw = v
	default:
		e.add("password", "A password is required.")
	}
	if err := e.err(); err != nil {
		return TestResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	conn, err := openWith(ctx, c, cfg, pw)
	if err != nil {
		return s.failed(err), nil
	}
	defer func() { _ = conn.Close() }()
	return s.runTest(ctx, conn), nil
}

// Test tests a saved connection with its saved password, through its
// cached pool, and records the outcome as the connection's last test.
func (s *Service) Test(ctx context.Context, id string) (TestResult, error) {
	c, err := s.Get(ctx, id)
	if err != nil {
		return TestResult{}, err
	}
	if c.ArchivedAt != nil {
		return TestResult{}, ErrArchived
	}
	tctx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	var res TestResult
	p, err := s.acquire(tctx, c)
	if err != nil {
		res = s.failed(err)
	} else {
		res = s.runTest(tctx, p.conn)
		s.release(p)
	}
	if res.OK {
		// A good test is a good moment to refresh the schema cache.
		s.schemaMu.Lock()
		delete(s.schemas, id)
		s.schemaMu.Unlock()
	}

	raw, err := mustJSON(res)
	if err != nil {
		return TestResult{}, err
	}
	err = s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		if err := targetsdb.New(tx).SetLastTest(ctx, targetsdb.SetLastTestParams{
			LastTest: sql.NullString{String: raw, Valid: true}, ID: id,
		}); err != nil {
			return fmt.Errorf("targets: record test: %w", err)
		}
		payload := map[string]any{"name": c.Name, "ok": res.OK, "warnings": len(res.Warnings)}
		if res.Server != nil {
			payload["server_version"] = res.Server.Version
		}
		if res.Privileges != nil {
			payload["excess_privileges"] = len(res.Privileges.Excess)
		}
		if !res.OK {
			payload["message"] = res.Message
		}
		return s.opts.Audit.Record(ctx, tx, audit.Event{
			Action: audit.ConnectionTested, SubjectType: "connection", SubjectID: id, Payload: payload,
		})
	})
	if err != nil {
		return TestResult{}, err
	}
	return res, nil
}

func (s *Service) failed(err error) TestResult {
	return TestResult{OK: false, TestedAt: s.opts.Clock.Now(), Message: failureMessage(err), Warnings: []string{}}
}

// runTest connects, reads the server version and checks privileges
// (PLAN 5.3). Problems that do not stop the connection from working are
// warnings, not failures.
func (s *Service) runTest(ctx context.Context, conn connector.Conn) TestResult {
	if err := conn.Ping(ctx); err != nil {
		return s.failed(err)
	}
	info, err := conn.ServerInfo(ctx)
	if err != nil {
		return s.failed(err)
	}
	priv, err := conn.Privileges(ctx)
	if err != nil {
		return s.failed(err)
	}
	res := TestResult{OK: true, TestedAt: s.opts.Clock.Now(), Server: &info, Privileges: &priv, Warnings: []string{}}
	if !info.Supported {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%s %s is not a supported version. dbairlock supports the releases its vendor still maintains; it may work, but it is not tested.",
			info.Product, info.Version))
	}
	if len(priv.Excess) > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"The user %s has privileges beyond SELECT, INSERT, UPDATE and DELETE. dbairlock relies on the grant, not the parser, to prevent schema changes. Remove them with the statements shown.",
			priv.User))
	}
	var missing []string
	for _, m := range []struct {
		ok   bool
		name string
	}{{priv.CanSelect, "SELECT"}, {priv.CanInsert, "INSERT"}, {priv.CanUpdate, "UPDATE"}, {priv.CanDelete, "DELETE"}} {
		if !m.ok {
			missing = append(missing, m.name)
		}
	}
	if len(missing) > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"The user cannot %s any table in the default schema, so changes that need it will fail.",
			strings.Join(missing, ", ")))
	}
	return res
}

// failureMessage is the reason a test failed, safe to show: connector
// errors name the host, user and cause, never the password.
func failureMessage(err error) string {
	msg := err.Error()
	switch {
	case errors.Is(err, ErrNoPassword):
		msg = "The connection has no password. Set one and test again."
	case errors.Is(err, context.DeadlineExceeded):
		msg = "The test timed out after " + testTimeout.String() + ": " + msg
	case errors.Is(err, connector.ErrUnavailable):
		msg = "Could not connect: " + strings.TrimPrefix(msg, connector.ErrUnavailable.Error()+": ")
	}
	if len(msg) > maxMessage {
		msg = strings.ToValidUTF8(msg[:maxMessage], "") + "…"
	}
	return msg
}
