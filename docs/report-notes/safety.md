# Phase 11 notes: safety rule audit

Workstream `safety`. Scope: the "Safety rule audit" section of the Phase 11
plan. The rule-to-test table is in `docs/security.md`, section "Safety rules
and the tests that prove them".

## What was added

- `internal/connector/postgres/hostile_test.go` and
  `internal/connector/mysql/hostile_test.go`: `TestHostileValidation`, about
  50 hostile statements per connector run through the same checks as the
  validation corpus (each is either rejected with a pinned code or accepted
  with a pinned kind, table and `WHERE` text), and an integration subtest per
  connector (`TestPostgresSpecific/hostile text on a server with
  standard_conforming_strings off`, `TestMySQLSpecific/hostile text on a
  server with every lexical sql_mode`) that runs the accepted ones on a
  deliberately misconfigured server and checks that the rows in the preview
  are exactly the rows the statement changes.
- `internal/app/safety_test.go`: `TestSafetyApprovedChangeIsFrozen`,
  `TestSafetyEditApproveExecuteRace`, `TestSafetySelfApproval`,
  `TestSafetyDoubleExecution` (five subtests), `TestSafetyRevalidatedAtExecution`,
  `TestSafetyRowImagesOff`, `TestSafetyPreviewFreshnessAndClock`.
- `internal/app/secretleak_test.go`: `TestSecretLeak`, the secret leak test.
- Two rules had no test: "with row images off, only column names and counts
  are kept" and "statements are validated again with the current policy at
  execution". Both have one now (`TestSafetyRowImagesOff`,
  `TestSafetyRevalidatedAtExecution`).

## SQL slipping past validation

Tried on both connectors: nested comments, comments closed early, `/*/`,
empty comments; no-break spaces, zero-width spaces, line and paragraph
separators, U+0085, fullwidth semicolons, Cyrillic look-alikes of `WHERE`
and `AS`; mixed-case keywords; tables, columns and strings named `where`,
`UPDATE`, `SET`; stacked statements after comments and inside comments;
Postgres dollar quoting (hidden statements, mismatched tags, tags that look
like parameters), `E''` strings, `U&''` strings and `U&""` identifiers
spelling `pg_authid`; MySQL `/*! */`, `/*!50000 */`, `/*+ */` (also inside a
string), `#` and `-- ` comments followed by tab, vertical tab, form feed,
carriage return, `\x01`, `\x7f`; `2--1`; backslashes before quotes in `'...'`
and `"..."`; `NO_BACKSLASH_ESCAPES`, `ANSI_QUOTES`, `PIPES_AS_CONCAT`,
`HIGH_NOT_PRECEDENCE` set globally on the server; constant `WHERE` clauses in
disguise; functions behind backquotes, a space, `ANY (SELECT ...)`.

Results:

- **Bug, fixed (serious).** Postgres connections did not pin
  `standard_conforming_strings`. On a database or role where it is off
  (common on old installations), the server treats `\'` inside `'...'` as an
  escaped quote, while pg_query (the validator) does not. The statement
  `UPDATE users SET name = 'x\' WHERE id = 1 --' WHERE id > 0` validated as
  a one-row update with `WHERE id = 1`; the dry run showed one row in the
  before image but updated all five, and execution would have committed all
  five (the one-row fingerprint matched and the count matched the dry run's
  five). The same trick hides calls outside the function allowlist
  (`... --' || current_user WHERE id = 1` ran `current_user`). Fix: every
  transaction now pins `standard_conforming_strings` on
  (`internal/connector/postgres/run.go`, `setup`). The new integration
  subtest failed before the fix and passes after it. This is the only
  setting that changes how Postgres tokenizes text; `backslash_quote` can
  only make the server reject text the parser accepted (fail closed).
- **Defence in depth, added.** Both connectors now refuse a dry run in which
  an `UPDATE` or `DELETE` changes a different number of rows than its
  `WHERE` text matched for the before image. Any future disagreement between
  validator and server about the `WHERE` clause then shows up as a refused
  preview instead of a misleading one. Side effect: a table with a trigger
  that skips rows (a `BEFORE` trigger returning `NULL`) can no longer be
  changed through dbairlock; the message says so.
- MySQL: no bypass found. The connector already pins the lexical `sql_mode`
  flags, and the server test confirms it with all of them set globally.
  Where the TiDB parser and MySQL differ (`--` followed by a control
  character other than whitespace is a comment for MySQL but not for TiDB),
  TiDB rejects the text, which fails closed. Nested comments are not nested
  in MySQL and the validator agrees.
- Unicode: neither parser treats any non-ASCII character as whitespace or
  punctuation; look-alikes and invisible characters become part of an
  identifier, so the statement is rejected or names a table that does not
  exist. Limitation: a zero-width character after a table name
  (`users​`) is accepted as part of the name; it fails at the database
  unless such a table exists, but on screen it looks like `users`. Not fixed.
- **Limitation, not fixed.** The constant-`WHERE` rule is syntactic. Any
  condition that references a column passes even when it is always true:
  `coalesce(true, id = 1)`, `lower(name) = lower(name)`, `id >= 0`, MySQL
  `WHERE id`. The backstops are `max_rows`, the row preview and the review
  itself. Deciding tautologies in general is not possible; catching more
  special cases would only move the line.

## Changing the SQL of an approved change

Tried: REST `PATCH /api/changes/{id}` on an approved change with the current,
a stale and a future revision, by the author, an admin and the approver; a
title-only edit; re-submit; refresh-preview; the service `Edit`. Raced, five
rounds: the author's edit, the reviewer's approval with the hashes they saw,
a preview refresh, three manual execution requests and three executor cycles.

Result: no route changes an approved change. In every race exactly one of
the edit and the approval wins; when the approval wins, the executed SQL is
the approved SQL and runs once; when the edit wins, the approval is refused
(`sql_changed`) and nothing runs. The Go race detector found nothing in
these tests.

## Approving one's own change

Tried: the same session, a second session, a rejection, two API tokens of
the author (as a bearer header and as a session cookie on the REST API, through
the service, and over MCP, which has no review tool), a change the author's
agent submitted over MCP, and the author promoted to admin mid-review (old
and new session).

Result: all refused (`self_review`, `agent_review`, or 401 for tokens on the
REST API). Admins have no bypass.

Finding, not fixed: users are keyed by the Google subject, and email is not
unique. If a Workspace admin deletes and recreates a Google account (new
subject, same email), the person gets a second dbairlock user that could
approve changes of the first. This needs Workspace admin rights and leaves
two users with the same email visible in Settings, but the product does not
refuse it. Suggested fix: refuse a first sign-in whose email belongs to an
active user with a different subject until an admin removes the old one.

## Executing twice

Tried: twelve concurrent manual triggers (REST, as author and admin, and the
service) and executor cycles on one change; a crash after the claim (with the
statement actually committed on the target) followed by three restarts with
recovery and executor cycles; manual execute and retry of the resulting
failed change; the clock moved back 48 hours and forward 96 after
execution; an unreachable target retried across a clock step back; the
clock jumping past the approval lifetime between the executor's expiry pass
and its claim.

Results:

- No double execution by any route: one claim, one `execution.started`, one
  `change.execute_requested`, the target changed once; an interrupted change
  is failed exactly once and never runs again.
- **Bug, fixed (minor).** `ClaimNext` did not check the approval lifetime;
  it relied on the expiry pass that runs just before it in each cycle. A
  change whose lifetime ended between the two (or after a clock jump) could
  be claimed and run after it expired. `claim` now skips an expired change
  (`internal/changes/execution.go`); the next cycle expires it.
- **Bug, fixed.** A preview time later than the server's clock (the clock
  stepped back) counted as fresh until the clock caught up, however old the
  preview really was. It is now stale (`checkApprovable` in
  `internal/changes/review.go`).
- **Bug, fixed (availability).** After the clock stepped back, a preview
  refresh was refused as "too soon" until the clock caught up with the last
  refresh, so a change could not be approved meanwhile. A wait longer than
  the refresh interval is now treated as a clock change and the refresh is
  allowed (`RefreshPreview` in `internal/changes/write.go`).
- Limitation, not fixed: if the clock steps back past a change's creation
  time, every write to that change fails the store's
  `updated_at >= created_at` check with an internal error until the clock
  catches up. This fails closed (nothing runs) but the message is generic.

## Reading a secret

`TestSecretLeak` sets known values for every kind of secret: the OpenID
client secret (sign-in only works if it reaches the fake provider), the
webhook URL (and a rejected one in a failed update), target passwords (a
wrong one that fails to authenticate, its replacement that works, a
rejected one on an invalid connection, one in an unsaved connection test),
and three API tokens after creation. Then it walks every GET route with real
ids as an admin and a member, every route with unknown ids and empty bodies
(error responses), sends a test notification and lets a delivery fail, runs
a change through review and execution, calls every MCP tool (including
failing calls and a preview on an unreachable connection), sends bad
credentials to `/mcp`, forces two panics while a secret is being handled
(the panic value itself carries a `secrets.Value`), exports the audit log,
and finally checks every response, every MCP response body, the debug log,
every audit payload and the raw database files for any of the values.

Result: no leak found. The test needs Docker for the live target; without
Docker it still runs everything except the live-target parts and says so in
its log.

## Not verified

- The hostile server tests run on the newest supported versions only
  (Postgres 18, MySQL 9.7), not on Postgres 14 or MySQL 8.4.
  `set_config('standard_conforming_strings', ...)` exists on every supported
  Postgres version.
- MariaDB and TiDB as targets were not tried (not supported).
- The Playwright suite was not run by this workstream; no UI was changed.
