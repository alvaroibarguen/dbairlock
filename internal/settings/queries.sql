-- name: ListSettings :many
SELECT * FROM settings ORDER BY key;

-- name: PutSetting :exec
INSERT INTO settings (key, value, updated_by, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT (key) DO UPDATE SET
    value = excluded.value,
    updated_by = excluded.updated_by,
    updated_at = excluded.updated_at;
