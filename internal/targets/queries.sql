-- name: GetConnection :one
SELECT * FROM connections WHERE id = ?;

-- name: ListActiveConnections :many
SELECT * FROM connections WHERE archived_at IS NULL ORDER BY connector, name, id;

-- name: CountActiveByName :one
SELECT count(*) FROM connections WHERE name = ? AND archived_at IS NULL AND id <> ?;

-- name: InsertConnection :exec
INSERT INTO connections (
    id, name, description, connector, config,
    required_approvals, max_rows, store_row_images, auto_execute, approval_ttl_hours, allowed_functions,
    created_by, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: UpdateConnection :exec
UPDATE connections SET
    name = ?, description = ?, config = ?,
    required_approvals = ?, max_rows = ?, store_row_images = ?, auto_execute = ?,
    approval_ttl_hours = ?, allowed_functions = ?, updated_at = ?
WHERE id = ? AND archived_at IS NULL;

-- name: ArchiveConnection :exec
UPDATE connections SET archived_at = ?, archived_by = ? WHERE id = ? AND archived_at IS NULL;

-- name: SetLastTest :exec
UPDATE connections SET last_test = ? WHERE id = ?;
