package postgres

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	pg "github.com/pganalyze/pg_query_go/v6"
	pgparser "github.com/pganalyze/pg_query_go/v6/parser"
	"google.golang.org/protobuf/proto"

	"github.com/alvaroibarguen/dbairlock/internal/connector"
)

// Validation follows PLAN.md 5.1. The walker accepts only node types it
// explicitly handles; every other node is a rejection (fail closed), so new
// syntax in a future parser version is rejected until someone decides it is
// safe.
//
// Known, documented limitations:
//   - Operators and unqualified functions resolve through the search path,
//     which the connector pins to pg_catalog (implicitly first) and the
//     default schema. A user who can CREATE in the default schema could add
//     an overload that wins resolution; the privilege check reports CREATE
//     on schemas as excess.
//   - Unqualified relation names starting with "pg_" are rejected, because
//     pg_catalog is searched first and they would resolve to catalog tables.

// validate checks one statement for a connection whose default schema is
// defaultSchema.
func validate(sql, defaultSchema string, p connector.Policy) (connector.Statement, error) {
	return validateWith(sql, defaultSchema, p, false)
}

// validateWith is validate; anyFunction skips the function allowlist, for
// re-deriving a statement's fields when the policy is not known.
func validateWith(sql, defaultSchema string, p connector.Policy, anyFunction bool) (connector.Statement, error) {
	if strings.TrimSpace(sql) == "" {
		return connector.Statement{}, connector.Reject(connector.RejectEmpty, -1, "the statement is empty")
	}
	if i := strings.IndexByte(sql, 0); i >= 0 {
		return connector.Statement{}, connector.Reject(connector.RejectParse, i, "the statement contains a NUL byte")
	}
	tree, err := pg.Parse(sql)
	if err != nil {
		return connector.Statement{}, parseRejection(err)
	}
	switch len(tree.GetStmts()) {
	case 0:
		return connector.Statement{}, connector.Reject(connector.RejectEmpty, -1, "the text contains no statement")
	case 1:
	default:
		return connector.Statement{}, connector.Reject(connector.RejectMultipleStatements,
			int(tree.GetStmts()[1].GetStmtLocation()), "only one statement is allowed per text; found %d", len(tree.GetStmts()))
	}
	v := &validator{
		src:           sql,
		policy:        p,
		anyFunction:   anyFunction,
		defaultSchema: defaultSchema,
		funcs:         map[string]struct{}{},
		blocked:       map[string]bool{},
	}
	for _, s := range p.BlockedSchemas {
		v.blocked[s] = true
	}
	if v.isSystemSchema(defaultSchema) {
		return connector.Statement{}, connector.Reject(connector.RejectSystemSchema, -1,
			"the connection's default schema %q is a system or blocked schema", defaultSchema)
	}
	raw := tree.GetStmts()[0]
	st, err := v.statement(raw.GetStmt())
	if err != nil {
		return connector.Statement{}, err
	}
	st.SQL = sql
	for f := range v.funcs {
		st.Functions = append(st.Functions, f)
	}
	slices.Sort(st.Functions)
	return st, nil
}

func parseRejection(err error) error {
	var pe *pgparser.Error
	if errors.As(err, &pe) {
		pos := -1
		if pe.Cursorpos > 0 {
			pos = pe.Cursorpos - 1
		}
		return connector.Reject(connector.RejectParse, pos, "the statement could not be parsed: %s", pe.Message)
	}
	return connector.Reject(connector.RejectParse, -1, "the statement could not be parsed: %v", err)
}

type validator struct {
	src           string
	policy        connector.Policy
	anyFunction   bool
	defaultSchema string
	funcs         map[string]struct{}
	blocked       map[string]bool

	// target table, for recognizing correlated references in subqueries
	table, alias string
	// column references found in the WHERE clause outside subqueries, or
	// qualified with the target table inside them
	whereRefs int
}

// exprCtx carries where in the tree an expression is.
type exprCtx struct {
	sublevel     int  // 0 = the statement itself; >0 = inside a subquery
	allowDefault bool // DEFAULT is allowed as this exact value (SET, VALUES)
	inWhere      bool // counting column references for the constant-WHERE rule
}

func (v *validator) statement(n *pg.Node) (connector.Statement, error) {
	switch {
	case n.GetInsertStmt() != nil:
		return v.insert(n.GetInsertStmt())
	case n.GetUpdateStmt() != nil:
		return v.update(n.GetUpdateStmt())
	case n.GetDeleteStmt() != nil:
		return v.delete(n.GetDeleteStmt())
	case n.GetSelectStmt() != nil:
		return connector.Statement{}, connector.Reject(connector.RejectStatementType, 0,
			"SELECT statements are not allowed; only INSERT, UPDATE and DELETE are")
	default:
		return connector.Statement{}, connector.Reject(connector.RejectStatementType, 0,
			"%s statements are not allowed; only INSERT, UPDATE and DELETE are", statementName(n))
	}
}

func (v *validator) insert(s *pg.InsertStmt) (connector.Statement, error) {
	if s.GetWithClause() != nil {
		return connector.Statement{}, connector.Reject(connector.RejectWith, int(s.GetWithClause().GetLocation()), "WITH clauses are not allowed")
	}
	if s.GetOnConflictClause() != nil {
		return connector.Statement{}, connector.Reject(connector.RejectUpsert, int(s.GetOnConflictClause().GetLocation()), "ON CONFLICT is not allowed")
	}
	if len(s.GetReturningList()) > 0 {
		return connector.Statement{}, connector.Reject(connector.RejectReturning, nodeLocation(s.GetReturningList()[0]), "RETURNING is not allowed")
	}
	if s.GetOverride() != pg.OverridingKind_OVERRIDING_NOT_SET {
		return connector.Statement{}, connector.Reject(connector.RejectUnsupported, -1, "OVERRIDING ... VALUE is not allowed")
	}
	st, err := v.target(s.GetRelation())
	if err != nil {
		return st, err
	}
	st.Kind = connector.KindInsert
	if len(s.GetCols()) == 0 {
		return st, connector.Reject(connector.RejectInsertForm, -1, "INSERT must list its columns: INSERT INTO t (a, b) VALUES (...)")
	}
	for _, c := range s.GetCols() {
		rt := c.GetResTarget()
		if rt == nil || rt.GetName() == "" || rt.GetVal() != nil {
			return st, connector.Reject(connector.RejectUnsupported, nodeLocation(c), "unsupported INSERT column list")
		}
		if len(rt.GetIndirection()) > 0 {
			return st, connector.Reject(connector.RejectUnsupported, int(rt.GetLocation()), "assigning to part of a column (%s[...] or %s.field) is not allowed", rt.GetName(), rt.GetName())
		}
		st.Columns = append(st.Columns, rt.GetName())
	}
	if s.GetSelectStmt() == nil {
		return st, connector.Reject(connector.RejectInsertForm, -1, "INSERT ... DEFAULT VALUES is not allowed; list the columns and VALUES")
	}
	sel := s.GetSelectStmt().GetSelectStmt()
	if sel == nil || len(sel.GetValuesLists()) == 0 {
		return st, connector.Reject(connector.RejectInsertForm, nodeLocation(s.GetSelectStmt()), "INSERT ... SELECT is not allowed; only INSERT ... VALUES")
	}
	if sel.GetWithClause() != nil {
		return st, connector.Reject(connector.RejectWith, int(sel.GetWithClause().GetLocation()), "WITH clauses are not allowed")
	}
	if !onlyValues(sel) {
		return st, connector.Reject(connector.RejectInsertForm, -1, "only a plain VALUES list is allowed in INSERT")
	}
	for _, row := range sel.GetValuesLists() {
		items := row.GetList().GetItems()
		if row.GetList() == nil {
			return st, connector.Reject(connector.RejectUnsupported, nodeLocation(row), "unsupported VALUES row")
		}
		if len(items) != len(st.Columns) {
			return st, connector.Reject(connector.RejectInsertForm, firstLocation(items),
				"a VALUES row has %d values but the column list has %d", len(items), len(st.Columns))
		}
		for _, it := range items {
			if err := v.expr(it, exprCtx{allowDefault: true}); err != nil {
				return st, err
			}
		}
	}
	st.InsertRows = len(sel.GetValuesLists())
	return st, nil
}

// onlyValues reports whether sel is a bare VALUES list.
func onlyValues(sel *pg.SelectStmt) bool {
	return len(sel.GetDistinctClause()) == 0 && sel.GetIntoClause() == nil && len(sel.GetTargetList()) == 0 &&
		len(sel.GetFromClause()) == 0 && sel.GetWhereClause() == nil && len(sel.GetGroupClause()) == 0 &&
		sel.GetHavingClause() == nil && len(sel.GetWindowClause()) == 0 && len(sel.GetSortClause()) == 0 &&
		sel.GetLimitOffset() == nil && sel.GetLimitCount() == nil && len(sel.GetLockingClause()) == 0 &&
		(sel.GetOp() == pg.SetOperation_SETOP_NONE || sel.GetOp() == pg.SetOperation_SET_OPERATION_UNDEFINED) &&
		sel.GetLarg() == nil && sel.GetRarg() == nil
}

func (v *validator) update(s *pg.UpdateStmt) (connector.Statement, error) {
	if s.GetWithClause() != nil {
		return connector.Statement{}, connector.Reject(connector.RejectWith, int(s.GetWithClause().GetLocation()), "WITH clauses are not allowed")
	}
	if len(s.GetReturningList()) > 0 {
		return connector.Statement{}, connector.Reject(connector.RejectReturning, nodeLocation(s.GetReturningList()[0]), "RETURNING is not allowed")
	}
	if len(s.GetFromClause()) > 0 {
		return connector.Statement{}, connector.Reject(connector.RejectMultipleTables, nodeLocation(s.GetFromClause()[0]), "UPDATE ... FROM is not allowed; a statement may change only one table")
	}
	st, err := v.target(s.GetRelation())
	if err != nil {
		return st, err
	}
	st.Kind = connector.KindUpdate
	for _, t := range s.GetTargetList() {
		rt := t.GetResTarget()
		if rt == nil || rt.GetName() == "" {
			return st, connector.Reject(connector.RejectUnsupported, nodeLocation(t), "unsupported SET clause")
		}
		if len(rt.GetIndirection()) > 0 {
			return st, connector.Reject(connector.RejectUnsupported, int(rt.GetLocation()), "assigning to part of a column (%s[...] or %s.field) is not allowed", rt.GetName(), rt.GetName())
		}
		if rt.GetVal().GetMultiAssignRef() != nil {
			return st, connector.Reject(connector.RejectUnsupported, int(rt.GetLocation()), "SET (a, b) = (...) is not supported; assign each column separately")
		}
		if err := v.expr(rt.GetVal(), exprCtx{allowDefault: true}); err != nil {
			return st, err
		}
		st.Columns = append(st.Columns, rt.GetName())
	}
	if err := v.where(s.GetWhereClause(), &st, "UPDATE"); err != nil {
		return st, err
	}
	return st, nil
}

func (v *validator) delete(s *pg.DeleteStmt) (connector.Statement, error) {
	if s.GetWithClause() != nil {
		return connector.Statement{}, connector.Reject(connector.RejectWith, int(s.GetWithClause().GetLocation()), "WITH clauses are not allowed")
	}
	if len(s.GetReturningList()) > 0 {
		return connector.Statement{}, connector.Reject(connector.RejectReturning, nodeLocation(s.GetReturningList()[0]), "RETURNING is not allowed")
	}
	if len(s.GetUsingClause()) > 0 {
		return connector.Statement{}, connector.Reject(connector.RejectMultipleTables, nodeLocation(s.GetUsingClause()[0]), "DELETE ... USING is not allowed; a statement may change only one table")
	}
	st, err := v.target(s.GetRelation())
	if err != nil {
		return st, err
	}
	st.Kind = connector.KindDelete
	if err := v.where(s.GetWhereClause(), &st, "DELETE"); err != nil {
		return st, err
	}
	return st, nil
}

// target checks the table a statement changes and resolves its schema.
func (v *validator) target(rv *pg.RangeVar) (connector.Statement, error) {
	if rv == nil {
		return connector.Statement{}, connector.Reject(connector.RejectUnsupported, -1, "the statement has no target table")
	}
	if err := v.relation(rv); err != nil {
		return connector.Statement{}, err
	}
	st := connector.Statement{Schema: rv.GetSchemaname(), Table: rv.GetRelname()}
	if st.Schema == "" {
		st.Schema = v.defaultSchema
	}
	if a := rv.GetAlias(); a != nil {
		if len(a.GetColnames()) > 0 {
			return st, connector.Reject(connector.RejectUnsupported, int(rv.GetLocation()), "column aliases on the target table are not allowed")
		}
		st.Alias = a.GetAliasname()
	}
	v.table, v.alias = st.Table, st.Alias
	return st, nil
}

// relation checks any table reference: the target or one in a subquery.
func (v *validator) relation(rv *pg.RangeVar) error {
	pos := int(rv.GetLocation())
	if rv.GetCatalogname() != "" {
		return connector.Reject(connector.RejectOtherDatabase, pos, "database-qualified table names (%s.%s.%s) are not allowed", rv.GetCatalogname(), rv.GetSchemaname(), rv.GetRelname())
	}
	if s := rv.GetSchemaname(); s != "" {
		if v.isSystemSchema(s) {
			return connector.Reject(connector.RejectSystemSchema, pos, "tables in system or blocked schema %q are not allowed", s)
		}
		return nil
	}
	if strings.HasPrefix(strings.ToLower(rv.GetRelname()), "pg_") {
		return connector.Reject(connector.RejectSystemSchema, pos,
			"unqualified table names starting with pg_ resolve to system catalogs and are not allowed; qualify the table with its schema")
	}
	return nil
}

// isSystemSchema reports whether s is a system schema or blocked by policy.
// Every schema name starting with pg_ is reserved by Postgres.
func (v *validator) isSystemSchema(s string) bool {
	l := strings.ToLower(s)
	return strings.HasPrefix(l, "pg_") || l == "information_schema" || v.blocked[s] || v.blocked[l]
}

// where validates the WHERE clause of an UPDATE or DELETE and extracts its
// source text.
func (v *validator) where(w *pg.Node, st *connector.Statement, verb string) error {
	if w == nil {
		return connector.Reject(connector.RejectMissingWhere, -1, "%s without a WHERE clause is not allowed", verb)
	}
	v.whereRefs = 0
	if err := v.expr(w, exprCtx{inWhere: true}); err != nil {
		return err
	}
	pos := nodeLocation(w)
	if v.whereRefs == 0 {
		return connector.Reject(connector.RejectConstantWhere, pos, "the WHERE clause does not reference any column of %s, so it matches every row or none", st.Table)
	}
	if constTruth(w) == truthTrue {
		return connector.Reject(connector.RejectConstantWhere, pos, "the WHERE clause is always true")
	}
	text, err := whereText(v.src, w)
	if err != nil {
		return err
	}
	st.Where = text
	return nil
}

// whereText slices the WHERE clause out of the original text, using the
// scanner to find the top-level WHERE keyword, and verifies that the slice
// parses to the same expression. The text is reused verbatim, never
// rebuilt.
func whereText(src string, w *pg.Node) (string, error) {
	fail := connector.Reject(connector.RejectUnsupported, -1, "the WHERE clause could not be extracted")
	scan, err := pg.Scan(src)
	if err != nil {
		return "", fail
	}
	depth, start, end := 0, -1, -1
	for _, tok := range scan.GetTokens() {
		switch tok.GetToken() {
		case pg.Token_SQL_COMMENT, pg.Token_C_COMMENT, pg.Token_ASCII_59:
			continue
		case pg.Token_ASCII_40, pg.Token_ASCII_91:
			depth++
		case pg.Token_ASCII_41, pg.Token_ASCII_93:
			depth--
		case pg.Token_WHERE:
			if depth == 0 && start < 0 {
				start = int(tok.GetEnd())
				continue
			}
		}
		if start >= 0 {
			end = int(tok.GetEnd())
		}
	}
	if start < 0 || end <= start || end > len(src) {
		return "", fail
	}
	text := strings.TrimSpace(src[start:end])
	if text == "" {
		return "", fail
	}
	// Verify: SELECT 1 WHERE <text> must deparse identically to the same
	// query with the original WHERE node substituted.
	probe, err := pg.Parse("SELECT 1 WHERE " + text)
	if err != nil || len(probe.GetStmts()) != 1 {
		return "", fail
	}
	sel := probe.GetStmts()[0].GetStmt().GetSelectStmt()
	if sel == nil || sel.GetWhereClause() == nil || len(sel.GetFromClause()) > 0 || len(sel.GetSortClause()) > 0 ||
		sel.GetLimitCount() != nil || sel.GetLimitOffset() != nil || len(sel.GetGroupClause()) > 0 ||
		sel.GetHavingClause() != nil || len(sel.GetLockingClause()) > 0 || len(sel.GetWindowClause()) > 0 {
		return "", fail
	}
	a, err := pg.Deparse(probe)
	if err != nil {
		return "", fail
	}
	orig, ok := proto.Clone(probe).(*pg.ParseResult)
	if !ok {
		return "", fail
	}
	orig.GetStmts()[0].GetStmt().GetSelectStmt().WhereClause = w
	b, err := pg.Deparse(orig)
	if err != nil || a != b {
		return "", fail
	}
	return text, nil
}

// expr validates an expression.
func (v *validator) expr(n *pg.Node, c exprCtx) error {
	if n == nil || n.GetNode() == nil {
		return nil
	}
	allowDefault := c.allowDefault
	c.allowDefault = false
	pos := nodeLocation(n)
	switch {
	case n.GetAConst() != nil:
		return nil
	case n.GetColumnRef() != nil:
		v.columnRef(n.GetColumnRef(), c)
		for _, f := range n.GetColumnRef().GetFields() {
			if f.GetString_() == nil && f.GetAStar() == nil {
				return connector.Reject(connector.RejectUnsupported, pos, "unsupported column reference")
			}
		}
		return nil
	case n.GetParamRef() != nil:
		return connector.Reject(connector.RejectUnsupported, pos, "parameters ($1) are not allowed; write the values into the statement")
	case n.GetSetToDefault() != nil:
		if !allowDefault {
			return connector.Reject(connector.RejectUnsupported, pos, "DEFAULT is allowed only as a whole value in SET or VALUES")
		}
		return nil
	case n.GetAExpr() != nil:
		return v.aExpr(n.GetAExpr(), c)
	case n.GetList() != nil:
		return v.exprs(n.GetList().GetItems(), c)
	case n.GetBoolExpr() != nil:
		return v.exprs(n.GetBoolExpr().GetArgs(), c)
	case n.GetNullTest() != nil:
		return v.expr(n.GetNullTest().GetArg(), c)
	case n.GetBooleanTest() != nil:
		return v.expr(n.GetBooleanTest().GetArg(), c)
	case n.GetTypeCast() != nil:
		tc := n.GetTypeCast()
		if err := v.useFunc("cast", pos); err != nil {
			return err
		}
		if err := v.typeName(tc.GetTypeName()); err != nil {
			return err
		}
		return v.expr(tc.GetArg(), c)
	case n.GetFuncCall() != nil:
		return v.funcCall(n.GetFuncCall(), c)
	case n.GetCoalesceExpr() != nil:
		if err := v.useFunc("coalesce", pos); err != nil {
			return err
		}
		return v.exprs(n.GetCoalesceExpr().GetArgs(), c)
	case n.GetMinMaxExpr() != nil:
		name := "greatest"
		if n.GetMinMaxExpr().GetOp() == pg.MinMaxOp_IS_LEAST {
			name = "least"
		}
		if err := v.useFunc(name, pos); err != nil {
			return err
		}
		return v.exprs(n.GetMinMaxExpr().GetArgs(), c)
	case n.GetSqlvalueFunction() != nil:
		return v.useFunc(sqlValueFunctionName(n.GetSqlvalueFunction().GetOp()), pos)
	case n.GetCaseExpr() != nil:
		ce := n.GetCaseExpr()
		if err := v.expr(ce.GetArg(), c); err != nil {
			return err
		}
		for _, w := range ce.GetArgs() {
			cw := w.GetCaseWhen()
			if cw == nil {
				return connector.Reject(connector.RejectUnsupported, nodeLocation(w), "unsupported CASE branch")
			}
			if err := v.expr(cw.GetExpr(), c); err != nil {
				return err
			}
			if err := v.expr(cw.GetResult(), c); err != nil {
				return err
			}
		}
		return v.expr(ce.GetDefresult(), c)
	case n.GetAArrayExpr() != nil:
		return v.exprs(n.GetAArrayExpr().GetElements(), c)
	case n.GetRowExpr() != nil:
		return v.exprs(n.GetRowExpr().GetArgs(), c)
	case n.GetCollateClause() != nil:
		return v.expr(n.GetCollateClause().GetArg(), c)
	case n.GetAIndirection() != nil:
		ind := n.GetAIndirection()
		if err := v.expr(ind.GetArg(), c); err != nil {
			return err
		}
		for _, i := range ind.GetIndirection() {
			switch {
			case i.GetAIndices() != nil:
				if err := v.expr(i.GetAIndices().GetLidx(), c); err != nil {
					return err
				}
				if err := v.expr(i.GetAIndices().GetUidx(), c); err != nil {
					return err
				}
			case i.GetString_() != nil, i.GetAStar() != nil:
			default:
				return connector.Reject(connector.RejectUnsupported, pos, "unsupported subscript or field selection")
			}
		}
		return nil
	case n.GetSubLink() != nil:
		return v.subLink(n.GetSubLink(), c)
	default:
		return connector.Reject(connector.RejectUnsupported, pos, "%s is not supported", nodeName(n))
	}
}

func (v *validator) exprs(ns []*pg.Node, c exprCtx) error {
	for _, n := range ns {
		if err := v.expr(n, c); err != nil {
			return err
		}
	}
	return nil
}

// columnRef counts references that tie the WHERE clause to the target
// table: any column at the statement level, and columns qualified with the
// target's name or alias inside subqueries (correlated references).
func (v *validator) columnRef(cr *pg.ColumnRef, c exprCtx) {
	if !c.inWhere {
		return
	}
	if c.sublevel == 0 {
		v.whereRefs++
		return
	}
	f := cr.GetFields()
	if len(f) >= 2 {
		q := f[len(f)-2].GetString_().GetSval()
		if q != "" && (q == v.alias || (v.alias == "" && q == v.table)) {
			v.whereRefs++
		}
	}
}

func (v *validator) aExpr(e *pg.A_Expr, c exprCtx) error {
	pos := int(e.GetLocation())
	if err := v.operatorName(e.GetName(), pos); err != nil {
		return err
	}
	switch e.GetKind() {
	case pg.A_Expr_Kind_AEXPR_OP, pg.A_Expr_Kind_AEXPR_OP_ANY, pg.A_Expr_Kind_AEXPR_OP_ALL,
		pg.A_Expr_Kind_AEXPR_DISTINCT, pg.A_Expr_Kind_AEXPR_NOT_DISTINCT,
		pg.A_Expr_Kind_AEXPR_IN, pg.A_Expr_Kind_AEXPR_LIKE, pg.A_Expr_Kind_AEXPR_ILIKE,
		pg.A_Expr_Kind_AEXPR_BETWEEN, pg.A_Expr_Kind_AEXPR_NOT_BETWEEN,
		pg.A_Expr_Kind_AEXPR_BETWEEN_SYM, pg.A_Expr_Kind_AEXPR_NOT_BETWEEN_SYM:
	case pg.A_Expr_Kind_AEXPR_NULLIF:
		if err := v.useFunc("nullif", pos); err != nil {
			return err
		}
	case pg.A_Expr_Kind_AEXPR_SIMILAR:
		// The parser wraps the pattern of SIMILAR TO in a call to
		// pg_catalog.similar_to_escape; that call is part of the operator.
		if fc := e.GetRexpr().GetFuncCall(); fc != nil && qualifiedName(fc.GetFuncname()) == "pg_catalog.similar_to_escape" {
			if err := v.expr(e.GetLexpr(), c); err != nil {
				return err
			}
			return v.exprs(fc.GetArgs(), c)
		}
	default:
		return connector.Reject(connector.RejectUnsupported, pos, "unsupported operator expression")
	}
	if err := v.expr(e.GetLexpr(), c); err != nil {
		return err
	}
	return v.expr(e.GetRexpr(), c)
}

// operatorName allows unqualified operators and operators qualified with
// pg_catalog (OPERATOR(pg_catalog.=)); operators in other schemas may run
// user-defined functions.
func (v *validator) operatorName(name []*pg.Node, pos int) error {
	for _, p := range name {
		if p.GetString_() == nil {
			return connector.Reject(connector.RejectUnsupported, pos, "unsupported operator name")
		}
	}
	if len(name) > 1 && (len(name) != 2 || name[0].GetString_().GetSval() != "pg_catalog") {
		return connector.Reject(connector.RejectFunction, pos, "operator %s is not allowed; only built-in operators are", qualifiedName(name))
	}
	return nil
}

func (v *validator) typeName(t *pg.TypeName) error {
	if t == nil {
		return nil
	}
	pos := int(t.GetLocation())
	if t.GetSetof() || t.GetPctType() {
		return connector.Reject(connector.RejectUnsupported, pos, "unsupported type name")
	}
	names := t.GetNames()
	for _, p := range names {
		if p.GetString_() == nil {
			return connector.Reject(connector.RejectUnsupported, pos, "unsupported type name")
		}
	}
	if len(names) > 1 && (len(names) != 2 || names[0].GetString_().GetSval() != "pg_catalog") {
		return connector.Reject(connector.RejectFunction, pos, "casts to type %s are not allowed; only built-in types are", qualifiedName(names))
	}
	for _, m := range t.GetTypmods() {
		if m.GetAConst() == nil {
			return connector.Reject(connector.RejectUnsupported, pos, "type modifiers must be constants")
		}
	}
	return nil
}

func (v *validator) funcCall(fc *pg.FuncCall, c exprCtx) error {
	pos := int(fc.GetLocation())
	parts := fc.GetFuncname()
	for _, p := range parts {
		if p.GetString_() == nil {
			return connector.Reject(connector.RejectUnsupported, pos, "unsupported function name")
		}
	}
	if len(parts) == 0 {
		return connector.Reject(connector.RejectUnsupported, pos, "unsupported function call")
	}
	if len(parts) > 2 || (len(parts) == 2 && parts[0].GetString_().GetSval() != "pg_catalog") {
		return connector.Reject(connector.RejectFunction, pos, "function %s is not allowed; only built-in functions on the allowlist are", qualifiedName(parts))
	}
	name := parts[len(parts)-1].GetString_().GetSval()
	if fc.GetFuncformat() == pg.CoercionForm_COERCE_SQL_SYNTAX {
		switch name {
		case "btrim", "ltrim", "rtrim":
			name = "trim" // TRIM([LEADING|TRAILING|BOTH] ... FROM ...)
		}
	}
	if fc.GetOver() != nil || fc.GetAggFilter() != nil || len(fc.GetAggOrder()) > 0 || fc.GetAggStar() ||
		fc.GetAggDistinct() || fc.GetAggWithinGroup() {
		return connector.Reject(connector.RejectFunction, pos, "aggregate and window function calls (%s) are not allowed", name)
	}
	if fc.GetFuncVariadic() {
		return connector.Reject(connector.RejectUnsupported, pos, "VARIADIC calls are not allowed")
	}
	if err := v.useFunc(name, pos); err != nil {
		return err
	}
	for _, a := range fc.GetArgs() {
		if a.GetNamedArgExpr() != nil {
			return connector.Reject(connector.RejectUnsupported, nodeLocation(a), "named function arguments are not allowed")
		}
	}
	return v.exprs(fc.GetArgs(), c)
}

func (v *validator) useFunc(name string, pos int) error {
	if !v.anyFunction && !v.policy.Allows(name) {
		return connector.Reject(connector.RejectFunction, pos, "function %s is not on this connection's allowlist", name)
	}
	v.funcs[name] = struct{}{}
	return nil
}

func (v *validator) subLink(sl *pg.SubLink, c exprCtx) error {
	pos := int(sl.GetLocation())
	switch sl.GetSubLinkType() {
	case pg.SubLinkType_EXISTS_SUBLINK, pg.SubLinkType_ALL_SUBLINK, pg.SubLinkType_ANY_SUBLINK,
		pg.SubLinkType_EXPR_SUBLINK, pg.SubLinkType_ARRAY_SUBLINK, pg.SubLinkType_ROWCOMPARE_SUBLINK:
	default:
		return connector.Reject(connector.RejectUnsupported, pos, "unsupported subquery")
	}
	if err := v.operatorName(sl.GetOperName(), pos); err != nil {
		return err
	}
	if err := v.expr(sl.GetTestexpr(), c); err != nil {
		return err
	}
	sel := sl.GetSubselect().GetSelectStmt()
	if sel == nil {
		return connector.Reject(connector.RejectUnsupported, pos, "unsupported subquery")
	}
	inner := c
	inner.sublevel++
	return v.subselect(sel, inner, pos)
}

// subselect allows a plain SELECT inside an expression, subject to the same
// function and schema rules.
func (v *validator) subselect(s *pg.SelectStmt, c exprCtx, pos int) error {
	if s.GetWithClause() != nil {
		return connector.Reject(connector.RejectWith, int(s.GetWithClause().GetLocation()), "WITH clauses are not allowed")
	}
	if s.GetIntoClause() != nil || len(s.GetLockingClause()) > 0 || len(s.GetWindowClause()) > 0 ||
		len(s.GetValuesLists()) > 0 || s.GetLarg() != nil || s.GetRarg() != nil ||
		(s.GetOp() != pg.SetOperation_SETOP_NONE && s.GetOp() != pg.SetOperation_SET_OPERATION_UNDEFINED) {
		return connector.Reject(connector.RejectUnsupported, pos, "only plain SELECT subqueries are allowed")
	}
	for _, t := range s.GetTargetList() {
		rt := t.GetResTarget()
		if rt == nil || len(rt.GetIndirection()) > 0 {
			return connector.Reject(connector.RejectUnsupported, nodeLocation(t), "unsupported subquery select list")
		}
		if err := v.expr(rt.GetVal(), c); err != nil {
			return err
		}
	}
	for _, f := range s.GetFromClause() {
		if err := v.fromItem(f, c); err != nil {
			return err
		}
	}
	for _, d := range s.GetDistinctClause() {
		if err := v.expr(d, c); err != nil {
			return err
		}
	}
	if err := v.expr(s.GetWhereClause(), c); err != nil {
		return err
	}
	if err := v.exprs(s.GetGroupClause(), c); err != nil {
		return err
	}
	if err := v.expr(s.GetHavingClause(), c); err != nil {
		return err
	}
	for _, sb := range s.GetSortClause() {
		by := sb.GetSortBy()
		if by == nil {
			return connector.Reject(connector.RejectUnsupported, nodeLocation(sb), "unsupported ORDER BY")
		}
		if err := v.operatorName(by.GetUseOp(), int(by.GetLocation())); err != nil {
			return err
		}
		if err := v.expr(by.GetNode(), c); err != nil {
			return err
		}
	}
	if err := v.expr(s.GetLimitCount(), c); err != nil {
		return err
	}
	return v.expr(s.GetLimitOffset(), c)
}

func (v *validator) fromItem(f *pg.Node, c exprCtx) error {
	switch {
	case f.GetRangeVar() != nil:
		return v.relation(f.GetRangeVar())
	case f.GetJoinExpr() != nil:
		j := f.GetJoinExpr()
		if err := v.fromItem(j.GetLarg(), c); err != nil {
			return err
		}
		if err := v.fromItem(j.GetRarg(), c); err != nil {
			return err
		}
		return v.expr(j.GetQuals(), c)
	default:
		return connector.Reject(connector.RejectUnsupported, nodeLocation(f), "%s in a subquery's FROM is not supported; use plain tables", nodeName(f))
	}
}

func sqlValueFunctionName(op pg.SQLValueFunctionOp) string {
	switch op {
	case pg.SQLValueFunctionOp_SVFOP_CURRENT_DATE:
		return "current_date"
	case pg.SQLValueFunctionOp_SVFOP_CURRENT_TIME, pg.SQLValueFunctionOp_SVFOP_CURRENT_TIME_N:
		return "current_time"
	case pg.SQLValueFunctionOp_SVFOP_CURRENT_TIMESTAMP, pg.SQLValueFunctionOp_SVFOP_CURRENT_TIMESTAMP_N:
		return "current_timestamp"
	case pg.SQLValueFunctionOp_SVFOP_LOCALTIME, pg.SQLValueFunctionOp_SVFOP_LOCALTIME_N:
		return "localtime"
	case pg.SQLValueFunctionOp_SVFOP_LOCALTIMESTAMP, pg.SQLValueFunctionOp_SVFOP_LOCALTIMESTAMP_N:
		return "localtimestamp"
	case pg.SQLValueFunctionOp_SVFOP_CURRENT_ROLE:
		return "current_role"
	case pg.SQLValueFunctionOp_SVFOP_CURRENT_USER:
		return "current_user"
	case pg.SQLValueFunctionOp_SVFOP_USER:
		return "user"
	case pg.SQLValueFunctionOp_SVFOP_SESSION_USER:
		return "session_user"
	case pg.SQLValueFunctionOp_SVFOP_CURRENT_CATALOG:
		return "current_catalog"
	case pg.SQLValueFunctionOp_SVFOP_CURRENT_SCHEMA:
		return "current_schema"
	default:
		return "sql_value_function_" + strings.ToLower(op.String())
	}
}

// qualifiedName joins name parts with dots.
func qualifiedName(parts []*pg.Node) string {
	s := make([]string, 0, len(parts))
	for _, p := range parts {
		s = append(s, p.GetString_().GetSval())
	}
	return strings.Join(s, ".")
}

// nodeLocation returns the byte offset stored in the node's location
// field, or -1 if it has none.
func nodeLocation(n *pg.Node) int {
	if n == nil {
		return -1
	}
	m := n.ProtoReflect()
	od := m.Descriptor().Oneofs().ByName("node")
	if od == nil {
		return -1
	}
	fd := m.WhichOneof(od)
	if fd == nil || fd.Message() == nil {
		return -1
	}
	inner := m.Get(fd).Message()
	lf := inner.Descriptor().Fields().ByName("location")
	if lf == nil {
		return -1
	}
	return int(inner.Get(lf).Int())
}

func firstLocation(ns []*pg.Node) int {
	for _, n := range ns {
		if l := nodeLocation(n); l >= 0 {
			return l
		}
	}
	return -1
}

// nodeName returns the parse node's type name, e.g. "TruncateStmt".
func nodeName(n *pg.Node) string {
	if n == nil {
		return "an empty node"
	}
	m := n.ProtoReflect()
	od := m.Descriptor().Oneofs().ByName("node")
	if od == nil {
		return "an unknown node"
	}
	fd := m.WhichOneof(od)
	if fd == nil || fd.Message() == nil {
		return "an unknown node"
	}
	return string(fd.Message().Name())
}

// statementName turns a statement node into a readable name, e.g.
// "TruncateStmt" into "TRUNCATE", "TransactionStmt" into "TRANSACTION".
func statementName(n *pg.Node) string {
	name := strings.TrimSuffix(nodeName(n), "Stmt")
	var b strings.Builder
	for i, r := range name {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return strings.ToUpper(b.String())
}

// truth is the result of constant-folding a boolean expression.
type truth int

const (
	truthUnknown truth = iota
	truthTrue
	truthFalse
)

// constTruth folds the parts of a WHERE clause that do not depend on the
// row: constants, comparisons of constants, a column compared with itself,
// and AND, OR and NOT over those. Anything else is unknown.
func constTruth(n *pg.Node) truth {
	switch {
	case n == nil:
		return truthUnknown
	case n.GetAConst() != nil:
		if bv, ok := n.GetAConst().GetVal().(*pg.A_Const_Boolval); ok {
			return boolTruth(bv.Boolval.GetBoolval())
		}
		return truthUnknown
	case n.GetTypeCast() != nil:
		tc := n.GetTypeCast()
		tn := tc.GetTypeName().GetNames()
		if len(tn) > 0 {
			last := tn[len(tn)-1].GetString_().GetSval()
			if last == "bool" || last == "boolean" {
				if t := constTruth(tc.GetArg()); t != truthUnknown {
					return t
				}
				if k, ok := constKey(tc.GetArg()); ok {
					switch strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(k, "s:"), "i:")) {
					case "t", "true", "y", "yes", "on", "1":
						return truthTrue
					case "f", "false", "n", "no", "off", "0":
						return truthFalse
					}
				}
			}
		}
		return truthUnknown
	case n.GetBoolExpr() != nil:
		b := n.GetBoolExpr()
		switch b.GetBoolop() {
		case pg.BoolExprType_AND_EXPR:
			all := true
			for _, a := range b.GetArgs() {
				switch constTruth(a) {
				case truthFalse:
					return truthFalse
				case truthUnknown:
					all = false
				}
			}
			if all {
				return truthTrue
			}
		case pg.BoolExprType_OR_EXPR:
			all := true
			for _, a := range b.GetArgs() {
				switch constTruth(a) {
				case truthTrue:
					return truthTrue
				case truthUnknown:
					all = false
				}
			}
			if all {
				return truthFalse
			}
		case pg.BoolExprType_NOT_EXPR:
			if len(b.GetArgs()) == 1 {
				switch constTruth(b.GetArgs()[0]) {
				case truthTrue:
					return truthFalse
				case truthFalse:
					return truthTrue
				}
			}
		}
		return truthUnknown
	case n.GetBooleanTest() != nil:
		bt := n.GetBooleanTest()
		t := constTruth(bt.GetArg())
		if t == truthUnknown {
			return truthUnknown
		}
		switch bt.GetBooltesttype() {
		case pg.BoolTestType_IS_TRUE, pg.BoolTestType_IS_NOT_FALSE:
			return t
		case pg.BoolTestType_IS_FALSE, pg.BoolTestType_IS_NOT_TRUE:
			return boolTruth(t == truthFalse)
		}
		return truthUnknown
	case n.GetNullTest() != nil:
		nt := n.GetNullTest()
		ac := nt.GetArg().GetAConst()
		if ac == nil {
			return truthUnknown
		}
		isNull := ac.GetIsnull()
		if nt.GetNulltesttype() == pg.NullTestType_IS_NULL {
			return boolTruth(isNull)
		}
		return boolTruth(!isNull)
	case n.GetAExpr() != nil:
		return compareTruth(n.GetAExpr())
	}
	return truthUnknown
}

func boolTruth(b bool) truth {
	if b {
		return truthTrue
	}
	return truthFalse
}

func compareTruth(e *pg.A_Expr) truth {
	if len(e.GetName()) != 1 {
		return truthUnknown
	}
	op := e.GetName()[0].GetString_().GetSval()
	switch e.GetKind() {
	case pg.A_Expr_Kind_AEXPR_OP:
	case pg.A_Expr_Kind_AEXPR_NOT_DISTINCT:
		op = "="
	case pg.A_Expr_Kind_AEXPR_DISTINCT:
		op = "<>"
	default:
		return truthUnknown
	}
	// A column compared with itself: true for every row (or every non-null
	// row), so it selects the whole table.
	if l, r := e.GetLexpr().GetColumnRef(), e.GetRexpr().GetColumnRef(); l != nil && r != nil && qualifiedName(l.GetFields()) == qualifiedName(r.GetFields()) {
		switch op {
		case "=", "<=", ">=":
			return truthTrue
		case "<>", "!=", "<", ">":
			return truthFalse
		}
		return truthUnknown
	}
	lk, lok := constKey(e.GetLexpr())
	rk, rok := constKey(e.GetRexpr())
	if !lok || !rok {
		return truthUnknown
	}
	if lk[:2] != rk[:2] {
		return truthUnknown // different literal types; leave it to the database
	}
	switch op {
	case "=":
		return boolTruth(lk == rk)
	case "<>", "!=":
		return boolTruth(lk != rk)
	}
	if lk[:2] == "i:" {
		var a, b int64
		_, _ = fmt.Sscan(lk[2:], &a)
		_, _ = fmt.Sscan(rk[2:], &b)
		switch op {
		case "<":
			return boolTruth(a < b)
		case ">":
			return boolTruth(a > b)
		case "<=":
			return boolTruth(a <= b)
		case ">=":
			return boolTruth(a >= b)
		}
	}
	return truthUnknown
}

// constKey returns a typed text key for a literal: "i:<int>", "s:<string>",
// "f:<float text>", "b:<bool>". NULL is not a key.
func constKey(n *pg.Node) (string, bool) {
	ac := n.GetAConst()
	if ac == nil || ac.GetIsnull() {
		return "", false
	}
	switch v := ac.GetVal().(type) {
	case *pg.A_Const_Ival:
		return fmt.Sprintf("i:%d", v.Ival.GetIval()), true
	case *pg.A_Const_Sval:
		return "s:" + v.Sval.GetSval(), true
	case *pg.A_Const_Fval:
		return "f:" + v.Fval.GetFval(), true
	case *pg.A_Const_Boolval:
		return fmt.Sprintf("b:%t", v.Boolval.GetBoolval()), true
	}
	return "", false
}
