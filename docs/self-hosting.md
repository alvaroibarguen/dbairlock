# Self-hosting dbairlock

dbairlock runs as one container with one volume. It has no environment
variables and no configuration files: a first-run wizard in the browser sets
up Google sign-in, and everything else is in the dashboard. This guide covers
installing it, putting it on your network, preparing the databases it
changes, and keeping it backed up.

Commands below assume the repository root as the working directory and the
Compose project from [`deploy/compose.yml`](../deploy/compose.yml), whose
container is the service `dbairlock` and whose volume is
`dbairlock_dbairlock-data`.

## Requirements

- A Linux host (amd64 or arm64) with Docker Engine 25 or later and the
  Compose plugin. The image is about 80 MB.
- Little CPU and memory: dbairlock is one Go process with a SQLite file. Its
  disk use grows with the audit log and stored row images.
- A Google Workspace domain. Sign-in uses Google and accepts only accounts of
  the Workspace domains you allow; personal Gmail accounts cannot sign in.
- Network access from dbairlock to each target database's port, and outbound
  HTTPS to Google (see [Where to run it](#where-to-run-it)).
- HTTPS in front of it for anything but a trial on `localhost`
  (see [TLS](#tls-and-a-reverse-proxy)).

## Install

```sh
git clone https://github.com/alvaroibarguen/dbairlock.git
cd dbairlock
docker compose -f deploy/compose.yml up -d
```

The first `up` builds the image `dbairlock:latest` from the checkout, which
takes a few minutes; later starts are immediate. `make image` builds the same
image with the version, commit and build date filled in (`docker run --rm
dbairlock:latest version` prints them).

The container:

- runs as the unprivileged user 65532, with a read-only root filesystem
  (`read_only: true`); the only writable path is the `/data` volume;
- serves plain HTTP on port 8080, published on the host's port 8080;
- keeps everything in `/data`: the SQLite database `dbairlock.db` (with its
  `-wal` and `-shm` files) and `master.key`, the key that encrypts stored
  secrets (see [operations.md](operations.md));
- reports its health through `dbairlock healthcheck`, which asks the server's
  `/readyz`; that endpoint answers 503 when the metadata store is unavailable,
  so `docker ps` shows the container as `unhealthy`.

To publish the port only to a reverse proxy on the same host, change the
`ports` line to `["127.0.0.1:8080:8080"]`.

### First-run setup

On a new volume dbairlock starts in setup mode and prints a one-time setup
code to its log:

```sh
docker compose -f deploy/compose.yml logs dbairlock
```

```
==================== dbairlock setup ====================
  This instance is not set up yet.
  Open:  http://localhost:8080/setup
  Code:  XXXXX-XXXXX-XXXXX-XXXXX
  The code changes every time dbairlock restarts.
=========================================================
```

Open the dashboard (any page redirects to `/setup` while setup is pending)
and:

1. **Enter the setup code.** Repeated wrong codes are rate limited.
2. **Fill in sign-in:** the public URL (the address people will use; the
   wizard suggests the one you opened it on), the Google client ID and secret
   from the next section, and the Workspace domains whose accounts may sign
   in. The wizard shows the redirect URI to register with Google.
3. **Sign in with Google.** That account becomes the first admin, and setup
   mode ends for good.

Afterwards, add databases under **Connections** and adjust everything else in
**Settings** (every value is listed in [configuration.md](configuration.md)).
People from the allowed domains join as members the first time they sign in;
admins change roles and remove people in **Settings → Users**.

## Google sign-in

dbairlock needs an OAuth client of type *Web application* in a Google Cloud
project:

1. In the [Google Cloud console](https://console.cloud.google.com/), choose or
   create a project.
2. Open **Google Auth Platform → Branding** (older consoles: **APIs &
   Services → OAuth consent screen**) and configure the consent screen. Choose
   the **Internal** audience so only your Workspace's accounts can use it.
   dbairlock asks only for the `openid`, `email` and `profile` scopes.
3. Open **Clients** (**APIs & Services → Credentials**), and create an OAuth
   client ID of type **Web application**.
4. Under **Authorized redirect URIs**, add exactly:

   ```
   <public URL>/api/auth/google/callback
   ```

   For example `https://dbairlock.example.com/api/auth/google/callback`, or
   `http://localhost:8080/api/auth/google/callback` for a trial on your own
   machine. The public URL has no trailing slash, and the URI must match it
   character for character, scheme and port included. Google accepts plain
   `http` only for `localhost`.
5. Copy the client ID and client secret into the setup wizard (later:
   **Settings → Sign-in**). The secret is write-only: dbairlock shows whether
   it is set, never its value.

If you change the public URL later, add the new redirect URI in Google
before saving it.

dbairlock verifies each Google ID token's signature, audience, expiry and
nonce, requires `email_verified`, and accepts the account only when its
Workspace domain (the `hd` claim) is one of the allowed domains.

## Where to run it

Run dbairlock **inside the same private network (VPC) as the databases it
changes**. It connects to each target directly by host, port, user and
password; there are no SSH tunnels or cloud IAM authentication in this
version, and private addresses are expected. Link-local addresses
(`169.254.0.0/16`, `fe80::/10`, where cloud metadata services live) are
always refused.

Network rules:

- **dbairlock → databases:** allow its address to reach each target's port
  (5432, 3306, or whatever you use), and nothing more is needed.
- **People and agents → dbairlock:** engineers' browsers and the AI agents
  that run on their laptops must reach the dashboard and `/mcp`. Expose it
  through the company VPN or an internal load balancer; it does not need to
  be on the public internet.
- **dbairlock → Google:** outbound HTTPS to `accounts.google.com`,
  `oauth2.googleapis.com` and `www.googleapis.com` (sign-in and Google's
  signing keys). Each person's browser must also be able to open the public
  URL, since Google redirects the browser back there.
- **dbairlock → notification receiver** (optional): outbound HTTPS to your
  chat webhook, if you turn on notifications.
- **The data volume:** only the dbairlock container should be able to read
  it. Anyone who can read both `dbairlock.db` and `master.key` can decrypt
  every stored database password.

Run exactly one instance per volume. The executor must not run twice, so do
not scale the service beyond one replica.

## TLS and a reverse proxy

dbairlock serves plain HTTP and leaves TLS to a reverse proxy or load
balancer. Its cookies are always `Secure` and use the `__Host-` prefix, so
**sign-in works over plain HTTP only on `localhost`** (`http://localhost:8080`
or `http://127.0.0.1:8080`, which browsers treat as secure). Anywhere else,
put HTTPS in front and set the public URL to the `https://` address.

The proxy must pass the original `Host` header and add `X-Forwarded-For`.
Then, in **Settings → Limits → Trusted proxies**, add the address range the
proxy connects from, as dbairlock sees it (for a proxy on the same host,
typically the Docker bridge gateway, for example `172.17.0.1` or
`172.16.0.0/12`). dbairlock then takes the client address from
`X-Forwarded-For` for the audit log and for per-client rate limits such as
sign-in attempts. With no trusted proxy, every request appears to come from
the proxy, so all clients share one rate limit. A header from an untrusted
peer is ignored, so it cannot be used to forge an address.

Request bodies are at most 1 MB on `/api` and 256 KB on `/mcp`, and `/mcp`
answers with plain JSON (no streaming), so no special buffering or timeout
settings are needed.

### Caddy

Caddy obtains and renews a certificate and sets `X-Forwarded-For` and
`X-Forwarded-Proto` itself:

```caddyfile
dbairlock.example.com {
	reverse_proxy 10.0.1.20:8080
}
```

### nginx

```nginx
server {
    listen 80;
    server_name dbairlock.example.com;
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl;
    server_name dbairlock.example.com;

    ssl_certificate     /etc/nginx/tls/dbairlock.crt;
    ssl_certificate_key /etc/nginx/tls/dbairlock.key;

    client_max_body_size 2m;

    location / {
        proxy_pass http://10.0.1.20:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

Replace `10.0.1.20` with dbairlock's address (or `127.0.0.1` when the proxy
runs on the same host and the port is published only on loopback).

## Target databases

### Supported versions

dbairlock supports every Postgres major version and every MySQL LTS release
that its vendor still maintains. At the time of this release that is
PostgreSQL 14 to 18 and MySQL 8.4 LTS and 9.7 LTS; the connector test suite
runs against the oldest and the newest of each
([VERSIONS.md](VERSIONS.md#supported-target-databases) has the details).

Another version (for example MySQL 8.0, a MySQL Innovation release, MariaDB,
or a Postgres major outside that range) is **not refused**. **Test
connection** reports it with a warning: "*X* is not a supported version.
dbairlock supports the releases its vendor still maintains; it may work, but
it is not tested." You can still save and use the connection.

### A least-privilege database user

Give dbairlock its own database user with only `SELECT`, `INSERT`, `UPDATE`
and `DELETE` on the tables it may change. **The grant, not the SQL parser, is
what prevents schema changes**, and every table this user can read is
readable by everyone who can sign in to dbairlock (row previews, see
[security.md](security.md)). Grant only tables your whole team may see.

**Test connection** checks the user's privileges and warns, with the exact
statement to remove each one, about superuser and role attributes, table
ownership and `CREATE` on schemas (Postgres), or `ALL`, `GRANT OPTION`,
global write grants and administrative privileges (MySQL).

Postgres, for a database `app` and schema `public` (run as the database
owner or an admin):

```sql
CREATE ROLE dbairlock LOGIN PASSWORD 'a long random password';
GRANT CONNECT ON DATABASE app TO dbairlock;
GRANT USAGE ON SCHEMA public TO dbairlock;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dbairlock;

-- Only if changes INSERT rows that take their id from a serial column's sequence:
GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO dbairlock;

-- Tables your migrations create later (run as the role that creates them):
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO dbairlock;

-- Postgres 14 lets every role create objects in schema public by default:
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
```

On the connection form, **Default schema** (default `public`) is the schema
an unqualified table name means; dbairlock pins the search path to exactly
that schema for every transaction.

MySQL, for a database `app`; replace `10.0.1.20` with the address dbairlock
connects from (or use `'%'`):

```sql
CREATE USER 'dbairlock'@'10.0.1.20' IDENTIFIED BY 'a long random password';
GRANT SELECT, INSERT, UPDATE, DELETE ON app.* TO 'dbairlock'@'10.0.1.20';
```

A MySQL connection can change only tables in its own database; a statement
naming another database is rejected. Tables with a non-transactional storage
engine (for example MyISAM) are refused, because a dry run on them could not
be rolled back.

Use TLS to the database where it crosses any network you do not fully
control: Postgres offers `disable`, `require` and `verify-full` (the
default), MySQL `disabled`, `required` and `verify-identity` (the default),
each with an optional CA certificate.

### Finding a dbairlock change in the database's own logs

dbairlock labels every session that runs a change, without altering the
reviewed statement text. Executions are labelled `dbairlock change #42` (the
change number in the dashboard) and dry runs (previews) `dbairlock preview`.

**Postgres** sets the label as `application_name` for the transaction. While
it runs:

```sql
SELECT pid, application_name, state, xact_start, query
FROM pg_stat_activity
WHERE application_name LIKE 'dbairlock%';
```

To keep it in the server log, include `%a` in `log_line_prefix`, for example
`log_line_prefix = '%m [%p] %a '`. Idle pooled sessions (schema reads,
connection tests) show `dbairlock`.

**MySQL** runs each dry run and execution on a dedicated connection whose
connection attributes are `program_name = dbairlock` and
`dbairlock_run = dbairlock change #42`. While it runs:

```sql
SELECT a.PROCESSLIST_ID, a.ATTR_VALUE AS dbairlock_run, t.PROCESSLIST_STATE, t.PROCESSLIST_INFO
FROM performance_schema.session_connect_attrs a
JOIN performance_schema.threads t ON t.PROCESSLIST_ID = a.PROCESSLIST_ID
WHERE a.ATTR_NAME = 'dbairlock_run';
```

The `PROCESSLIST_ID` is the connection id that the general query log and
the slow query log print, and audit log plugins that record connection
attributes record `dbairlock_run` too. Pooled connections carry only
`program_name = dbairlock`.

Each execution is also in dbairlock's own audit log (**Audit**), with the
change number, the SQL hash and the label.

## Backups

`/data` holds two things to back up, **separately**:

- **The database**, `dbairlock.db`. It runs in WAL mode, so never copy the
  file (or snapshot the volume) while dbairlock is running; take a
  consistent copy with SQLite's `VACUUM INTO`.
- **`master.key`**. Without it, the stored secrets (database passwords, the
  Google client secret, the webhook URL) cannot be decrypted; with it and the
  database together, anyone can decrypt them. Store it apart from the
  database backups, for example in your secrets manager.

The image has no shell or `sqlite3`, so run `VACUUM INTO` from a throwaway
container that mounts the same volume, **while dbairlock is running**:

```sh
mkdir -p backups
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
docker run --rm -i -v dbairlock_dbairlock-data:/data -v "$PWD/backups:/backup" alpine:3 \
  sh -c 'apk add --no-cache -q sqlite && sqlite3 /data/dbairlock.db' \
  <<< "VACUUM INTO '/backup/dbairlock-$STAMP.db';"
```

The result is one self-contained file. Run it from cron or your backup tool,
and copy the key once (and again after every `rotate-key`):

```sh
docker compose -f deploy/compose.yml cp dbairlock:/data/master.key ./master.key.backup
```

Check a backup with `dbairlock audit verify` against a copy of it, or after a
restore (below). For an extra guarantee against tampering, keep the newest
audit hash outside the database (see [security.md](security.md#audit-log)).

## Restore

Stop dbairlock, put the backup in place as `dbairlock.db`, remove the old
`-wal` and `-shm` files, and give the files to the container's user:

```sh
docker compose -f deploy/compose.yml stop
docker run --rm -v dbairlock_dbairlock-data:/data -v "$PWD/backups:/backup:ro" alpine:3 sh -c '
  rm -f /data/dbairlock.db-wal /data/dbairlock.db-shm &&
  cp /backup/dbairlock-20261110T120000Z.db /data/dbairlock.db &&
  chown 65532:65532 /data/dbairlock.db && chmod 600 /data/dbairlock.db'
docker compose -f deploy/compose.yml run --rm dbairlock audit verify --data-dir /data
docker compose -f deploy/compose.yml start
```

Use the `master.key` that belongs to that database. If the volume is new or
lost its key, copy the saved key in before starting, with the same owner and
mode:

```sh
docker run --rm -v dbairlock_dbairlock-data:/data -v "$PWD:/backup:ro" alpine:3 sh -c '
  cp /backup/master.key.backup /data/master.key &&
  chown 65532:65532 /data/master.key && chmod 600 /data/master.key'
```

dbairlock refuses to start when `master.key` does not match the database, is
missing although the database holds secrets, or is readable by group or
other; the log says which. Any change that was `executing` when the backup
was taken is marked failed ("interrupted, outcome unknown, verify manually")
on start, never re-run.

## Upgrades

Back up first, then rebuild and restart:

```sh
git pull
docker compose -f deploy/compose.yml up -d --build
```

Migrations run automatically on start. dbairlock refuses to start on a
database migrated by a newer version, so a downgrade fails safely; to roll
back, restore the backup taken before the upgrade and run the older version.
On `SIGTERM` dbairlock stops taking requests and gives running work up to 20
seconds to finish. Docker waits only 10 seconds by default before killing a
container, so `deploy/compose.yml` sets `stop_grace_period: 30s`; with plain
`docker stop`, pass `-t 30`. A change killed mid-execution is marked failed
on the next start and never re-run.

## Recovery when sign-in is misconfigured

If nobody can sign in (a rotated client secret, a wrong domain list, a
changed public URL), print a one-time recovery link on the host:

```sh
docker compose -f deploy/compose.yml exec dbairlock dbairlock recover --data-dir /data
```

The link works once, within 15 minutes, and opens only the sign-in settings.
It works while the server runs. Fix the settings, then complete a Google
sign-in to finish; issuing, using and completing a recovery are all in the
audit log. The link starts with the public URL, or, if none is set, with
`http://localhost:8080`; open it on a machine that reaches dbairlock at that
address, or adjust the host.

## Other commands

Every subcommand runs with the image's entrypoint. Those that need the
server stopped use `run` on the stopped service:

| Command | Purpose |
|---|---|
| `docker compose -f deploy/compose.yml run --rm dbairlock audit verify --data-dir /data` | Check the audit log's hash chain (exit 0 intact, 1 broken). Also a button in **Settings → System**. |
| `docker compose -f deploy/compose.yml stop` then `docker compose -f deploy/compose.yml run --rm dbairlock rotate-key --data-dir /data` | Replace the master key and re-encrypt every secret ([operations.md](operations.md#rotating-the-key)). Back up the new key afterwards. |
| `docker run --rm dbairlock:latest version` | Version, commit and build date. |

Removing the volume (`docker compose -f deploy/compose.yml down -v`) deletes
every change, setting and audit event, and the next start is a new instance
in setup mode.

## Keeping master.key in a Docker secret

By default `master.key` lives in the data volume next to the database. To
keep it apart (for example, so volume snapshots never contain it), mount it
from a file-based Compose secret instead. dbairlock reads the key from
`/data/master.key` and accepts an existing key on a new instance.

1. Create the key next to the compose file (paths in `deploy/compose.yml`
   are relative to `deploy/`): exactly 32 random bytes, owned by the
   container's user and readable only by it:

   ```sh
   openssl rand -out deploy/master.key 32
   sudo chown 65532:65532 deploy/master.key
   sudo chmod 400 deploy/master.key
   ```

   For an existing instance, copy its current key out instead
   (`docker compose -f deploy/compose.yml cp dbairlock:/data/master.key deploy/master.key`)
   and set the same owner and mode. Keep the file out of version control.

2. Add the secret to the service in `deploy/compose.yml`:

   ```yaml
   services:
     dbairlock:
       # ...as before...
       secrets:
         - source: master_key
           target: /data/master.key

   secrets:
     master_key:
       file: ./master.key
   ```

The secret is mounted read-only, so `rotate-key` cannot replace it in place:
to rotate, generate a new key with `rotate-key` on a copy of the volume
without the secret mount, then replace the secret file with the new key.
