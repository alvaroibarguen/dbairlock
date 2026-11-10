-- name: GetUser :one
SELECT * FROM users WHERE id = ?;

-- name: GetUserBySub :one
SELECT * FROM users WHERE google_sub = ?;

-- name: ListUsers :many
SELECT * FROM users ORDER BY email, id;

-- name: InsertUser :exec
INSERT INTO users (id, google_sub, email, name, picture_url, role, created_at, last_sign_in_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: UpdateProfile :exec
UPDATE users SET email = ?, name = ?, picture_url = ?, last_sign_in_at = ? WHERE id = ?;

-- name: SetRole :exec
UPDATE users SET role = ? WHERE id = ?;

-- name: DisableUser :exec
UPDATE users SET disabled_at = ?, disabled_by = ? WHERE id = ? AND disabled_at IS NULL;

-- name: EnableUser :exec
UPDATE users SET disabled_at = NULL, disabled_by = '' WHERE id = ?;

-- name: CountEnabledAdmins :one
SELECT count(*) FROM users WHERE role = 'admin' AND disabled_at IS NULL;

-- name: CountUsers :one
SELECT count(*) FROM users;

-- name: InsertSession :exec
INSERT INTO sessions (id_hash, user_id, created_at, expires_at, last_seen_at, ip, user_agent)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetSession :one
SELECT * FROM sessions WHERE id_hash = ?;

-- name: TouchSession :exec
UPDATE sessions SET last_seen_at = ?, expires_at = ? WHERE id_hash = ?;

-- name: DeleteSession :execrows
DELETE FROM sessions WHERE id_hash = ?;

-- name: DeleteUserSessions :execrows
DELETE FROM sessions WHERE user_id = ?;

-- name: DeleteExpiredSessions :exec
DELETE FROM sessions WHERE expires_at <= ?;

-- name: InsertToken :exec
INSERT INTO api_tokens (id, user_id, name, prefix, token_hash, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetToken :one
SELECT * FROM api_tokens WHERE id = ?;

-- name: GetTokenByHash :one
SELECT * FROM api_tokens WHERE token_hash = ?;

-- name: ListTokensOfUser :many
SELECT api_tokens.*, users.email AS owner_email
FROM api_tokens JOIN users ON users.id = api_tokens.user_id
WHERE api_tokens.user_id = ? AND api_tokens.revoked_at IS NULL
ORDER BY api_tokens.created_at DESC, api_tokens.id;

-- name: ListAllTokens :many
SELECT api_tokens.*, users.email AS owner_email
FROM api_tokens JOIN users ON users.id = api_tokens.user_id
WHERE api_tokens.revoked_at IS NULL
ORDER BY api_tokens.created_at DESC, api_tokens.id;

-- name: TouchToken :exec
UPDATE api_tokens SET last_used_at = ? WHERE id = ?;

-- name: RevokeToken :execrows
UPDATE api_tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL;

-- name: RevokeUserTokens :execrows
UPDATE api_tokens SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL;
