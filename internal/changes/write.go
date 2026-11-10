package changes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/changes/changesdb"
	"github.com/alvaroibarguen/dbairlock/internal/connector"
	"github.com/alvaroibarguen/dbairlock/internal/identity"
	"github.com/alvaroibarguen/dbairlock/internal/store"
	"github.com/alvaroibarguen/dbairlock/internal/targets"
)

// PreviewStatement is one statement's result in Preview. Nothing is saved.
type PreviewStatement struct {
	Position      int
	SQL           string
	Valid         bool
	Kind          connector.Kind
	Schema        string
	Table         string
	HasPrimaryKey bool
	Issue         *Issue
	Preview       *Preview
}

// PreviewResult is the outcome of Preview.
type PreviewResult struct {
	// OK is true when every statement is valid and the dry run succeeded:
	// the statements can be submitted as they are.
	OK         bool
	Problem    *Issue
	Statements []PreviewStatement
	SQLHash    string
	// Duplicate is the viewer's own open change with the same SQL on the
	// same connection, if there is one: submitting returns it.
	Duplicate *DuplicateError
}

// Draft is a new change.
type Draft struct {
	ConnectionID string
	Title        string
	Reason       string
	Statements   []string
}

// EditInput changes a change. Nil fields are unchanged; Statements, when
// not nil, replaces every statement. Revision is the revision the edit is
// based on.
type EditInput struct {
	Revision   int
	Title      *string
	Reason     *string
	Statements []string
}

// RefreshOutcome says what a preview refresh found.
type RefreshOutcome struct {
	// OK is false when the dry run failed; Problem then says why.
	OK      bool
	Problem string
	// DataChanged is true when the affected rows or the values being
	// overwritten differ from the previous preview. Earlier approvals then
	// no longer count.
	DataChanged bool
}

// Preview validates statements and dry-runs them on the connection without
// saving anything. It is what the editor's Preview button and the MCP
// preview_change tool call. Every statement's outcome is reported.
func (s *Service) Preview(ctx context.Context, p identity.Principal, connectionID string, statements []string) (PreviewResult, error) {
	if err := identity.Can(p, identity.ActionChangeSubmit, connectionResource(connectionID)); err != nil {
		return PreviewResult{}, err
	}
	var fe fieldErrs
	sqls := checkStatements(&fe, statements)
	c, err := s.connectionFor(ctx, &fe, connectionID)
	if err != nil {
		return PreviewResult{}, err
	}
	if err := fe.err(); err != nil {
		return PreviewResult{}, err
	}
	out, err := s.dryRun(ctx, c, sqls)
	if err != nil {
		return PreviewResult{}, err
	}
	res := PreviewResult{OK: out.ok(), Problem: out.Problem, SQLHash: SQLHash(sqls)}
	for _, st := range out.Statements {
		ps := PreviewStatement{Position: st.Position, SQL: st.SQL, Valid: st.Statement != nil, Issue: st.Issue}
		if st.Statement != nil {
			ps.Kind, ps.Schema, ps.Table = st.Statement.Kind, st.Statement.Schema, st.Statement.Table
		}
		if st.Result != nil {
			// Nothing is stored, so the rows are shown whatever the
			// connection's storage setting.
			pv := previewOf(*st.Result, true)
			ps.Preview = &pv
			ps.HasPrimaryKey = st.Result.HasPrimaryKey
		}
		res.Statements = append(res.Statements, ps)
	}
	res.Statements = visibleResults(p, res.Statements)
	dup, err := s.findDuplicate(ctx, s.opts.Store.Reader(), p.UserID, c.ID, res.SQLHash, "")
	if err != nil {
		return PreviewResult{}, err
	}
	if dup != nil {
		res.Duplicate = &DuplicateError{ID: dup.ID, Number: dup.Number}
	}
	return res, nil
}

// connectionFor loads the connection a new change targets, adding a field
// error if it does not exist or is archived.
func (s *Service) connectionFor(ctx context.Context, fe *fieldErrs, id string) (targets.Connection, error) {
	c, err := s.opts.Targets.Get(ctx, id)
	switch {
	case errors.Is(err, targets.ErrNotFound):
		fe.add("connection_id", "Choose a connection.")
		return c, nil
	case err != nil:
		return c, err
	case c.ArchivedAt != nil:
		fe.add("connection_id", "This connection is archived.")
	}
	return c, nil
}

// findDuplicate returns the author's open (in review or approved) change
// with the same SQL on the connection, other than exceptID, or nil.
func (s *Service) findDuplicate(ctx context.Context, db store.DBTX, authorID, connectionID, sqlHash, exceptID string) (*changesdb.ChangeRequest, error) {
	row, err := changesdb.New(db).FindOpenDuplicate(ctx, changesdb.FindOpenDuplicateParams{
		AuthorID: authorID, ConnectionID: connectionID, SqlHash: sqlHash, ID: exceptID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("changes: find duplicate: %w", err)
	}
	return &row, nil
}

// requireActive fails if user id has been removed. It runs inside the
// writing transaction so a removal committed a moment earlier wins.
func (s *Service) requireActive(ctx context.Context, tx *store.Tx, id string) error {
	users, err := s.opts.Directory.Users(ctx, tx, []string{id})
	if err != nil {
		return err
	}
	if u, ok := users[id]; !ok || u.Removed() {
		return identity.ErrUnauthenticated
	}
	return nil
}

// statementParams builds the stored rows of a dry run's statements.
// images says whether row images are stored.
func statementParams(changeID string, revision int64, out runOutcome, images bool) ([]changesdb.InsertStatementParams, error) {
	rows := make([]changesdb.InsertStatementParams, len(out.Statements))
	for i, st := range out.Statements {
		r := changesdb.InsertStatementParams{ChangeID: changeID, Revision: revision, Position: int64(i), SqlText: st.SQL}
		if st.Statement != nil {
			r.Kind, r.SchemaName, r.TableName = string(st.Statement.Kind), st.Statement.Schema, st.Statement.Table
		}
		var err error
		if st.Issue != nil {
			if r.Error, err = encodeOpt(st.Issue); err != nil {
				return nil, err
			}
		}
		if st.Result != nil {
			r.ExpectedRows = someInt(st.Result.RowsAffected)
			r.ExpectedFingerprint = st.Result.Fingerprint
			r.HasPrimaryKey = boolInt(st.Result.HasPrimaryKey)
			if r.Preview, err = encodeOpt(previewOf(*st.Result, images)); err != nil {
				return nil, err
			}
		}
		rows[i] = r
	}
	return rows, nil
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// statementAudit is how statements appear in audit payloads: the SQL and
// what the dry run found, never row images.
func statementAudit(out runOutcome) []map[string]any {
	list := make([]map[string]any, len(out.Statements))
	for i, st := range out.Statements {
		m := map[string]any{"position": i, "sql": st.SQL}
		if st.Statement != nil {
			m["kind"] = st.Statement.Kind
			m["table"] = st.Statement.Schema + "." + st.Statement.Table
		}
		if st.Result != nil {
			m["expected_rows"] = st.Result.RowsAffected
			if st.Result.Fingerprint != "" {
				m["expected_fingerprint"] = st.Result.Fingerprint
			}
		}
		if st.Issue != nil {
			m["issue"] = st.Issue.Code
		}
		list[i] = m
	}
	return list
}

func viaOf(p identity.Principal) string {
	if isAgent(p) {
		return string(audit.ViaMCP)
	}
	return string(audit.ViaWeb)
}

// Create saves a new change. Without submit it is a draft, saved even when
// statements are invalid or the dry run fails (the problems are stored and
// shown). With submit it is created and submitted in one step: it then
// needs clean validation and a successful dry run, and nothing is created
// otherwise; and if the author already has an open change with the same
// SQL on the connection, that change is returned with AlreadyExisted set
// and nothing is created.
func (s *Service) Create(ctx context.Context, p identity.Principal, in Draft, submit bool) (Change, error) {
	if err := identity.Can(p, identity.ActionChangeSubmit, connectionResource(in.ConnectionID)); err != nil {
		return Change{}, err
	}
	var fe fieldErrs
	title := checkTitle(&fe, in.Title)
	reason := checkText(&fe, "reason", in.Reason, MaxReason)
	sqls := checkStatements(&fe, in.Statements)
	c, err := s.connectionFor(ctx, &fe, in.ConnectionID)
	if err != nil {
		return Change{}, err
	}
	if err := fe.err(); err != nil {
		return Change{}, err
	}
	hash := SQLHash(sqls)
	if submit {
		dup, err := s.findDuplicate(ctx, s.opts.Store.Reader(), p.UserID, c.ID, hash, "")
		if err != nil {
			return Change{}, err
		}
		if dup != nil {
			return s.existing(ctx, p, dup.ID)
		}
	}

	out, err := s.dryRun(ctx, c, sqls)
	if err != nil {
		return Change{}, err
	}
	if submit && !out.ok() {
		return Change{}, out.err()
	}
	c = out.Conn

	id := s.opts.IDs.NewID()
	var dupID string
	err = s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		q := changesdb.New(tx)
		if err := s.requireActive(ctx, tx, p.UserID); err != nil {
			return err
		}
		if submit {
			dup, err := s.findDuplicate(ctx, tx, p.UserID, c.ID, hash, "")
			if err != nil {
				return err
			}
			if dup != nil {
				dupID = dup.ID
				return nil
			}
		}
		number, err := q.NextNumber(ctx)
		if err != nil {
			return fmt.Errorf("changes: next number: %w", err)
		}
		agent := ""
		if isAgent(p) {
			if agent, err = s.opts.Directory.TokenName(ctx, tx, p.APITokenID); err != nil {
				return err
			}
		}
		now := s.now()
		params := changesdb.InsertChangeParams{
			ID: id, Number: number, ConnectionID: c.ID, AuthorID: p.UserID, Via: viaOf(p),
			ApiTokenID: p.APITokenID, AgentName: agent, Title: title, Reason: reason,
			Status: string(StatusDraft), SqlHash: hash, Revision: 1,
			CreatedAt: msOf(now), UpdatedAt: msOf(now),
		}
		if out.ok() {
			params.PreviewHash = previewHash(out.expectations())
			params.PreviewedAt = someInt(msOf(now))
		} else {
			params.PreviewError = out.summary()
		}
		var expires time.Time
		if submit {
			to, err := transition(StatusDraft, EventSubmit)
			if err != nil {
				return err
			}
			expires = now.Add(time.Duration(c.Policy.ApprovalTTLHours) * time.Hour)
			params.Status = string(to)
			params.SubmittedAt = someInt(msOf(now))
			params.ExpiresAt = someInt(msOf(expires))
		}
		if err := q.InsertChange(ctx, params); err != nil {
			return fmt.Errorf("changes: insert change: %w", err)
		}
		rows, err := statementParams(id, 1, out, c.Policy.StoreRowImages)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if err := q.InsertStatement(ctx, r); err != nil {
				return fmt.Errorf("changes: insert statement: %w", err)
			}
		}
		payload := map[string]any{
			"number": number, "connection_id": c.ID, "connection": c.Name, "title": title, "reason": reason,
			"statements": statementAudit(out), "sql_hash": hash, "preview_hash": params.PreviewHash,
			"via": params.Via,
		}
		if agent != "" {
			payload["agent"] = agent
		}
		if params.PreviewError != "" {
			payload["preview_error"] = params.PreviewError
		}
		if err := s.record(ctx, tx, audit.ChangeCreated, id, payload); err != nil {
			return err
		}
		if submit {
			return s.record(ctx, tx, audit.ChangeSubmitted, id, map[string]any{
				"number": number, "sql_hash": hash, "preview_hash": params.PreviewHash, "expires_at": expires,
			})
		}
		return nil
	})
	if err != nil {
		return Change{}, err
	}
	if dupID != "" {
		return s.existing(ctx, p, dupID)
	}
	return s.Get(ctx, p, id)
}

// existing returns change id marked as already existing.
func (s *Service) existing(ctx context.Context, p identity.Principal, id string) (Change, error) {
	c, err := s.Get(ctx, p, id)
	if err != nil {
		return Change{}, err
	}
	c.AlreadyExisted = true
	return c, nil
}

// record writes the audit event for a change and, for the events that
// notify, queues the notification in the same transaction.
func (s *Service) record(ctx context.Context, tx *store.Tx, action audit.Action, changeID string, payload map[string]any) error {
	if err := s.opts.Audit.Record(ctx, tx, audit.Event{Action: action, SubjectType: "change", SubjectID: changeID, Payload: payload}); err != nil {
		return err
	}
	return s.notify(ctx, tx, action, changeID)
}

// load reads change ref (its id, or its number) through db.
func load(ctx context.Context, db store.DBTX, ref string) (changesdb.ChangeRequest, error) {
	q := changesdb.New(db)
	var (
		row changesdb.ChangeRequest
		err error
	)
	if n, perr := strconv.ParseInt(ref, 10, 64); perr == nil && n > 0 {
		row, err = q.GetChangeByNumber(ctx, n)
	} else {
		row, err = q.GetChange(ctx, ref)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return row, ErrNotFound
	}
	if err != nil {
		return row, fmt.Errorf("changes: read change: %w", err)
	}
	return row, nil
}

// Edit changes the title, reason or statements of a draft or a change in
// review. Only the author may edit. It increments the revision; new
// statements get a new sql_hash and a new dry run, so earlier reviews stay
// on record but no longer count. A change in review must stay submittable:
// its new statements need clean validation and a successful dry run.
func (s *Service) Edit(ctx context.Context, p identity.Principal, ref string, in EditInput) (Change, error) {
	row, err := load(ctx, s.opts.Store.Reader(), ref)
	if err != nil {
		return Change{}, err
	}
	if err := identity.Can(p, identity.ActionChangeSubmit, connectionResource(row.ConnectionID)); err != nil {
		return Change{}, err
	}
	if row.AuthorID != p.UserID {
		return Change{}, ErrNotAuthor
	}
	if st := Status(row.Status); st != StatusDraft && st != StatusPendingReview {
		return Change{}, stateErr("Only a draft or a change in review can be edited; this one is %s.", label(st))
	}
	if int64(in.Revision) != row.Revision {
		return Change{}, ErrConflict
	}
	var fe fieldErrs
	title, reason := row.Title, row.Reason
	if in.Title != nil {
		title = checkTitle(&fe, *in.Title)
	}
	if in.Reason != nil {
		reason = checkText(&fe, "reason", *in.Reason, MaxReason)
	}
	var sqls []string
	if in.Statements != nil {
		sqls = checkStatements(&fe, in.Statements)
	}
	if err := fe.err(); err != nil {
		return Change{}, err
	}
	c, err := s.opts.Targets.Get(ctx, row.ConnectionID)
	if err != nil {
		return Change{}, err
	}

	var out runOutcome
	hash := row.SqlHash
	if sqls != nil {
		hash = SQLHash(sqls)
		if Status(row.Status) == StatusPendingReview {
			dup, err := s.findDuplicate(ctx, s.opts.Store.Reader(), p.UserID, row.ConnectionID, hash, row.ID)
			if err != nil {
				return Change{}, err
			}
			if dup != nil {
				return Change{}, &DuplicateError{ID: dup.ID, Number: dup.Number}
			}
		}
		if out, err = s.dryRun(ctx, c, sqls); err != nil {
			return Change{}, err
		}
		if Status(row.Status) == StatusPendingReview && !out.ok() {
			return Change{}, out.err()
		}
		c = out.Conn
	}

	err = s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		q := changesdb.New(tx)
		cur, err := q.GetChange(ctx, row.ID)
		if err != nil {
			return fmt.Errorf("changes: read change: %w", err)
		}
		if cur.Revision != row.Revision || cur.Status != row.Status {
			return ErrConflict
		}
		if err := s.requireActive(ctx, tx, p.UserID); err != nil {
			return err
		}
		now := msOf(s.now())
		next := cur.Revision + 1
		upd := changesdb.UpdateContentParams{
			Title: title, Reason: reason, SqlHash: cur.SqlHash, PreviewHash: cur.PreviewHash,
			PreviewedAt: cur.PreviewedAt, PreviewError: cur.PreviewError, Revision: next, UpdatedAt: now, ID: cur.ID,
		}
		changed := []string{}
		if title != cur.Title {
			changed = append(changed, "title")
		}
		if reason != cur.Reason {
			changed = append(changed, "reason")
		}
		payload := map[string]any{"number": cur.Number, "revision": next}
		if sqls != nil {
			if Status(cur.Status) == StatusPendingReview {
				dup, err := s.findDuplicate(ctx, tx, p.UserID, cur.ConnectionID, hash, cur.ID)
				if err != nil {
					return err
				}
				if dup != nil {
					return &DuplicateError{ID: dup.ID, Number: dup.Number}
				}
			}
			changed = append(changed, "statements")
			rows, err := statementParams(cur.ID, next, out, c.Policy.StoreRowImages)
			if err != nil {
				return err
			}
			for _, r := range rows {
				if err := q.InsertStatement(ctx, r); err != nil {
					return fmt.Errorf("changes: insert statement: %w", err)
				}
			}
			upd.SqlHash = hash
			upd.PreviewHash, upd.PreviewedAt, upd.PreviewError = "", sql.NullInt64{}, out.summary()
			if out.ok() {
				upd.PreviewHash, upd.PreviewedAt, upd.PreviewError = previewHash(out.expectations()), someInt(now), ""
			}
			payload["statements"] = statementAudit(out)
			payload["old_sql_hash"] = cur.SqlHash
		} else if err := copyStatements(ctx, q, cur.ID, cur.Revision, next); err != nil {
			return err
		}
		if err := q.UpdateContent(ctx, upd); err != nil {
			return fmt.Errorf("changes: update change: %w", err)
		}
		payload["changed"] = changed
		payload["sql_hash"] = upd.SqlHash
		payload["preview_hash"] = upd.PreviewHash
		if slicesContains(changed, "title") {
			payload["title"] = title
		}
		if slicesContains(changed, "reason") {
			payload["reason"] = reason
		}
		return s.record(ctx, tx, audit.ChangeEdited, cur.ID, payload)
	})
	if err != nil {
		return Change{}, err
	}
	return s.Get(ctx, p, row.ID)
}

func slicesContains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// copyStatements carries the statements of revision from to revision to,
// unchanged, for an edit of the title or reason only.
func copyStatements(ctx context.Context, q *changesdb.Queries, id string, from, to int64) error {
	rows, err := q.ListStatements(ctx, changesdb.ListStatementsParams{ChangeID: id, Revision: from})
	if err != nil {
		return fmt.Errorf("changes: read statements: %w", err)
	}
	for _, r := range rows {
		if err := q.InsertStatement(ctx, changesdb.InsertStatementParams{
			ChangeID: id, Revision: to, Position: r.Position, SqlText: r.SqlText, Kind: r.Kind,
			SchemaName: r.SchemaName, TableName: r.TableName, ExpectedRows: r.ExpectedRows,
			ExpectedFingerprint: r.ExpectedFingerprint, HasPrimaryKey: r.HasPrimaryKey,
			Error: r.Error, Preview: r.Preview,
		}); err != nil {
			return fmt.Errorf("changes: copy statement: %w", err)
		}
	}
	return nil
}

func (s *Service) statementsOf(ctx context.Context, db store.DBTX, row changesdb.ChangeRequest) ([]changesdb.ChangeStatement, error) {
	rows, err := changesdb.New(db).ListStatements(ctx, changesdb.ListStatementsParams{ChangeID: row.ID, Revision: row.Revision})
	if err != nil {
		return nil, fmt.Errorf("changes: read statements: %w", err)
	}
	return rows, nil
}

func sqlTexts(rows []changesdb.ChangeStatement) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.SqlText
	}
	return out
}

// saveRun stores the outcome of a dry run of a change's current statements:
// the new previews and hashes when it succeeded, or the problems (keeping
// the last good preview) when it failed. It reports the new preview hash
// and whether the data differs from the previous preview, in which case
// the previous preview is kept beside the new one for comparison.
func saveRun(ctx context.Context, q *changesdb.Queries, row changesdb.ChangeRequest, out runOutcome, now int64, refreshedAt sql.NullInt64) (string, bool, error) {
	stored, err := q.ListStatements(ctx, changesdb.ListStatementsParams{ChangeID: row.ID, Revision: row.Revision})
	if err != nil {
		return "", false, fmt.Errorf("changes: read statements: %w", err)
	}
	if len(stored) != len(out.Statements) {
		return "", false, fmt.Errorf("changes: %d stored statements, %d dry-run results", len(stored), len(out.Statements))
	}
	ok := out.ok()
	hash, changed := row.PreviewHash, false
	if ok {
		hash = previewHash(out.expectations())
		changed = hash != row.PreviewHash
	}
	images := out.Conn.Policy.StoreRowImages
	for i, st := range out.Statements {
		old := stored[i]
		p := changesdb.UpdateStatementPreviewParams{
			Kind: old.Kind, SchemaName: old.SchemaName, TableName: old.TableName,
			ExpectedRows: old.ExpectedRows, ExpectedFingerprint: old.ExpectedFingerprint,
			HasPrimaryKey: old.HasPrimaryKey, Preview: old.Preview, PreviousPreview: old.PreviousPreview,
			ChangeID: row.ID, Revision: row.Revision, Position: old.Position,
		}
		if st.Statement != nil {
			p.Kind, p.SchemaName, p.TableName = string(st.Statement.Kind), st.Statement.Schema, st.Statement.Table
		}
		if p.Error, err = encodeOpt(st.Issue); err != nil {
			return "", false, err
		}
		if st.Issue == nil {
			p.Error = sql.NullString{}
		}
		if ok {
			r := *st.Result
			p.ExpectedRows, p.ExpectedFingerprint, p.HasPrimaryKey = someInt(r.RowsAffected), r.Fingerprint, boolInt(r.HasPrimaryKey)
			if p.Preview, err = encodeOpt(previewOf(r, images)); err != nil {
				return "", false, err
			}
			p.PreviousPreview = sql.NullString{}
			if changed && row.PreviewHash != "" {
				p.PreviousPreview = old.Preview
			}
		}
		if err := q.UpdateStatementPreview(ctx, p); err != nil {
			return "", false, fmt.Errorf("changes: update statement: %w", err)
		}
	}
	upd := changesdb.UpdatePreviewParams{
		PreviewHash: hash, PreviewedAt: row.PreviewedAt, PreviewError: out.summary(),
		PreviewChanged: row.PreviewChanged, LastRefreshAt: refreshedAt, UpdatedAt: now, ID: row.ID,
	}
	if ok {
		upd.PreviewedAt, upd.PreviewError = someInt(now), ""
		upd.PreviewChanged = boolInt(changed && row.PreviewHash != "")
	}
	if err := q.UpdatePreview(ctx, upd); err != nil {
		return "", false, fmt.Errorf("changes: update preview: %w", err)
	}
	return hash, changed, nil
}

// Submit sends a draft for review. If the author already has an open
// change with the same SQL on the connection, that change is returned with
// AlreadyExisted set and the draft stays a draft. Otherwise the draft is
// dry-run again and needs clean validation and a successful dry run; the
// approval lifetime starts now.
func (s *Service) Submit(ctx context.Context, p identity.Principal, ref string, revision int) (Change, error) {
	row, err := load(ctx, s.opts.Store.Reader(), ref)
	if err != nil {
		return Change{}, err
	}
	if err := identity.Can(p, identity.ActionChangeSubmit, connectionResource(row.ConnectionID)); err != nil {
		return Change{}, err
	}
	if row.AuthorID != p.UserID {
		return Change{}, ErrNotAuthor
	}
	if _, err := transition(Status(row.Status), EventSubmit); err != nil {
		return Change{}, stateErr("Only a draft can be submitted; this change is %s.", label(Status(row.Status)))
	}
	if int64(revision) != row.Revision {
		return Change{}, ErrConflict
	}
	dup, err := s.findDuplicate(ctx, s.opts.Store.Reader(), row.AuthorID, row.ConnectionID, row.SqlHash, row.ID)
	if err != nil {
		return Change{}, err
	}
	if dup != nil {
		return s.existing(ctx, p, dup.ID)
	}
	c, err := s.opts.Targets.Get(ctx, row.ConnectionID)
	if err != nil {
		return Change{}, err
	}
	stmts, err := s.statementsOf(ctx, s.opts.Store.Reader(), row)
	if err != nil {
		return Change{}, err
	}
	out, err := s.dryRun(ctx, c, sqlTexts(stmts))
	if err != nil {
		return Change{}, err
	}
	var dupID string
	err = s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		q := changesdb.New(tx)
		cur, err := q.GetChange(ctx, row.ID)
		if err != nil {
			return fmt.Errorf("changes: read change: %w", err)
		}
		if cur.Revision != row.Revision || cur.Status != row.Status {
			return ErrConflict
		}
		if err := s.requireActive(ctx, tx, p.UserID); err != nil {
			return err
		}
		now := s.now()
		if _, _, err := saveRun(ctx, q, cur, out, msOf(now), cur.LastRefreshAt); err != nil {
			return err
		}
		if !out.ok() {
			return nil // the draft keeps the problems; reported below
		}
		dup, err := s.findDuplicate(ctx, tx, cur.AuthorID, cur.ConnectionID, cur.SqlHash, cur.ID)
		if err != nil {
			return err
		}
		if dup != nil {
			dupID = dup.ID
			return nil
		}
		if _, err := transition(Status(cur.Status), EventSubmit); err != nil {
			return err
		}
		expires := now.Add(time.Duration(out.Conn.Policy.ApprovalTTLHours) * time.Hour)
		if err := q.MarkSubmitted(ctx, changesdb.MarkSubmittedParams{
			SubmittedAt: someInt(msOf(now)), ExpiresAt: someInt(msOf(expires)), UpdatedAt: msOf(now), ID: cur.ID,
		}); err != nil {
			return fmt.Errorf("changes: submit: %w", err)
		}
		return s.record(ctx, tx, audit.ChangeSubmitted, cur.ID, map[string]any{
			"number": cur.Number, "sql_hash": cur.SqlHash, "preview_hash": previewHash(out.expectations()),
			"statements": statementAudit(out), "expires_at": expires,
		})
	})
	if err != nil {
		return Change{}, err
	}
	if dupID != "" {
		return s.existing(ctx, p, dupID)
	}
	if !out.ok() {
		return Change{}, out.err()
	}
	return s.Get(ctx, p, row.ID)
}

// RefreshPreview re-runs the dry run of a change in review for its current
// SQL and stores the new preview (PLAN.md 5.5). It does not change the
// revision or sql_hash. When the data differs, earlier approvals stop
// counting (their preview_hash no longer matches). When the dry run fails,
// the reason is stored, the status stays, and approval is blocked until a
// later refresh succeeds or the author edits the change. At most one
// refresh per change every RefreshInterval.
func (s *Service) RefreshPreview(ctx context.Context, p identity.Principal, ref string) (Change, RefreshOutcome, error) {
	row, err := load(ctx, s.opts.Store.Reader(), ref)
	if err != nil {
		return Change{}, RefreshOutcome{}, err
	}
	if err := identity.Can(p, identity.ActionChangeReview, connectionResource(row.ConnectionID)); err != nil {
		return Change{}, RefreshOutcome{}, err
	}
	if Status(row.Status) != StatusPendingReview {
		return Change{}, RefreshOutcome{}, stateErr("Only a change in review has a preview to update; this one is %s.", label(Status(row.Status)))
	}
	// Reserve the refresh first, so concurrent requests cannot run more
	// dry runs than the limit allows.
	now := s.now()
	err = s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		q := changesdb.New(tx)
		cur, err := q.GetChange(ctx, row.ID)
		if err != nil {
			return fmt.Errorf("changes: read change: %w", err)
		}
		if cur.LastRefreshAt.Valid {
			// A wait longer than the interval means the clock went back;
			// the refresh is allowed rather than blocked until it catches
			// up (a stale preview could not be approved meanwhile).
			if wait := time.UnixMilli(cur.LastRefreshAt.Int64).Add(RefreshInterval).Sub(now); wait > 0 && wait <= RefreshInterval {
				return &TooSoonError{RetryAfter: wait}
			}
		}
		return q.SetRefreshAttempt(ctx, changesdb.SetRefreshAttemptParams{LastRefreshAt: someInt(msOf(now)), ID: cur.ID})
	})
	if err != nil {
		return Change{}, RefreshOutcome{}, err
	}
	c, err := s.opts.Targets.Get(ctx, row.ConnectionID)
	if err != nil {
		return Change{}, RefreshOutcome{}, err
	}
	stmts, err := s.statementsOf(ctx, s.opts.Store.Reader(), row)
	if err != nil {
		return Change{}, RefreshOutcome{}, err
	}
	out, err := s.dryRun(ctx, c, sqlTexts(stmts))
	if err != nil {
		return Change{}, RefreshOutcome{}, err
	}
	res := RefreshOutcome{OK: out.ok(), Problem: out.summary()}
	err = s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		q := changesdb.New(tx)
		cur, err := q.GetChange(ctx, row.ID)
		if err != nil {
			return fmt.Errorf("changes: read change: %w", err)
		}
		if cur.Revision != row.Revision || Status(cur.Status) != StatusPendingReview {
			return ErrConflict
		}
		at := msOf(s.now())
		hash, changed, err := saveRun(ctx, q, cur, out, at, cur.LastRefreshAt)
		if err != nil {
			return err
		}
		res.DataChanged = res.OK && changed && cur.PreviewHash != ""
		payload := map[string]any{
			"number": cur.Number, "ok": res.OK, "data_changed": res.DataChanged,
			"preview_hash": hash, "old_preview_hash": cur.PreviewHash,
		}
		if !res.OK {
			payload["error"] = res.Problem
		} else {
			payload["statements"] = statementAudit(out)
		}
		return s.record(ctx, tx, audit.ChangePreviewRefreshed, cur.ID, payload)
	})
	if err != nil {
		return Change{}, RefreshOutcome{}, err
	}
	ch, err := s.Get(ctx, p, row.ID)
	return ch, res, err
}

// label is a status in words, for messages.
func label(st Status) string {
	switch st {
	case StatusPendingReview:
		return "in review"
	default:
		return string(st)
	}
}
