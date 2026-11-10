-- name: GetSecret :one
SELECT * FROM secrets WHERE owner_type = ? AND owner_id = ? AND kind = ?;

-- name: InsertSecret :exec
INSERT INTO secrets (id, kind, owner_type, owner_id, ciphertext, nonce, key_version, set_by, set_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ReplaceSecret :exec
UPDATE secrets
SET ciphertext = ?, nonce = ?, key_version = ?, set_by = ?, set_at = ?
WHERE id = ?;

-- name: ReencryptSecret :exec
UPDATE secrets SET ciphertext = ?, nonce = ?, key_version = ? WHERE id = ?;

-- name: DeleteSecret :exec
DELETE FROM secrets WHERE id = ?;

-- name: ListSecrets :many
SELECT * FROM secrets ORDER BY id;

-- name: GetKeyCheck :one
SELECT * FROM secret_key_check WHERE id = 1;

-- name: PutKeyCheck :exec
INSERT INTO secret_key_check (id, ciphertext, nonce, key_version, updated_at)
VALUES (1, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET
    ciphertext = excluded.ciphertext,
    nonce = excluded.nonce,
    key_version = excluded.key_version,
    updated_at = excluded.updated_at;
