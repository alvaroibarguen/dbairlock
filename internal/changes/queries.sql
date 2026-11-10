-- name: NextNumber :one
UPDATE change_counter SET last = last + 1 WHERE id = 1 RETURNING last;

-- name: InsertChange :exec
INSERT INTO change_requests (
    id, number, connection_id, author_id, via, api_token_id, agent_name,
    title, reason, status, sql_hash, preview_hash, previewed_at, preview_error,
    revision, created_at, updated_at, submitted_at, expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetChange :one
SELECT * FROM change_requests WHERE id = ?;

-- name: GetChangeByNumber :one
SELECT * FROM change_requests WHERE number = ?;

-- name: FindOpenDuplicate :one
SELECT * FROM change_requests
WHERE author_id = ? AND connection_id = ? AND sql_hash = ?
  AND status IN ('pending_review', 'approved') AND id <> ?
ORDER BY number LIMIT 1;

-- name: UpdateContent :exec
UPDATE change_requests SET
    title = ?, reason = ?, sql_hash = ?, preview_hash = ?, previewed_at = ?, preview_error = ?,
    preview_changed = 0, revision = ?, updated_at = ?
WHERE id = ?;

-- name: UpdatePreview :exec
UPDATE change_requests SET
    preview_hash = ?, previewed_at = ?, preview_error = ?, preview_changed = ?,
    last_refresh_at = ?, updated_at = ?
WHERE id = ?;

-- name: SetRefreshAttempt :exec
UPDATE change_requests SET last_refresh_at = ? WHERE id = ?;

-- name: SetStatus :exec
UPDATE change_requests SET
    status = ?, status_code = ?, status_detail = ?, status_message = ?, updated_at = ?
WHERE id = ?;

-- name: MarkSubmitted :exec
UPDATE change_requests SET
    status = 'pending_review', submitted_at = ?, expires_at = ?, updated_at = ?
WHERE id = ? AND status = 'draft';

-- name: MarkDecided :exec
UPDATE change_requests SET
    status = ?, status_code = ?, status_detail = ?, status_message = ?, decided_at = ?, updated_at = ?
WHERE id = ?;

-- name: ReturnToReview :exec
UPDATE change_requests SET
    status = 'pending_review', status_code = ?, status_detail = '', status_message = ?,
    decided_at = NULL, execute_requested_at = NULL, updated_at = ?
WHERE id = ? AND status = 'approved';

-- name: RequestExecution :exec
UPDATE change_requests SET execute_requested_at = ?, updated_at = ? WHERE id = ? AND status = 'approved';

-- name: ClearNextAttempt :exec
UPDATE change_requests SET next_attempt_at = NULL, updated_at = ? WHERE id = ? AND status = 'approved';

-- name: ClaimChange :one
UPDATE change_requests SET status = 'executing', status_code = '', status_detail = '', status_message = '', updated_at = ?
WHERE change_requests.id = ? AND change_requests.status = 'approved'
  AND NOT EXISTS (
      SELECT 1 FROM change_requests e
      WHERE e.connection_id = change_requests.connection_id AND e.status = 'executing'
  )
RETURNING *;

-- name: DeferChange :exec
UPDATE change_requests SET
    status = 'approved', attempts = ?, next_attempt_at = ?, last_attempt_error = ?, updated_at = ?
WHERE id = ? AND status = 'executing';

-- name: FinishChange :exec
UPDATE change_requests SET
    status = ?, status_code = ?, status_detail = ?, status_message = ?, executed_at = ?, updated_at = ?
WHERE id = ? AND status = 'executing';

-- name: ClaimCandidates :many
SELECT * FROM change_requests c
WHERE c.status = 'approved'
  AND (c.next_attempt_at IS NULL OR c.next_attempt_at <= CAST(sqlc.arg(now) AS INTEGER))
  AND NOT EXISTS (
      SELECT 1 FROM change_requests e WHERE e.connection_id = c.connection_id AND e.status = 'executing'
  )
ORDER BY c.decided_at, c.number
LIMIT 50;

-- name: ListDueForExpiry :many
SELECT * FROM change_requests
WHERE status IN ('pending_review', 'approved') AND expires_at IS NOT NULL AND expires_at <= ?
ORDER BY number;

-- name: ListExecuting :many
SELECT * FROM change_requests WHERE status = 'executing' ORDER BY number;

-- name: ListOpenByAuthor :many
SELECT * FROM change_requests
WHERE author_id = ? AND status IN ('draft', 'pending_review', 'approved')
ORDER BY number;

-- name: ListApprovedWithReviewer :many
SELECT DISTINCT c.* FROM change_requests c
JOIN reviews r ON r.change_id = c.id
WHERE c.status IN ('pending_review', 'approved') AND r.reviewer_id = ? AND r.voided_at IS NULL
ORDER BY c.number;

-- name: ListChanges :many
SELECT * FROM change_requests c
WHERE c.number < sqlc.arg(before_number)
  AND (CAST(sqlc.arg(status) AS TEXT) = '' OR c.status = sqlc.arg(status))
  AND (CAST(sqlc.arg(connection_id) AS TEXT) = '' OR c.connection_id = sqlc.arg(connection_id))
  AND (CAST(sqlc.arg(author_id) AS TEXT) = '' OR c.author_id = sqlc.arg(author_id))
  AND (CAST(sqlc.arg(open_only) AS INTEGER) = 0 OR c.status IN ('draft', 'pending_review', 'approved', 'executing'))
  AND (CAST(sqlc.arg(reviewer_id) AS TEXT) = '' OR (
      c.status = 'pending_review' AND c.author_id <> sqlc.arg(reviewer_id)
      AND NOT EXISTS (
          SELECT 1 FROM reviews r
          WHERE r.change_id = c.id AND r.reviewer_id = sqlc.arg(reviewer_id) AND r.voided_at IS NULL
            AND (r.decision = 'reject' OR (r.sql_hash = c.sql_hash AND r.preview_hash = c.preview_hash))
      )
  ))
ORDER BY c.number DESC
LIMIT sqlc.arg(page_limit);

-- name: CountNeedsReview :one
SELECT count(*) FROM change_requests c
WHERE c.status = 'pending_review' AND c.author_id <> sqlc.arg(reviewer_id)
  AND NOT EXISTS (
      SELECT 1 FROM reviews r
      WHERE r.change_id = c.id AND r.reviewer_id = sqlc.arg(reviewer_id) AND r.voided_at IS NULL
        AND (r.decision = 'reject' OR (r.sql_hash = c.sql_hash AND r.preview_hash = c.preview_hash))
  );

-- name: InsertStatement :exec
INSERT INTO change_statements (
    change_id, revision, position, sql_text, kind, schema_name, table_name,
    expected_rows, expected_fingerprint, has_primary_key, error, preview
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: UpdateStatementPreview :exec
UPDATE change_statements SET
    kind = ?, schema_name = ?, table_name = ?, expected_rows = ?, expected_fingerprint = ?,
    has_primary_key = ?, error = ?, preview = ?, previous_preview = ?
WHERE change_id = ? AND revision = ? AND position = ?;

-- name: ListStatements :many
SELECT * FROM change_statements WHERE change_id = ? AND revision = ? ORDER BY position;

-- name: ListStatementsOfChanges :many
SELECT change_statements.* FROM change_statements
JOIN change_requests ON change_requests.id = change_statements.change_id
    AND change_requests.revision = change_statements.revision
WHERE change_statements.change_id IN (sqlc.slice(ids))
ORDER BY change_statements.change_id, change_statements.position;

-- name: InsertReview :exec
INSERT INTO reviews (id, change_id, reviewer_id, decision, comment, sql_hash, preview_hash, revision, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListReviews :many
SELECT * FROM reviews WHERE change_id = ? ORDER BY created_at, id;

-- name: VoidReviewsOf :many
UPDATE reviews SET voided_at = ?, void_reason = ?
WHERE reviewer_id = ? AND voided_at IS NULL
  AND change_id IN (SELECT id FROM change_requests WHERE status IN ('draft', 'pending_review', 'approved'))
RETURNING change_id;

-- name: InsertExecution :exec
INSERT INTO executions (id, change_id, attempt, started_at, outcome) VALUES (?, ?, ?, ?, 'running');

-- name: FinishExecution :exec
UPDATE executions SET
    finished_at = ?, outcome = ?, error_code = ?, error_message = ?, detail = ?, statement_results = ?
WHERE id = ?;

-- name: InterruptExecutions :exec
UPDATE executions SET finished_at = ?, outcome = 'interrupted', error_code = 'interrupted', error_message = ?
WHERE change_id = ? AND outcome = 'running';

-- name: ListExecutions :many
SELECT * FROM executions WHERE change_id = ? ORDER BY attempt, started_at;

-- name: CountExecutions :one
SELECT count(*) FROM executions WHERE change_id = ?;
