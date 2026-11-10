# dbairlock

Reviewed, audited data changes for production databases, for engineers and their AI agents.

Instead of running `UPDATE` statements by hand against production, an engineer or an agent submits the change to dbairlock. A teammate reviews it in a web dashboard, seeing the before and after image of every row it will touch, and dbairlock then executes it, rolling it back if the rows have drifted since the review. Nobody on the team needs write credentials.

![The review page: a pending change with its SQL, the fresh preview, and the before and after image of each affected row](docs/images/review.png)

## What it does

- **Review exact rows, not code.** Every change is dry-run on the target in a rolled-back transaction. The reviewer sees the affected rows before and after, and an approval needs a preview taken in the last 5 minutes.
- **Guarded execution.** At run time the matching rows are locked and fingerprinted again. If different rows match, the values about to be overwritten have changed, or the row count differs, the whole change is rolled back and nothing is changed.
- **Agents propose, humans approve.** AI agents connect over MCP at `/mcp`, read the schema, check SQL and submit changes. They never see row data and cannot approve anything. The author of a change can never approve it.
- **Row changes only.** Single-table `INSERT`, `UPDATE` and `DELETE`. No DDL, no `SELECT`, no `UPDATE` or `DELETE` without a `WHERE`. SQL that cannot be parsed is rejected.
- **Tamper-evident audit log.** Every sign-in, submission, review, execution and setting change is recorded in a hash chain, in the same transaction as the change itself.
- **Postgres and MySQL**, each connection with its own review policy.
- **Self-hosted, configured in the browser.** One container, one volume, no environment variables or config files. Google sign-in, restricted to your Workspace domains.
- **Write-only secrets.** Database passwords, the Google client secret and the webhook URL can be set and replaced, never read back.

## Quick start

You need Docker with Compose and Git. From a terminal:

```sh
git clone https://github.com/alvaroibarguen/dbairlock.git
cd dbairlock
docker compose -f deploy/compose.yml up -d
docker compose -f deploy/compose.yml logs dbairlock
```

The first `up` builds the image from source, which takes a few minutes. The log then shows a setup block:

```
==================== dbairlock setup ====================
  This instance is not set up yet.
  Open:  http://localhost:8080/setup
  Code:  XXXXX-XXXXX-XXXXX-XXXXX
  The code changes every time dbairlock restarts.
=========================================================
```

Open <http://localhost:8080> in your browser, enter the code, and follow the wizard. It asks for a Google OAuth client; [docs/self-hosting.md](docs/self-hosting.md#google-sign-in) shows how to create one in two minutes. The first person to sign in becomes the admin. Databases are added under **Connections**, and everything else (review policy defaults, users, API tokens, notifications) is in **Settings**.

To stop it: `docker compose -f deploy/compose.yml down`. Your data stays in the `dbairlock_dbairlock-data` volume until you remove it with `down -v`.

## Documentation

- [Self-hosting](docs/self-hosting.md): requirements, Google sign-in, network placement, TLS proxy, database users, backups, upgrades and recovery.
- [Connecting AI agents](docs/agents.md): MCP setup for Claude Code and other clients, and what agents can and cannot do.
- [Configuration](docs/configuration.md): every setting, connection field, process flag and fixed limit.
- [Security](docs/security.md): threat model, what the database grant and the dry run do, secrets and the audit chain.
- [Operations](docs/operations.md): the data directory, the master key and its rotation.
- [Architecture](docs/architecture.md): module map, import rules, and adding a connector.
- [Versions](docs/VERSIONS.md): toolchain, dependencies, base images and supported databases.

## Development

Go, cgo (gcc) and pnpm are required. `make build` builds the dashboard and one binary at `bin/dbairlock`; `make dev` runs the server and the Vite dev server together; `make test`, `make lint` and `make e2e` run the checks; `make image` builds the container image; `make screenshots` regenerates the image above from a Playwright run with seeded data.

## License

[MIT](LICENSE)
