package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	pg "github.com/pganalyze/pg_query_go/v6"

	"github.com/alvaroibarguen/dbairlock/internal/connector"
)

// Dry run and execution, PLAN.md 5.4 and 5.6.
//
// Both run every statement in one transaction on one pooled connection.
// For UPDATE and DELETE the matching rows are first selected with
// FOR UPDATE (the before image) and fingerprinted; then the statement runs
// verbatim. A dry run always rolls back. An execution compares each
// statement against its Expectation and commits only if nothing drifted.
//
// Errors before BEGIN returned are ErrUnavailable (nothing ran). Every error
// after that is final and never ErrUnavailable.

// slack is added to the database-side statement timeout for the client-side
// context deadline, so a hung network cannot hold a caller forever.
const slack = 3 * time.Second

// DryRun runs stmts and rolls back.
func (c *conn) DryRun(ctx context.Context, stmts []connector.Statement, lim connector.Limits, run connector.RunInfo) ([]connector.Result, error) {
	return c.run(ctx, stmts, nil, lim, run, true)
}

// Execute runs stmts, checks them against expected and commits.
func (c *conn) Execute(ctx context.Context, stmts []connector.Statement, expected []connector.Expectation, lim connector.Limits, run connector.RunInfo) ([]connector.Result, error) {
	if len(expected) != len(stmts) {
		return nil, fmt.Errorf("postgres: %d statements but %d expectations", len(stmts), len(expected))
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

func (c *conn) run(ctx context.Context, stmts []connector.Statement, expected []connector.Expectation, lim connector.Limits, run connector.RunInfo, dry bool) ([]connector.Result, error) {
	if len(stmts) == 0 {
		return nil, errors.New("postgres: no statements")
	}
	lim = normalizeLimits(lim)
	// Re-derive every statement from its SQL so the fields this function
	// builds queries from (schema, table, WHERE text) cannot disagree with
	// the text that runs.
	for i, st := range stmts {
		if err := c.checkStatement(st); err != nil {
			return nil, &connector.StatementError{Index: i, Err: err}
		}
	}

	beginCtx, cancel := context.WithTimeout(ctx, connectTimout+slack)
	tx, err := c.pool.BeginTx(beginCtx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadWrite})
	cancel()
	if err != nil {
		return nil, connector.Unavailable(err)
	}
	// From here on nothing may return ErrUnavailable.
	done := false
	defer func() {
		if !done {
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), slack)
			defer cancel()
			_ = tx.Rollback(rctx)
		}
	}()
	r := &runner{ctx: ctx, pc: tx.Conn().PgConn(), lim: lim, schema: c.schema}
	if err := r.setup(run); err != nil {
		return nil, classify(fmt.Errorf("postgres: set up transaction: %w", err))
	}
	results := make([]connector.Result, 0, len(stmts))
	for i, st := range stmts {
		var exp *connector.Expectation
		if expected != nil {
			exp = &expected[i]
		}
		res, err := r.statement(i, st, exp, dry)
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
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), slack)
		defer cancel()
		// A failed rollback closes the connection, and the server then
		// discards the transaction, so the results stand either way.
		_ = tx.Rollback(rctx)
		return results, nil
	}
	cctx, cancel := context.WithTimeout(ctx, lim.StatementTimeout+slack)
	defer cancel()
	if err := tx.Commit(cctx); err != nil {
		done = true
		return nil, fmt.Errorf("postgres: commit (outcome unknown, verify manually): %w", err)
	}
	done = true
	return results, nil
}

// checkStatement validates st.SQL again and checks that the derived fields
// match. Functions are not checked here (the caller's policy is not known);
// the caller validates with the connection's policy before every run.
func (c *conn) checkStatement(st connector.Statement) error {
	got, err := validateWith(st.SQL, c.schema, connector.Policy{}, true)
	if err != nil {
		return err
	}
	if got.Kind != st.Kind || got.Schema != st.Schema || got.Table != st.Table || got.Alias != st.Alias ||
		got.Where != st.Where || !slices.Equal(got.Columns, st.Columns) {
		return errors.New("postgres: the statement does not match its SQL text; validate it again")
	}
	return nil
}

type runner struct {
	ctx    context.Context
	pc     *pgconn.PgConn
	lim    connector.Limits
	schema string
}

// query runs one query on the transaction's connection with text results
// and text parameters, under the statement deadline.
func (r *runner) query(sql string, args ...string) (*pgconn.Result, error) {
	ctx, cancel := context.WithTimeout(r.ctx, r.lim.StatementTimeout+slack)
	defer cancel()
	params := make([][]byte, len(args))
	for i, a := range args {
		params[i] = []byte(a)
	}
	res := r.pc.ExecParams(ctx, sql, params, nil, nil, nil).Read()
	if res.Err != nil {
		return nil, res.Err
	}
	return res, nil
}

// setup pins the session for this transaction: the run label, the search
// path (exactly the default schema; pg_catalog is still searched first
// implicitly), timeouts, and the settings that make text output canonical.
func (r *runner) setup(run connector.RunInfo) error {
	ms := func(d time.Duration) string { return strconv.FormatInt(d.Milliseconds(), 10) }
	_, err := r.query(`SELECT
		pg_catalog.set_config('application_name', $1::text, true),
		pg_catalog.set_config('search_path', $2::text, true),
		pg_catalog.set_config('statement_timeout', $3::text, true),
		pg_catalog.set_config('lock_timeout', $4::text, true),
		pg_catalog.set_config('idle_in_transaction_session_timeout', $5::text, true),
		pg_catalog.set_config('DateStyle', 'ISO, YMD', true),
		pg_catalog.set_config('TimeZone', 'UTC', true),
		pg_catalog.set_config('IntervalStyle', 'postgres', true),
		pg_catalog.set_config('extra_float_digits', '1', true),
		pg_catalog.set_config('bytea_output', 'hex', true),
		pg_catalog.set_config('lc_monetary', 'C', true)`,
		label(run.Label),
		quoteIdent(r.schema),
		ms(r.lim.StatementTimeout),
		ms(r.lim.LockTimeout),
		ms(r.lim.StatementTimeout+30*time.Second),
	)
	return err
}

// label makes a RunInfo label fit application_name: printable ASCII, at
// most 63 bytes.
func label(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 0x20 && r < 0x7f {
			b.WriteRune(r)
		} else {
			b.WriteByte('?')
		}
		if b.Len() >= 63 {
			break
		}
	}
	if b.Len() == 0 {
		return "dbairlock"
	}
	return b.String()
}

// tableMeta is what the connector needs to know about a target table.
type tableMeta struct {
	columns []string // in attnum order
	types   []string // format_type without typmod, for casts
	pk      []int    // indexes into columns, in key order
}

func (r *runner) table(schema, table string) (tableMeta, error) {
	res, err := r.query(`SELECT c.oid::text, c.relkind::text FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1::text AND c.relname = $2::text`, schema, table)
	if err != nil {
		return tableMeta{}, err
	}
	if len(res.Rows) == 0 {
		return tableMeta{}, fmt.Errorf("table %s does not exist", quoteQualified(schema, table))
	}
	oid, kind := string(res.Rows[0][0]), string(res.Rows[0][1])
	switch kind {
	case "r", "p":
	case "f":
		return tableMeta{}, fmt.Errorf("%w: %s is a foreign table", connector.ErrNotTransactional, quoteQualified(schema, table))
	default:
		return tableMeta{}, fmt.Errorf("%s is not a table", quoteQualified(schema, table))
	}
	res, err = r.query(`SELECT a.attname::text, pg_catalog.format_type(a.atttypid, NULL)
		FROM pg_catalog.pg_attribute a
		WHERE a.attrelid = $1::oid AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY a.attnum`, oid)
	if err != nil {
		return tableMeta{}, err
	}
	var m tableMeta
	for _, row := range res.Rows {
		m.columns = append(m.columns, string(row[0]))
		m.types = append(m.types, string(row[1]))
	}
	res, err = r.query(`SELECT a.attname::text
		FROM pg_catalog.pg_index i
		CROSS JOIN LATERAL pg_catalog.unnest(i.indkey::int2[]) WITH ORDINALITY AS k(attnum, ord)
		JOIN pg_catalog.pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
		WHERE i.indrelid = $1::oid AND i.indisprimary
		ORDER BY k.ord`, oid)
	if err != nil {
		return tableMeta{}, err
	}
	for _, row := range res.Rows {
		idx := slices.Index(m.columns, string(row[0]))
		if idx < 0 {
			return tableMeta{}, fmt.Errorf("primary key column %q not found", row[0])
		}
		m.pk = append(m.pk, idx)
	}
	return m, nil
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

func (r *runner) statement(i int, st connector.Statement, exp *connector.Expectation, dry bool) (connector.Result, error) {
	meta, err := r.table(st.Schema, st.Table)
	if err != nil {
		return connector.Result{}, err
	}
	res := connector.Result{
		Schema:        st.Schema,
		Table:         st.Table,
		HasPrimaryKey: len(meta.pk) > 0,
		Columns:       meta.columns,
	}
	for _, k := range meta.pk {
		res.PrimaryKey = append(res.PrimaryKey, meta.columns[k])
	}
	from := quoteQualified(st.Schema, st.Table)
	if st.Alias != "" {
		from += " AS " + quoteIdent(st.Alias)
	}

	var before *pgconn.Result
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
		matched := int64(len(before.Rows))
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

	text := st.SQL
	var returning bool
	if dry && st.Kind == connector.KindInsert {
		// Show the inserted rows, including defaults, in the preview. Only
		// the dry run (which always rolls back) appends RETURNING; the
		// execution runs the reviewed text unchanged.
		body, err := statementBody(st.SQL)
		if err != nil {
			return res, err
		}
		text, returning = body+"\nRETURNING "+meta.selectList(), true
	}
	out, err := r.query(text)
	if err != nil {
		return res, err
	}
	res.RowsAffected = out.CommandTag.RowsAffected()
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
	if returning {
		var t bool
		res.After, t = displayRows(out, r.lim.MaxCellBytes)
		res.Truncated = res.Truncated || t
	}

	if st.Kind == connector.KindUpdate && len(meta.pk) > 0 && before != nil && len(before.Rows) > 0 {
		after, err := r.afterImage(meta, st, before)
		if err != nil {
			return res, err
		}
		var t bool
		res.After, t = displayRows(after, r.lim.MaxCellBytes)
		res.Truncated = res.Truncated || t
	}
	return res, nil
}

func countText(n int64, maxRows int) string {
	if n > int64(maxRows) {
		return "more than " + strconv.Itoa(maxRows)
	}
	return strconv.FormatInt(n, 10)
}

// afterImage re-selects the updated rows by their primary key values.
// Rows whose key the statement changed are not found; the after image is
// for display only.
func (r *runner) afterImage(meta tableMeta, st connector.Statement, before *pgconn.Result) (*pgconn.Result, error) {
	keyCols := make([]string, len(meta.pk))
	casts := make([]string, len(meta.pk))
	params := make([]string, len(meta.pk))
	unnest := make([]string, len(meta.pk))
	aliases := make([]string, len(meta.pk))
	for j, k := range meta.pk {
		keyCols[j] = quoteIdent(meta.columns[k])
		aliases[j] = "k" + strconv.Itoa(j+1)
		casts[j] = fmt.Sprintf("u.%s::%s", aliases[j], meta.types[k])
		unnest[j] = fmt.Sprintf("$%d::text[]", j+1)
		vals := make([]string, len(before.Rows))
		for n, row := range before.Rows {
			vals[n] = string(row[k])
		}
		params[j] = textArray(vals)
	}
	q := fmt.Sprintf("SELECT %s FROM %s WHERE (%s) IN (SELECT %s FROM pg_catalog.unnest(%s) AS u(%s))%s",
		meta.selectList(), quoteQualified(st.Schema, st.Table), strings.Join(keyCols, ", "),
		strings.Join(casts, ", "), strings.Join(unnest, ", "), strings.Join(aliases, ", "), meta.orderBy())
	return r.query(q, params...)
}

// textArray encodes strings as a text[] literal.
func textArray(vals []string) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, v := range vals {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		b.WriteString(strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

// fingerprint computes the row fingerprint of a before image (PLAN.md 5.4).
func fingerprint(st connector.Statement, meta tableMeta, before *pgconn.Result) (string, error) {
	var setIdx []int
	if st.Kind == connector.KindUpdate && len(meta.pk) > 0 {
		for _, col := range st.Columns {
			idx := slices.Index(meta.columns, col)
			if idx < 0 {
				return "", fmt.Errorf("column %q does not exist in %s", col, quoteQualified(st.Schema, st.Table))
			}
			setIdx = append(setIdx, idx)
		}
	}
	oids := fieldOIDs(before, len(meta.columns))
	rows := make([]connector.FingerprintRow, len(before.Rows))
	for n, raw := range before.Rows {
		cell := func(i int) connector.Cell { return canonicalCell(oids[i], raw[i]) }
		var fr connector.FingerprintRow
		if len(meta.pk) > 0 {
			for _, k := range meta.pk {
				fr.Key = append(fr.Key, cell(k))
			}
			for _, k := range setIdx {
				fr.Values = append(fr.Values, cell(k))
			}
		} else {
			for k := range meta.columns {
				fr.Values = append(fr.Values, cell(k))
			}
		}
		rows[n] = fr
	}
	return connector.Fingerprint(rows), nil
}

// fieldOIDs returns the type OIDs of a result's columns. A result without
// rows has no field descriptions; then every OID is 0 (unused).
func fieldOIDs(res *pgconn.Result, n int) []uint32 {
	oids := make([]uint32, n)
	for i, fd := range res.FieldDescriptions {
		if i < n {
			oids[i] = fd.DataTypeOID
		}
	}
	return oids
}

func displayRows(res *pgconn.Result, maxBytes int) ([]connector.Row, bool) {
	if res == nil || len(res.Rows) == 0 {
		return nil, false
	}
	oids := fieldOIDs(res, len(res.FieldDescriptions))
	truncated := false
	out := make([]connector.Row, len(res.Rows))
	for n, raw := range res.Rows {
		row := make(connector.Row, len(raw))
		for i, v := range raw {
			var t bool
			row[i], t = displayValue(oids[i], v, maxBytes)
			truncated = truncated || t
		}
		out[n] = row
	}
	return out, truncated
}

// statementBody returns the statement text without a trailing semicolon or
// trailing comments, so a clause can be appended.
func statementBody(sql string) (string, error) {
	scan, err := pg.Scan(sql)
	if err != nil {
		return "", fmt.Errorf("scan statement: %w", err)
	}
	end := -1
	for _, tok := range scan.GetTokens() {
		switch tok.GetToken() {
		case pg.Token_SQL_COMMENT, pg.Token_C_COMMENT, pg.Token_ASCII_59:
			continue
		}
		end = int(tok.GetEnd())
	}
	if end <= 0 || end > len(sql) {
		return "", errors.New("empty statement")
	}
	return sql[:end], nil
}
