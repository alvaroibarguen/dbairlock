-- name: GetState :one
SELECT * FROM setup_state WHERE id = 1;

-- name: PutCode :exec
INSERT INTO setup_state (id, code_hash, code_issued_at)
VALUES (1, ?, ?)
ON CONFLICT (id) DO UPDATE SET
    code_hash = excluded.code_hash,
    code_issued_at = excluded.code_issued_at;

-- name: MarkCompleted :execrows
UPDATE setup_state
SET completed_at = ?, completed_by = ?, code_hash = NULL
WHERE id = 1 AND completed_at IS NULL;

-- name: InsertGrant :exec
INSERT INTO setup_grants (id_hash, kind, created_at, expires_at)
VALUES (?, ?, ?, ?);

-- name: GetGrant :one
SELECT * FROM setup_grants WHERE id_hash = ?;

-- name: DeleteGrant :execrows
DELETE FROM setup_grants WHERE id_hash = ?;

-- name: DeleteGrantsOfKind :exec
DELETE FROM setup_grants WHERE kind = ?;

-- name: DeleteExpiredGrants :exec
DELETE FROM setup_grants WHERE expires_at <= ?;
