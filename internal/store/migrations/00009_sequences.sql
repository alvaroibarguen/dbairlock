-- Sequences: ordered lists of 2 to 10 changes ("steps") that may target
-- different connections and run one after another. Conventions as in 00001.
--
-- Each step is a full change request (its own connection, statements,
-- preview, fingerprint, approvals, execution and audit events) that points
-- at its sequence with sequence_id and step (0-based). Only the current step
-- is reviewed: later steps wait as drafts with no preview, and the next one
-- is dry-run and submitted when the one before it has executed. Each step is
-- atomic on its own database; the sequence as a whole is not.
--
-- A sequence takes its number from change_counter, like a change, so every
-- number names exactly one thing (#12 the sequence, #13 and #14 its steps).

-- +goose Up
CREATE TABLE change_sequences (
    id             TEXT    PRIMARY KEY CHECK (id <> ''),
    number         INTEGER NOT NULL UNIQUE CHECK (number >= 1),
    -- As on change_requests: a sequence submitted through an API token is
    -- authored by the token's owner.
    author_id      TEXT    NOT NULL REFERENCES users (id),
    via            TEXT    NOT NULL CHECK (via IN ('web', 'mcp')),
    api_token_id   TEXT    NOT NULL DEFAULT '',
    agent_name     TEXT    NOT NULL DEFAULT '',
    title          TEXT    NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    reason         TEXT    NOT NULL DEFAULT '' CHECK (length(reason) <= 4000),
    -- running: a step is open or about to be previewed; completed: every
    -- step executed; stopped: a step failed, was rejected, expired or was
    -- cancelled, and the steps after it were cancelled.
    status         TEXT    NOT NULL CHECK (status IN ('running', 'completed', 'stopped')),
    -- For a stopped sequence: why (failed, rejected, expired, cancelled) and
    -- a plain sentence ("Stopped: step 2 failed.").
    status_code    TEXT    NOT NULL DEFAULT '',
    status_message TEXT    NOT NULL DEFAULT '',
    -- The 0-based step that is open now, or the one that ended the sequence.
    current_step   INTEGER NOT NULL CHECK (current_step >= 0 AND current_step < steps),
    steps          INTEGER NOT NULL CHECK (steps BETWEEN 2 AND 10),
    -- Identifies every step's connection and SQL, in order: a retried
    -- submission of the same sequence returns the open one.
    steps_hash     TEXT    NOT NULL CHECK (length(steps_hash) = 64),
    created_at     INTEGER NOT NULL CHECK (created_at >= 0),
    updated_at     INTEGER NOT NULL CHECK (updated_at >= created_at),
    finished_at    INTEGER CHECK (finished_at IS NULL OR finished_at >= created_at)
) STRICT;

CREATE INDEX change_sequences_status_idx ON change_sequences (status, number);
CREATE INDEX change_sequences_author_idx ON change_sequences (author_id, number);

-- An author has at most one running sequence with the same steps.
CREATE UNIQUE INDEX change_sequences_open_steps_idx ON change_sequences (author_id, steps_hash)
    WHERE status = 'running';

ALTER TABLE change_requests ADD COLUMN sequence_id TEXT REFERENCES change_sequences (id);
ALTER TABLE change_requests ADD COLUMN step INTEGER CHECK (step IS NULL OR step BETWEEN 0 AND 9);

CREATE UNIQUE INDEX change_requests_sequence_step_idx ON change_requests (sequence_id, step)
    WHERE sequence_id IS NOT NULL;

-- The duplicate rule of single changes (PLAN.md 5.4) does not apply to the
-- steps of a sequence: a sequence is deduplicated as a whole (above), and
-- its steps may repeat SQL that is open elsewhere.
DROP INDEX change_requests_open_sql_idx;
CREATE UNIQUE INDEX change_requests_open_sql_idx ON change_requests (author_id, connection_id, sql_hash)
    WHERE status IN ('pending_review', 'approved') AND sequence_id IS NULL;

-- +goose Down
DROP INDEX change_requests_open_sql_idx;
CREATE UNIQUE INDEX change_requests_open_sql_idx ON change_requests (author_id, connection_id, sql_hash)
    WHERE status IN ('pending_review', 'approved');
DROP INDEX change_requests_sequence_step_idx;
ALTER TABLE change_requests DROP COLUMN step;
ALTER TABLE change_requests DROP COLUMN sequence_id;
DROP INDEX change_sequences_open_steps_idx;
DROP INDEX change_sequences_author_idx;
DROP INDEX change_sequences_status_idx;
DROP TABLE change_sequences;
