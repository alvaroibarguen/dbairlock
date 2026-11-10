-- name: InsertOutbox :exec
INSERT INTO notify_outbox (id, event, payload, status, created_at, next_attempt_at, give_up_at)
VALUES (?, ?, ?, 'pending', ?, ?, ?);

-- name: DueOutbox :many
SELECT * FROM notify_outbox
WHERE status = 'pending' AND next_attempt_at <= ?
ORDER BY next_attempt_at, id
LIMIT ?;

-- name: NextDue :one
SELECT CAST(COALESCE(MIN(next_attempt_at), 0) AS INTEGER) FROM notify_outbox WHERE status = 'pending';

-- name: MarkDelivered :exec
UPDATE notify_outbox
SET status = 'delivered', attempts = attempts + 1, finished_at = ?, last_status = ?, last_error = ''
WHERE id = ? AND status = 'pending';

-- name: MarkRetry :exec
UPDATE notify_outbox
SET attempts = attempts + 1, next_attempt_at = ?, last_status = ?, last_error = ?
WHERE id = ? AND status = 'pending';

-- name: MarkGaveUp :exec
UPDATE notify_outbox
SET status = 'gave_up', attempts = attempts + 1, finished_at = ?, last_status = ?, last_error = ?
WHERE id = ? AND status = 'pending';

-- name: RecentOutbox :many
SELECT * FROM notify_outbox ORDER BY created_at DESC, id DESC LIMIT ?;

-- name: PruneOutbox :exec
DELETE FROM notify_outbox WHERE status <> 'pending' AND created_at < ?;
