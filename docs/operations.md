# Operations

## Data directory

`--data-dir` (default `./data`) holds everything dbairlock stores:

| File | Contents |
|---|---|
| `dbairlock.db` | SQLite database (WAL mode), mode 0600 |
| `dbairlock.db-wal`, `dbairlock.db-shm` | SQLite write-ahead log and index; part of the live database |
| `master.key` | Encryption key for stored secrets (from Phase 3), mode 0600 |

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
