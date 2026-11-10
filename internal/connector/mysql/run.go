package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/alvaroibarguen/dbairlock/internal/connector"
)

// Dry run and execution, PLAN.md 5.4 and 5.6.
//
// Each run opens a dedicated connection (not from the pool) whose
// connection attributes carry the RunInfo label, so the label shows in
// performance_schema.session_connect_attrs next to the session, and so
// session settings never leak into the pool. The statement text itself is
// never changed.
//
// On that connection: START TRANSACTION at REPEATABLE READ, then pin the
// session (sql_mode lexical flags, time zone, timeouts). For each
// statement, check the table's storage engine, select the matching rows
// with FOR UPDATE (the before image), fingerprint them, run the statement
// verbatim, and compare. A dry run always rolls back; an execution commits
// only if nothing drifted.
//
// Errors before BEGIN returned are ErrUnavailable (nothing ran). Every
// error after that is final and never ErrUnavailable.

// slack is the extra time given to non-statement work (connecting,
// commit) beyond its own timeout.
const slack = 3 * time.Second

// killTimeout bounds the KILL issued when a statement overruns its timeout.
const killTimeout = 5 * time.Second

// lexicalModes are the sql_mode flags that change how statement text is
// tokenized or parsed. The validator's parser assumes none of them, so
// every transaction removes them from the session's sql_mode.
var lexicalModes = map[string]bool{
	"ANSI":                 true, // implies ANSI_QUOTES, PIPES_AS_CONCAT and IGNORE_SPACE
	"ANSI_QUOTES":          true, // "x" would be an identifier, not a string
	"NO_BACKSLASH_ESCAPES": true, // '\' would end a string
	"PIPES_AS_CONCAT":      true, // || would be concatenation, not OR
	"HIGH_NOT_PRECEDENCE":  true, // NOT a BETWEEN b AND c would group differently
	"IGNORE_SPACE":         true, // "count (x)" would be a function call
}

var sqlModeRe = regexp.MustCompile(`^[A-Z_]*(,[A-Z_]+)*$`)

// pinSQLMode returns mode without the lexical flags.
func pinSQLMode(mode string) (string, error) {
	mode = strings.ToUpper(strings.TrimSpace(mode))
	if !sqlModeRe.MatchString(mode) {
		return "", fmt.Errorf("unexpected sql_mode %q", mode)
	}
	var keep []string
	for m := range strings.SplitSeq(mode, ",") {
		if m != "" && !lexicalModes[m] {
			keep = append(keep, m)
		}
	}
	return strings.Join(keep, ","), nil
}

// DryRun runs stmts and rolls back.
func (c *conn) DryRun(ctx context.Context, stmts []connector.Statement, lim connector.Limits, run connector.RunInfo) ([]connector.Result, error) {
	return c.run(ctx, stmts, nil, lim, run, true)
}

// Execute runs stmts, checks them against expected and commits.
func (c *conn) Execute(ctx context.Context, stmts []connector.Statement, expected []connector.Expectation, lim connector.Limits, run connector.RunInfo) ([]connector.Result, error) {
	if len(expected) != len(stmts) {
		return nil, fmt.Errorf("mysql: %d statements but %d expectations", len(stmts), len(expected))
	}
	return c.run(ctx, stmts, expected, lim, run, false)
}

func normalizeLimits(lim connector.Limits) connector.Limits {
	d := connector.DefaultLimits()
	if lim.MaxRows <= 0 {
		lim.MaxRows = d.MaxRows
	}
	if lim.StatementTimeout <= 0 {
		lim.StatementTimeout = d.StatementTimeout
	}
	if lim.LockTimeout <= 0 {
		lim.LockTimeout = d.LockTimeout
	}
	if lim.MaxCellBytes <= 0 {
		lim.MaxCellBytes = d.MaxCellBytes
	}
	return lim
}

// label makes a RunInfo label fit a connection attribute value: printable
// ASCII without commas (the driver's attribute separator), at most 64
// bytes.
func label(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 0x20 && r < 0x7f && r != ',' {
			b.WriteRune(r)
		} else {
			b.WriteByte('?')
		}
		if b.Len() >= 64 {
			break
		}
	}
	if b.Len() == 0 {
		return programName
	}
	return b.String()
}

// dedicated opens a connection of its own for one run, labelled with the
// run's label. Failing here is ErrUnavailable.
func (c *conn) dedicated(ctx context.Context, runLabel string) (*sql.DB, *sql.Conn, error) {
	cfg := c.base.Clone()
	cfg.ConnectionAttributes = "program_name:" + programName + "," + labelAttr + ":" + label(runLabel)
	dc, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("mysql: driver config: %w", err)
	}
	db := sql.OpenDB(dc)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	cctx, cancel := context.WithTimeout(ctx, connectTimeout+slack)
	defer cancel()
	sc, err := db.Conn(cctx)
	if err != nil {
		_ = db.Close()
		return nil, nil, connector.Unavailable(err)
	}
	return db, sc, nil
}

func (c *conn) run(ctx context.Context, stmts []connector.Statement, expected []connector.Expectation, lim connector.Limits, run connector.RunInfo, dry bool) ([]connector.Result, error) {
	if len(stmts) == 0 {
		return nil, errors.New("mysql: no statements")
	}
	lim = normalizeLimits(lim)
	// Re-derive every statement from its SQL so the fields this function
	// builds queries from (table, WHERE text) cannot disagree with the text
	// that runs.
	for i, st := range stmts {
		if err := c.checkStatement(st); err != nil {
			return nil, &connector.StatementError{Index: i, Err: err}
		}
	}

	db, sc, err := c.dedicated(ctx, run.Label)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = sc.Close()
		_ = db.Close()
	}()
	// database/sql rolls the transaction back if ctx ends.
	tx, err := sc.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, connector.Unavailable(err)
	}
	// From here on nothing may return ErrUnavailable.
	done := false
	defer func() {
		if !done {
			_ = tx.Rollback()
		}
	}()
	r := &runner{ctx: ctx, tx: tx, lim: lim, c: c}
	if err := r.setup(); err != nil {
		return nil, classify(fmt.Errorf("mysql: set up transaction: %w", err))
	}
	results := make([]connector.Result, 0, len(stmts))
	for i, st := range stmts {
		var exp *connector.Expectation
		if expected != nil {
			exp = &expected[i]
		}
		res, err := r.statement(i, st, exp)
		if err != nil {
			var drift *connector.DriftError
			if errors.As(err, &drift) {
				return nil, err
			}
			return nil, &connector.StatementError{Index: i, Err: classify(err)}
		}
		results = append(results, res)
	}
	if dry {
		done = true
		// A failed rollback means the connection is gone, and the server
		// then discards the transaction, so the results stand either way.
		_ = tx.Rollback()
		return results, nil
	}
	done = true
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("mysql: commit (outcome unknown, verify manually): %w", err)
	}
	return results, nil
}

// checkStatement validates st.SQL again and checks that the derived fields
// match. Functions are not checked here (the caller's policy is not known);
// the caller validates with the connection's policy before every run.
func (c *conn) checkStatement(st connector.Statement) error {
	got, err := validateWith(st.SQL, c.db, connector.Policy{}, true)
	if err != nil {
		return err
	}
	if got.Kind != st.Kind || got.Schema != st.Schema || got.Table != st.Table || got.Alias != st.Alias ||
		got.Where != st.Where || !slices.Equal(got.Columns, st.Columns) {
		return errors.New("mysql: the statement does not match its SQL text; validate it again")
	}
	return nil
}

type runner struct {
	ctx    context.Context
	tx     *sql.Tx
	lim    connector.Limits
	c      *conn
	connID uint64
	// sqlMode is the session's sql_mode as pinned by setup, and version
	// the server's VERSION(); both are recorded in every Result.
	sqlMode string
	version string
}

// rowSet is a fully read query result.
type rowSet struct {
	types []string // database type names, e.g. "VARCHAR"
	rows  [][]any
}

// withTimeout runs fn under the statement deadline. If the deadline passes,
// the driver abandons the connection; the server-side statement is then
// killed so it stops holding locks. The deadline counts even when fn
// returned without an error: the driver may close the connection as the
// deadline passes, and nothing more can run on it.
func (r *runner) withTimeout(fn func(ctx context.Context) error) error {
	ctx, cancel := context.WithTimeout(r.ctx, r.lim.StatementTimeout)
	defer cancel()
	err := fn(ctx)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		r.kill()
		if err == nil {
			err = errors.New("the statement deadline passed")
		}
		return fmt.Errorf("%w: %w", context.DeadlineExceeded, err)
	}
	return err
}

// kill terminates the run's server session from a pooled connection. A
// user may always kill its own sessions. Best effort: the transaction is
// rolled back either way once the server notices the connection is gone.
func (r *runner) kill() {
	if r.connID == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.ctx), killTimeout)
	defer cancel()
	_, _ = r.c.pool.ExecContext(ctx, "KILL "+strconv.FormatUint(r.connID, 10))
}

func (r *runner) query(q string, args ...any) (rowSet, error) {
	var out rowSet
	err := r.withTimeout(func(ctx context.Context) error {
		rows, err := r.tx.QueryContext(ctx, q, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		cts, err := rows.ColumnTypes()
		if err != nil {
			return err
		}
		out.types = make([]string, len(cts))
		for i, ct := range cts {
			out.types[i] = ct.DatabaseTypeName()
		}
		for rows.Next() {
			vals := make([]any, len(cts))
			ptrs := make([]any, len(cts))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				return err
			}
			out.rows = append(out.rows, vals)
		}
		return rows.Err()
	})
	return out, err
}

func (r *runner) exec(q string) (int64, error) {
	var n int64
	err := r.withTimeout(func(ctx context.Context) error {
		res, err := r.tx.ExecContext(ctx, q)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return n, err
}

// setup pins the session for this transaction: the lexical sql_mode flags
// removed (see lexicalModes), UTC so TIMESTAMP values are canonical, and
// the timeouts. innodb_lock_wait_timeout and lock_wait_timeout are whole
// seconds (rounded up, at least 1). max_execution_time only limits SELECT,
// so the statement timeout is enforced by a context deadline on every
// statement (followed by KILL); max_execution_time is set slightly longer,
// as a server-side backstop. It is not the primary limit because an
// interrupted SLEEP() inside a SELECT returns 1 instead of failing, so a
// before image cut short by it could silently miss rows.
func (r *runner) setup() error {
	var mode string
	if err := r.withTimeout(func(ctx context.Context) error {
		return r.tx.QueryRowContext(ctx, "SELECT @@SESSION.sql_mode, CONNECTION_ID(), VERSION()").Scan(&mode, &r.connID, &r.version)
	}); err != nil {
		return err
	}
	pinned, err := pinSQLMode(mode)
	if err != nil {
		return err
	}
	r.sqlMode = pinned
	lockSecs := int64((r.lim.LockTimeout + time.Second - 1) / time.Second)
	if lockSecs < 1 {
		lockSecs = 1
	}
	// Every value is generated here (sql_mode checked against sqlModeRe),
	// so the statement can be sent as text.
	_, err = r.exec(fmt.Sprintf("SET SESSION sql_mode = '%s', SESSION time_zone = '+00:00', "+
		"SESSION innodb_lock_wait_timeout = %d, SESSION lock_wait_timeout = %d, SESSION max_execution_time = %d",
		pinned, lockSecs, lockSecs, (r.lim.StatementTimeout + slack).Milliseconds()))
	return err
}

// tableMeta is what the connector needs to know about a target table.
type tableMeta struct {
	columns []string // in ordinal order
	pk      []int    // indexes into columns, in key order
}

// transactionalEngines are the storage engines whose changes roll back.
var transactionalEngines = map[string]bool{"INNODB": true, "NDBCLUSTER": true, "NDB": true}

func (r *runner) table(db, table string) (tableMeta, error) {
	var m tableMeta
	rs, err := r.query(`SELECT TABLE_TYPE, COALESCE(ENGINE, '') FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`, db, table)
	if err != nil {
		return m, err
	}
	if len(rs.rows) == 0 {
		return m, fmt.Errorf("table %s does not exist or is not visible to this user", quoteQualified(db, table))
	}
	typ, engine := text(rs.rows[0][0]), text(rs.rows[0][1])
	if typ != "BASE TABLE" {
		return m, fmt.Errorf("%s is not a table (%s)", quoteQualified(db, table), strings.ToLower(typ))
	}
	if !transactionalEngines[strings.ToUpper(engine)] {
		return m, fmt.Errorf("%w: %s uses the %s storage engine, whose changes cannot be rolled back",
			connector.ErrNotTransactional, quoteQualified(db, table), engine)
	}
	rs, err = r.query(`SELECT COLUMN_NAME FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION`, db, table)
	if err != nil {
		return m, err
	}
	for _, row := range rs.rows {
		m.columns = append(m.columns, text(row[0]))
	}
	rs, err = r.query(`SELECT COLUMN_NAME FROM information_schema.KEY_COLUMN_USAGE
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND CONSTRAINT_NAME = 'PRIMARY'
		ORDER BY ORDINAL_POSITION`, db, table)
	if err != nil {
		return m, err
	}
	for _, row := range rs.rows {
		idx := columnIndex(m.columns, text(row[0]))
		if idx < 0 {
			return m, fmt.Errorf("primary key column %q not found", text(row[0]))
		}
		m.pk = append(m.pk, idx)
	}
	return m, nil
}

// text returns a driver value as a string.
func text(v any) string {
	switch x := v.(type) {
	case []byte:
		return string(x)
	case string:
		return x
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}

// columnIndex finds a column by name; MySQL column names are not case
// sensitive.
func columnIndex(columns []string, name string) int {
	return slices.IndexFunc(columns, func(c string) bool { return strings.EqualFold(c, name) })
}

func (m tableMeta) selectList() string {
	q := make([]string, len(m.columns))
	for i, c := range m.columns {
		q[i] = quoteIdent(c)
	}
	return strings.Join(q, ", ")
}

func (m tableMeta) orderBy() string {
	if len(m.pk) == 0 {
		return ""
	}
	q := make([]string, len(m.pk))
	for i, k := range m.pk {
		q[i] = quoteIdent(m.columns[k])
	}
	return " ORDER BY " + strings.Join(q, ", ")
}

func (r *runner) statement(i int, st connector.Statement, exp *connector.Expectation) (connector.Result, error) {
	meta, err := r.table(st.Schema, st.Table)
	if err != nil {
		return connector.Result{}, err
	}
	res := connector.Result{
		Schema:        st.Schema,
		Table:         st.Table,
		HasPrimaryKey: len(meta.pk) > 0,
		Columns:       meta.columns,
		Session:       map[string]string{"sql_mode": r.sqlMode, "server_version": r.version},
	}
	for _, k := range meta.pk {
		res.PrimaryKey = append(res.PrimaryKey, meta.columns[k])
	}
	from := quoteQualified(st.Schema, st.Table)
	if st.Alias != "" {
		from += " AS " + quoteIdent(st.Alias)
	}

	var before rowSet
	if st.Kind == connector.KindUpdate || st.Kind == connector.KindDelete {
		q := fmt.Sprintf("SELECT %s FROM %s WHERE (\n%s\n)%s LIMIT %d FOR UPDATE",
			meta.selectList(), from, st.Where, meta.orderBy(), r.lim.MaxRows+1)
		before, err = r.query(q)
		if err != nil {
			return res, err
		}
		fp, err := fingerprint(st, meta, before)
		if err != nil {
			return res, err
		}
		res.Fingerprint = fp
		matched := int64(len(before.rows))
		if exp != nil {
			if fp != exp.Fingerprint {
				if matched != exp.Rows {
					return res, &connector.DriftError{Statement: i, Kind: connector.DriftCount,
						Expected: strconv.FormatInt(exp.Rows, 10), Actual: countText(matched, r.lim.MaxRows)}
				}
				return res, &connector.DriftError{Statement: i, Kind: connector.DriftRows, Expected: exp.Fingerprint, Actual: fp}
			}
		} else if matched > int64(r.lim.MaxRows) {
			return res, fmt.Errorf("%w: more than %d rows match", connector.ErrTooManyRows, r.lim.MaxRows)
		}
		res.Before, res.Truncated = displayRows(before, r.lim.MaxCellBytes)
	}

	res.RowsAffected, err = r.exec(st.SQL)
	if err != nil {
		return res, err
	}
	if exp != nil {
		if res.RowsAffected != exp.Rows {
			return res, &connector.DriftError{Statement: i, Kind: connector.DriftCount,
				Expected: strconv.FormatInt(exp.Rows, 10), Actual: strconv.FormatInt(res.RowsAffected, 10)}
		}
	} else {
		if res.RowsAffected > int64(r.lim.MaxRows) {
			return res, fmt.Errorf("%w: %d rows affected, the limit is %d", connector.ErrTooManyRows, res.RowsAffected, r.lim.MaxRows)
		}
		if res.RowsAffected == 0 {
			return res, connector.ErrNoRows
		}
	}

	if st.Kind == connector.KindUpdate && len(meta.pk) > 0 && len(before.rows) > 0 {
		after, ok, err := r.afterImage(meta, st, before)
		if err != nil {
			return res, err
		}
		if ok {
			var t bool
			res.After, t = displayRows(after, r.lim.MaxCellBytes)
			res.Truncated = res.Truncated || t
		}
	}
	return res, nil
}

func countText(n int64, maxRows int) string {
	if n > int64(maxRows) {
		return "more than " + strconv.Itoa(maxRows)
	}
	return strconv.FormatInt(n, 10)
}

// maxPlaceholders stays below MySQL's limit of 65535 per prepared
// statement.
const maxPlaceholders = 60000

// afterImage re-selects the updated rows by their primary key values. Rows
// whose key the statement changed are not found; the after image is for
// display only. ok is false when there are too many keys to look up.
func (r *runner) afterImage(meta tableMeta, st connector.Statement, before rowSet) (rowSet, bool, error) {
	if len(before.rows)*len(meta.pk) > maxPlaceholders {
		return rowSet{}, false, nil
	}
	keyCols := make([]string, len(meta.pk))
	marks := make([]string, len(meta.pk))
	for j, k := range meta.pk {
		keyCols[j] = quoteIdent(meta.columns[k])
		marks[j] = "?"
	}
	tuple := "(" + strings.Join(marks, ", ") + ")"
	tuples := make([]string, len(before.rows))
	args := make([]any, 0, len(before.rows)*len(meta.pk))
	for n, row := range before.rows {
		tuples[n] = tuple
		for _, k := range meta.pk {
			args = append(args, row[k])
		}
	}
	q := fmt.Sprintf("SELECT %s FROM %s WHERE (%s) IN (%s)%s",
		meta.selectList(), quoteQualified(st.Schema, st.Table), strings.Join(keyCols, ", "),
		strings.Join(tuples, ", "), meta.orderBy())
	rs, err := r.query(q, args...)
	return rs, err == nil, err
}

// fingerprint computes the row fingerprint of a before image (PLAN.md 5.4)
// with the shared helper.
func fingerprint(st connector.Statement, meta tableMeta, before rowSet) (string, error) {
	var setIdx []int
	if st.Kind == connector.KindUpdate && len(meta.pk) > 0 {
		for _, col := range st.Columns {
			idx := columnIndex(meta.columns, col)
			if idx < 0 {
				return "", fmt.Errorf("column %q does not exist in %s", col, quoteQualified(st.Schema, st.Table))
			}
			setIdx = append(setIdx, idx)
		}
	}
	rows := make([]connector.FingerprintRow, len(before.rows))
	for n, raw := range before.rows {
		var fr connector.FingerprintRow
		if len(meta.pk) > 0 {
			for _, k := range meta.pk {
				fr.Key = append(fr.Key, canonicalCell(raw[k]))
			}
			for _, k := range setIdx {
				fr.Values = append(fr.Values, canonicalCell(raw[k]))
			}
		} else {
			for k := range meta.columns {
				fr.Values = append(fr.Values, canonicalCell(raw[k]))
			}
		}
		rows[n] = fr
	}
	return connector.Fingerprint(rows), nil
}

func displayRows(rs rowSet, maxBytes int) ([]connector.Row, bool) {
	if len(rs.rows) == 0 {
		return nil, false
	}
	truncated := false
	out := make([]connector.Row, len(rs.rows))
	for n, raw := range rs.rows {
		row := make(connector.Row, len(raw))
		for i, v := range raw {
			var t bool
			row[i], t = displayValue(v, rs.types[i], maxBytes)
			truncated = truncated || t
		}
		out[n] = row
	}
	return out, truncated
}
