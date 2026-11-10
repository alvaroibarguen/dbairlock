package mysql

import (
	"context"
	"fmt"

	"github.com/alvaroibarguen/dbairlock/internal/connector"
)

// Schema lists the base tables of the configured database with their
// columns and primary keys, from information_schema. It is the only
// namespace: statements cannot reach other databases.
func (c *conn) Schema(ctx context.Context) (connector.Schema, error) {
	sc, err := c.acquire(ctx)
	if err != nil {
		return connector.Schema{}, err
	}
	defer func() { _ = sc.Close() }()

	ns := connector.Namespace{Name: c.db, Tables: []connector.Table{}}
	idx := map[string]int{}
	rows, err := sc.QueryContext(ctx, `SELECT c.TABLE_NAME, c.COLUMN_NAME, c.COLUMN_TYPE, c.IS_NULLABLE
		FROM information_schema.COLUMNS c
		JOIN information_schema.TABLES t ON t.TABLE_SCHEMA = c.TABLE_SCHEMA AND t.TABLE_NAME = c.TABLE_NAME
		WHERE c.TABLE_SCHEMA = ? AND t.TABLE_TYPE = 'BASE TABLE'
		ORDER BY c.TABLE_NAME, c.ORDINAL_POSITION`, c.db)
	if err != nil {
		return connector.Schema{}, fmt.Errorf("mysql: read columns: %w", err)
	}
	for rows.Next() {
		var table, col, typ, nullable string
		if err := rows.Scan(&table, &col, &typ, &nullable); err != nil {
			_ = rows.Close()
			return connector.Schema{}, fmt.Errorf("mysql: read columns: %w", err)
		}
		i, ok := idx[table]
		if !ok {
			i = len(ns.Tables)
			idx[table] = i
			ns.Tables = append(ns.Tables, connector.Table{Name: table})
		}
		ns.Tables[i].Columns = append(ns.Tables[i].Columns, connector.Column{Name: col, Type: typ, Nullable: nullable == "YES"})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return connector.Schema{}, fmt.Errorf("mysql: read columns: %w", err)
	}
	_ = rows.Close()

	rows, err = sc.QueryContext(ctx, `SELECT TABLE_NAME, COLUMN_NAME
		FROM information_schema.KEY_COLUMN_USAGE
		WHERE TABLE_SCHEMA = ? AND CONSTRAINT_NAME = 'PRIMARY'
		ORDER BY TABLE_NAME, ORDINAL_POSITION`, c.db)
	if err != nil {
		return connector.Schema{}, fmt.Errorf("mysql: read primary keys: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var table, col string
		if err := rows.Scan(&table, &col); err != nil {
			return connector.Schema{}, fmt.Errorf("mysql: read primary keys: %w", err)
		}
		if i, ok := idx[table]; ok {
			ns.Tables[i].PrimaryKey = append(ns.Tables[i].PrimaryKey, col)
		}
	}
	if err := rows.Err(); err != nil {
		return connector.Schema{}, fmt.Errorf("mysql: read primary keys: %w", err)
	}
	return connector.Schema{Namespaces: []connector.Namespace{ns}}, nil
}
