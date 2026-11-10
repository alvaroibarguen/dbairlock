# Security notes

This file records the security properties dbairlock relies on and their
limits. Later phases add sections for sign-in and web security.

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
- **Row previews are visible to everyone who can sign in (accepted for
  v1).** Any signed-in person can submit a statement against any table of a
  configured database and see up to `max_rows` matching rows in its preview,
  and can see the previews of other people's changes. In effect dbairlock
  grants its users read access to every table its database user can read.
  Grant the dbairlock user access only to tables your whole team may see.
  The add-connection form says the same. Table and column restrictions will
  come later; previews already pass through one function so they can be
  filtered and masked there.
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
