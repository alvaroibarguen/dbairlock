-- Connections: configured target databases (phase 5). Conventions as in 00001.

-- +goose Up

-- One configured target database. A connector (postgres, mysql) may have any
-- number of connections; every piece of per-target state is keyed by the
-- connection id. The password lives in the secret store, owned by
-- ('connection', id). config holds the connector's non-secret fields as a
-- JSON object, normalized by the connector. Policy columns are copied from
-- the policy.default_* settings when the connection is created.
CREATE TABLE connections (
    id                 TEXT    PRIMARY KEY CHECK (id <> ''),
    name               TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 63),
    description        TEXT    NOT NULL DEFAULT '' CHECK (length(description) <= 500),
    connector          TEXT    NOT NULL CHECK (connector <> ''),
    config             TEXT    NOT NULL CHECK (json_valid(config) AND json_type(config) = 'object'),
    required_approvals INTEGER NOT NULL CHECK (required_approvals BETWEEN 1 AND 5),
    max_rows           INTEGER NOT NULL CHECK (max_rows BETWEEN 1 AND 10000),
    store_row_images   INTEGER NOT NULL CHECK (store_row_images IN (0, 1)),
    auto_execute       INTEGER NOT NULL CHECK (auto_execute IN (0, 1)),
    approval_ttl_hours INTEGER NOT NULL CHECK (approval_ttl_hours BETWEEN 1 AND 720),
    -- JSON array of lower-case function names statements may call.
    allowed_functions  TEXT    NOT NULL CHECK (json_valid(allowed_functions) AND json_type(allowed_functions) = 'array'),
    created_by         TEXT    NOT NULL DEFAULT '',
    created_at         INTEGER NOT NULL CHECK (created_at >= 0),
    -- Bumped on every update, including a new password; a cached connection
    -- pool opened before it is replaced.
    updated_at         INTEGER NOT NULL CHECK (updated_at >= created_at),
    archived_at        INTEGER CHECK (archived_at IS NULL OR archived_at >= created_at),
    archived_by        TEXT    NOT NULL DEFAULT '',
    -- The outcome of the last "Test connection" of the saved connection, as
    -- a JSON object (see targets.TestResult). NULL until first tested.
    last_test          TEXT    CHECK (last_test IS NULL OR (json_valid(last_test) AND json_type(last_test) = 'object'))
) STRICT;

-- Agents refer to a connection by name alone, so names are unique across all
-- connectors. An archived connection gives its name up.
CREATE UNIQUE INDEX connections_name_active_idx ON connections (name) WHERE archived_at IS NULL;

-- +goose Down
DROP TABLE connections;
