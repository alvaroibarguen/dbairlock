# Security notes

This file records the security properties dbairlock relies on and their
limits. Later phases add sections for secrets, sign-in and web security.

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

Audit payloads never contain secret values (enforced in `audit.Record` from
Phase 3), and string values longer than 8 KB are truncated with a
`...[truncated: N bytes]` marker.
