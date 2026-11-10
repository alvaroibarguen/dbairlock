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

-- name: ListEvents :many
SELECT * FROM audit_events
WHERE seq < sqlc.arg(before_seq)
  AND (CAST(sqlc.arg(actor_user_id) AS TEXT) = '' OR actor_user_id = sqlc.arg(actor_user_id))
  AND (CAST(sqlc.arg(action_prefix) AS TEXT) = '' OR substr(action, 1, length(sqlc.arg(action_prefix))) = sqlc.arg(action_prefix))
  AND (CAST(sqlc.arg(subject_type) AS TEXT) = '' OR subject_type = sqlc.arg(subject_type))
  AND (CAST(sqlc.arg(subject_id) AS TEXT) = '' OR subject_id = sqlc.arg(subject_id))
  AND occurred_at >= sqlc.arg(from_ms)
  AND occurred_at < sqlc.arg(to_ms)
ORDER BY seq DESC
LIMIT sqlc.arg(page_limit);
