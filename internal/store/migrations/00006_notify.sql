-- Notification outbox (phase 9). A row is written in the same transaction as
-- the state change it reports, so a rolled-back change sends nothing; a
-- background worker delivers it with retries and records the outcome here.
-- Conventions as in 00001. The webhook URL is never stored here: it is a
-- secret, read at delivery time.

-- +goose Up
CREATE TABLE notify_outbox (
    id              TEXT    PRIMARY KEY CHECK (id <> ''),
    event           TEXT    NOT NULL CHECK (event <> ''),
    payload         TEXT    NOT NULL CHECK (json_valid(payload) AND json_type(payload) = 'object'),
    status          TEXT    NOT NULL CHECK (status IN ('pending', 'delivered', 'gave_up')),
    attempts        INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    created_at      INTEGER NOT NULL CHECK (created_at >= 0),
    next_attempt_at INTEGER NOT NULL CHECK (next_attempt_at >= 0),
    -- Delivery stops being retried after this time.
    give_up_at      INTEGER NOT NULL CHECK (give_up_at >= 0),
    finished_at     INTEGER,
    last_status     INTEGER NOT NULL DEFAULT 0,
    last_error      TEXT    NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX notify_outbox_due_idx ON notify_outbox (status, next_attempt_at);
CREATE INDEX notify_outbox_created_idx ON notify_outbox (created_at);

-- +goose Down
DROP TABLE notify_outbox;
