package postgres

import (
	"context"
	"fmt"

	"github.com/alvaroibarguen/dbairlock/internal/connector"
)

// userTables restricts pg_class rows to ordinary and partitioned tables
// (not partitions) in non-system schemas that the user can use.
const userTables = `c.relkind IN ('r', 'p') AND NOT c.relispartition
	AND n.nspname !~ '^pg_' AND n.nspname <> 'information_schema'
	AND pg_catalog.has_schema_privilege(n.oid, 'USAGE')
	AND pg_catalog.has_table_privilege(c.oid, 'SELECT, INSERT, UPDATE, DELETE')`

// Schema lists the tables the user can use, with columns and primary keys,
// from pg_catalog. System schemas are excluded.
func (c *conn) Schema(ctx context.Context) (connector.Schema, error) {
	rows, err := c.pool.Query(ctx, `SELECT n.nspname::text, c.relname::text, a.attname::text,
			pg_catalog.format_type(a.atttypid, a.atttypmod), NOT a.attnotnull
		FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
		WHERE `+userTables+`
		ORDER BY n.nspname, c.relname, a.attnum`)
	if err != nil {
		if isConnectError(err) {
			return connector.Schema{}, connector.Unavailable(err)
		}
		return connector.Schema{}, fmt.Errorf("postgres: read columns: %w", err)
	}
	type key struct{ ns, table string }
	var out connector.Schema
	nsIdx := map[string]int{}
	tblIdx := map[key]int{}
	for rows.Next() {
		var ns, table, col, typ string
		var nullable bool
		if err := rows.Scan(&ns, &table, &col, &typ, &nullable); err != nil {
			rows.Close()
			return connector.Schema{}, fmt.Errorf("postgres: read columns: %w", err)
		}
		ni, ok := nsIdx[ns]
		if !ok {
			ni = len(out.Namespaces)
			nsIdx[ns] = ni
			out.Namespaces = append(out.Namespaces, connector.Namespace{Name: ns})
		}
		k := key{ns, table}
		ti, ok := tblIdx[k]
		if !ok {
			ti = len(out.Namespaces[ni].Tables)
			tblIdx[k] = ti
			out.Namespaces[ni].Tables = append(out.Namespaces[ni].Tables, connector.Table{Name: table})
		}
		t := &out.Namespaces[ni].Tables[ti]
		t.Columns = append(t.Columns, connector.Column{Name: col, Type: typ, Nullable: nullable})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return connector.Schema{}, fmt.Errorf("postgres: read columns: %w", err)
	}

	rows, err = c.pool.Query(ctx, `SELECT n.nspname::text, c.relname::text, a.attname::text
		FROM pg_catalog.pg_index i
		JOIN pg_catalog.pg_class c ON c.oid = i.indrelid
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		CROSS JOIN LATERAL pg_catalog.unnest(i.indkey::int2[]) WITH ORDINALITY AS k(attnum, ord)
		JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid AND a.attnum = k.attnum
		WHERE i.indisprimary AND `+userTables+`
		ORDER BY n.nspname, c.relname, k.ord`)
	if err != nil {
		return connector.Schema{}, fmt.Errorf("postgres: read primary keys: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ns, table, col string
		if err := rows.Scan(&ns, &table, &col); err != nil {
			return connector.Schema{}, fmt.Errorf("postgres: read primary keys: %w", err)
		}
		ni, ok := nsIdx[ns]
		if !ok {
			continue
		}
		ti, ok := tblIdx[key{ns, table}]
		if !ok {
			continue
		}
		t := &out.Namespaces[ni].Tables[ti]
		t.PrimaryKey = append(t.PrimaryKey, col)
	}
	if err := rows.Err(); err != nil {
		return connector.Schema{}, fmt.Errorf("postgres: read primary keys: %w", err)
	}
	return out, nil
}
