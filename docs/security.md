# Security

This file records what dbairlock protects against, the properties it relies
on, and their limits.

## Threat model

dbairlock exists so that nobody on a team needs write credentials for
production databases, and so that every row change is seen by a second
person before it runs and is recorded afterwards.

**It protects against:**

- **Unreviewed changes.** Every change needs approvals from people other than
  its author (one by default, up to five per connection). Admins have no
  bypass. Agents, and anything else using an API token, can propose but never
  approve or run.
- **Approving something other than what runs.** An approval is bound to one
  SQL hash and one preview hash, and needs a preview from the last 5 minutes.
  At execution the matching rows are locked and fingerprinted again; if
  different rows match, the values being overwritten changed, or the row
  count differs, the whole change rolls back.
- **Statements broader than a row change.** Only single-table `INSERT`,
  `UPDATE` and `DELETE` pass the validator: no DDL, no multi-statement text,
  no `UPDATE`/`DELETE` without a real `WHERE`, no joins, CTEs, `RETURNING`,
  upserts, executable comments or functions outside an allowlist. Anything
  the parser cannot classify is rejected.
- **Credential sprawl.** Target passwords live encrypted in dbairlock and can
  be set, never read back, through any interface.
- **Silent history edits.** The audit log is hash-chained (see below).

**It does not protect against:**

- **Someone with access to the host or the data volume.** They can read the
  database and `master.key`, decrypt every target password, and rewrite the
  audit log (detectably, unless they also recompute the chain).
- **A database user with more rights than it needs.** The grant is the real
  boundary (next section).
- **Two colluding people.** An author and an approver acting together can
  run any row change the database user allows. The audit log records who did
  it.
- **A compromised Google account** of an allowed domain, beyond what Google's
  own controls stop. Session length and Workspace policies are your levers;
  removing a user ends their sessions and tokens at once.
- **Reads.** Every signed-in person can see row previews (below).
- **Availability.** dbairlock is one instance; if it is down, changes wait.

## The role of database grants

Give each connection a database user with only `SELECT`, `INSERT`, `UPDATE`
and `DELETE` on the tables it may change ([self-hosting.md](self-hosting.md#a-least-privilege-database-user)
has the statements). The validator is a second line, not the first: a
parser bug or an unexpected dialect feature must not be enough to drop a
table, and with this grant it is not. **Test connection** reports privileges
beyond these four, with the statements that remove them.

## What the dry run does on the target

Previews are real work on the production database, inside a transaction
that is always rolled back:

1. Begin, and set the statement timeout and lock timeout (settings
   `limits.statement_timeout_ms`, default 10 s, and `limits.lock_timeout_ms`,
   default 3 s).
2. For `UPDATE` and `DELETE`, select the matching rows with `FOR UPDATE`, up
   to `max_rows + 1`, so they are **locked** for the duration of the dry
   run. Concurrent writers to those rows wait.
3. Run the statement itself and count the rows it affected; for an `UPDATE`
   on a table with a primary key, re-read those rows for the after image.
4. Roll back.

Consequences to be aware of:

- **Triggers fire** during the dry run, and their effects are rolled back
  with it, except effects outside the transaction (for example a trigger
  that calls out over the network, or `dblink`).
- **Sequences advance.** An `INSERT` that draws ids from a sequence consumes
  them even though it is rolled back, leaving gaps.
- Each preview, and each **Update preview** on a review page, repeats this.
  A preview of one change can be refreshed at most every 10 seconds, and MCP
  previews are rate limited per token (30 per hour by default).
- A MySQL table with a non-transactional engine (MyISAM, for example) is
  refused, since its changes could not be rolled back.

## Row previews are visible to every signed-in user

Any signed-in person can submit a statement against any table of a
configured database and see up to `max_rows` matching rows in its preview,
and can see the previews of other people's changes. In effect dbairlock
grants its users read access to every table its database users can read.
This is accepted for this version and stated on the add-connection form.
Grant the dbairlock user only tables your whole team may read, and turn off
**Store row images** on a connection to keep only column names and counts
with each change. Agents never receive row data.

## Safety rules and the tests that prove them

Each safety rule of the plan (section 5) and the tests that fail if it is
broken. Paths are relative to `internal/`. "Corpus" means a validation case
run by `TestValidate` (the shared corpus in `connector/connectortest/validation.go`
plus each connector's own in `connector/{postgres,mysql}/validate_test.go`);
"hostile" means a case in `TestHostileValidation`
(`connector/{postgres,mysql}/hostile_test.go`), the Phase 11 attempts to slip
SQL past validation. Integration tests need Docker; they start their own
Postgres and MySQL containers.

### Statement validation (5.1)

| Rule | Tests |
|---|---|
| Only single-table `INSERT ... VALUES`, `UPDATE ... WHERE`, `DELETE ... WHERE` are accepted, and an accepted statement has one kind, one table and its `WHERE` text | Corpus "update one row", "delete", "insert several rows" and the other accepted cases (`connectortest.RunValidation` checks kind, table, `WHERE` text and that the text is unchanged); `FuzzValidate` in both connectors; hostile accepted cases (nested comments, dollar quoting, keyword-named identifiers, comment rules) |
| Other statement types are rejected: DDL, `TRUNCATE`, `GRANT`, `SET`, `CALL`, `COPY`, `LOCK`, transaction control, `SELECT` | Corpus "select", "create table", "drop table", "alter table", "truncate", "grant", "begin", "commit", "rollback", "set", "call"; "pg: copy", "pg: lock", "pg: merge", "pg: do block", "pg: explain analyze", "pg: prepare", "pg: set search_path"; MySQL "my: ..." cases; hostile "explain wraps a delete", "client delimiter command" |
| One statement per text | Corpus "two statements", "smuggled second statement", "pg: stacked semicolons second statement", "my: stacked semicolons second statement"; hostile "stacked after a comment line", "select then delete", "two inserts", "fullwidth semicolon", "stacked after a hash comment line". The server enforces it too: Postgres statements go through the extended protocol, MySQL connections have multi-statements off |
| `UPDATE`/`DELETE` need a `WHERE` that is not constant-true | Corpus "update without where", "delete without where", "where 1=1", "where true", "where or true", "where column equals itself", "pg: uncorrelated exists"; hostile "null is null", "scalar subquery true", "pipes are OR", "string that is true", "hex literal that is true", "homoglyph WHERE becomes alias" |
| No joins or multiple tables | Corpus "pg: update from", "pg: delete using", MySQL multi-table cases; hostile "set on another table" |
| No `INSERT ... SELECT`, `ON CONFLICT`, `ON DUPLICATE KEY UPDATE`, `REPLACE`, `RETURNING` | Corpus "insert select", "pg: on conflict ...", "pg: ... returning", MySQL upsert and `REPLACE` cases; hostile "insert row constructor", "insert table", "delete inside a value" |
| No `WITH` | Corpus "with", "pg: with in subquery"; hostile "data-modifying CTE in a value" |
| Functions only from the allowlist, which admins can override per connection | Corpus "function not allowed in set/where/values/subquery", "empty allowlist", "policy allows a function", "pg: pg_sleep", "pg: set_config", "pg: dblink"; hostile "current_setting", "function in ANY subquery", "backquoted function"; `app/connections_test.go` `TestConnectionsAPI` (override saved); `app/safety_test.go` `TestSafetyRevalidatedAtExecution` (the policy at execution time applies) |
| No system schemas | Corpus "system schema target", "system schema in subquery", "pg: pg_catalog target", "pg: unqualified catalog target"; `TestValidateDefaultSchema`, `TestValidateSchemaIsTheDatabase`; hostile "uppercase catalog schema", "unicode-escaped catalog table", "uppercase system database", "quoted system database" |
| On MySQL, no database other than the connection's | `connector/mysql/validate_test.go` `TestValidateSchemaIsTheDatabase`; hostile "other database by case", "set qualified with another database" |
| Unqualified table names always resolve to the connection's default schema | `connector/postgres/postgres_test.go` `TestPostgresSpecific/search path is pinned to the default schema`; `TestValidateDefaultSchema` |
| No executable comments (MySQL `/*! */`) | `connector/mysql/validate_test.go` `TestExecutableComment`, `TestValidateRejectionPositions`; hostile "executable comment", "versioned executable comment", "optimizer hint", "executable comment opener in a string"; `TestMySQLSpecific/statement fields must match the SQL` |
| Subqueries in `WHERE` are plain `SELECT`s under the same rules | Corpus "subquery", "correlated exists", "function not allowed in subquery", "pg: locking subquery", "pg: union subquery", "pg: catalog in subquery" |
| The server runs the text exactly as the validator read it | `connector/postgres/postgres_test.go` `TestPostgresSpecific/hostile text on a server with standard_conforming_strings off`; `connector/mysql/mysql_test.go` `TestMySQLSpecific/lexical sql_mode is pinned` and `/hostile text on a server with every lexical sql_mode`; `TestPostgresSpecific/statement fields must match the SQL`, `TestMySQLSpecific/statement fields must match the SQL` |

### Secret store (5.2)

| Rule | Tests |
|---|---|
| Secrets can be set, replaced and cleared, their status seen, and their value never read back by any interface | `app/secretleak_test.go` `TestSecretLeak` (the secret leak test: every route, error responses, every MCP tool, debug logs, the audit log and export, the database files, forced panics); `app/secrets_test.go` `TestNoInterfaceRevealsASecret`; `httpapi/settings_test.go` `TestPatchSettingsSecretSemantics`; `targets/targets_test.go` `TestCreateUsesDefaultsAndNeverExposesThePassword`; `app/connections_test.go` `TestConnectionsAPI`; `app/identity_test.go` `TestAPITokens` (tokens shown once) |
| Secret fields are write-only in the API contract | `httpapi/contract_test.go` `TestNoSecretInAnyResponseSchema`, `TestSecretPropertiesAreWriteOnly`, `TestSecretCheckCatchesALeak` |
| Only targets, identity and notify can decrypt | `archtest/archtest_test.go` `TestSecretsOpenerHolders`; the depguard rules in `make lint` |
| AES-256-GCM with the secret's identity as additional data | `secrets/secrets_test.go` `TestSetOpenRoundTrip`, `TestCiphertextMovedToAnotherRowFailsToOpen`, the start-up and rotation tests |
| `secrets.Value` prints, marshals and logs as `[redacted]` | `secrets/secrets_test.go` `TestValueRedaction`; `TestSecretLeak` (a panic carrying a `secrets.Value`) |
| Setting, replacing and clearing are audited without the value | `secrets/secrets_test.go` `TestAuditRejectsValueInPayload`; `audit/audit_test.go` `TestRecordRejectsSecretsAnywhereInPayload`; `TestNoInterfaceRevealsASecret` |

### Target credentials (5.3)

| Rule | Tests |
|---|---|
| Adding a database runs a privilege check that warns about DDL or superuser rights | `connector/{postgres,mysql}/conformance_test.go` `TestConformance/*/privileges`; `connector/mysql/values_test.go` `TestAnalyzeGrants`; `targets/targets_test.go` `TestTestReportsWarnings`; `app/connections_test.go` `TestMultipleConnections` |

### Dry run at submission (5.4)

| Rule | Tests |
|---|---|
| All statements run in one transaction with timeouts, then always roll back | `TestConformance/*/dry run/*` (including "lock timeout", "statement timeout", "second statement depends on the first") |
| Before image with `FOR UPDATE`, fingerprint, expected rows, after image | `TestConformance/*/dry run`, `/fingerprint`; `connector/fingerprint_test.go`; `TestPostgresSpecific/update after image`, `TestMySQLSpecific/update after image` |
| The rows in the preview are the rows the statement changes | `TestPostgresSpecific/hostile text ...`, `TestMySQLSpecific/hostile text ...` (each accepted hostile statement: before image = rows affected) |
| More than `max_rows`, zero rows, or a non-transactional table is refused | `TestConformance/*/dry run/over max rows`, `/zero rows`; `TestMySQLSpecific/non-transactional table` |
| Agents receive counts only | `app/mcp_test.go` `TestMCP`; `app/changes_test.go` `TestChangeAgentsAndAPI` |
| With row images off, only column names and counts are kept | `app/safety_test.go` `TestSafetyRowImagesOff` |
| A duplicate open submission returns the existing change | `app/changes_test.go` `TestChangeDuplicates` |

### Review (5.5)

| Rule | Tests |
|---|---|
| The author cannot review; a change submitted with a token is its owner's | `TestChangeReview/self-review refused`; `app/safety_test.go` `TestSafetySelfApproval` (same session, second session, two API tokens as bearer and as cookie, MCP, an agent's change, the author promoted to admin mid-review) |
| Agents cannot review | `TestChangeAgentsAndAPI`; `TestMCP/tools`; `TestSafetySelfApproval` |
| Removed users cannot review and their approvals stop counting | `TestChangeReview/review by a removed user refused`; `TestChangeUserRemoval/*`; `app/identity_test.go` `TestRemovedUser` |
| Editing the SQL voids earlier reviews; an approved change cannot be edited | `TestChangeReview/edit invalidates approval`; `TestChangeConcurrency/two edits from the same revision: one wins`; `app/safety_test.go` `TestSafetyApprovedChangeIsFrozen` (REST edits with current, stale and future revisions, by author, admin and approver; resubmit; refresh), `TestSafetyEditApproveExecuteRace` (edit, approve, refresh, execute requests and executor cycles racing) |
| Approval needs a preview from the last 5 minutes | `TestChangeReview/approval needs a fresh preview`; `TestSafetyPreviewFreshnessAndClock` (clock forward and back) |
| Updating the preview keeps or voids approvals; a failed update blocks approval | `TestChangeReview/refresh keeps or voids approvals`, `/a failed refresh blocks approval but not rejection` |
| One rejection rejects; the required number of distinct approvals approves | `TestChangeReview/rejection`, `/two approvals required`; `TestChangeConcurrency/two reviewers approving at once make one transition` |
| Admins have no bypass | `TestSafetySelfApproval`; `TestChangeReview/self-review refused` (the author there is an admin) |

### Execution (5.6)

| Rule | Tests |
|---|---|
| The claim is committed before the target is touched; one change per target at a time | `TestChangeConcurrency/two approved changes on one database run one after the other`; `targets/targets_test.go` `TestRunMutexIsPerConnection`; `TestSafetyDoubleExecution/concurrent manual triggers and executor cycles` |
| Rows are locked and fingerprinted again; any drift rolls the whole change back | `TestChangeDrift/*`; `TestConformance/*/drift`, `/execute` |
| A change found executing at start-up is failed and never re-run | `TestChangeCrashRecovery`; `TestSafetyDoubleExecution/restart after the claim never re-runs, however often` |
| An unreachable target defers with backoff until the approval expires; errors after `BEGIN` are final | `TestChangeOutage/*`; `changes/status_test.go` `TestBackoff`; `TestConformance/*/unavailable`; `TestSafetyDoubleExecution/an unreachable target retried across clock jumps runs once`, `/a change past its approval lifetime is never claimed` |
| Every execution is labelled with its change number | `TestConformance/*/run label`; `TestMySQLSpecific/program name on pooled connections` |
| Statements are validated again with the current policy | `TestSafetyRevalidatedAtExecution` |
| A failed change is terminal | `TestChangeDrift` ("a failed change is terminal"); `TestSafetyDoubleExecution/restart ...` (execute and retry refused) |

### Sequences (migration 00009)

A sequence runs 2 to 10 changes in order across connections. Each step is a
change with every rule above; the sequence adds ordering, never a bypass.
Each step is atomic on its own database, and the sequence as a whole is
not: steps already run are not undone when a later step stops.

| Rule | Tests |
|---|---|
| Only the current step can be reviewed or previewed; later steps wait with no preview and cannot be submitted by hand | `app/sequences_test.go` `TestSequenceStateMachine/gated review, advance and complete` |
| The next step is dry-run after the previous one executed, and needs a fresh preview and the normal approvals; a failed preview blocks approval | same, plus `/a step depends on the one before`, `/the next step's preview fails`, `/the database is unreachable when the next step is previewed`; `TestSequenceAcrossDatabases/completes` (Postgres, MySQL, Postgres) |
| A step that fails, is rejected, expires or is cancelled (by the author, an admin, or the author's removal) stops the sequence in the same transaction; other open steps are cancelled; executed steps stay applied | `TestSequenceStateMachine/stops when ...` (six cases); `TestSequenceAcrossDatabases/a failure stops it and earlier steps stay applied`; `changes/sequence_test.go` `TestStopCodes` |
| Every step is validated at submission (dialect, one table, a real `WHERE`); step 1 is dry-run; nothing is created on a problem; retries return the running sequence | `TestSequenceStateMachine/submission checks every step and creates nothing on a problem` |
| Agents submit and follow sequences but never approve or see rows, and see only their owner's | `TestSequenceStateMachine/agents submit and follow but never see rows or approve`; `TestMCP/sequences`; `TestSecretLeak` |
| Sequence events are audited in the step's transaction; the chain verifies | every sequence test verifies the chain on cleanup |

### Audit (5.7)

| Rule | Tests |
|---|---|
| One `Record`, in the state change's transaction | `audit/audit_test.go` `TestFailedBusinessWriteLeavesNoAuditRow`, `TestFailedAuditWriteRollsBackBusinessWrite`, `TestSuccessfulWriteCommitsBoth`; every `app` changes test verifies the chain on cleanup |
| Hash chain detects edits and deletions | `TestTamperedRowIsReported`, `TestEditedHashIsReported`, `TestDeletedRowIsReported`, `TestDeletedTailIsReported`, `TestDeletedFirstRowIsReported`, `audit/chain_test.go`, `audit/canonical_test.go` |
| Triggers reject `UPDATE` and `DELETE` | `TestUpdateAndDeleteAreRejected` |
| `audit verify` and the Settings button | `app/store_test.go` `TestVerifyAudit`; `audit/export_test.go` `TestCheckerReportsFirstBrokenLink`; `app/audit_test.go` `TestAuditAPI` |

### Web security (5.8)

| Rule | Tests |
|---|---|
| Session cookie attributes; only its hash stored | `app/identity_test.go` `TestSetupEndToEnd`, `TestSessions` |
| Cross-origin mutating requests rejected | `TestCrossSitePostIsRejected`, `app/secrets_test.go` `TestCrossOriginPatchIsRejected` |
| ID tokens verified (signature, audience, `email_verified`, `hd`) | `TestSignInChecks` |
| API tokens shown once, hash stored | `TestAPITokens`; `TestSecretLeak` |
| OAuth: loopback redirects only, PKCE S256, single-use codes bound to client, redirect URI and resource, rotating refresh tokens with reuse detection, revocation, the off switch, user removal | `identity/oauthcheck_test.go` (`TestLoopbackRedirectURIs`, `TestRedirectMatching`, `TestPKCE`, `TestResourceIndicators`); `app/oauth_test.go` (`TestOAuthEndToEnd` with the go-sdk's OAuth client, `TestOAuthDiscovery`, `TestOAuthRegistration`, `TestOAuthAuthorization`, `TestOAuthCodes`, `TestOAuthResourceServer`); `TestMCP/an_agent_signed_in_with_OAuth_acts_like_an_API_token`; `TestSecretLeak`; `web/e2e/oauth.spec.ts` |
| SQL, titles and reasons rendered as plain text | `web/src/app/audit.test.tsx` (payload as plain text); see the web security section for the full check |

### Fixed by this audit

- **Postgres `standard_conforming_strings`.** A database or role with this
  setting off made the server read a backslash in `'...'` as an escape while
  the validator's parser did not. `UPDATE users SET name = 'x\' WHERE id = 1 --' WHERE id > 0`
  validated as a one-row update but changed every row, with a one-row
  preview, and functions outside the allowlist could be hidden the same
  way. Every transaction now pins the setting on.
- **Preview rows and changed rows must agree.** A dry run now fails when an
  `UPDATE` or `DELETE` changes a different number of rows than its `WHERE`
  text matched for the preview, so any future gap between the validator and
  the server shows up as a refused preview rather than a misleading one. A
  table whose trigger skips rows can no longer be changed through dbairlock.
- **Clock changes.** A preview whose time is later than the server's clock
  (the clock stepped back) no longer counts as fresh; a refresh is not
  blocked until the clock catches up; and the executor never claims a
  change whose approval lifetime has ended, even between its expiry pass and
  its claim.

## Sign-in and sessions

- Google OpenID Connect with PKCE, a nonce and a signed, short-lived flow
  cookie. ID tokens are verified for signature, audience and expiry,
  `email_verified` must be true, and the Workspace domain (`hd`) must be
  allowed. Personal Google accounts have no `hd` and are refused.
- Session cookies hold 32 random bytes; only their hash is stored. They are
  `HttpOnly`, `Secure`, `SameSite=Lax` and use the `__Host-` prefix. A
  session ends after the idle length (`auth.session_hours`, default 12) and
  at most after 30 days.
- API tokens (`dbal_pat_...`) are shown once; only their hash is stored.
  Removing a user ends their sessions, revokes their tokens and OAuth
  connected apps, and cancels their open changes; approvals they gave stop
  counting.
- The setup code and recovery codes are one-time, short-lived and rate
  limited. Setup ends at the first admin's sign-in, for good; afterwards only
  `dbairlock recover`, run on the host, reopens the sign-in settings.
- SQL, titles, reasons and comments are rendered as plain text everywhere;
  agent-written content is never interpreted as HTML or markdown.

## OAuth sign-in for agents

MCP clients can sign in with a browser instead of a pasted API token
([agents.md](agents.md#how-oauth-works-here)). dbairlock is the
authorization server of its own `/mcp` endpoint and nothing else; the
person authenticates with their existing Google sign-in session. An access
token gives exactly what an API token of that person gives (the MCP tools:
no approvals, no row data, own changes only) and nothing more.

**Properties.**

- Codes (`dbal_oac_`), access tokens (`dbal_oat_`) and refresh tokens
  (`dbal_ort_`) carry 256 random bits and are stored as SHA-256 hashes only,
  like API tokens. They appear in exactly one response each (the
  redirect URL the consent page sends the browser to, and the token
  response) and never in logs, audit payloads or other responses
  (`TestSecretLeak`).
- PKCE `S256` is required (`plain` is refused); a code is bound to the
  client, the redirect URI, the challenge, the person and the resource,
  lives 60 seconds and works once. A second redemption revokes everything
  issued from it.
- Access tokens live one hour and are bound to the MCP endpoint (RFC 8707):
  the token endpoint and the resource server both refuse another resource,
  and a change of `instance.public_url` invalidates every token.
- Refresh tokens rotate on every use; a used one presented again revokes
  the grant (both the thief's and the client's tokens die). One left unused
  for 30 days expires.
- Fail closed: with `mcp.oauth_enabled` off or no public URL, the metadata
  documents answer 404, the protocol endpoints refuse, and every OAuth
  access token is rejected. API tokens are unaffected.
- Every state change is audited in its transaction: `oauth.client_registered`,
  `oauth.clients_pruned`, `oauth.grant_approved`, `oauth.token_issued`,
  `oauth.grant_revoked` (with the reason: revoked in the dashboard, by the
  client, superseded by a new approval, refresh-token or code reuse, user
  removed). Using an access token is not audited (like API tokens), but every
  tool call is, as `mcp.tool_called` with the agent's name.

**Threats and what limits them.**

- **Abuse of dynamic client registration.** Registration is open by design
  (RFC 7591), so anyone who can reach the instance can register clients.
  Registration grants nothing: a client acts only after a signed-in person
  approves it. It is rate limited per address (10, then one a minute);
  at most 200 never-approved clients may exist at once, and those older
  than a day are deleted. Names are limited to 100 characters without
  control characters; logos and URIs a client sends are ignored, never
  shown.
- **Consent phishing.** Someone sends a person a link to the authorization
  endpoint for a client named like a trusted one. The consent page marks
  the name **Unverified**, shows the exact host and port the sign-in goes
  to, says what the agent can and cannot do, and tells the person to allow
  only a sign-in they just started. Because every redirect URI is a
  loopback address, an approval delivers the code to a program on the
  person's own computer, not to the attacker; the attacker would also need
  the PKCE verifier. The decision is a same-origin `POST` behind
  cross-origin protection, so another site cannot approve on the person's
  behalf, and the page cannot be framed (`frame-ancestors 'none'`).
- **Loopback redirect.** Only `http://127.0.0.1`, `http://localhost` and
  `http://[::1]` are accepted, with plain paths: no user info, query,
  fragment, percent-encoding, dot segments or look-alike hosts
  (`localhost.evil.com`, `127.0.0.1.nip.io`, integer and hex forms). The
  host and path must match the registration exactly; only the port may
  vary (RFC 8252). Errors about an untrusted client or redirect URI are
  shown on the consent page and never redirected. The residual risk is
  another program on the same computer listening on the port first; it
  still lacks the PKCE verifier of the real client.
- **Token theft.** A stolen access token works for at most an hour, only at
  `/mcp`, and only as the person's agent; a stolen refresh token is
  detected the first time both parties use it. People see and disconnect
  their apps in **Settings → API tokens → Connected apps**; admins see and
  disconnect everyone's. Removing a person revokes all their apps.
- **Brute force and floods.** Authorization requests (20, then one every 3
  seconds) and token and revocation requests (60, then one a second) are
  rate limited per address; bad bearer tokens on `/mcp` share the API
  token back-off.

**Not supported, on purpose.**

- *Client ID metadata documents.* The 2026-07-28 MCP specification prefers
  them to dynamic registration. Supporting them means fetching any HTTPS URL
  a caller names, from inside the private network dbairlock runs in (a
  server-side request forgery surface), and many such networks have no
  internet egress, where a client that prefers them would fail. They add no
  protection for loopback redirect URIs (the specification says so). Not
  advertising them makes clients, Claude Code among them, use dynamic
  registration.
- *Confidential clients, other grant types, browser-based clients.* Only
  public native clients with loopback redirects; no CORS on the protocol
  endpoints.

## Audit log

Every state change writes one row to `audit_events` in the same SQLite
transaction as the change itself. If the audit write fails, the change is
rolled back; if the change fails, no audit row is written.

Two separate mechanisms protect the log, and they protect against different
things:

- **Triggers stop accidents, not attackers.** `BEFORE UPDATE` and
  `BEFORE DELETE` triggers on `audit_events` abort any statement that would
  modify or remove a row, so a bug or a careless manual query cannot rewrite
  history. Anyone who holds the database file can drop the triggers, so they
  are not a security boundary.
- **The hash chain makes tampering detectable.** Each row stores
  `prev_hash` (the previous row's hash; 32 zero bytes for the first row) and
  `hash = SHA-256(prev_hash || canonical_json(row))`. The canonical JSON covers
  the sequence number, id, integer millisecond timestamp, actor, channel,
  action, subject and payload. Editing a row changes its hash; deleting a row
  leaves a gap in `seq` (AUTOINCREMENT never reuses numbers, and the verifier
  also compares against the highest number SQLite ever assigned, so deleting
  the newest rows is detected too).

`dbairlock audit verify --data-dir DIR` walks the chain and reports the first
broken link. Exit codes: `0` intact, `1` broken, `2` usage error, `3` the check
could not run (for example, no database in that directory).

Limits: the chain is self-contained. An attacker with write access to the file
who rewrites a row *and recomputes every later hash* produces a chain that
verifies. Detecting that requires an anchor outside the database, such as
keeping a copy of a recent `hash` value elsewhere (for example in your log
pipeline or alongside backups) and checking that it is still present.

Audit payloads never contain secret values: `audit.Record` walks every
payload and rejects one that holds a `secrets.Value` anywhere. String values
longer than 8 KB are truncated with a `...[truncated: N bytes]` marker.

## Secrets

Secrets are target database passwords, the Google client secret and the
notification webhook URL. They are write-only: the dashboard and API can set,
replace and clear a secret and see whether it is set, by whom and when, but
nothing returns the value.

- **Encryption.** AES-256-GCM under the master key, with a random 12-byte
  nonce per write. The additional data binds each ciphertext to its row (id,
  kind and owner), so a ciphertext copied to another row does not decrypt.
- **No fingerprints.** Nothing derived from a value (length, hash, last
  characters) is stored or shown; for short passwords any of these leaks
  information.
- **Three independent guards** keep values from leaking, so a leak needs three
  separate mistakes:
  1. *Interfaces.* `secrets.Writer` can set, clear and describe; only
     `secrets.Opener` decrypts, and only the modules that use secrets
     (targets, identity, notify) are given one. The linter and an
     architecture test enforce this; transports never hold an Opener.
  2. *Contract.* Secret fields are `writeOnly` in `api/openapi.yaml`, and a
     contract test fails if one appears in any response schema. A test walks
     every API route after setting secrets and checks that no response, log
     line or audit payload contains a value.
  3. *Type.* `secrets.Value` prints, marshals and logs as `[redacted]` in
     every format. Its bytes come out only through `Reveal`, which a lint
     rule allows only in targets, identity and notify.
- **Memory.** Decrypted values are kept only for the duration of the call
  that uses them and never cached. Go cannot guarantee that memory is zeroed,
  so dbairlock makes no such claim.
- **Auditing.** Setting, replacing and clearing a secret are audited with the
  secret's reference, never the value. Decrypting a secret for use is not
  audited (it happens on every connection); it is logged at debug level with
  the reference only.
- **The master key** protects every secret. Anyone holding both `master.key`
  and the database can decrypt them, so keep their backups apart. See
  [operations.md](operations.md#master-key) for start-up checks and rotation.

## Target databases

A connection is one configured target database: its connector (Postgres or
MySQL), host, database user, review policy and password. The password lives
in the secret store, owned by the connection, and is opened only when a
connection pool is created. Archiving a connection removes its password.

- **The grant is the boundary, not the parser.** Give dbairlock a dedicated
  database user with only `SELECT`, `INSERT`, `UPDATE` and `DELETE` on the
  application tables. The statement validator rejects DDL and much else, but
  it is the database grant that makes schema changes impossible. "Test
  connection" checks the user's privileges and warns, with the exact
  statements to remove them, when it finds superuser, ownership, `CREATE`,
  `GRANT OPTION` or similar rights.
- **Row previews are visible to everyone who can sign in** (see above).
  Table and column restrictions will come later; previews already pass
  through one function so they can be filtered and masked there.
- **Network.** dbairlock runs inside the private network of its targets, so
  private addresses are accepted. Link-local addresses (`169.254.0.0/16`,
  `fe80::/10`), where cloud metadata services live, are refused both when a
  connection is saved and at dial time, after name resolution.
- **Isolation between connections.** Every piece of per-target state (the
  password, the connection pool, the schema cache, the run lock and the
  policy) is keyed by the connection, never by the connector. Changing one
  connection's settings or password closes only its own pool.

## Cross-site requests

Mutating API requests from another origin are rejected with
`http.CrossOriginProtection` (403, code `cross_origin`).

## Web security

Checked and tightened in phase 11. Everything here is enforced by tests
named in each part.

### Content Security Policy

Every response carries this policy (`internal/platform/httpx/headers.go`):

```
default-src 'self'; script-src 'self';
style-src 'self' 'sha256-StEa…' 'sha256-47DE…' ['nonce-…' on the dashboard page];
img-src 'self'; font-src 'self'; connect-src 'self'; object-src 'none';
base-uri 'none'; form-action 'self'; frame-ancestors 'none'
```

- **Scripts:** same-origin files only. No inline script, no `'unsafe-eval'`,
  no nonce or hash for scripts. The built `index.html` has no inline script;
  the theme is applied by the bundle before the first render. zod is set to
  `jitless`, so it does not probe for `new Function` (which the browser would
  report as a violation even though zod catches the error).
- **Styles: no `'unsafe-inline'`.** Three things in the dashboard need inline
  `<style>`, and each is allowed narrowly:
  - *sonner* (toasts) inserts one fixed stylesheet, appending the element
    empty and then adding the text. Both texts are allowed by hash.
    `web/src/test/csp.test.ts` recomputes the hash from the installed sonner,
    so an upgrade that changes the CSS fails with the new hash.
  - *CodeMirror* (the SQL editor) keeps its styles in one `<style>` element
    whose text grows as editors mount, so no hash can cover it. The server
    generates a 128-bit nonce for each response of the dashboard page, puts
    it in the policy and in `<meta name="csp-nonce">`, and the editor sets it
    on its element (`EditorView.cspNonce`). The nonce applies to styles only.
    The page is served `Cache-Control: no-store` without `ETag` or
    `Last-Modified`, so a cached page with an old nonce is never reused.
  - *Base UI* would insert a `<style>` to hide the scrollbar of Select
    popups. `CSPProvider disableStyleElements` turns that off
    (`web/src/ui/StrictStyles.tsx`) and `index.css` carries the same rule.
  
  Style *properties* that React, Base UI and CodeMirror set from script go
  through the CSSOM, which CSP does not govern, so they need no exception.
- **Everything else:** same-origin fonts (Geist is self-hosted), no `data:`
  images, no plugins, no `<base>`, forms post only to this origin, and no
  site may frame the dashboard.
- **Verified in a browser.** Every Playwright test fails on any CSP violation
  in any page it opens (`web/e2e/csp.ts`, which also covers the second
  user's browser context). `web/e2e/security.spec.ts` checks the headers a
  real browser receives, that an injected inline script and style are
  blocked and noticed, and that toast styles still apply. The whole suite,
  including the SQL editor, passes with no violation.

### Other headers

| Header | Value |
|---|---|
| `X-Content-Type-Options` | `nosniff` |
| `X-Frame-Options` | `DENY` (for browsers that ignore `frame-ancestors`) |
| `Referrer-Policy` | `no-referrer` |
| `Permissions-Policy` | camera, microphone, geolocation, display capture, payment, USB, serial, HID, MIDI, motion sensors and Topics all denied |
| `Cross-Origin-Opener-Policy`, `Cross-Origin-Resource-Policy` | `same-origin` |
| `Strict-Transport-Security` | `max-age=31536000`, only while `instance.public_url` starts with `https://`. No `includeSubDomains` or `preload`: dbairlock is usually one host in a domain it does not own. The setting is read per request, so changing it needs no restart. |

The header middleware is the outermost layer, so the dashboard, its assets,
the API, MCP, health probes and every error (404, 405, 413, a cross-origin
403, a panic's 500) carry them. `TestSecurityHeadersOnEveryResponse`
(`internal/app/headers_test.go`) checks each of these, with and without an
HTTPS public URL. The only responses without them are the few that Go's HTTP
server writes itself before any handler runs (for example `400 Bad Request`
for a malformed request line); those are short plain-text bodies.

### Access levels and the route access test

Every route declares who may call it:

- REST operations, in `internal/httpapi/access.go`, by operationId:
  `public` (health, readiness, instance metadata, starting and finishing
  Google sign-in, sign-out of the caller's own cookie), `setup` (the setup
  and recovery code routes, which need the code or the short-lived session
  it grants, and work only in setup or recovery), `dev` (`--dev` only, 404
  otherwise) or `user` with an optional required action (`database.manage`,
  `settings.manage`, `user.manage`, `audit.read`, `change.submit`).
- Routes outside the OpenAPI contract, where `internal/app/app.go`
  registers them: `/mcp` (`token`: an API token or OAuth access token,
  never a session cookie), the OAuth discovery documents and protocol
  endpoints (`/.well-known/oauth-*`, `/oauth/register`, `/oauth/authorize`,
  `/oauth/token`, `/oauth/revoke`: `public`, each authenticating what it
  needs) and `/` (the dashboard's static files; all data comes from the
  API).

The REST middleware enforces the declaration before any handler runs, and
refuses (403) an operation that has none. Handlers authorize again against
the specific resource. `TestEveryOperationDeclaresAccess` fails if an
operation in `api/openapi.yaml` has no declaration or a declaration names no
operation, and if a public operation other than sign-out is not a `GET`.
`TestRouteAccess` (`internal/app/routes_test.go`) walks the complete list of
routes actually registered on the root mux and, for each:

- fails if it has no declared access level, or is in the contract but not
  registered (or the reverse);
- sends every mutating method from another origin (both `Sec-Fetch-Site:
  cross-site` and a foreign `Origin`), with a signed-in admin's cookie, and
  expects 403 `cross_origin`; cross-origin protection wraps every route,
  `/mcp` and the dashboard included (MCP clients are not browsers and send
  neither header);
- expects 401 without a session for `user` routes, 403 for a member on
  admin-only routes, 401 for `/mcp` with a session cookie instead of a
  token, 404 for `dev` routes, and no success for `setup` routes once setup
  is complete.

### Rate limits

All limits are per client address (the forwarded address only from
`network.trusted_proxies`) and kept in memory.

| What | Limit | Test |
|---|---|---|
| Sign-in start and callback | 20, then one every 3 s; redirects to `/signin?error=rate_limited` | `TestSignInIsRateLimited` |
| Setup code | 5 per minute, then after 5 failures a lock-out doubling from 30 s to 15 min | `TestSetupEndToEnd` |
| Recovery code | the same limiter as the setup code | `TestRecoveryCodeIsRateLimited` |
| Bad API tokens on `/mcp` (added in phase 11) | 10 free failures, then a lock-out doubling from 1 s to 5 min; a valid token resets it | `TestBadAPITokensAreRateLimited` |
| MCP tool calls, per token or connected app | `limits.mcp_calls_per_minute`, plus `limits.mcp_previews_per_hour` and `limits.mcp_submissions_per_hour` | `TestMCP/rate_limits`, `TestLimits` |
| OAuth client registration | 10, then one a minute; at most 200 unapproved clients | `TestOAuthRegistration` |
| OAuth authorization requests | 20, then one every 3 s | `TestOAuthAuthorization` |
| OAuth token and revocation requests | 60, then one a second | `TestOAuthCodes` |
| Preview refresh, per change | one every 10 s | `TestChangeReview` |

API tokens carry 256 random bits, so the token limit is about bounding
database lookups from a misbehaving client, not about guessing. While an
address is locked out, even a valid token from it waits, which can briefly
affect other agents behind the same NAT after one of them sends a bad token
repeatedly.

### No HTML injection

SQL, titles, reasons, comments, error messages and everything an agent
writes are rendered as React text. There is no `dangerouslySetInnerHTML`,
`innerHTML`, `outerHTML`, `insertAdjacentHTML`, `document.write`,
`DOMParser`, `srcDoc` or `eval` anywhere in `web/src`; CodeMirror shows SQL
as editor text and the read-only SQL view renders highlighted runs as React
text. Every `href` is a fixed path built in code. An ESLint rule
(`no-restricted-syntax` in `web/eslint.config.js`, plus `no-eval`,
`no-new-func` and `no-script-url`) keeps all of these out, and
`web/src/test/html-injection.test.ts` checks that each pattern is still
caught.

## Reporting a vulnerability

Please report security problems privately, not in a public issue: use
GitHub's **Report a vulnerability** button on the repository's **Security**
tab (<https://github.com/alvaroibarguen/dbairlock/security>). Include the
version (`dbairlock version`), what an attacker needs, and what they gain.
You will get an acknowledgement, and a fix or a plan, before anything is
disclosed.
