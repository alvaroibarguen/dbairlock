-- Secrets (write-only, AES-256-GCM under the master key) and instance settings.
-- Conventions as in 00001.

-- +goose Up
CREATE TABLE secrets (
    id          TEXT    PRIMARY KEY CHECK (id <> ''),
    kind        TEXT    NOT NULL CHECK (kind IN ('target_password', 'oauth_client_secret', 'webhook_url')),
    owner_type  TEXT    NOT NULL CHECK (owner_type <> ''),
    owner_id    TEXT    NOT NULL CHECK (owner_id <> ''),
    ciphertext  BLOB    NOT NULL CHECK (length(ciphertext) >= 16),
    nonce       BLOB    NOT NULL CHECK (length(nonce) = 12),
    key_version INTEGER NOT NULL CHECK (key_version >= 1),
    set_by      TEXT    NOT NULL DEFAULT '',
    set_at      INTEGER NOT NULL CHECK (set_at >= 0),
    UNIQUE (owner_type, owner_id, kind)
) STRICT;

-- One row: a fixed string encrypted under the master key. Decrypting it at
-- start-up proves that master.key belongs to this database.
CREATE TABLE secret_key_check (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    ciphertext  BLOB    NOT NULL CHECK (length(ciphertext) >= 16),
    nonce       BLOB    NOT NULL CHECK (length(nonce) = 12),
    key_version INTEGER NOT NULL CHECK (key_version >= 1),
    updated_at  INTEGER NOT NULL CHECK (updated_at >= 0)
) STRICT;

-- A setting that was never changed has no row and reads as its registered
-- default. Keys are validated against the registry in internal/settings.
CREATE TABLE settings (
    key        TEXT    PRIMARY KEY CHECK (key <> ''),
    value      TEXT    NOT NULL CHECK (json_valid(value)),
    updated_by TEXT    NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL CHECK (updated_at >= 0)
) STRICT;

-- +goose Down
DROP TABLE settings;
DROP TABLE secret_key_check;
DROP TABLE secrets;
