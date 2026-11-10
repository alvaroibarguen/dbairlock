-- Conventions for every table (PLAN phase 2):
--   id           UUIDv7 as text
--   timestamps   integer milliseconds since the Unix epoch, UTC
--   JSON         text, validated with json_valid
--   enums        text with a CHECK constraint
--   every table  STRICT

-- +goose Up
CREATE TABLE audit_events (
    seq           INTEGER PRIMARY KEY AUTOINCREMENT,
    id            TEXT    NOT NULL UNIQUE CHECK (id <> ''),
    occurred_at   INTEGER NOT NULL CHECK (occurred_at >= 0),
    actor_user_id TEXT    NOT NULL DEFAULT '',
    actor_email   TEXT    NOT NULL DEFAULT '',
    via           TEXT    NOT NULL CHECK (via IN ('web', 'mcp', 'cli', 'system')),
    api_token_id  TEXT    NOT NULL DEFAULT '',
    ip            TEXT    NOT NULL DEFAULT '',
    action        TEXT    NOT NULL CHECK (action <> ''),
    subject_type  TEXT    NOT NULL DEFAULT '',
    subject_id    TEXT    NOT NULL DEFAULT '',
    payload       TEXT    NOT NULL CHECK (json_valid(payload) AND json_type(payload) = 'object'),
    prev_hash     BLOB    NOT NULL CHECK (length(prev_hash) = 32),
    hash          BLOB    NOT NULL CHECK (length(hash) = 32)
) STRICT;

CREATE INDEX audit_events_actor_idx ON audit_events (actor_user_id, seq);
CREATE INDEX audit_events_action_idx ON audit_events (action, seq);
CREATE INDEX audit_events_subject_idx ON audit_events (subject_type, subject_id, seq);
CREATE INDEX audit_events_occurred_at_idx ON audit_events (occurred_at);

-- The audit log is append-only. These triggers stop accidents, not attackers:
-- anyone holding the database file can drop them. The hash chain is what makes
-- tampering detectable (see docs/security.md).
-- +goose StatementBegin
CREATE TRIGGER audit_events_no_update
BEFORE UPDATE ON audit_events
BEGIN
    SELECT RAISE(ABORT, 'audit_events is append-only: UPDATE is not allowed');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER audit_events_no_delete
BEFORE DELETE ON audit_events
BEGIN
    SELECT RAISE(ABORT, 'audit_events is append-only: DELETE is not allowed');
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER audit_events_no_delete;
DROP TRIGGER audit_events_no_update;
DROP TABLE audit_events;
