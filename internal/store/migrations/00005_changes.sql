-- Changes: change requests, their statements, reviews and executions
-- (phase 7). Conventions as in 00001.

-- +goose Up

-- One row holding the last change number handed out. Numbers come from here
-- inside the creating transaction, never from MAX(number) + 1.
CREATE TABLE change_counter (
    id   INTEGER PRIMARY KEY CHECK (id = 1),
    last INTEGER NOT NULL CHECK (last >= 0)
) STRICT;

INSERT INTO change_counter (id, last) VALUES (1, 0);

-- A change request. status follows the state machine in internal/changes
-- (PLAN.md section 6). sql_hash identifies what will run (every statement of
-- the current revision, in order); preview_hash identifies the data the
-- current preview showed (every statement's expected rows and fingerprint).
-- An approval counts only while both match.
CREATE TABLE change_requests (
    id              TEXT    PRIMARY KEY CHECK (id <> ''),
    number          INTEGER NOT NULL UNIQUE CHECK (number >= 1),
    connection_id   TEXT    NOT NULL REFERENCES connections (id),
    -- A change submitted through an API token is authored by the token's
    -- owner; via and api_token_id say an agent wrote it, agent_name is the
    -- token's name when it was submitted.
    author_id       TEXT    NOT NULL REFERENCES users (id),
    via             TEXT    NOT NULL CHECK (via IN ('web', 'mcp')),
    api_token_id    TEXT    NOT NULL DEFAULT '',
    agent_name      TEXT    NOT NULL DEFAULT '',
    title           TEXT    NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    reason          TEXT    NOT NULL DEFAULT '' CHECK (length(reason) <= 4000),
    status          TEXT    NOT NULL CHECK (status IN (
                        'draft', 'pending_review', 'approved', 'executing', 'executed',
                        'failed', 'rejected', 'expired', 'cancelled')),
    -- Why the change is in its status, for the terminal ones and a waiting
    -- approved one: for example failed / drift / count, or cancelled /
    -- author_removed. status_message is a plain sentence.
    status_code     TEXT    NOT NULL DEFAULT '',
    status_detail   TEXT    NOT NULL DEFAULT '',
    status_message  TEXT    NOT NULL DEFAULT '',
    sql_hash        TEXT    NOT NULL CHECK (length(sql_hash) = 64),
    preview_hash    TEXT    NOT NULL DEFAULT '',
    previewed_at    INTEGER CHECK (previewed_at IS NULL OR previewed_at >= 0),
    -- Set when the last dry run failed (validation, too many or no rows,
    -- unreachable target). A change with a preview error cannot be approved.
    preview_error   TEXT    NOT NULL DEFAULT '',
    -- True when the last refresh found different data than the one before
    -- it; the statements then keep the previous preview for comparison.
    preview_changed INTEGER NOT NULL DEFAULT 0 CHECK (preview_changed IN (0, 1)),
    revision        INTEGER NOT NULL CHECK (revision >= 1),
    -- Execution attempts that could not reach the target, and when the next
    -- one may start (PLAN.md 5.6).
    attempts        INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at INTEGER CHECK (next_attempt_at IS NULL OR next_attempt_at >= 0),
    last_attempt_error TEXT NOT NULL DEFAULT '',
    -- Set by "Execute now" on a connection without automatic execution.
    execute_requested_at INTEGER CHECK (execute_requested_at IS NULL OR execute_requested_at >= 0),
    last_refresh_at INTEGER CHECK (last_refresh_at IS NULL OR last_refresh_at >= 0),
    created_at      INTEGER NOT NULL CHECK (created_at >= 0),
    updated_at      INTEGER NOT NULL CHECK (updated_at >= created_at),
    submitted_at    INTEGER CHECK (submitted_at IS NULL OR submitted_at >= created_at),
    decided_at      INTEGER CHECK (decided_at IS NULL OR decided_at >= 0),
    executed_at     INTEGER CHECK (executed_at IS NULL OR executed_at >= 0),
    expires_at      INTEGER CHECK (expires_at IS NULL OR expires_at >= 0)
) STRICT;

CREATE INDEX change_requests_status_idx ON change_requests (status, number);
CREATE INDEX change_requests_author_idx ON change_requests (author_id, number);
CREATE INDEX change_requests_connection_idx ON change_requests (connection_id, status);

-- An author has at most one open change with the same SQL on a connection:
-- a retried submission returns the existing one (PLAN.md 5.4).
CREATE UNIQUE INDEX change_requests_open_sql_idx ON change_requests (author_id, connection_id, sql_hash)
    WHERE status IN ('pending_review', 'approved');

-- At most one change per connection executes at a time (PLAN.md 5.6).
CREATE UNIQUE INDEX change_requests_one_executing_idx ON change_requests (connection_id)
    WHERE status = 'executing';

-- The statements of every revision of a change, in order. Only the current
-- revision runs; older ones stay for the record. error holds the validation
-- or dry-run error of the statement as JSON ({code, message, position}).
-- preview holds the dry-run result as JSON: counts, columns and, when the
-- connection stores row images, the before and after rows.
CREATE TABLE change_statements (
    change_id            TEXT    NOT NULL REFERENCES change_requests (id),
    revision             INTEGER NOT NULL CHECK (revision >= 1),
    position             INTEGER NOT NULL CHECK (position BETWEEN 0 AND 19),
    sql_text             TEXT    NOT NULL CHECK (length(CAST(sql_text AS BLOB)) BETWEEN 1 AND 65536),
    kind                 TEXT    NOT NULL DEFAULT '' CHECK (kind IN ('', 'insert', 'update', 'delete')),
    schema_name          TEXT    NOT NULL DEFAULT '',
    table_name           TEXT    NOT NULL DEFAULT '',
    expected_rows        INTEGER CHECK (expected_rows IS NULL OR expected_rows >= 0),
    expected_fingerprint TEXT    NOT NULL DEFAULT '',
    has_primary_key      INTEGER NOT NULL DEFAULT 0 CHECK (has_primary_key IN (0, 1)),
    error                TEXT    CHECK (error IS NULL OR (json_valid(error) AND json_type(error) = 'object')),
    preview              TEXT    CHECK (preview IS NULL OR (json_valid(preview) AND json_type(preview) = 'object')),
    previous_preview     TEXT    CHECK (previous_preview IS NULL OR (json_valid(previous_preview) AND json_type(previous_preview) = 'object')),
    PRIMARY KEY (change_id, revision, position)
) STRICT;

-- Reviews. An approval records the sql_hash and preview_hash it was given
-- for; it counts only while both are the change's current ones, the
-- reviewer is not removed and it was not voided (its reviewer was removed).
CREATE TABLE reviews (
    id           TEXT    PRIMARY KEY CHECK (id <> ''),
    change_id    TEXT    NOT NULL REFERENCES change_requests (id),
    reviewer_id  TEXT    NOT NULL REFERENCES users (id),
    decision     TEXT    NOT NULL CHECK (decision IN ('approve', 'reject')),
    comment      TEXT    NOT NULL DEFAULT '' CHECK (length(comment) <= 4000),
    sql_hash     TEXT    NOT NULL CHECK (length(sql_hash) = 64),
    preview_hash TEXT    NOT NULL DEFAULT '',
    revision     INTEGER NOT NULL CHECK (revision >= 1),
    created_at   INTEGER NOT NULL CHECK (created_at >= 0),
    voided_at    INTEGER CHECK (voided_at IS NULL OR voided_at >= created_at),
    void_reason  TEXT    NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX reviews_change_idx ON reviews (change_id, created_at);
CREATE INDEX reviews_reviewer_idx ON reviews (reviewer_id);

-- Execution attempts. outcome is running while the target transaction may
-- be open; not_started means the target could not be reached and nothing
-- ran. detail holds drift details as JSON; statement_results the
-- per-statement results (row images only when the connection stores them).
CREATE TABLE executions (
    id                TEXT    PRIMARY KEY CHECK (id <> ''),
    change_id         TEXT    NOT NULL REFERENCES change_requests (id),
    attempt           INTEGER NOT NULL CHECK (attempt >= 1),
    started_at        INTEGER NOT NULL CHECK (started_at >= 0),
    finished_at       INTEGER CHECK (finished_at IS NULL OR finished_at >= started_at),
    outcome           TEXT    NOT NULL CHECK (outcome IN (
                          'running', 'executed', 'failed', 'not_started', 'interrupted')),
    error_code        TEXT    NOT NULL DEFAULT '',
    error_message     TEXT    NOT NULL DEFAULT '',
    detail            TEXT    CHECK (detail IS NULL OR (json_valid(detail) AND json_type(detail) = 'object')),
    statement_results TEXT    CHECK (statement_results IS NULL OR (json_valid(statement_results) AND json_type(statement_results) = 'array'))
) STRICT;

CREATE INDEX executions_change_idx ON executions (change_id, attempt);

-- +goose Down
DROP TABLE executions;
DROP TABLE reviews;
DROP TABLE change_statements;
DROP TABLE change_requests;
DROP TABLE change_counter;
