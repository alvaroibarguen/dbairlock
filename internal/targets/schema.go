package targets

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/connector"
)

// SchemaTTL is how long an introspected schema is reused.
const SchemaTTL = 60 * time.Second

// maxSearchResults bounds SearchSchema.
const maxSearchResults = 100

type cachedSchema struct {
	schema connector.Schema
	key    int64 // Connection.UpdatedAt when it was read
	at     time.Time
}

// Schema returns the schema of connection id: its namespaces, tables,
// columns and primary keys. It is cached for SchemaTTL, per connection.
func (s *Service) Schema(ctx context.Context, id string) (connector.Schema, error) {
	c, err := s.Get(ctx, id)
	if err != nil {
		return connector.Schema{}, err
	}
	return s.schemaOf(ctx, c)
}

func (s *Service) schemaOf(ctx context.Context, c Connection) (connector.Schema, error) {
	if c.ArchivedAt != nil {
		return connector.Schema{}, ErrArchived
	}
	key := c.UpdatedAt.UnixMilli()
	now := s.opts.Clock.Now()
	s.schemaMu.Lock()
	cached, ok := s.schemas[c.ID]
	s.schemaMu.Unlock()
	if ok && cached.key == key && now.Sub(cached.at) < SchemaTTL {
		return cached.schema, nil
	}
	p, err := s.acquire(ctx, c)
	if err != nil {
		return connector.Schema{}, err
	}
	defer s.release(p)
	sc, err := p.conn.Schema(ctx)
	if err != nil {
		return connector.Schema{}, err
	}
	if sc.Namespaces == nil {
		sc.Namespaces = []connector.Namespace{}
	}
	s.schemaMu.Lock()
	s.schemas[c.ID] = cachedSchema{schema: sc, key: key, at: now}
	s.schemaMu.Unlock()
	return sc, nil
}

// SchemaMatch is one table or column whose name matched a search.
type SchemaMatch struct {
	ConnectionID   string
	ConnectionName string
	Namespace      string
	Table          string
	// Column is empty when the table name itself matched.
	Column string
	Type   string
}

// SearchSchema finds tables and columns whose names contain query (case
// insensitive), across every active connection or only connection id. A
// connection whose schema cannot be read is skipped. At most 100 matches
// are returned.
func (s *Service) SearchSchema(ctx context.Context, query, id string) ([]SchemaMatch, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	out := []SchemaMatch{}
	if q == "" {
		return out, nil
	}
	var conns []Connection
	if id != "" {
		c, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		conns = []Connection{c}
	} else {
		all, err := s.List(ctx)
		if err != nil {
			return nil, err
		}
		conns = all
	}
	for _, c := range conns {
		sc, err := s.schemaOf(ctx, c)
		if err != nil {
			if id != "" {
				return nil, err
			}
			s.opts.Logger.Warn("schema search skipped a connection", slog.String("connection", c.Name), slog.Any("error", err))
			continue
		}
		for _, ns := range sc.Namespaces {
			for _, t := range ns.Tables {
				m := SchemaMatch{ConnectionID: c.ID, ConnectionName: c.Name, Namespace: ns.Name, Table: t.Name}
				if strings.Contains(strings.ToLower(t.Name), q) {
					out = append(out, m)
				}
				for _, col := range t.Columns {
					if strings.Contains(strings.ToLower(col.Name), q) {
						cm := m
						cm.Column, cm.Type = col.Name, col.Type
						out = append(out, cm)
					}
				}
				if len(out) >= maxSearchResults {
					return out[:maxSearchResults], nil
				}
			}
		}
	}
	return out, nil
}
