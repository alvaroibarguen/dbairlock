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

-- OAuth (00008).

-- name: InsertOAuthClient :exec
INSERT INTO oauth_clients (id, name, redirect_uris, created_at, created_ip)
VALUES (?, ?, ?, ?, ?);

-- name: GetOAuthClient :one
SELECT * FROM oauth_clients WHERE id = ?;

-- name: CountUnusedOAuthClients :one
SELECT count(*) FROM oauth_clients WHERE last_used_at IS NULL;

-- name: DeleteUnusedOAuthClients :execrows
DELETE FROM oauth_clients
WHERE oauth_clients.last_used_at IS NULL AND oauth_clients.created_at <= ?
  AND NOT EXISTS (SELECT 1 FROM oauth_grants WHERE oauth_grants.client_id = oauth_clients.id);

-- name: TouchOAuthClient :exec
UPDATE oauth_clients SET last_used_at = ? WHERE id = ?;

-- name: InsertOAuthGrant :exec
INSERT INTO oauth_grants (id, client_id, user_id, client_name, redirect_uri, resource, scope, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetOAuthGrant :one
SELECT * FROM oauth_grants WHERE id = ?;

-- name: ListActiveOAuthGrantsOfClientUser :many
SELECT * FROM oauth_grants
WHERE client_id = ? AND user_id = ? AND revoked_at IS NULL
ORDER BY created_at, id;

-- name: ListActiveOAuthGrantsOfUser :many
SELECT * FROM oauth_grants WHERE user_id = ? AND revoked_at IS NULL;

-- name: ListOAuthGrantsOfUser :many
SELECT oauth_grants.*, users.email AS owner_email
FROM oauth_grants JOIN users ON users.id = oauth_grants.user_id
WHERE oauth_grants.user_id = ? AND oauth_grants.revoked_at IS NULL
ORDER BY oauth_grants.created_at DESC, oauth_grants.id;

-- name: ListAllOAuthGrants :many
SELECT oauth_grants.*, users.email AS owner_email
FROM oauth_grants JOIN users ON users.id = oauth_grants.user_id
WHERE oauth_grants.revoked_at IS NULL
ORDER BY oauth_grants.created_at DESC, oauth_grants.id;

-- name: TouchOAuthGrant :exec
UPDATE oauth_grants SET last_used_at = ? WHERE id = ?;

-- name: RevokeOAuthGrant :execrows
UPDATE oauth_grants SET revoked_at = ?, revoked_reason = ? WHERE id = ? AND revoked_at IS NULL;

-- name: DeleteOAuthGrantTokens :exec
DELETE FROM oauth_tokens WHERE grant_id = ?;

-- name: DeleteOAuthGrantCodes :exec
DELETE FROM oauth_codes WHERE grant_id = ?;

-- name: InsertOAuthCode :exec
INSERT INTO oauth_codes (code_hash, grant_id, redirect_uri, code_challenge, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?);

-- name: GetOAuthCode :one
SELECT * FROM oauth_codes WHERE code_hash = ?;

-- name: UseOAuthCode :execrows
UPDATE oauth_codes SET used_at = ? WHERE code_hash = ? AND used_at IS NULL;

-- name: InsertOAuthToken :exec
INSERT INTO oauth_tokens (token_hash, grant_id, kind, created_at, expires_at)
VALUES (?, ?, ?, ?, ?);

-- name: GetOAuthToken :one
SELECT * FROM oauth_tokens WHERE token_hash = ?;

-- name: UseOAuthToken :execrows
UPDATE oauth_tokens SET used_at = ? WHERE token_hash = ? AND used_at IS NULL;

-- name: DeleteExpiredOAuthTokens :exec
DELETE FROM oauth_tokens WHERE expires_at <= ?;

-- name: DeleteExpiredOAuthCodes :exec
DELETE FROM oauth_codes WHERE expires_at <= ?;
