package postgres

import (
	"context"
	"fmt"

	"github.com/alvaroibarguen/dbairlock/internal/connector"
)

// maxOwnedReported caps how many owned tables are listed.
const maxOwnedReported = 20

// Privileges checks role attributes, table ownership and CREATE on schemas
// (PLAN.md 5.3), and whether the user can read and change tables in the
// default schema.
func (c *conn) Privileges(ctx context.Context) (connector.PrivilegeReport, error) {
	var rep connector.PrivilegeReport
	var super, createDB, createRole, replication, bypassRLS bool
	err := c.pool.QueryRow(ctx, `SELECT current_user::text, r.rolsuper, r.rolcreatedb, r.rolcreaterole, r.rolreplication, r.rolbypassrls
		FROM pg_catalog.pg_roles r WHERE r.rolname = current_user`).
		Scan(&rep.User, &super, &createDB, &createRole, &replication, &bypassRLS)
	if err != nil {
		if isConnectError(err) {
			return rep, connector.Unavailable(err)
		}
		return rep, fmt.Errorf("postgres: read role: %w", err)
	}
	user := quoteIdent(rep.User)
	add := func(priv, detail, fix string) {
		rep.Excess = append(rep.Excess, connector.ExcessPrivilege{Privilege: priv, Detail: detail, Fix: fix})
	}
	if super {
		add("SUPERUSER", "A superuser bypasses every permission check and can change the schema.", "ALTER ROLE "+user+" NOSUPERUSER;")
	}
	if createDB {
		add("CREATEDB", "The user can create databases.", "ALTER ROLE "+user+" NOCREATEDB;")
	}
	if createRole {
		add("CREATEROLE", "The user can create and alter roles.", "ALTER ROLE "+user+" NOCREATEROLE;")
	}
	if replication {
		add("REPLICATION", "The user can start streaming replication.", "ALTER ROLE "+user+" NOREPLICATION;")
	}
	if bypassRLS {
		add("BYPASSRLS", "The user bypasses row-level security policies.", "ALTER ROLE "+user+" NOBYPASSRLS;")
	}

	err = c.pool.QueryRow(ctx, `SELECT
			COALESCE(bool_or(pg_catalog.has_table_privilege(c.oid, 'SELECT')), false),
			COALESCE(bool_or(pg_catalog.has_table_privilege(c.oid, 'INSERT')), false),
			COALESCE(bool_or(pg_catalog.has_table_privilege(c.oid, 'UPDATE')), false),
			COALESCE(bool_or(pg_catalog.has_table_privilege(c.oid, 'DELETE')), false)
		FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1::text AND c.relkind IN ('r', 'p')
			AND pg_catalog.has_schema_privilege(n.oid, 'USAGE')`, c.schema).
		Scan(&rep.CanSelect, &rep.CanInsert, &rep.CanUpdate, &rep.CanDelete)
	if err != nil {
		return rep, fmt.Errorf("postgres: read table privileges: %w", err)
	}
	if super {
		return rep, nil // ownership and CREATE follow from superuser
	}

	rows, err := c.pool.Query(ctx, `SELECT n.nspname::text, c.relname::text, pg_catalog.pg_get_userbyid(c.relowner)::text
		FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')
			AND n.nspname !~ '^pg_' AND n.nspname <> 'information_schema'
			AND pg_catalog.pg_has_role(c.relowner, 'USAGE')
		ORDER BY 1, 2 LIMIT $1::int`, maxOwnedReported+1)
	if err != nil {
		return rep, fmt.Errorf("postgres: read ownership: %w", err)
	}
	n := 0
	for rows.Next() {
		var ns, rel, owner string
		if err := rows.Scan(&ns, &rel, &owner); err != nil {
			rows.Close()
			return rep, fmt.Errorf("postgres: read ownership: %w", err)
		}
		n++
		if n > maxOwnedReported {
			add("OWNER", fmt.Sprintf("The user owns more than %d relations; only the first %d are listed.", maxOwnedReported, maxOwnedReported), "")
			break
		}
		add("OWNER OF "+quoteQualified(ns, rel),
			"The owner of a table can alter, drop and truncate it, whatever is granted.",
			fmt.Sprintf("ALTER TABLE %s OWNER TO <another role>;", quoteQualified(ns, rel)))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return rep, fmt.Errorf("postgres: read ownership: %w", err)
	}

	rows, err = c.pool.Query(ctx, `SELECT n.nspname::text
		FROM pg_catalog.pg_namespace n
		WHERE n.nspname !~ '^pg_' AND n.nspname <> 'information_schema'
			AND pg_catalog.has_schema_privilege(n.oid, 'CREATE')
		ORDER BY 1`)
	if err != nil {
		return rep, fmt.Errorf("postgres: read schema privileges: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ns string
		if err := rows.Scan(&ns); err != nil {
			return rep, fmt.Errorf("postgres: read schema privileges: %w", err)
		}
		add("CREATE ON SCHEMA "+quoteIdent(ns),
			"The user can create tables, functions and operators in this schema. If the grant comes from PUBLIC (the default for schema public before Postgres 15), revoke it from PUBLIC instead.",
			fmt.Sprintf("REVOKE CREATE ON SCHEMA %s FROM %s;", quoteIdent(ns), user))
	}
	return rep, rows.Err()
}
