-- name: LastEvent :one
SELECT seq, hash FROM audit_events ORDER BY seq DESC LIMIT 1;

-- name: InsertEvent :exec
INSERT INTO audit_events (
    seq, id, occurred_at, actor_user_id, actor_email, via, api_token_id, ip,
    action, subject_type, subject_id, payload, prev_hash, hash
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
);

-- name: EventsAfter :many
SELECT * FROM audit_events WHERE seq > ? ORDER BY seq ASC LIMIT ?;

-- name: EventsBetween :many
SELECT * FROM audit_events
WHERE seq > sqlc.arg(after_seq)
  AND occurred_at >= sqlc.arg(from_ms)
  AND occurred_at < sqlc.arg(to_ms)
ORDER BY seq ASC
LIMIT sqlc.arg(page_limit);

-- The filtered page query (List) is built in repository.go, so that only
-- the filters in use reach SQLite's planner.
