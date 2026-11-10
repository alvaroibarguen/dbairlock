package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/alvaroibarguen/dbairlock/internal/connector"
	"github.com/alvaroibarguen/dbairlock/internal/identity"
	"github.com/alvaroibarguen/dbairlock/internal/targets"
)

// Size caps that keep results small enough for a model's context.
const (
	maxSchemaTables = 50   // get_schema without a table
	maxListTables   = 1000 // list_tables
	maxFindMatches  = 50   // find_tables
)

// The four discovery tools go from broad to narrow. They read the schema
// cache in targets and never query row data. Reading schemas is authorized
// like the dashboard's schema view: change.submit on the connection.

// --- list_connections ---

type listConnectionsIn struct{}

// SchemaSummary is one schema of a connection and its table count.
type SchemaSummary struct {
	Name   string `json:"name" jsonschema:"Schema name (a Postgres schema or the MySQL database)"`
	Tables int    `json:"tables" jsonschema:"Number of tables in the schema"`
}

// ConnectionInfo is one connection in list_connections.
type ConnectionInfo struct {
	Name              string          `json:"name" jsonschema:"Connection name; pass it as connection to the other tools"`
	Connector         string          `json:"connector" jsonschema:"Database product: postgres or mysql"`
	Description       string          `json:"description" jsonschema:"What the database is for, written by an admin"`
	SchemaStatus      string          `json:"schema_status" jsonschema:"available, or unavailable when the database could not be reached just now"`
	Schemas           []SchemaSummary `json:"schemas" jsonschema:"Schemas with their table counts (empty when the schema is unavailable)"`
	MaxRowsPerChange  int             `json:"max_rows_per_change" jsonschema:"Most rows one statement of a change may affect"`
	ApprovalsRequired int             `json:"approvals_required" jsonschema:"Approvals from people other than the author needed before the change runs"`
}

type listConnectionsOut struct {
	Connections []ConnectionInfo `json:"connections"`
}

// --- list_tables ---

type listTablesIn struct {
	Connection string `json:"connection" jsonschema:"Connection name from list_connections"`
	Schema     string `json:"schema,omitempty" jsonschema:"Only this schema (optional)"`
}

// TableSummary is one table in list_tables.
type TableSummary struct {
	Name       string   `json:"name"`
	Columns    int      `json:"columns" jsonschema:"Number of columns"`
	PrimaryKey []string `json:"primary_key" jsonschema:"Primary key columns; empty when the table has none"`
}

// SchemaTables is one schema's tables in list_tables.
type SchemaTables struct {
	Name   string         `json:"name"`
	Tables []TableSummary `json:"tables"`
}

type listTablesOut struct {
	Connection string         `json:"connection"`
	Schemas    []SchemaTables `json:"schemas"`
	Truncated  bool           `json:"truncated" jsonschema:"True when not every table is listed"`
	Note       string         `json:"note,omitempty"`
}

// --- get_schema ---

type getSchemaIn struct {
	Connection string `json:"connection" jsonschema:"Connection name from list_connections"`
	Schema     string `json:"schema,omitempty" jsonschema:"Only this schema (optional)"`
	Table      string `json:"table,omitempty" jsonschema:"Only this table (optional; recommended on large databases)"`
}

// ColumnInfo is one column.
type ColumnInfo struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
}

// TableInfo is one table with its columns.
type TableInfo struct {
	Name       string       `json:"name"`
	PrimaryKey []string     `json:"primary_key" jsonschema:"Primary key columns; empty when the table has none"`
	Columns    []ColumnInfo `json:"columns"`
}

// SchemaDetail is one schema's tables in get_schema.
type SchemaDetail struct {
	Name   string      `json:"name"`
	Tables []TableInfo `json:"tables"`
}

type getSchemaOut struct {
	Connection string         `json:"connection"`
	Connector  string         `json:"connector"`
	Schemas    []SchemaDetail `json:"schemas"`
	Truncated  bool           `json:"truncated" jsonschema:"True when only the first tables are included"`
	Note       string         `json:"note,omitempty"`
}

// --- find_tables ---

type findTablesIn struct {
	Query      string `json:"query" jsonschema:"Part of a table or column name, for example display_name"`
	Connection string `json:"connection,omitempty" jsonschema:"Search only this connection (optional; default all)"`
}

// TableMatch is a table whose name, or some of whose column names, match.
type TableMatch struct {
	Connection     string   `json:"connection"`
	Schema         string   `json:"schema"`
	Table          string   `json:"table"`
	TableMatched   bool     `json:"table_matched" jsonschema:"True when the table name itself matches"`
	MatchedColumns []string `json:"matched_columns" jsonschema:"Columns whose names match"`
}

type findTablesOut struct {
	Matches     []TableMatch `json:"matches"`
	Truncated   bool         `json:"truncated" jsonschema:"True when there were more matches than listed"`
	Unavailable []string     `json:"unavailable_connections" jsonschema:"Connections whose schema could not be read just now"`
}

func readOnly(title string) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, IdempotentHint: true}
}

func (s *Server) registerDiscovery() {
	addTool(s, tool[listConnectionsIn, listConnectionsOut]{
		def: &mcp.Tool{
			Name: "list_connections",
			Description: "Start here. Lists every database connection you can propose changes to: its name, connector (postgres or mysql), " +
				"what it is for, its schemas with table counts, the most rows a change may affect and how many human approvals it needs. " +
				"A connection whose database cannot be reached is still listed, with schema_status unavailable. Then use find_tables or list_tables.",
			Annotations: readOnly("List database connections"),
		},
		run: s.listConnections,
	})
	addTool(s, tool[listTablesIn, listTablesOut]{
		def: &mcp.Tool{
			Name: "list_tables",
			Description: "Lists the tables of one connection, grouped by schema, with each table's column count and primary key. " +
				"No column detail, so it stays small on large databases; use get_schema with a table for its columns.",
			Annotations: readOnly("List tables"),
		},
		args: func(in listTablesIn) map[string]any { return map[string]any{"connection": in.Connection} },
		run:  s.listTables,
	})
	addTool(s, tool[getSchemaIn, getSchemaOut]{
		def: &mcp.Tool{
			Name: "get_schema",
			Description: "Returns full schema detail of a connection: tables, columns, types, nullability and primary keys. " +
				fmt.Sprintf("Pass a table to get just that table. Without one, at most %d tables are returned; narrow by schema or table to see the rest. ", maxSchemaTables) +
				"Never returns row data.",
			Annotations: readOnly("Get schema"),
		},
		args: func(in getSchemaIn) map[string]any {
			return map[string]any{"connection": in.Connection, "schema": in.Schema, "table": in.Table}
		},
		run: s.getSchema,
	})
	addTool(s, tool[findTablesIn, findTablesOut]{
		def: &mcp.Tool{
			Name: "find_tables",
			Description: "Finds tables and columns whose names contain the query (case insensitive), across all connections or one. " +
				fmt.Sprintf("The shortcut for questions like \"where does display_name live?\". Returns up to %d tables with the matching column names.", maxFindMatches),
			Annotations: readOnly("Find tables and columns"),
		},
		args: func(in findTablesIn) map[string]any {
			return map[string]any{"connection": in.Connection, "query": in.Query}
		},
		run: s.findTables,
	})
}

func canReadSchemas(p identity.Principal, id string) error {
	return identity.Can(p, identity.ActionChangeSubmit, identity.Resource{Type: "connection", ID: id})
}

func (s *Server) listConnections(ctx context.Context, c call[listConnectionsIn]) (result[listConnectionsOut], error) {
	if err := canReadSchemas(c.p, ""); err != nil {
		return result[listConnectionsOut]{}, err
	}
	list, err := s.opts.Targets.List(ctx)
	if err != nil {
		return result[listConnectionsOut]{}, err
	}
	out := listConnectionsOut{Connections: make([]ConnectionInfo, len(list))}
	// Schemas are read concurrently: an unreachable database costs its
	// connect timeout once, not once per connection before it.
	var wg sync.WaitGroup
	for i, conn := range list {
		info := ConnectionInfo{
			Name: conn.Name, Connector: conn.Connector, Description: conn.Description,
			Schemas: []SchemaSummary{}, MaxRowsPerChange: conn.Policy.MaxRows,
			ApprovalsRequired: conn.Policy.RequiredApprovals, SchemaStatus: "available",
		}
		out.Connections[i] = info
		wg.Go(func() {
			sc, err := s.opts.Targets.Schema(ctx, conn.ID)
			if err != nil {
				out.Connections[i].SchemaStatus = "unavailable"
				return
			}
			for _, ns := range sc.Namespaces {
				out.Connections[i].Schemas = append(out.Connections[i].Schemas, SchemaSummary{Name: ns.Name, Tables: len(ns.Tables)})
			}
		})
	}
	wg.Wait()
	summary := fmt.Sprintf("%d connections.", len(list))
	if len(list) == 0 {
		summary = "No connections are configured yet. An admin adds them in the dashboard."
	}
	return result[listConnectionsOut]{out: out, summary: summary}, nil
}

// schemaOf resolves a connection by name and reads its schema.
func (s *Server) schemaOf(ctx context.Context, p identity.Principal, name string) (targets.Connection, connector.Schema, error) {
	conn, err := s.connection(ctx, name)
	if err != nil {
		return conn, connector.Schema{}, err
	}
	if err := canReadSchemas(p, conn.ID); err != nil {
		return conn, connector.Schema{}, err
	}
	sc, err := s.opts.Targets.Schema(ctx, conn.ID)
	if err != nil {
		if errors.Is(err, targets.ErrArchived) || errors.Is(err, targets.ErrNotFound) {
			return conn, connector.Schema{}, err
		}
		return conn, connector.Schema{}, userErrorf("The schema of %q could not be read because the database could not be reached. Try again in a few minutes.", conn.Name)
	}
	return conn, sc, nil
}

// pickNamespaces returns the namespaces named schema (all when empty).
func pickNamespaces(conn targets.Connection, sc connector.Schema, schema string) ([]connector.Namespace, error) {
	if schema == "" {
		return sc.Namespaces, nil
	}
	for _, ns := range sc.Namespaces {
		if ns.Name == schema {
			return []connector.Namespace{ns}, nil
		}
	}
	for _, ns := range sc.Namespaces {
		if strings.EqualFold(ns.Name, schema) {
			return []connector.Namespace{ns}, nil
		}
	}
	names := make([]string, len(sc.Namespaces))
	for i, ns := range sc.Namespaces {
		names[i] = ns.Name
	}
	return nil, userErrorf("Connection %q has no schema %q. Its schemas are: %s.", conn.Name, schema, strings.Join(names, ", "))
}

func (s *Server) listTables(ctx context.Context, c call[listTablesIn]) (result[listTablesOut], error) {
	conn, sc, err := s.schemaOf(ctx, c.p, c.in.Connection)
	if err != nil {
		return result[listTablesOut]{}, err
	}
	nss, err := pickNamespaces(conn, sc, c.in.Schema)
	if err != nil {
		return result[listTablesOut]{}, err
	}
	out := listTablesOut{Connection: conn.Name, Schemas: []SchemaTables{}}
	n := 0
	for _, ns := range nss {
		st := SchemaTables{Name: ns.Name, Tables: []TableSummary{}}
		for _, t := range ns.Tables {
			if n == maxListTables {
				out.Truncated = true
				break
			}
			st.Tables = append(st.Tables, TableSummary{Name: t.Name, Columns: len(t.Columns), PrimaryKey: nonNil(t.PrimaryKey)})
			n++
		}
		out.Schemas = append(out.Schemas, st)
	}
	if out.Truncated {
		out.Note = fmt.Sprintf("Only the first %d tables are listed. Pass schema, or use find_tables.", maxListTables)
	}
	return result[listTablesOut]{out: out, summary: fmt.Sprintf("%d tables in %q.", n, conn.Name)}, nil
}

func (s *Server) getSchema(ctx context.Context, c call[getSchemaIn]) (result[getSchemaOut], error) {
	conn, sc, err := s.schemaOf(ctx, c.p, c.in.Connection)
	if err != nil {
		return result[getSchemaOut]{}, err
	}
	nss, err := pickNamespaces(conn, sc, c.in.Schema)
	if err != nil {
		return result[getSchemaOut]{}, err
	}
	out := getSchemaOut{Connection: conn.Name, Connector: conn.Connector, Schemas: []SchemaDetail{}}
	table := strings.TrimSpace(c.in.Table)
	if table != "" {
		found := findTable(nss, table, func(a, b string) bool { return a == b })
		if len(found) == 0 {
			found = findTable(nss, table, strings.EqualFold)
		}
		if len(found) == 0 {
			return result[getSchemaOut]{}, userErrorf("Connection %q has no table %q. Use find_tables to search by name.", conn.Name, table)
		}
		out.Schemas = found
		return result[getSchemaOut]{out: out, summary: fmt.Sprintf("Table %q in %q.", table, conn.Name)}, nil
	}
	n := 0
	for _, ns := range nss {
		sd := SchemaDetail{Name: ns.Name, Tables: []TableInfo{}}
		for _, t := range ns.Tables {
			if n == maxSchemaTables {
				out.Truncated = true
				break
			}
			sd.Tables = append(sd.Tables, tableInfo(t))
			n++
		}
		out.Schemas = append(out.Schemas, sd)
	}
	if out.Truncated {
		out.Note = fmt.Sprintf("Only the first %d tables are included. Call get_schema with a table (or a schema) to narrow the request; list_tables lists every table.", maxSchemaTables)
	}
	return result[getSchemaOut]{out: out, summary: fmt.Sprintf("%d tables in %q.", n, conn.Name)}, nil
}

func findTable(nss []connector.Namespace, name string, eq func(a, b string) bool) []SchemaDetail {
	var out []SchemaDetail
	for _, ns := range nss {
		for _, t := range ns.Tables {
			if eq(t.Name, name) {
				out = append(out, SchemaDetail{Name: ns.Name, Tables: []TableInfo{tableInfo(t)}})
			}
		}
	}
	return out
}

func tableInfo(t connector.Table) TableInfo {
	ti := TableInfo{Name: t.Name, PrimaryKey: nonNil(t.PrimaryKey), Columns: make([]ColumnInfo, len(t.Columns))}
	for i, col := range t.Columns {
		ti.Columns[i] = ColumnInfo{Name: col.Name, Type: col.Type, Nullable: col.Nullable}
	}
	return ti
}

func (s *Server) findTables(ctx context.Context, c call[findTablesIn]) (result[findTablesOut], error) {
	q := strings.ToLower(strings.TrimSpace(c.in.Query))
	if q == "" {
		return result[findTablesOut]{}, agentErr("Pass a query: part of a table or column name.")
	}
	var conns []targets.Connection
	if c.in.Connection != "" {
		conn, err := s.connection(ctx, c.in.Connection)
		if err != nil {
			return result[findTablesOut]{}, err
		}
		conns = []targets.Connection{conn}
	} else {
		list, err := s.opts.Targets.List(ctx)
		if err != nil {
			return result[findTablesOut]{}, err
		}
		conns = list
	}
	type found struct {
		sc  connector.Schema
		err error
	}
	schemas := make([]found, len(conns))
	var wg sync.WaitGroup
	for i, conn := range conns {
		if err := canReadSchemas(c.p, conn.ID); err != nil {
			return result[findTablesOut]{}, err
		}
		wg.Go(func() {
			sc, err := s.opts.Targets.Schema(ctx, conn.ID)
			schemas[i] = found{sc: sc, err: err}
		})
	}
	wg.Wait()
	out := findTablesOut{Matches: []TableMatch{}, Unavailable: []string{}}
	for i, conn := range conns {
		if schemas[i].err != nil {
			out.Unavailable = append(out.Unavailable, conn.Name)
			continue
		}
		for _, ns := range schemas[i].sc.Namespaces {
			for _, t := range ns.Tables {
				m := TableMatch{Connection: conn.Name, Schema: ns.Name, Table: t.Name, MatchedColumns: []string{},
					TableMatched: strings.Contains(strings.ToLower(t.Name), q)}
				for _, col := range t.Columns {
					if strings.Contains(strings.ToLower(col.Name), q) {
						m.MatchedColumns = append(m.MatchedColumns, col.Name)
					}
				}
				if !m.TableMatched && len(m.MatchedColumns) == 0 {
					continue
				}
				if len(out.Matches) == maxFindMatches {
					out.Truncated = true
					continue
				}
				out.Matches = append(out.Matches, m)
			}
		}
	}
	summary := fmt.Sprintf("%d tables match %q.", len(out.Matches), c.in.Query)
	if out.Truncated {
		summary = fmt.Sprintf("More than %d tables match %q; only the first %d are listed. Use a longer query or pass connection.", maxFindMatches, c.in.Query, maxFindMatches)
	}
	if len(out.Unavailable) > 0 {
		slices.Sort(out.Unavailable)
		summary += " Some connections could not be searched: " + strings.Join(out.Unavailable, ", ") + "."
	}
	return result[findTablesOut]{out: out, summary: summary}, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return append([]string{}, s...)
}
