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
  Removing a user ends their sessions, revokes their tokens and cancels their
  open changes; approvals they gave stop counting.
- The setup code and recovery codes are one-time, short-lived and rate
  limited. Setup ends at the first admin's sign-in, for good; afterwards only
  `dbairlock recover`, run on the host, reopens the sign-in settings.
- SQL, titles, reasons and comments are rendered as plain text everywhere;
  agent-written content is never interpreted as HTML or markdown.

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

## Reporting a vulnerability

Please report security problems privately, not in a public issue: use
GitHub's **Report a vulnerability** button on the repository's **Security**
tab (<https://github.com/alvaroibarguen/dbairlock/security>). Include the
version (`dbairlock version`), what an attacker needs, and what they gain.
You will get an acknowledgement, and a fix or a plan, before anything is
disclosed.
