-- Setup state, users, sessions and API tokens (phase 4). Conventions as in 00001.

-- +goose Up

-- One row. The instance is in setup mode until completed_at is set. code_hash
-- is the SHA-256 of the current one-time setup code (NULL once setup is done).
CREATE TABLE setup_state (
    id           INTEGER PRIMARY KEY CHECK (id = 1),
    code_hash    BLOB    CHECK (code_hash IS NULL OR length(code_hash) = 32),
    code_issued_at INTEGER NOT NULL DEFAULT 0 CHECK (code_issued_at >= 0),
    completed_at INTEGER CHECK (completed_at IS NULL OR completed_at >= 0),
    completed_by TEXT    NOT NULL DEFAULT ''
) STRICT;

-- Short-lived grants of the setup module, stored as SHA-256 hashes only:
--   setup_session     cookie issued after the setup code was entered
--   recovery_code     one-time code printed by `dbairlock recover`
--   recovery_session  cookie issued after a recovery code was used
CREATE TABLE setup_grants (
    id_hash    BLOB    PRIMARY KEY CHECK (length(id_hash) = 32),
    kind       TEXT    NOT NULL CHECK (kind IN ('setup_session', 'recovery_code', 'recovery_session')),
    created_at INTEGER NOT NULL CHECK (created_at >= 0),
    expires_at INTEGER NOT NULL CHECK (expires_at >= created_at)
) STRICT;

-- People. Identity is the Google subject (sub), not the email. A removed user
-- keeps their row (disabled_at) so audit events and changes still resolve and
-- the same account is not created afresh on its next sign-in.
CREATE TABLE users (
    id              TEXT    PRIMARY KEY CHECK (id <> ''),
    google_sub      TEXT    NOT NULL UNIQUE CHECK (google_sub <> ''),
    email           TEXT    NOT NULL CHECK (email <> ''),
    name            TEXT    NOT NULL DEFAULT '',
    picture_url     TEXT    NOT NULL DEFAULT '',
    role            TEXT    NOT NULL CHECK (role IN ('admin', 'member')),
    created_at      INTEGER NOT NULL CHECK (created_at >= 0),
    last_sign_in_at INTEGER CHECK (last_sign_in_at IS NULL OR last_sign_in_at >= 0),
    disabled_at     INTEGER CHECK (disabled_at IS NULL OR disabled_at >= 0),
    disabled_by     TEXT    NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX users_email_idx ON users (email);

-- Browser sessions. Only the SHA-256 of the cookie value is stored.
CREATE TABLE sessions (
    id_hash      BLOB    PRIMARY KEY CHECK (length(id_hash) = 32),
    user_id      TEXT    NOT NULL REFERENCES users (id),
    created_at   INTEGER NOT NULL CHECK (created_at >= 0),
    expires_at   INTEGER NOT NULL CHECK (expires_at >= created_at),
    last_seen_at INTEGER NOT NULL CHECK (last_seen_at >= 0),
    ip           TEXT    NOT NULL DEFAULT '',
    user_agent   TEXT    NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX sessions_user_idx ON sessions (user_id);

-- Personal access tokens for agents (MCP). Only the SHA-256 of the full token
-- is stored; prefix is for display and is never used for lookup.
CREATE TABLE api_tokens (
    id           TEXT    PRIMARY KEY CHECK (id <> ''),
    user_id      TEXT    NOT NULL REFERENCES users (id),
    name         TEXT    NOT NULL CHECK (name <> ''),
    prefix       TEXT    NOT NULL CHECK (prefix <> ''),
    token_hash   BLOB    NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    created_at   INTEGER NOT NULL CHECK (created_at >= 0),
    last_used_at INTEGER CHECK (last_used_at IS NULL OR last_used_at >= 0),
    expires_at   INTEGER CHECK (expires_at IS NULL OR expires_at >= created_at),
    revoked_at   INTEGER CHECK (revoked_at IS NULL OR revoked_at >= 0)
) STRICT;

CREATE INDEX api_tokens_user_idx ON api_tokens (user_id, created_at);

-- +goose Down
DROP TABLE api_tokens;
DROP TABLE sessions;
DROP TABLE users;
DROP TABLE setup_grants;
DROP TABLE setup_state;
