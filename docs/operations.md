# Operations

## Data directory

`--data-dir` (default `./data`) holds everything dbairlock stores:

| File | Contents |
|---|---|
| `dbairlock.db` | SQLite database (WAL mode), mode 0600 |
| `dbairlock.db-wal`, `dbairlock.db-shm` | SQLite write-ahead log and index; part of the live database |
| `master.key` | Encryption key for stored secrets: exactly 32 bytes, mode 0600 |
| `master.key.old` | The previous key, kept after `rotate-key` until the next successful start |

The directory is created with mode 0700. Migrations run automatically at
startup. dbairlock refuses to start if the database was migrated by a newer
version than the binary, so downgrades fail safely instead of corrupting data;
restore a backup taken before the upgrade if you need to roll back.

## Backups

The database runs in WAL mode: recent commits live in `dbairlock.db-wal`
until SQLite checkpoints them into the main file. **Do not copy `dbairlock.db`
(or the `-wal` file) with `cp`, `rsync` or a volume snapshot tool while the
server is running.** The copy can be inconsistent or miss committed data.

Take a consistent copy of the live database with either:

- **`VACUUM INTO`** (simplest; works while the server runs):

  ```sh
  sqlite3 /data/dbairlock.db "VACUUM INTO '/backups/dbairlock-$(date +%Y%m%dT%H%M%S).db'"
  ```

  The target file must not exist. The result is a compact, self-contained
  database with no `-wal` file.

- **The SQLite online backup API**, for example `sqlite3 /data/dbairlock.db
  ".backup '/backups/dbairlock.db'"`, or the equivalent call in your backup
  tooling.

Alternatively, stop the server first; once it has exited cleanly, copying
`dbairlock.db` is safe.

Back up `master.key` separately and store it apart from the database backups:
without it, stored secrets cannot be decrypted, and anyone who holds both can
decrypt them.

After restoring a backup, run `dbairlock audit verify --data-dir DIR` to check
the audit log's hash chain.

## Master key

On first start dbairlock creates `master.key` (32 random bytes, mode 0600)
and stores a check value encrypted with it in the database. Every later start
decrypts that check value and **refuses to start** when:

- `master.key` is not exactly 32 bytes, or grants any permission to group or
  other (fix with `chmod 600`). If you mount the key from a Docker or
  Kubernetes secret, set the mount mode to `0400` or `0600` and make it owned
  by the user dbairlock runs as;
- `master.key` is missing although the database already holds secrets
  (restore the key from your backup; dbairlock will not create a new one);
- the key does not belong to this database.

### Rotating the key

```sh
# stop the server first: a running server keeps the old key in memory
dbairlock rotate-key --data-dir /data
```

`rotate-key` generates a new key, re-encrypts every secret in one transaction,
and installs the new `master.key` atomically. The previous key is kept as
`master.key.old` until the next successful start, then removed. If the command
is interrupted, the next start sorts it out: before the transaction commits
the old key stays in use; after it commits, the new key (left in
`master.key.new`) is installed automatically. Back up the new `master.key`
after rotating.
