-- Indexes found necessary by the phase 11 load test (50,000 changes,
-- 500,000 audit events; see docs/report-notes/robust.md).
--
-- reviews (change_id, reviewer_id): "has this reviewer already reviewed this
-- change" is asked for every change on the needs-my-review list and in its
-- count. With only reviews_reviewer_idx, SQLite walked every review the
-- reviewer ever gave, once per change in review: about 10 seconds for an
-- active reviewer, on every page load.
--
-- audit_events (actor_email, seq): the audit page's actor filter matches the
-- user id or the email; only the user id was indexed.

-- +goose Up
CREATE INDEX reviews_change_reviewer_idx ON reviews (change_id, reviewer_id);
CREATE INDEX audit_events_actor_email_idx ON audit_events (actor_email, seq);

-- +goose Down
DROP INDEX audit_events_actor_email_idx;
DROP INDEX reviews_change_reviewer_idx;
