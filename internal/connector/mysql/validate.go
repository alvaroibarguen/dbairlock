package mysql

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/parser/ast"
	"github.com/pingcap/tidb/pkg/parser/format"
	pmysql "github.com/pingcap/tidb/pkg/parser/mysql"
	"github.com/pingcap/tidb/pkg/parser/opcode"

	// The parser needs a driver for literal values. test_driver is the
	// self-contained one that does not pull in the rest of TiDB.
	_ "github.com/pingcap/tidb/pkg/parser/test_driver"

	"github.com/alvaroibarguen/dbairlock/internal/connector"
)

// Validation follows PLAN.md 5.1 and phase 6 step 3. Two independent checks
// apply to every statement, and both are fail closed:
//
//  1. A walker that handles each statement, clause and expression type it
//     knows, and rejects every other type in its default branch.
//  2. An ast.Visitor over the whole tree that rejects any node type not on
//     an explicit list, so a field the walker does not look at cannot hide
//     an unknown construct.
//
// Executable comments are rejected on the raw text before parsing.

// parsers holds reusable TiDB parsers. A parser is not safe for concurrent
// use and its results are only valid until its next Parse call, so each is
// taken for one validation and returned afterwards.
var parsers = sync.Pool{New: func() any { return parser.New() }}

// validate checks one statement for a connection to database db.
func validate(sql, db string, p connector.Policy) (connector.Statement, error) {
	return validateWith(sql, db, p, false)
}

// validateWith is validate; anyFunction skips the function allowlist, for
// re-deriving a statement's fields when the policy is not known.
func validateWith(sql, db string, p connector.Policy, anyFunction bool) (st connector.Statement, err error) {
	defer func() {
		// A panic in the parser or the walker must never let a statement
		// through.
		if r := recover(); r != nil {
			st, err = connector.Statement{}, connector.Reject(connector.RejectParse, -1, "the statement could not be parsed")
		}
	}()
	if strings.TrimSpace(sql) == "" {
		return connector.Statement{}, connector.Reject(connector.RejectEmpty, -1, "the statement is empty")
	}
	if i := strings.IndexByte(sql, 0); i >= 0 {
		return connector.Statement{}, connector.Reject(connector.RejectParse, i, "the statement contains a NUL byte")
	}
	if i := executableComment(sql); i >= 0 {
		return connector.Statement{}, connector.Reject(connector.RejectExecutableComment, i,
			"executable comments (/*! ... */, /*+ ... */) are not allowed; MySQL runs their contents as SQL")
	}
	if db == "" {
		return connector.Statement{}, connector.Reject(connector.RejectUnsupported, -1, "the connection has no database configured")
	}
	v := &validator{
		src:         sql,
		policy:      p,
		anyFunction: anyFunction,
		db:          db,
		funcs:       map[string]struct{}{},
		blocked:     map[string]bool{},
	}
	for _, s := range p.BlockedSchemas {
		v.blocked[strings.ToLower(s)] = true
	}
	if v.isSystemDatabase(db) {
		return connector.Statement{}, connector.Reject(connector.RejectSystemSchema, -1,
			"the connection's database %q is a system or blocked database", db)
	}

	ps, _ := parsers.Get().(*parser.Parser)
	if ps == nil {
		ps = parser.New()
	}
	defer parsers.Put(ps)
	stmts, _, perr := ps.Parse(sql, "", "")
	if perr != nil {
		return connector.Statement{}, connector.Reject(connector.RejectParse, -1, "the statement could not be parsed: %s", parseMessage(perr))
	}
	switch len(stmts) {
	case 0:
		return connector.Statement{}, connector.Reject(connector.RejectEmpty, -1, "the text contains no statement")
	case 1:
	default:
		pos := -1
		if t := strings.TrimSpace(stmts[1].OriginalText()); t != "" {
			pos = strings.LastIndex(sql, t)
		}
		return connector.Statement{}, connector.Reject(connector.RejectMultipleStatements, pos,
			"only one statement is allowed per text; found %d", len(stmts))
	}
	st, err = v.statement(stmts[0])
	if err != nil {
		return connector.Statement{}, err
	}
	if err := v.allowlist(stmts[0]); err != nil {
		return connector.Statement{}, err
	}
	st.SQL = sql
	st.Schema = db
	for f := range v.funcs {
		st.Functions = append(st.Functions, f)
	}
	slices.Sort(st.Functions)
	return st, nil
}

// parseMessage keeps the useful part of a TiDB parse error.
func parseMessage(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, "line "); i >= 0 {
		msg = msg[i:]
	}
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}

// executableComment returns the offset of the first comment that MySQL (or
// a fork) executes: /*! (MySQL), /*+ (optimizer hints), /*M! (MariaDB) and
// /*T! (TiDB, which the parser would execute but MySQL would not). The
// whole text is searched, string literals included: a false positive costs
// a rewrite, a miss could change what runs.
func executableComment(sql string) int {
	for i := 0; i+2 < len(sql); i++ {
		if sql[i] != '/' || sql[i+1] != '*' {
			continue
		}
		c := sql[i+2]
		if c == '!' || c == '+' {
			return i
		}
		if i+3 < len(sql) && ((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) && sql[i+3] == '!' {
			return i
		}
	}
	return -1
}

type validator struct {
	src         string
	policy      connector.Policy
	anyFunction bool
	db          string
	funcs       map[string]struct{}
	blocked     map[string]bool

	// target table and alias, for recognizing references to the target
	// row inside subqueries
	table, alias string
}

// exprCtx carries where in the tree an expression is.
type exprCtx struct {
	allowDefault bool // DEFAULT is allowed as this exact value (SET, VALUES)
}

func (v *validator) isSystemDatabase(name string) bool {
	return isSystemDatabase(name) || v.blocked[strings.ToLower(name)]
}

// pos returns an expression's byte offset in the text, or -1.
func (v *validator) pos(e ast.ExprNode) int {
	if e == nil {
		return -1
	}
	off := e.OriginTextPosition()
	if off <= 0 || off >= len(v.src) {
		return -1
	}
	return off
}

func (v *validator) statement(n ast.StmtNode) (connector.Statement, error) {
	switch s := n.(type) {
	case *ast.InsertStmt:
		return v.insert(s)
	case *ast.UpdateStmt:
		return v.update(s)
	case *ast.DeleteStmt:
		return v.delete(s)
	case *ast.SelectStmt, *ast.SetOprStmt:
		return connector.Statement{}, connector.Reject(connector.RejectStatementType, 0,
			"SELECT statements are not allowed; only INSERT, UPDATE and DELETE are")
	default:
		return connector.Statement{}, connector.Reject(connector.RejectStatementType, 0,
			"%s statements are not allowed; only INSERT, UPDATE and DELETE are", statementName(n))
	}
}

func (v *validator) insert(s *ast.InsertStmt) (connector.Statement, error) {
	var none connector.Statement
	switch {
	case s.IsReplace:
		return none, connector.Reject(connector.RejectUpsert, 0, "REPLACE is not allowed; it deletes and re-inserts rows. Use INSERT, UPDATE or DELETE")
	case len(s.OnDuplicate) > 0:
		return none, connector.Reject(connector.RejectUpsert, -1, "INSERT ... ON DUPLICATE KEY UPDATE is not allowed")
	case len(s.Returning) > 0:
		return none, connector.Reject(connector.RejectReturning, -1, "RETURNING is not allowed")
	case s.IgnoreErr:
		return none, connector.Reject(connector.RejectUnsupported, -1, "INSERT IGNORE is not allowed; errors must not be turned into warnings")
	case s.Priority != pmysql.NoPriority:
		return none, connector.Reject(connector.RejectUnsupported, -1, "%s is not allowed", pmysql.Priority2Str[s.Priority])
	case len(s.TableHints) > 0:
		return none, connector.Reject(connector.RejectUnsupported, -1, "optimizer hints are not allowed")
	case len(s.PartitionNames) > 0:
		return none, connector.Reject(connector.RejectUnsupported, -1, "partition selection is not allowed")
	case s.RowAlias.O != "" || len(s.ColumnAliases) > 0:
		return none, connector.Reject(connector.RejectUnsupported, -1, "row aliases (VALUES ... AS) are not allowed")
	}
	st, err := v.target(s.Table)
	if err != nil {
		return none, err
	}
	st.Kind = connector.KindInsert
	switch {
	case s.Setlist:
		return none, connector.Reject(connector.RejectInsertForm, -1, "INSERT ... SET is not allowed; use INSERT INTO t (a, b) VALUES (...)")
	case s.Select != nil:
		return none, connector.Reject(connector.RejectInsertForm, -1, "INSERT ... SELECT is not allowed; only INSERT ... VALUES")
	case len(s.Columns) == 0:
		return none, connector.Reject(connector.RejectInsertForm, -1, "INSERT must list its columns: INSERT INTO t (a, b) VALUES (...)")
	case len(s.Lists) == 0:
		return none, connector.Reject(connector.RejectInsertForm, -1, "INSERT must have a VALUES list")
	}
	for _, c := range s.Columns {
		if c == nil || c.Name.O == "" {
			return none, connector.Reject(connector.RejectUnsupported, -1, "unsupported INSERT column list")
		}
		if c.Schema.O != "" || c.Table.O != "" {
			return none, connector.Reject(connector.RejectUnsupported, -1, "qualified column names are not allowed in an INSERT column list")
		}
		st.Columns = append(st.Columns, c.Name.O)
	}
	for _, row := range s.Lists {
		if len(row) != len(st.Columns) {
			pos := -1
			if len(row) > 0 {
				pos = v.pos(row[0])
			}
			return none, connector.Reject(connector.RejectInsertForm, pos,
				"a VALUES row has %d values but the column list has %d", len(row), len(st.Columns))
		}
		for _, e := range row {
			if err := v.expr(e, exprCtx{allowDefault: true}); err != nil {
				return none, err
			}
		}
	}
	st.InsertRows = len(s.Lists)
	return st, nil
}

func (v *validator) update(s *ast.UpdateStmt) (connector.Statement, error) {
	var none connector.Statement
	switch {
	case s.With != nil:
		return none, connector.Reject(connector.RejectWith, 0, "WITH clauses are not allowed")
	case len(s.Returning) > 0:
		return none, connector.Reject(connector.RejectReturning, -1, "RETURNING is not allowed")
	case s.MultipleTable || (s.TableRefs != nil && s.TableRefs.TableRefs != nil && s.TableRefs.TableRefs.Right != nil):
		return none, connector.Reject(connector.RejectMultipleTables, -1, "multi-table UPDATE is not allowed; a statement may change only one table")
	case s.IgnoreErr:
		return none, connector.Reject(connector.RejectUnsupported, -1, "UPDATE IGNORE is not allowed; errors must not be turned into warnings")
	case s.Priority != pmysql.NoPriority:
		return none, connector.Reject(connector.RejectUnsupported, -1, "%s is not allowed", pmysql.Priority2Str[s.Priority])
	case len(s.TableHints) > 0:
		return none, connector.Reject(connector.RejectUnsupported, -1, "optimizer hints are not allowed")
	case s.Order != nil:
		return none, connector.Reject(connector.RejectUnsupported, -1, "ORDER BY is not allowed on UPDATE; select the rows with WHERE")
	case s.Limit != nil:
		return none, connector.Reject(connector.RejectUnsupported, -1, "LIMIT is not allowed on UPDATE; select the rows with WHERE")
	}
	st, err := v.target(s.TableRefs)
	if err != nil {
		return none, err
	}
	st.Kind = connector.KindUpdate
	if len(s.List) == 0 {
		return none, connector.Reject(connector.RejectUnsupported, -1, "UPDATE without SET")
	}
	for _, a := range s.List {
		if a == nil || a.Column == nil || a.Column.Name.O == "" {
			return none, connector.Reject(connector.RejectUnsupported, -1, "unsupported SET clause")
		}
		if err := v.assignedColumn(a.Column); err != nil {
			return none, err
		}
		if err := v.expr(a.Expr, exprCtx{allowDefault: true}); err != nil {
			return none, err
		}
		st.Columns = append(st.Columns, a.Column.Name.O)
	}
	if err := v.where(s.Where, &st, "UPDATE"); err != nil {
		return none, err
	}
	return st, nil
}

func (v *validator) delete(s *ast.DeleteStmt) (connector.Statement, error) {
	var none connector.Statement
	switch {
	case s.With != nil:
		return none, connector.Reject(connector.RejectWith, 0, "WITH clauses are not allowed")
	case len(s.Returning) > 0:
		return none, connector.Reject(connector.RejectReturning, -1, "RETURNING is not allowed")
	case s.IsMultiTable || s.Tables != nil || (s.TableRefs != nil && s.TableRefs.TableRefs != nil && s.TableRefs.TableRefs.Right != nil):
		return none, connector.Reject(connector.RejectMultipleTables, -1, "multi-table DELETE is not allowed; a statement may change only one table")
	case s.IgnoreErr:
		return none, connector.Reject(connector.RejectUnsupported, -1, "DELETE IGNORE is not allowed; errors must not be turned into warnings")
	case s.Quick:
		return none, connector.Reject(connector.RejectUnsupported, -1, "DELETE QUICK is not allowed")
	case s.Priority != pmysql.NoPriority:
		return none, connector.Reject(connector.RejectUnsupported, -1, "%s is not allowed", pmysql.Priority2Str[s.Priority])
	case len(s.TableHints) > 0:
		return none, connector.Reject(connector.RejectUnsupported, -1, "optimizer hints are not allowed")
	case s.Order != nil:
		return none, connector.Reject(connector.RejectUnsupported, -1, "ORDER BY is not allowed on DELETE; select the rows with WHERE")
	case s.Limit != nil:
		return none, connector.Reject(connector.RejectUnsupported, -1, "LIMIT is not allowed on DELETE; select the rows with WHERE")
	}
	st, err := v.target(s.TableRefs)
	if err != nil {
		return none, err
	}
	st.Kind = connector.KindDelete
	if err := v.where(s.Where, &st, "DELETE"); err != nil {
		return none, err
	}
	return st, nil
}

// target checks the single table a statement changes.
func (v *validator) target(refs *ast.TableRefsClause) (connector.Statement, error) {
	var none connector.Statement
	if refs == nil || refs.TableRefs == nil {
		return none, connector.Reject(connector.RejectUnsupported, -1, "the statement has no target table")
	}
	j := refs.TableRefs
	if j.Right != nil {
		return none, connector.Reject(connector.RejectMultipleTables, -1, "a statement may change only one table")
	}
	ts, ok := j.Left.(*ast.TableSource)
	if !ok {
		return none, connector.Reject(connector.RejectMultipleTables, -1, "a statement may change only one plain table")
	}
	if ts.Lateral || len(ts.ColumnNames) > 0 {
		return none, connector.Reject(connector.RejectUnsupported, -1, "unsupported target table")
	}
	tn, ok := ts.Source.(*ast.TableName)
	if !ok {
		return none, connector.Reject(connector.RejectUnsupported, -1, "the target must be a table, not a subquery")
	}
	if err := v.relation(tn); err != nil {
		return none, err
	}
	st := connector.Statement{Schema: v.db, Table: tn.Name.O, Alias: ts.AsName.O}
	v.table, v.alias = st.Table, st.Alias
	return st, nil
}

// relation checks any table reference: the target or one in a subquery.
func (v *validator) relation(tn *ast.TableName) error {
	if tn.Name.O == "" {
		return connector.Reject(connector.RejectUnsupported, -1, "unsupported table reference")
	}
	if err := v.qualifier(tn.Schema.O, tn.Name.O); err != nil {
		return err
	}
	switch {
	case len(tn.IndexHints) > 0:
		return connector.Reject(connector.RejectUnsupported, -1, "index hints (USE, FORCE, IGNORE INDEX) are not allowed")
	case len(tn.PartitionNames) > 0:
		return connector.Reject(connector.RejectUnsupported, -1, "partition selection is not allowed")
	case tn.TableSample != nil || tn.AsOf != nil:
		return connector.Reject(connector.RejectUnsupported, -1, "unsupported table reference")
	}
	return nil
}

// qualifier checks the database a table or column is qualified with: none,
// or exactly the connection's database.
func (v *validator) qualifier(schema, name string) error {
	if schema == "" {
		return nil
	}
	if v.isSystemDatabase(schema) {
		return connector.Reject(connector.RejectSystemSchema, -1, "%s.%s is in the system or blocked database %q", schema, name, schema)
	}
	if schema != v.db {
		return connector.Reject(connector.RejectOtherDatabase, -1,
			"%s.%s is in database %q; this connection may only use database %q", schema, name, schema, v.db)
	}
	return nil
}

// assignedColumn checks the column on the left of SET: unqualified, or
// qualified with the target table (or its alias).
func (v *validator) assignedColumn(c *ast.ColumnName) error {
	if err := v.qualifier(c.Schema.O, c.Table.O); err != nil {
		return err
	}
	if c.Table.O != "" && !v.isTarget(c) {
		return connector.Reject(connector.RejectMultipleTables, -1, "SET %s.%s does not refer to the target table", c.Table.O, c.Name.O)
	}
	return nil
}

// isTarget reports whether a column reference is qualified with the target
// table's alias, or its name when it has no alias.
func (v *validator) isTarget(c *ast.ColumnName) bool {
	if c.Table.O == "" || (c.Schema.O != "" && c.Schema.O != v.db) {
		return false
	}
	if v.alias != "" {
		return c.Table.O == v.alias
	}
	return c.Table.O == v.table
}

// where validates the WHERE clause of an UPDATE or DELETE and extracts its
// source text.
func (v *validator) where(w ast.ExprNode, st *connector.Statement, verb string) error {
	if w == nil {
		return connector.Reject(connector.RejectMissingWhere, -1, "%s without a WHERE clause is not allowed", verb)
	}
	if err := v.expr(w, exprCtx{}); err != nil {
		return err
	}
	pos := v.pos(w)
	if v.rowRefs(w) == 0 {
		return connector.Reject(connector.RejectConstantWhere, pos,
			"the WHERE clause does not reference any column of %s, so it matches every row or none", st.Table)
	}
	if v.constTrue(w) {
		return connector.Reject(connector.RejectConstantWhere, pos,
			"the WHERE clause is always true, or has a part that does not depend on the row (for example OR 1=1)")
	}
	text, err := whereText(v.src, w)
	if err != nil {
		return err
	}
	st.Where = text
	return nil
}

// expr validates an expression.
func (v *validator) expr(e ast.ExprNode, c exprCtx) error {
	if e == nil {
		return nil
	}
	allowDefault := c.allowDefault
	c.allowDefault = false
	pos := v.pos(e)
	switch x := e.(type) {
	case ast.ParamMarkerExpr: // before ValueExpr: a parameter marker is also a ValueExpr
		return connector.Reject(connector.RejectUnsupported, pos, "parameters (?) are not allowed; write the values into the statement")
	case ast.ValueExpr:
		return nil
	case *ast.ColumnNameExpr:
		if x.Name == nil || x.Name.Name.O == "" {
			return connector.Reject(connector.RejectUnsupported, pos, "unsupported column reference")
		}
		return v.qualifier(x.Name.Schema.O, x.Name.Table.O)
	case *ast.DefaultExpr:
		if x.Name != nil {
			return connector.Reject(connector.RejectUnsupported, pos, "DEFAULT(column) is not allowed")
		}
		if !allowDefault {
			return connector.Reject(connector.RejectUnsupported, pos, "DEFAULT is allowed only as a whole value in SET or VALUES")
		}
		return nil
	case *ast.VariableExpr:
		return connector.Reject(connector.RejectUnsupported, pos, "user and system variables (@x, @@x) are not allowed")
	case *ast.BinaryOperationExpr:
		return v.exprs(c, x.L, x.R)
	case *ast.UnaryOperationExpr:
		return v.expr(x.V, c)
	case *ast.ParenthesesExpr:
		return v.expr(x.Expr, c)
	case *ast.IsNullExpr:
		return v.expr(x.Expr, c)
	case *ast.IsTruthExpr:
		return v.expr(x.Expr, c)
	case *ast.BetweenExpr:
		return v.exprs(c, x.Expr, x.Left, x.Right)
	case *ast.PatternInExpr:
		if err := v.exprs(c, x.Expr, x.Sel); err != nil {
			return err
		}
		return v.exprs(c, x.List...)
	case *ast.PatternLikeOrIlikeExpr:
		return v.exprs(c, x.Expr, x.Pattern)
	case *ast.PatternRegexpExpr:
		return v.exprs(c, x.Expr, x.Pattern)
	case *ast.CaseExpr:
		if err := v.exprs(c, x.Value, x.ElseClause); err != nil {
			return err
		}
		for _, w := range x.WhenClauses {
			if w == nil {
				return connector.Reject(connector.RejectUnsupported, pos, "unsupported CASE branch")
			}
			if err := v.exprs(c, w.Expr, w.Result); err != nil {
				return err
			}
		}
		return nil
	case *ast.RowExpr:
		return v.exprs(c, x.Values...)
	case *ast.SetCollationExpr:
		return v.expr(x.Expr, c)
	case *ast.FuncCallExpr:
		return v.funcCall(x, c)
	case *ast.FuncCastExpr:
		name := "cast"
		if x.FunctionType == ast.CastConvertFunction {
			name = "convert"
		}
		if err := v.useFunc(name, pos); err != nil {
			return err
		}
		return v.expr(x.Expr, c)
	case *ast.AggregateFuncExpr:
		return connector.Reject(connector.RejectFunction, pos, "aggregate function calls (%s) are not allowed", strings.ToLower(x.F))
	case *ast.WindowFuncExpr:
		return connector.Reject(connector.RejectFunction, pos, "window function calls (%s) are not allowed", strings.ToLower(x.Name))
	case *ast.TrimDirectionExpr, *ast.TimeUnitExpr, *ast.GetFormatSelectorExpr:
		return nil // keyword arguments of TRIM, DATE_ADD and GET_FORMAT
	case *ast.SubqueryExpr:
		return v.subquery(x, c)
	case *ast.ExistsSubqueryExpr:
		return v.expr(x.Sel, c)
	case *ast.CompareSubqueryExpr:
		return v.exprs(c, x.L, x.R)
	case *ast.ValuesExpr:
		return connector.Reject(connector.RejectUpsert, pos, "VALUES(column) belongs to ON DUPLICATE KEY UPDATE, which is not allowed")
	default:
		return connector.Reject(connector.RejectUnsupported, pos, "%s is not supported", nodeName(e))
	}
}

func (v *validator) exprs(c exprCtx, es ...ast.ExprNode) error {
	for _, e := range es {
		if err := v.expr(e, c); err != nil {
			return err
		}
	}
	return nil
}

// literalFuncs are the internal function names the parser gives typed
// literals (DATE '2024-01-01'); they are casts.
var literalFuncs = map[string]bool{
	strings.ToLower(ast.DateLiteral):      true,
	strings.ToLower(ast.TimeLiteral):      true,
	strings.ToLower(ast.TimestampLiteral): true,
}

func (v *validator) funcCall(x *ast.FuncCallExpr, c exprCtx) error {
	pos := v.pos(x)
	if x.Schema.O != "" {
		return connector.Reject(connector.RejectFunction, pos, "stored function %s.%s is not allowed; only built-in functions on the allowlist are", x.Schema.O, x.FnName.O)
	}
	name := x.FnName.L
	if literalFuncs[name] {
		name = "cast"
	}
	if name == "" || strings.ContainsAny(name, "`'(. ") {
		return connector.Reject(connector.RejectUnsupported, pos, "unsupported function call")
	}
	if err := v.useFunc(name, pos); err != nil {
		return err
	}
	return v.exprs(c, x.Args...)
}

func (v *validator) useFunc(name string, pos int) error {
	if !v.anyFunction && !v.policy.Allows(name) {
		return connector.Reject(connector.RejectFunction, pos, "function %s is not on this connection's allowlist", name)
	}
	v.funcs[name] = struct{}{}
	return nil
}

func (v *validator) subquery(sq *ast.SubqueryExpr, c exprCtx) error {
	pos := v.pos(sq)
	switch q := sq.Query.(type) {
	case *ast.SelectStmt:
		return v.subselect(q, c, pos)
	case *ast.SetOprStmt:
		return connector.Reject(connector.RejectUnsupported, pos, "UNION, INTERSECT and EXCEPT are not allowed in subqueries")
	default:
		return connector.Reject(connector.RejectUnsupported, pos, "unsupported subquery")
	}
}

// subselect allows a plain SELECT inside an expression, subject to the same
// function and database rules.
func (v *validator) subselect(s *ast.SelectStmt, c exprCtx, pos int) error {
	if s.With != nil {
		return connector.Reject(connector.RejectWith, pos, "WITH clauses are not allowed")
	}
	if s.Kind != ast.SelectStmtKindSelect || len(s.Lists) > 0 {
		return connector.Reject(connector.RejectUnsupported, pos, "only plain SELECT subqueries are allowed")
	}
	if s.LockInfo != nil && s.LockInfo.LockType != ast.SelectLockNone {
		return connector.Reject(connector.RejectUnsupported, pos, "locking clauses (FOR UPDATE, FOR SHARE) are not allowed in subqueries")
	}
	if s.SelectIntoOpt != nil {
		return connector.Reject(connector.RejectUnsupported, pos, "SELECT ... INTO is not allowed")
	}
	if len(s.WindowSpecs) > 0 {
		return connector.Reject(connector.RejectUnsupported, pos, "window definitions are not allowed")
	}
	if len(s.TableHints) > 0 {
		return connector.Reject(connector.RejectUnsupported, pos, "optimizer hints are not allowed")
	}
	if o := s.SelectStmtOpts; o != nil {
		if len(o.TableHints) > 0 || o.CalcFoundRows || o.StraightJoin || o.SQLBigResult || o.SQLSmallResult ||
			o.SQLBufferResult || o.Priority != pmysql.NoPriority {
			return connector.Reject(connector.RejectUnsupported, pos, "SELECT modifiers (SQL_CALC_FOUND_ROWS, STRAIGHT_JOIN, HIGH_PRIORITY and the like) are not allowed")
		}
	}
	if s.Fields == nil {
		return connector.Reject(connector.RejectUnsupported, pos, "unsupported subquery select list")
	}
	for _, f := range s.Fields.Fields {
		if f == nil {
			return connector.Reject(connector.RejectUnsupported, pos, "unsupported subquery select list")
		}
		if f.WildCard != nil {
			if err := v.qualifier(f.WildCard.Schema.O, f.WildCard.Table.O); err != nil {
				return err
			}
			continue
		}
		if err := v.expr(f.Expr, c); err != nil {
			return err
		}
	}
	if s.From != nil {
		if s.From.TableRefs == nil {
			return connector.Reject(connector.RejectUnsupported, pos, "unsupported FROM clause")
		}
		if err := v.fromItem(s.From.TableRefs, c, pos); err != nil {
			return err
		}
	}
	if err := v.expr(s.Where, c); err != nil {
		return err
	}
	if s.GroupBy != nil {
		for _, it := range s.GroupBy.Items {
			if err := v.byItem(it, c, pos); err != nil {
				return err
			}
		}
	}
	if s.Having != nil {
		if err := v.expr(s.Having.Expr, c); err != nil {
			return err
		}
	}
	if s.OrderBy != nil {
		for _, it := range s.OrderBy.Items {
			if err := v.byItem(it, c, pos); err != nil {
				return err
			}
		}
	}
	if s.Limit != nil {
		return v.exprs(c, s.Limit.Count, s.Limit.Offset)
	}
	return nil
}

func (v *validator) byItem(it *ast.ByItem, c exprCtx, pos int) error {
	if it == nil {
		return connector.Reject(connector.RejectUnsupported, pos, "unsupported ORDER BY or GROUP BY item")
	}
	return v.expr(it.Expr, c)
}

func (v *validator) fromItem(n ast.ResultSetNode, c exprCtx, pos int) error {
	switch x := n.(type) {
	case nil:
		return nil
	case *ast.Join:
		if err := v.fromItem(x.Left, c, pos); err != nil {
			return err
		}
		if err := v.fromItem(x.Right, c, pos); err != nil {
			return err
		}
		if x.On != nil {
			return v.expr(x.On.Expr, c)
		}
		return nil
	case *ast.TableSource:
		if x.Lateral || len(x.ColumnNames) > 0 {
			return connector.Reject(connector.RejectUnsupported, pos, "unsupported table reference in a subquery")
		}
		tn, ok := x.Source.(*ast.TableName)
		if !ok {
			return connector.Reject(connector.RejectUnsupported, pos, "derived tables in a subquery's FROM are not supported; use plain tables")
		}
		return v.relation(tn)
	default:
		return connector.Reject(connector.RejectUnsupported, pos, "%s in a subquery's FROM is not supported; use plain tables", nodeName(n))
	}
}

// allowedNodes are the only node types a statement may contain. The walker
// above decides what each is allowed to do; this list catches anything it
// did not look at.
var allowedNodes = func() map[reflect.Type]bool {
	m := map[reflect.Type]bool{}
	for _, n := range []ast.Node{
		&ast.InsertStmt{}, &ast.UpdateStmt{}, &ast.DeleteStmt{},
		&ast.TableRefsClause{}, &ast.Join{}, &ast.TableSource{}, &ast.TableName{}, &ast.OnCondition{},
		&ast.Assignment{}, &ast.ColumnName{}, &ast.ColumnNameExpr{}, &ast.DefaultExpr{},
		&ast.BinaryOperationExpr{}, &ast.UnaryOperationExpr{}, &ast.ParenthesesExpr{},
		&ast.IsNullExpr{}, &ast.IsTruthExpr{}, &ast.BetweenExpr{}, &ast.PatternInExpr{},
		&ast.PatternLikeOrIlikeExpr{}, &ast.PatternRegexpExpr{}, &ast.CaseExpr{}, &ast.WhenClause{},
		&ast.RowExpr{}, &ast.SetCollationExpr{}, &ast.FuncCallExpr{}, &ast.FuncCastExpr{},
		&ast.TrimDirectionExpr{}, &ast.TimeUnitExpr{}, &ast.GetFormatSelectorExpr{},
		&ast.SubqueryExpr{}, &ast.ExistsSubqueryExpr{}, &ast.CompareSubqueryExpr{},
		&ast.SelectStmt{}, &ast.FieldList{}, &ast.SelectField{}, &ast.WildCardField{},
		&ast.GroupByClause{}, &ast.HavingClause{}, &ast.OrderByClause{}, &ast.ByItem{}, &ast.Limit{},
	} {
		m[reflect.TypeOf(n)] = true
	}
	return m
}()

type allowVisitor struct {
	v   *validator
	err error
}

func (a *allowVisitor) Enter(n ast.Node) (ast.Node, bool) {
	if a.err != nil {
		return n, true
	}
	if _, ok := n.(ast.ParamMarkerExpr); ok {
		a.err = connector.Reject(connector.RejectUnsupported, -1, "parameters (?) are not allowed")
		return n, true
	}
	if _, ok := n.(ast.ValueExpr); ok {
		return n, false
	}
	if !allowedNodes[reflect.TypeOf(n)] {
		pos := -1
		if e, ok := n.(ast.ExprNode); ok {
			pos = a.v.pos(e)
		}
		a.err = connector.Reject(connector.RejectUnsupported, pos, "%s is not supported", nodeName(n))
		return n, true
	}
	return n, false
}

func (a *allowVisitor) Leave(n ast.Node) (ast.Node, bool) { return n, a.err == nil }

// allowlist rejects any node type not in allowedNodes, anywhere in the tree.
func (v *validator) allowlist(n ast.Node) error {
	a := &allowVisitor{v: v}
	n.Accept(a)
	return a.err
}

// nodeName returns a node's type name without package, e.g. "MatchAgainst".
func nodeName(n any) string {
	t := reflect.TypeOf(n)
	if t == nil {
		return "an empty node"
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Name()
}

// statementName turns a statement node into a readable name, e.g.
// "TruncateTableStmt" into "TRUNCATE TABLE".
func statementName(n ast.StmtNode) string {
	name := strings.TrimSuffix(nodeName(n), "Stmt")
	var b strings.Builder
	for i, r := range name {
		if i > 0 && unicode.IsUpper(r) {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return strings.ToUpper(b.String())
}

// rowRefs counts the column references in e that depend on the target row:
// any column outside subqueries (the statement has one table), and columns
// qualified with the target's name or alias inside them (correlated
// references).
func (v *validator) rowRefs(e ast.Node) int {
	r := &refCounter{v: v}
	e.Accept(r)
	return r.n
}

type refCounter struct {
	v     *validator
	depth int
	n     int
}

func (r *refCounter) Enter(n ast.Node) (ast.Node, bool) {
	switch x := n.(type) {
	case *ast.SelectStmt:
		r.depth++
	case *ast.ColumnNameExpr:
		if x.Name != nil && (r.depth == 0 || r.v.isTarget(x.Name)) {
			r.n++
		}
	}
	return n, false
}

func (r *refCounter) Leave(n ast.Node) (ast.Node, bool) {
	if _, ok := n.(*ast.SelectStmt); ok {
		r.depth--
	}
	return n, true
}

// constTrue reports whether a WHERE clause selects rows independently of
// their contents: it is a tautology, or an OR has a branch that does not
// reference the row and is not provably false (WHERE id = 2 OR 1 = 1).
// Unknown constant branches count as true: fail closed.
func (v *validator) constTrue(e ast.ExprNode) bool {
	switch x := e.(type) {
	case *ast.ParenthesesExpr:
		return v.constTrue(x.Expr)
	case *ast.BinaryOperationExpr:
		switch x.Op {
		case opcode.LogicAnd:
			return v.constTrue(x.L) && v.constTrue(x.R)
		case opcode.LogicOr:
			return v.constTrue(x.L) || v.constTrue(x.R)
		}
	}
	if v.rowRefs(e) == 0 {
		return constTruth(e) != truthFalse
	}
	return selfComparison(e)
}

// selfComparison reports whether e compares a column with itself in a way
// that is true for every (non-null) row, e.g. id = id.
func selfComparison(e ast.ExprNode) bool {
	b, ok := unparen(e).(*ast.BinaryOperationExpr)
	if !ok {
		return false
	}
	switch b.Op {
	case opcode.EQ, opcode.LE, opcode.GE, opcode.NullEQ:
	default:
		return false
	}
	l, lok := unparen(b.L).(*ast.ColumnNameExpr)
	r, rok := unparen(b.R).(*ast.ColumnNameExpr)
	return lok && rok && l.Name != nil && r.Name != nil &&
		l.Name.Schema.L == r.Name.Schema.L && l.Name.Table.L == r.Name.Table.L && l.Name.Name.L == r.Name.Name.L
}

func unparen(e ast.ExprNode) ast.ExprNode {
	for {
		p, ok := e.(*ast.ParenthesesExpr)
		if !ok {
			return e
		}
		e = p.Expr
	}
}

// truth is the result of constant-folding a boolean expression.
type truth int

const (
	truthUnknown truth = iota
	truthTrue
	truthFalse
)

func boolTruth(b bool) truth {
	if b {
		return truthTrue
	}
	return truthFalse
}

func (t truth) not() truth {
	switch t {
	case truthTrue:
		return truthFalse
	case truthFalse:
		return truthTrue
	}
	return truthUnknown
}

// constTruth folds an expression that does not reference the row, with
// MySQL's rules: a number is true when it is not zero, a string is
// converted to the number its leading characters spell, NULL never selects
// a row. Anything it cannot decide is unknown.
func constTruth(e ast.ExprNode) truth {
	switch x := e.(type) {
	case *ast.ParenthesesExpr:
		return constTruth(x.Expr)
	case ast.ParamMarkerExpr:
		return truthUnknown
	case ast.ValueExpr:
		return valueTruth(x.GetValue())
	case *ast.UnaryOperationExpr:
		if x.Op == opcode.Not || x.Op == opcode.Not2 {
			if isNull(x.V) {
				return truthUnknown // NOT NULL is NULL
			}
			return constTruth(x.V).not()
		}
	case *ast.IsNullExpr:
		if ve, ok := unparen(x.Expr).(ast.ValueExpr); ok {
			if _, pm := ve.(ast.ParamMarkerExpr); !pm {
				return boolTruth((ve.GetValue() == nil) != x.Not)
			}
		}
	case *ast.IsTruthExpr:
		t := constTruth(x.Expr)
		if t == truthUnknown || isNull(x.Expr) {
			return truthUnknown
		}
		res := (t == truthTrue) == (x.True != 0)
		return boolTruth(res != x.Not)
	case *ast.BinaryOperationExpr:
		switch x.Op {
		case opcode.LogicAnd:
			l, r := constTruth(x.L), constTruth(x.R)
			if l == truthFalse || r == truthFalse {
				return truthFalse
			}
			if l == truthTrue && r == truthTrue {
				return truthTrue
			}
		case opcode.LogicOr:
			l, r := constTruth(x.L), constTruth(x.R)
			if l == truthTrue || r == truthTrue {
				return truthTrue
			}
			if l == truthFalse && r == truthFalse && !isNull(x.L) && !isNull(x.R) {
				return truthFalse
			}
		default:
			return compareTruth(x)
		}
	}
	return truthUnknown
}

func isNull(e ast.ExprNode) bool {
	ve, ok := unparen(e).(ast.ValueExpr)
	if !ok {
		return false
	}
	if _, pm := ve.(ast.ParamMarkerExpr); pm {
		return false
	}
	return ve.GetValue() == nil
}

// numericPrefix matches the part of a string MySQL converts to a number.
var numericPrefix = regexp.MustCompile(`^[ \t\n\r]*[+-]?([0-9]+\.?[0-9]*|\.[0-9]+)([eE][+-]?[0-9]+)?`)

func valueTruth(v any) truth {
	switch x := v.(type) {
	case nil:
		return truthFalse
	case int64:
		return boolTruth(x != 0)
	case uint64:
		return boolTruth(x != 0)
	case float32:
		return boolTruth(x != 0)
	case float64:
		return boolTruth(x != 0)
	case string:
		return stringTruth(x)
	case []byte:
		return stringTruth(string(x))
	case fmt.Stringer: // decimal
		f, err := strconv.ParseFloat(x.String(), 64)
		if err != nil {
			return truthUnknown
		}
		return boolTruth(f != 0)
	}
	return truthUnknown
}

func stringTruth(s string) truth {
	m := numericPrefix.FindString(s)
	if m == "" {
		return truthFalse
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(m), 64)
	if err != nil {
		return truthUnknown
	}
	return boolTruth(f != 0)
}

// compareTruth folds a comparison of two literals. Integers compare by
// value. Strings are equal when they are byte-identical; other string
// comparisons depend on the collation and are unknown.
func compareTruth(b *ast.BinaryOperationExpr) truth {
	switch b.Op {
	case opcode.EQ, opcode.NE, opcode.LT, opcode.LE, opcode.GT, opcode.GE, opcode.NullEQ:
	default:
		return truthUnknown
	}
	lk, lok := constKey(b.L)
	rk, rok := constKey(b.R)
	if !lok || !rok || lk[:2] != rk[:2] {
		return truthUnknown
	}
	if lk[:2] == "s:" {
		if lk != rk {
			return truthUnknown
		}
		switch b.Op {
		case opcode.EQ, opcode.LE, opcode.GE, opcode.NullEQ:
			return truthTrue
		case opcode.NE, opcode.LT, opcode.GT:
			return truthFalse
		}
		return truthUnknown
	}
	a, err1 := strconv.ParseInt(lk[2:], 10, 64)
	c, err2 := strconv.ParseInt(rk[2:], 10, 64)
	if err1 != nil || err2 != nil {
		return truthUnknown
	}
	switch b.Op {
	case opcode.EQ, opcode.NullEQ:
		return boolTruth(a == c)
	case opcode.NE:
		return boolTruth(a != c)
	case opcode.LT:
		return boolTruth(a < c)
	case opcode.LE:
		return boolTruth(a <= c)
	case opcode.GT:
		return boolTruth(a > c)
	case opcode.GE:
		return boolTruth(a >= c)
	}
	return truthUnknown
}

// constKey returns a typed key for an integer ("i:42") or string ("s:x")
// literal.
func constKey(e ast.ExprNode) (string, bool) {
	ve, ok := unparen(e).(ast.ValueExpr)
	if !ok {
		return "", false
	}
	if _, pm := ve.(ast.ParamMarkerExpr); pm {
		return "", false
	}
	switch x := ve.GetValue().(type) {
	case int64:
		return "i:" + strconv.FormatInt(x, 10), true
	case string:
		return "s:" + x, true
	}
	return "", false
}

// whereText slices the WHERE clause out of the original text and verifies
// that the slice parses to the same expression. The text is reused
// verbatim, never rebuilt from the tree.
//
// The WHERE clause runs from the top-level WHERE keyword to the end of the
// statement, because ORDER BY, LIMIT and everything else that could follow
// it is rejected.
func whereText(src string, w ast.ExprNode) (string, error) {
	fail := connector.Reject(connector.RejectUnsupported, -1, "the WHERE clause could not be extracted")
	start, end, ok := whereBounds(src)
	if !ok || end <= start || end > len(src) {
		return "", fail
	}
	text := strings.TrimSpace(src[start:end])
	if text == "" {
		return "", fail
	}
	want, err := restore(w)
	if err != nil {
		return "", fail
	}
	ps, _ := parsers.Get().(*parser.Parser)
	if ps == nil {
		ps = parser.New()
	}
	defer parsers.Put(ps)
	stmts, _, err := ps.Parse("DELETE FROM t WHERE "+text, "", "")
	if err != nil || len(stmts) != 1 {
		return "", fail
	}
	d, ok := stmts[0].(*ast.DeleteStmt)
	if !ok || d.Where == nil || d.Order != nil || d.Limit != nil || d.IsMultiTable || len(d.Returning) > 0 {
		return "", fail
	}
	got, err := restore(d.Where)
	if err != nil || got != want {
		return "", fail
	}
	return text, nil
}

func restore(n ast.Node) (string, error) {
	var b strings.Builder
	if err := n.Restore(format.NewRestoreCtx(format.DefaultRestoreFlags, &b)); err != nil {
		return "", err
	}
	return b.String(), nil
}

// errScan means the text could not be tokenized.
var errScan = errors.New("scan error")

// token is one lexical token of MySQL text: a word, a quoted string or
// identifier, or a punctuation character. Comments and whitespace are
// skipped.
type token struct {
	start, end int
	kind       byte // 'w' word, 's' string, 'q' quoted identifier, 'p' punctuation
}

// scan tokenizes MySQL text with the lexical rules the session is pinned
// to (backslash escapes in strings, double quotes delimit strings).
func scan(src string) ([]token, error) {
	var out []token
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '#':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '-' && i+1 < len(src) && src[i+1] == '-' && (i+2 == len(src) || src[i+2] <= ' '):
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			j := strings.Index(src[i+2:], "*/")
			if j < 0 {
				return nil, errScan
			}
			i += 2 + j + 2
		case c == '\'' || c == '"' || c == '`':
			start := i
			end, ok := quotedEnd(src, i)
			if !ok {
				return nil, errScan
			}
			i = end
			kind := byte('s')
			if c == '`' {
				kind = 'q'
			}
			out = append(out, token{start, i, kind})
		case isWordByte(c):
			start := i
			for i < len(src) && isWordByte(src[i]) {
				i++
			}
			out = append(out, token{start, i, 'w'})
		default:
			out = append(out, token{i, i + 1, 'p'})
			i++
		}
	}
	return out, nil
}

// quotedEnd returns the offset just past the quoted string or identifier
// that starts at src[i]. A doubled quote character stands for itself; in
// strings (not identifiers) a backslash escapes the next byte.
func quotedEnd(src string, i int) (int, bool) {
	q := src[i]
	i++
	for i < len(src) {
		switch {
		case src[i] == '\\' && q != '`':
			i += 2
		case src[i] == q && i+1 < len(src) && src[i+1] == q:
			i += 2
		case src[i] == q:
			return i + 1, true
		default:
			i++
		}
	}
	return 0, false
}

func isWordByte(c byte) bool {
	return c == '_' || c == '$' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

// whereBounds returns the byte range from just after the first top-level
// WHERE keyword to the end of the last token that is not a semicolon.
func whereBounds(src string) (start, end int, ok bool) {
	toks, err := scan(src)
	if err != nil {
		return 0, 0, false
	}
	depth := 0
	start, end = -1, -1
	for i, t := range toks {
		text := src[t.start:t.end]
		if t.kind == 'p' {
			switch text {
			case "(":
				depth++
			case ")":
				depth--
			case ";":
				continue
			}
		}
		if t.kind == 'w' && depth == 0 && start < 0 && strings.EqualFold(text, "where") &&
			(i == 0 || src[toks[i-1].start:toks[i-1].end] != ".") {
			start = t.end
			continue
		}
		if start >= 0 {
			end = t.end
		}
	}
	return start, end, start >= 0 && end > start
}
