package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/alvaroibarguen/dbairlock/internal/connector"
)

// Privileges parses SHOW GRANTS for the current user, including the
// privileges of its active roles (PLAN.md 5.3), and reports what dbairlock
// does not need: ALL PRIVILEGES, SUPER, DDL (CREATE, ALTER, DROP, INDEX and
// the like), GRANT OPTION, FILE, PROCESS, administrative and dynamic
// privileges, and any global (*.*) write grant, which also covers the mysql
// system database.
func (c *conn) Privileges(ctx context.Context) (connector.PrivilegeReport, error) {
	var rep connector.PrivilegeReport
	sc, err := c.acquire(ctx)
	if err != nil {
		return rep, err
	}
	defer func() { _ = sc.Close() }()
	var roles string
	if err := sc.QueryRowContext(ctx, "SELECT CURRENT_USER(), CURRENT_ROLE()").Scan(&rep.User, &roles); err != nil {
		return rep, fmt.Errorf("mysql: read current user: %w", err)
	}
	query := "SHOW GRANTS"
	rolesChecked := true
	if roles != "" && roles != "NONE" {
		if roleListRe.MatchString(roles) {
			// CURRENT_ROLE() returns the active roles already quoted, e.g.
			// `r1`@`%`,`r2`@`%`; the pattern above admits nothing else.
			query = "SHOW GRANTS FOR CURRENT_USER() USING " + roles
		} else {
			rolesChecked = false
		}
	}
	lines, err := queryStrings(ctx, sc, query)
	if err != nil {
		return rep, fmt.Errorf("mysql: read grants: %w", err)
	}
	analyzeGrants(&rep, lines, c.db)
	if !rolesChecked {
		rep.Excess = append(rep.Excess, connector.ExcessPrivilege{
			Privilege: "ROLES " + roles,
			Detail:    "The user has active roles whose privileges could not be checked. Review them with SHOW GRANTS ... USING.",
		})
	}
	return rep, nil
}

// roleListRe matches a list of quoted role names as CURRENT_ROLE() prints
// them.
var roleListRe = regexp.MustCompile("^`(?:[^`]|``)+`@`(?:[^`]|``)*`(?:,`(?:[^`]|``)+`@`(?:[^`]|``)*`)*$")

func queryStrings(ctx context.Context, sc *sql.Conn, q string) ([]string, error) {
	rows, err := sc.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// grant is one parsed GRANT line.
type grant struct {
	privs       []string // upper case, without column lists
	scope       string   // as printed, e.g. *.* or `app`.*
	db, table   string   // unquoted; "*" for a wildcard
	grantee     string   // as printed
	grantOption bool
	role        bool // a role grant (GRANT `r`@`%` TO ...)
}

// neededPrivs are the privileges dbairlock needs (or that grant nothing).
var neededPrivs = map[string]bool{"USAGE": true, "SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true, "SHOW VIEW": true}

// namedExcess are reported one by one, with a specific explanation.
var namedExcess = map[string]string{
	"ALL PRIVILEGES":          "Every privilege, including DDL.",
	"ALL":                     "Every privilege, including DDL.",
	"SUPER":                   "Administrative control of the server: kill sessions, change global settings, bypass read-only.",
	"CREATE":                  "Create databases and tables.",
	"ALTER":                   "Change table definitions.",
	"DROP":                    "Drop databases, tables and views, and TRUNCATE tables.",
	"INDEX":                   "Create and drop indexes.",
	"FILE":                    "Read and write files on the database server.",
	"PROCESS":                 "See every session and the statements they run.",
	"GRANT OPTION":            "Grant its own privileges to other users.",
	"CREATE USER":             "Create, drop and rename users.",
	"RELOAD":                  "Flush logs, caches and privileges.",
	"SHUTDOWN":                "Shut down the server.",
	"CREATE ROUTINE":          "Create stored procedures and functions.",
	"ALTER ROUTINE":           "Change and drop stored procedures and functions.",
	"EXECUTE":                 "Run stored procedures and functions.",
	"TRIGGER":                 "Create and drop triggers.",
	"EVENT":                   "Create scheduled events.",
	"CREATE VIEW":             "Create views.",
	"CREATE TEMPORARY TABLES": "Create temporary tables.",
	"LOCK TABLES":             "Lock whole tables.",
	"REFERENCES":              "Create foreign keys referencing tables.",
	"REPLICATION SLAVE":       "Read the binary log as a replica.",
	"REPLICATION CLIENT":      "Inspect replication status.",
	"CREATE TABLESPACE":       "Create and drop tablespaces.",
	"CREATE ROLE":             "Create roles.",
	"DROP ROLE":               "Drop roles.",
	"PROXY":                   "Act as another user.",
	"SHOW DATABASES":          "List every database on the server.",
}

func analyzeGrants(rep *connector.PrivilegeReport, lines []string, db string) {
	add := func(priv, detail, fix string) {
		rep.Excess = append(rep.Excess, connector.ExcessPrivilege{Privilege: priv, Detail: detail, Fix: fix})
	}
	for _, line := range lines {
		g, ok := parseGrant(line)
		if !ok {
			add("UNPARSED GRANT", "This grant could not be analyzed: "+line, "")
			continue
		}
		if g.role {
			add("ROLE "+strings.Join(g.privs, ", "),
				"The user is granted roles; privileges of roles that are not active were not checked.",
				fmt.Sprintf("REVOKE %s FROM %s;", strings.Join(g.privs, ", "), g.grantee))
			continue
		}
		global := g.db == "*"
		coversDB := global || likeMatch(g.db, db)
		var other []string
		for _, p := range g.privs {
			switch {
			case p == "ALL" || p == "ALL PRIVILEGES":
				add("ALL PRIVILEGES ON "+g.scope, namedExcess[p], fmt.Sprintf("REVOKE ALL PRIVILEGES ON %s FROM %s; then GRANT SELECT, INSERT, UPDATE, DELETE ON %s.* TO %s;", g.scope, g.grantee, quoteIdent(db), g.grantee))
				if coversDB {
					rep.CanSelect, rep.CanInsert, rep.CanUpdate, rep.CanDelete = true, true, true, true
				}
			case neededPrivs[p]:
				if global && (p == "INSERT" || p == "UPDATE" || p == "DELETE") {
					add(p+" ON *.*", "A global write grant also covers the mysql system database and every other database on the server.",
						fmt.Sprintf("REVOKE %s ON *.* FROM %s; then GRANT %s ON %s.* TO %s;", p, g.grantee, p, quoteIdent(db), g.grantee))
				}
				if coversDB {
					switch p {
					case "SELECT":
						rep.CanSelect = true
					case "INSERT":
						rep.CanInsert = true
					case "UPDATE":
						rep.CanUpdate = true
					case "DELETE":
						rep.CanDelete = true
					}
				}
			case namedExcess[p] != "":
				add(p+" ON "+g.scope, namedExcess[p], fmt.Sprintf("REVOKE %s ON %s FROM %s;", p, g.scope, g.grantee))
			default:
				other = append(other, p)
			}
		}
		if len(other) > 0 {
			add(strings.Join(other, ", ")+" ON "+g.scope,
				"Administrative or dynamic privileges dbairlock does not need.",
				fmt.Sprintf("REVOKE %s ON %s FROM %s;", strings.Join(other, ", "), g.scope, g.grantee))
		}
		if g.grantOption {
			add("GRANT OPTION ON "+g.scope, namedExcess["GRANT OPTION"], fmt.Sprintf("REVOKE GRANT OPTION ON %s FROM %s;", g.scope, g.grantee))
		}
	}
	slices.SortStableFunc(rep.Excess, func(a, b connector.ExcessPrivilege) int { return strings.Compare(a.Privilege, b.Privilege) })
}

// parseGrant parses one line of SHOW GRANTS output.
func parseGrant(line string) (grant, bool) {
	var g grant
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "GRANT ") {
		return g, false
	}
	s = s[len("GRANT "):]
	for _, suffix := range []string{" WITH GRANT OPTION", " WITH ADMIN OPTION"} {
		if strings.HasSuffix(s, suffix) {
			s = strings.TrimSuffix(s, suffix)
			g.grantOption = suffix == " WITH GRANT OPTION"
		}
	}
	onAt := topLevelIndex(s, " ON ")
	toAt := topLevelIndex(s, " TO ")
	if toAt < 0 {
		return g, false
	}
	g.grantee = strings.TrimSpace(s[toAt+len(" TO "):])
	if onAt < 0 || onAt > toAt {
		g.role = true
		g.privs = splitTopLevel(s[:toAt])
		return g, len(g.privs) > 0
	}
	for _, p := range splitTopLevel(s[:onAt]) {
		if i := strings.IndexByte(p, '('); i >= 0 {
			p = p[:i] // column-level privilege
		}
		p = strings.ToUpper(strings.TrimSpace(p))
		if p != "" {
			g.privs = append(g.privs, p)
		}
	}
	g.scope = strings.TrimSpace(s[onAt+len(" ON ") : toAt])
	scope := g.scope
	for _, kw := range []string{"TABLE ", "FUNCTION ", "PROCEDURE "} {
		scope = strings.TrimPrefix(scope, kw)
	}
	if len(g.privs) == 1 && g.privs[0] == "PROXY" {
		return g, true // the scope is a user, not a database
	}
	var ok bool
	g.db, g.table, ok = splitScope(scope)
	return g, ok && len(g.privs) > 0
}

// topLevelIndex finds sep outside backquotes, quotes and parentheses.
func topLevelIndex(s, sep string) int {
	depth := 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '`' || c == '\'' || c == '"':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			depth--
		case depth == 0 && strings.HasPrefix(s[i:], sep):
			return i
		}
	}
	return -1
}

// splitTopLevel splits a comma list outside quotes and parentheses.
func splitTopLevel(s string) []string {
	var out []string
	for {
		i := topLevelIndex(s, ",")
		if i < 0 {
			break
		}
		if p := strings.TrimSpace(s[:i]); p != "" {
			out = append(out, p)
		}
		s = s[i+1:]
	}
	if p := strings.TrimSpace(s); p != "" {
		out = append(out, p)
	}
	return out
}

// splitScope splits db.table, either part quoted with backticks or "*".
func splitScope(s string) (db, table string, ok bool) {
	db, rest, ok := identPart(s)
	if !ok {
		return "", "", false
	}
	if rest == "" { // a bare "*" means the default database; treat as a database wildcard
		return db, "*", true
	}
	if !strings.HasPrefix(rest, ".") {
		return "", "", false
	}
	table, rest, ok = identPart(rest[1:])
	if !ok || rest != "" {
		return "", "", false
	}
	return db, table, true
}

func identPart(s string) (name, rest string, ok bool) {
	if s == "" {
		return "", "", false
	}
	if s[0] == '*' {
		return "*", s[1:], true
	}
	if s[0] == '`' {
		var b strings.Builder
		for i := 1; i < len(s); i++ {
			if s[i] == '`' {
				if i+1 < len(s) && s[i+1] == '`' {
					b.WriteByte('`')
					i++
					continue
				}
				return b.String(), s[i+1:], true
			}
			b.WriteByte(s[i])
		}
		return "", "", false
	}
	i := strings.IndexByte(s, '.')
	if i < 0 {
		return s, "", true
	}
	return s[:i], s[i:], true
}

// likeMatch matches a grant's database name, which may contain the LIKE
// wildcards % and _ (escaped with a backslash when literal).
func likeMatch(pattern, s string) bool {
	if pattern == "" {
		return s == ""
	}
	switch pattern[0] {
	case '%':
		for i := 0; i <= len(s); i++ {
			if likeMatch(pattern[1:], s[i:]) {
				return true
			}
		}
		return false
	case '_':
		return s != "" && likeMatch(pattern[1:], s[1:])
	case '\\':
		if len(pattern) > 1 {
			return s != "" && s[0] == pattern[1] && likeMatch(pattern[2:], s[1:])
		}
	}
	return s != "" && s[0] == pattern[0] && likeMatch(pattern[1:], s[1:])
}
