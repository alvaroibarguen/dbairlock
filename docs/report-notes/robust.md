# Phase 11 robustness notes (workstream `robust`)

Machine: 16-core WSL2 (Linux 6.6), Docker Desktop 29.7, Go 1.27.3. Other
Phase 11 workstreams were running builds and tests at the same time, so
timings are on a loaded machine.

## Fuzzing (30 minutes each, run concurrently)

| Target | Package | Executions | Result |
|---|---|---|---|
| `FuzzValidate` (Postgres validator) | `internal/connector/postgres` | 19.1 M | no failure |
| `FuzzValidate` (MySQL validator) | `internal/connector/mysql` | 18.6 M | no failure |
| `FuzzCanonicalPayload` (new) | `internal/audit` | 15.0 M | no failure |
| `FuzzPreparePayload` (new) | `internal/audit` | 9.6 M | no failure |
| `FuzzDefinitionParse` (new, every setting) | `internal/settings` | 15.8 M | no failure |
| `FuzzAPIRequests` (new, 36 API routes) | `internal/app` | 2.8 M | no failure |

What the new targets check:

- Canonical audit JSON: never panics; output is valid JSON; canonicalization
  is idempotent (the verifier re-canonicalizes stored payloads); the
  recorder's stored payload is exactly what the verifier hashes; the
  canonical record is valid JSON and its chain hash is deterministic, for
  any actor strings including invalid UTF-8.
- Settings: every registered setting, arbitrary JSON. A value is either
  rejected with a user-facing message, or accepted in a normalized form
  that is accepted unchanged when parsed again (the stored value is parsed
  on every start, so a value that validates once but not twice would stop
  the instance from booting) and has the registered type.
- API decoders: arbitrary bodies, path parameters and query strings sent as
  a signed-in admin to 36 routes (sign-out, Google redirects, connection
  tests and the notification test are excluded so the fuzzer never dials
  an address it made up). Any 5xx fails, and so does losing the session.

No crashers were found, so no corpus entries were committed. The fuzz
targets run their seed corpora as part of `go test`.

Limitation: the API fuzzer cannot reach handlers that need an existing
change or a working connection (no target database in the fuzz harness),
so it exercises their decoding and the "not found" paths only.

## Fault tests

`internal/app/fault_test.go` (integration tests; Postgres and MySQL in
testcontainers through a fault-injecting TCP proxy in
`changes_targets_test.go`, which can now also stall all traffic and lose
the reply to a chosen message). Existing tests already covered an
unreachable target (`TestChangeOutage`: deferred, retried with backoff,
expires) and a connection killed while a statement waits (failed, never
retried).

| Fault | Behaviour verified | Result |
|---|---|---|
| Target stops answering before the transaction (stalled network) | Begin times out (about 13 s), the change is deferred, not failed; it executes once the target answers again | pass, both connectors |
| Target stops answering inside the transaction | Failed (timeout), never retried; the target rolls back when it notices the client is gone | pass, both |
| Statement slower than the lock timeout | Failed with code `timeout`, never retried, nothing changed | pass, both |
| COMMIT reaches the target but its reply is lost | Failed with "outcome unknown, verify manually"; the target did commit; never retried (one `execution.started`) | pass, both |
| Disk full on the data directory, on writes from the API | Submissions, approvals and settings changes fail; nothing is half-written (no change rows, reviews or audit events); reads and `/readyz` keep working; everything works once there is room | pass |
| Disk full after the target committed | The executor keeps retrying the store write (never the change); once there is room the change is recorded executed | pass |
| Disk full, then the process stops | The change stays `executing` and is marked failed (interrupted) at the next start, never re-run | pass |
| Database file read-only (mode 0400) | **Bug found and fixed**, see below | pass |
| Data directory read-only | Refuses to start with `store: the database cannot be written: ... (check that the file, the data directory and the filesystem are writable by this user)` | pass |
| Database turns read-only while running | Writes fail, nothing changes, the audit chain still verifies | pass |
| Master key file missing after setup | Refuses to start: "master.key is missing, but this database holds secrets encrypted with a master key; restore master.key from your backup"; does not create a new key | pass |
| Master key file unreadable | Refuses to start, naming the file | pass |

How "disk full" is simulated: a real full filesystem needs a size-limited
mount, which needs root. Capping SQLite's `max_page_count` is not
deterministic (small rows still fit in the free space of existing pages).
The app-level tests therefore install triggers that abort every write with
SQLite's disk-full message, which is what the application sees on a full
disk. `TestStoreFullRollsBack` (`internal/store`) separately checks a real
`SQLITE_FULL` from `max_page_count`: the transaction fails as a whole,
nothing remains, and writes work again once there is room. A test on a real
full tmpfs was not run (needs root).

### Bug: a read-only database file was opened read-only, silently

With `dbairlock.db` at mode 0400 (owned by the service user), SQLite opened
it read-only without an error. The instance then started, reported ready,
and failed every write ("attempt to write a readonly database"). The store
did `chmod 0600` after opening, which does not make an already-open
read-only connection writable.

Fix (`internal/store/store.go`): restore mode 0600 before opening an
existing file, and after opening, check that the writer connection can
write (write the header field `user_version` back unchanged in a
rolled-back transaction). A database that cannot be written (read-only
file not owned by the user, read-only directory or filesystem) now refuses
to start with `store.ErrReadOnly` and a message saying what to check.

### Flaky conformance test (reported by the coordinator)

`TestConformance/mysql9/database/unavailable/connection_killed_after_begin`
failed once under load with "a connection lost after BEGIN must not be
ErrUnavailable". Cause: the test waited for the run's label to appear and
then killed the session. MySQL's label is a connection attribute, visible
as soon as the dedicated connection is established, before BEGIN. A kill
landing in that window is correctly `ErrUnavailable` (nothing ran, the
change would be retried), so the product was right and the test's timing
was wrong. Postgres sets its label inside the transaction, so it was not
affected. The test now also waits, on targets that implement the new
`connectortest.TransactionTarget`, until the session is inside its
transaction (MySQL: the labelled session is running its `SELECT ... FOR
UPDATE`, which the connector issues only after BEGIN, per
`information_schema.processlist`). `information_schema.innodb_trx` was
tried first and was not reliable: a session waiting for a row lock during
optimization ("statistics") is sometimes not listed there yet.
Product classification was reviewed: in both connectors
every error before `BeginTx` returns is `ErrUnavailable` and every error
after it is final.

Observed, not changed: on MySQL, BEGIN and COMMIT are bounded only by the
executor's overall deadline for the change (30 s + statements + 15 s), not
by a per-step deadline, because `database/sql` takes no context for
COMMIT. A COMMIT on a hung network can therefore take up to about a minute
to fail with default settings (Postgres: statement timeout + 3 s). It still
fails as "outcome unknown" and is never retried.

### Also fixed

The MySQL test container helper skipped all MySQL integration tests ("Docker
is not available: connection refused") when Docker Desktop's port
forwarder lagged behind the server's ready log line. It now waits for the
server to answer before creating the test user, instead of skipping.

## Load sanity

`make load` (`internal/app/load_test.go`, build tag `load`): 50,000 changes
(one statement each; 89% executed, 3% failed, 2% pending review, the rest
approved, rejected, cancelled and drafts; 20 authors, 3 connections, most
approved by one reviewer) and 500,000 audit events written through the real
recorder (genuine hash chain) over a simulated year. Database size about
328 MB. Each request runs 5 times through the full HTTP handler; the
budget is 250 ms median.

Before the fixes, the first page of the changes list took **15.4 s**
(median): the "needs my review" count, computed on every list request, did
a correlated lookup per change in review that SQLite planned on
`reviews (reviewer_id)`, walking all 45,000 reviews of the active reviewer
for each of about 1,000 pending changes. The audit page's filters were
hidden from the planner by its `(? = '' OR col = ?)` query, so a filtered
page (for example one change's timeline) scanned the whole log through the
`occurred_at` index and sorted it (about 165 ms per request, growing with
the log).

Fixes:

- Migration `00007_load_indexes.sql`: `reviews (change_id, reviewer_id)` and
  `audit_events (actor_email, seq)`.
- The audit page query is built from only the filters in use
  (`internal/audit/repository.go`, still fully parameterized), and an action
  prefix is a range on the action index instead of `substr()`.

After (seeding: 8 s for the changes, 48 s for the audit events):

| Request | Median | Max |
|---|---|---|
| changes: all, first page | 3.7 ms | 5.4 ms |
| changes: all, page 100 | 2.6 ms | 3.3 ms |
| changes: needs my review | 3.4 ms | 4.4 ms |
| changes: mine | 2.7 ms | 3.5 ms |
| changes: status=pending_review | 3.3 ms | 4.0 ms |
| changes: status=failed | 2.9 ms | 3.4 ms |
| changes: open | 3.0 ms | 3.4 ms |
| changes: by connection | 3.0 ms | 3.9 ms |
| changes: by author | 2.8 ms | 3.0 ms |
| change: one | 0.16 ms | 0.39 ms |
| change: timeline | 0.09 ms | 0.20 ms |
| audit: first page | 0.32 ms | 0.51 ms |
| audit: page 1000 | 0.29 ms | 0.75 ms |
| audit: action=change.approved (50,000 matches) | 94 ms | 98 ms |
| audit: action prefix `setting.` | 5.9 ms | 6.3 ms |
| audit: actor email | 60 ms | 77 ms |
| audit: one change (subject) | 0.07 ms | 0.21 ms |
| audit: via=cli | 2.3 ms | 2.4 ms |
| audit: one day | 2.2 ms | 3.1 ms |
| audit: one day + action prefix | 0.8 ms | 0.9 ms |
| System page | 3.3 ms | 3.8 ms |
| `dbairlock audit verify` (500,043 events, `app.VerifyAudit`) | 3.0 s | |

The slowest remaining filters (an action or actor matching tens of
thousands of events) sort their matches by sequence; they grow linearly
with the number of matching events, about 2 ms per thousand.

## Race detector

`make test` now runs `go test -race ./...` (unit and integration tests).
`make test-all` runs `make test`, then Playwright against a race-enabled
binary (`bin/dbairlock-race`, `GORACE=halt_on_error=1`); the e2e global
teardown fails the run if the server's log contains a data race report.
`e2e/global-setup.ts` takes the binary from `DBAIRLOCK_BIN` (test tooling
only; the server itself still reads no environment variables).

## "Why not configurable" comments

Added next to the constants that `docs/configuration.md` lists as fixed:
change limits and the preview refresh interval (`changes`), session cap and
sign-in flow lifetime and the sign-in rate limit (`identity`), setup
session and recovery code lifetimes and the setup-code rate limit
(`setup`), the executor interval, the schema cache and connection test
timeout (`targets`), connector pool and connect limits (both connectors,
now named constants), cell display truncation (`connector`), audit page
size and payload string limit (`audit`), and the `/mcp` body limit. The
notification limits, the API body limit, the shutdown grace and SQLite's
temp store already had their reasons.

## Not verified

- A real full filesystem (needs a size-limited mount) and a read-only
  mount; both are simulated as described above.
- Load beyond the plan's sizes, and concurrent load (requests were timed one
  at a time).
- Merge note: this branch adds migration `00007`; if another branch also
  adds one, renumber.
