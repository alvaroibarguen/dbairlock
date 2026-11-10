package targets

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/connector"
	"github.com/alvaroibarguen/dbairlock/internal/secrets"
	"github.com/alvaroibarguen/dbairlock/internal/store"
	"github.com/alvaroibarguen/dbairlock/internal/targets/targetsdb"
)

// PolicyPatch changes some policy fields; nil fields are unchanged (or, on
// create, taken from the policy.default_* settings).
type PolicyPatch struct {
	RequiredApprovals *int
	MaxRows           *int
	ApprovalTTLHours  *int
	StoreRowImages    *bool
	AutoExecute       *bool
	AllowedFunctions  *[]string
}

// CreateInput is a new connection.
type CreateInput struct {
	Name        string
	Description string
	Connector   string
	// Config holds the connector's non-secret fields.
	Config   connector.Config
	Password secrets.Value
	Policy   PolicyPatch
}

// UpdateInput changes a connection; nil fields are unchanged. Config, when
// not nil, replaces the whole config.
type UpdateInput struct {
	Name        *string
	Description *string
	Config      connector.Config
	Password    *secrets.Value
	Policy      PolicyPatch
}

var (
	namePattern     = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9_-]{0,61}[a-z0-9])?$`)
	functionPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)
)

const msgName = "Use 1 to 63 lower-case letters, digits, hyphens and underscores, starting and ending with a letter or digit."

// errs collects field errors.
type errs []FieldError

func (e *errs) add(field, format string, args ...any) {
	*e = append(*e, FieldError{Field: field, Message: fmt.Sprintf(format, args...)})
}

func (e errs) err() error {
	if len(e) == 0 {
		return nil
	}
	return &ValidationError{Fields: e}
}

func checkName(e *errs, name string) {
	if !namePattern.MatchString(name) {
		e.add("name", msgName)
	}
}

func checkDescription(e *errs, d string) {
	if !utf8.ValidString(d) || utf8.RuneCountInString(d) > maxDescription {
		e.add("description", "Use at most %d characters.", maxDescription)
	}
}

func checkPassword(e *errs, v secrets.Value) {
	switch {
	case v.IsEmpty():
		e.add("password", "A password is required.")
	case v.Len() > secrets.MaxValueBytes:
		e.add("password", "The password is too long.")
	}
}

// applyPolicy applies patch to p and validates the result.
func applyPolicy(e *errs, p Policy, patch PolicyPatch) Policy {
	if patch.RequiredApprovals != nil {
		p.RequiredApprovals = *patch.RequiredApprovals
	}
	if patch.MaxRows != nil {
		p.MaxRows = *patch.MaxRows
	}
	if patch.ApprovalTTLHours != nil {
		p.ApprovalTTLHours = *patch.ApprovalTTLHours
	}
	if patch.StoreRowImages != nil {
		p.StoreRowImages = *patch.StoreRowImages
	}
	if patch.AutoExecute != nil {
		p.AutoExecute = *patch.AutoExecute
	}
	if patch.AllowedFunctions != nil {
		p.AllowedFunctions = *patch.AllowedFunctions
	}
	bound := func(field string, v, lo, hi int) {
		if v < lo || v > hi {
			e.add("policy."+field, "Must be between %d and %d.", lo, hi)
		}
	}
	bound("required_approvals", p.RequiredApprovals, minApprovals, maxApprovals)
	bound("max_rows", p.MaxRows, minMaxRows, maxMaxRows)
	bound("approval_ttl_hours", p.ApprovalTTLHours, minTTLHours, maxTTLHours)

	seen := map[string]bool{}
	fns := []string{}
	for _, f := range p.AllowedFunctions {
		f = strings.ToLower(strings.TrimSpace(f))
		if f == "" || seen[f] {
			continue
		}
		if !functionPattern.MatchString(f) {
			e.add("policy.allowed_functions", "%q is not a function name.", f)
			continue
		}
		seen[f] = true
		fns = append(fns, f)
	}
	if len(fns) > maxAllowedFunctions {
		e.add("policy.allowed_functions", "Allow at most %d functions.", maxAllowedFunctions)
	}
	p.AllowedFunctions = sortedCopy(fns)
	return p
}

// checkConfig validates cfg with the connector and returns the normalized
// config.
func checkConfig(e *errs, c connector.Connector, cfg connector.Config) connector.Config {
	if cfg == nil {
		cfg = connector.Config{}
	}
	if key := passwordKey(c); key != "" {
		if _, ok := cfg[key]; ok {
			e.add("config."+key, "The password is a secret: send it as the password, never in the config.")
			return nil
		}
	}
	out, err := c.ValidateConfig(cfg)
	if err != nil {
		e.add("config", "%s", configMessage(err))
		return nil
	}
	return out
}

// configMessage turns a connector's config error into a sentence.
func configMessage(err error) string {
	msg := err.Error()
	if msg == "" {
		return "The connection settings are invalid."
	}
	msg = strings.ToUpper(msg[:1]) + msg[1:]
	if !strings.HasSuffix(msg, ".") {
		msg += "."
	}
	return msg
}

func (s *Service) connectorFor(id string) (connector.Connector, bool) {
	return s.opts.Connectors.Get(id)
}

func actorLabel(ctx context.Context) string {
	a, _ := audit.ActorFrom(ctx)
	return a.Label()
}

func ms(t time.Time) int64 { return t.UnixMilli() }

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func mustJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("targets: encode: %w", err)
	}
	return string(b), nil
}

// Create adds a connection. Its policy starts from the policy.default_*
// settings, with any fields in in.Policy applied on top.
func (s *Service) Create(ctx context.Context, in CreateInput) (Connection, error) {
	var e errs
	in.Name = strings.TrimSpace(in.Name)
	checkName(&e, in.Name)
	checkDescription(&e, in.Description)
	checkPassword(&e, in.Password)
	var cfg connector.Config
	c, ok := s.connectorFor(in.Connector)
	if !ok {
		e.add("connector", "Unknown connector %q.", in.Connector)
	} else {
		cfg = checkConfig(&e, c, in.Config)
	}
	policy := applyPolicy(&e, s.defaultPolicy(), in.Policy)
	if err := e.err(); err != nil {
		return Connection{}, err
	}
	cfgJSON, err := mustJSON(cfg)
	if err != nil {
		return Connection{}, err
	}
	fnJSON, err := mustJSON(policy.AllowedFunctions)
	if err != nil {
		return Connection{}, err
	}
	id := s.opts.IDs.NewID()
	now := ms(s.opts.Clock.Now())
	err = s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		q := targetsdb.New(tx)
		if err := checkNameFree(ctx, q, in.Name, id); err != nil {
			return err
		}
		if err := q.InsertConnection(ctx, targetsdb.InsertConnectionParams{
			ID: id, Name: in.Name, Description: in.Description, Connector: in.Connector, Config: cfgJSON,
			RequiredApprovals: int64(policy.RequiredApprovals), MaxRows: int64(policy.MaxRows),
			StoreRowImages: boolInt(policy.StoreRowImages), AutoExecute: boolInt(policy.AutoExecute),
			ApprovalTtlHours: int64(policy.ApprovalTTLHours), AllowedFunctions: fnJSON,
			CreatedBy: actorLabel(ctx), CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return fmt.Errorf("targets: insert connection: %w", err)
		}
		if err := s.opts.Secrets.Set(ctx, tx, passwordRef(id), in.Password); err != nil {
			return err
		}
		return s.opts.Audit.Record(ctx, tx, audit.Event{
			Action: audit.ConnectionCreated, SubjectType: "connection", SubjectID: id,
			Payload: map[string]any{
				"name": in.Name, "description": in.Description, "connector": in.Connector,
				"config": cfg, "policy": policy,
			},
		})
	})
	if err != nil {
		return Connection{}, err
	}
	return s.Get(ctx, id)
}

func checkNameFree(ctx context.Context, q *targetsdb.Queries, name, id string) error {
	n, err := q.CountActiveByName(ctx, targetsdb.CountActiveByNameParams{Name: name, ID: id})
	if err != nil {
		return fmt.Errorf("targets: check name: %w", err)
	}
	if n > 0 {
		return ErrNameTaken
	}
	return nil
}

// Update changes a connection. Changing the config or the password closes
// the cached connection pool of this connection only.
func (s *Service) Update(ctx context.Context, id string, in UpdateInput) (Connection, error) {
	err := s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		q := targetsdb.New(tx)
		row, err := q.GetConnection(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("targets: read connection: %w", err)
		}
		if row.ArchivedAt.Valid {
			return ErrArchived
		}
		cur, err := fromRow(row)
		if err != nil {
			return err
		}
		c, ok := s.connectorFor(cur.Connector)
		if !ok {
			return fmt.Errorf("targets: connection %s uses connector %q, which is not registered", id, cur.Connector)
		}

		var e errs
		next := cur
		changed := []string{}
		if in.Name != nil {
			next.Name = strings.TrimSpace(*in.Name)
			checkName(&e, next.Name)
		}
		if in.Description != nil {
			next.Description = *in.Description
			checkDescription(&e, next.Description)
		}
		if in.Config != nil {
			next.Config = checkConfig(&e, c, in.Config)
		}
		if in.Password != nil {
			checkPassword(&e, *in.Password)
		}
		next.Policy = applyPolicy(&e, cur.Policy, in.Policy)
		if err := e.err(); err != nil {
			return err
		}

		if next.Name != cur.Name {
			changed = append(changed, "name")
			if err := checkNameFree(ctx, q, next.Name, id); err != nil {
				return err
			}
		}
		if next.Description != cur.Description {
			changed = append(changed, "description")
		}
		if !configEqual(next.Config, cur.Config) {
			changed = append(changed, "config")
		}
		if !policyEqual(next.Policy, cur.Policy) {
			changed = append(changed, "policy")
		}
		if in.Password != nil {
			changed = append(changed, "password")
		}
		if len(changed) == 0 {
			return nil
		}
		cfgJSON, err := mustJSON(next.Config)
		if err != nil {
			return err
		}
		fnJSON, err := mustJSON(next.Policy.AllowedFunctions)
		if err != nil {
			return err
		}
		now := ms(s.opts.Clock.Now())
		if now < row.UpdatedAt {
			now = row.UpdatedAt
		}
		if now == row.UpdatedAt {
			now++ // the pool key must change
		}
		p := next.Policy
		if err := q.UpdateConnection(ctx, targetsdb.UpdateConnectionParams{
			Name: next.Name, Description: next.Description, Config: cfgJSON,
			RequiredApprovals: int64(p.RequiredApprovals), MaxRows: int64(p.MaxRows),
			StoreRowImages: boolInt(p.StoreRowImages), AutoExecute: boolInt(p.AutoExecute),
			ApprovalTtlHours: int64(p.ApprovalTTLHours), AllowedFunctions: fnJSON,
			UpdatedAt: now, ID: id,
		}); err != nil {
			return fmt.Errorf("targets: update connection: %w", err)
		}
		if in.Password != nil {
			if err := s.opts.Secrets.Set(ctx, tx, passwordRef(id), *in.Password); err != nil {
				return err
			}
		}
		payload := map[string]any{"name": next.Name, "changed": changed}
		if next.Name != cur.Name {
			payload["old_name"] = cur.Name
		}
		if slices.Contains(changed, "description") {
			payload["description"] = next.Description
		}
		if slices.Contains(changed, "config") {
			payload["config"] = next.Config
		}
		if slices.Contains(changed, "policy") {
			payload["policy"] = next.Policy
		}
		return s.opts.Audit.Record(ctx, tx, audit.Event{
			Action: audit.ConnectionUpdated, SubjectType: "connection", SubjectID: id, Payload: payload,
		})
	})
	if err != nil {
		return Connection{}, err
	}
	s.invalidate(id)
	return s.Get(ctx, id)
}

// Archive hides a connection. Its history remains; its name becomes free;
// its password is removed from the secret store and its pool closed. An
// archived connection cannot be changed or used.
func (s *Service) Archive(ctx context.Context, id string) (Connection, error) {
	err := s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		q := targetsdb.New(tx)
		row, err := q.GetConnection(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("targets: read connection: %w", err)
		}
		if row.ArchivedAt.Valid {
			return nil
		}
		now := ms(s.opts.Clock.Now())
		if now < row.CreatedAt {
			now = row.CreatedAt
		}
		if err := q.ArchiveConnection(ctx, targetsdb.ArchiveConnectionParams{
			ArchivedAt: sql.NullInt64{Int64: now, Valid: true}, ArchivedBy: actorLabel(ctx), ID: id,
		}); err != nil {
			return fmt.Errorf("targets: archive connection: %w", err)
		}
		if err := s.opts.Secrets.Clear(ctx, tx, passwordRef(id)); err != nil {
			return err
		}
		return s.opts.Audit.Record(ctx, tx, audit.Event{
			Action: audit.ConnectionArchived, SubjectType: "connection", SubjectID: id,
			Payload: map[string]any{"name": row.Name, "connector": row.Connector},
		})
	})
	if err != nil {
		return Connection{}, err
	}
	s.invalidate(id)
	return s.Get(ctx, id)
}

// Get returns one connection, archived or not.
func (s *Service) Get(ctx context.Context, id string) (Connection, error) {
	row, err := targetsdb.New(s.opts.Store.Reader()).GetConnection(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Connection{}, ErrNotFound
	}
	if err != nil {
		return Connection{}, fmt.Errorf("targets: read connection: %w", err)
	}
	return s.complete(ctx, row)
}

// List returns every connection that is not archived, ordered by connector
// and name.
func (s *Service) List(ctx context.Context) ([]Connection, error) {
	rows, err := targetsdb.New(s.opts.Store.Reader()).ListActiveConnections(ctx)
	if err != nil {
		return nil, fmt.Errorf("targets: list connections: %w", err)
	}
	out := make([]Connection, 0, len(rows))
	for _, r := range rows {
		c, err := s.complete(ctx, r)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// complete converts a row and adds the password status.
func (s *Service) complete(ctx context.Context, row targetsdb.Connection) (Connection, error) {
	c, err := fromRow(row)
	if err != nil {
		return Connection{}, err
	}
	meta, err := s.opts.Secrets.Describe(ctx, passwordRef(c.ID))
	if err != nil {
		return Connection{}, err
	}
	c.Password = meta
	return c, nil
}

func fromRow(r targetsdb.Connection) (Connection, error) {
	c := Connection{
		ID: r.ID, Name: r.Name, Description: r.Description, Connector: r.Connector,
		Policy: Policy{
			RequiredApprovals: int(r.RequiredApprovals), MaxRows: int(r.MaxRows),
			ApprovalTTLHours: int(r.ApprovalTtlHours),
			StoreRowImages:   r.StoreRowImages != 0, AutoExecute: r.AutoExecute != 0,
		},
		CreatedBy: r.CreatedBy, CreatedAt: time.UnixMilli(r.CreatedAt).UTC(),
		UpdatedAt: time.UnixMilli(r.UpdatedAt).UTC(), ArchivedBy: r.ArchivedBy,
	}
	if err := json.Unmarshal([]byte(r.Config), &c.Config); err != nil {
		return Connection{}, fmt.Errorf("targets: decode config of %s: %w", r.ID, err)
	}
	if err := json.Unmarshal([]byte(r.AllowedFunctions), &c.Policy.AllowedFunctions); err != nil {
		return Connection{}, fmt.Errorf("targets: decode allowed functions of %s: %w", r.ID, err)
	}
	if c.Policy.AllowedFunctions == nil {
		c.Policy.AllowedFunctions = []string{}
	}
	if r.ArchivedAt.Valid {
		t := time.UnixMilli(r.ArchivedAt.Int64).UTC()
		c.ArchivedAt = &t
	}
	if r.LastTest.Valid {
		var tr TestResult
		if err := json.Unmarshal([]byte(r.LastTest.String), &tr); err != nil {
			return Connection{}, fmt.Errorf("targets: decode last test of %s: %w", r.ID, err)
		}
		c.LastTest = &tr
	}
	return c, nil
}

// configEqual compares two configs by their JSON encoding, so an int and
// the float64 it decodes to from the store compare equal.
func configEqual(a, b connector.Config) bool {
	ja, err1 := json.Marshal(a)
	jb, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(ja) == string(jb)
}

func policyEqual(a, b Policy) bool {
	return a.RequiredApprovals == b.RequiredApprovals && a.MaxRows == b.MaxRows &&
		a.ApprovalTTLHours == b.ApprovalTTLHours && a.StoreRowImages == b.StoreRowImages &&
		a.AutoExecute == b.AutoExecute && slices.Equal(a.AllowedFunctions, b.AllowedFunctions)
}

// cloneConfig copies cfg so callers cannot change a cached one.
func cloneConfig(cfg connector.Config) connector.Config {
	return maps.Clone(cfg)
}
