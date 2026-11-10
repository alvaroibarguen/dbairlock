# Architecture

dbairlock is one Go binary. It embeds the React dashboard, keeps its own
state in a SQLite file, and talks to the target databases through
connectors. One process serves:

- `/api/*`: the JSON REST API for the dashboard, authenticated by session
  cookie. The contract is [`api/openapi.yaml`](../api/openapi.yaml); the Go
  server stubs (`internal/httpapi/gen`) and the TypeScript types
  (`web/src/api/schema.d.ts`) are generated from it with `make generate`.
- `/mcp`: the MCP server for agents (stateless streamable HTTP, bearer
  tokens). See [agents.md](agents.md).
- `/healthz` (liveness) and `/readyz` (the metadata store answers).
- `/*`: the dashboard, built by Vite and embedded from `internal/webui/dist`.
- Background workers: the executor that runs approved changes, and the
  notification outbox.

`cmd/dbairlock` only parses the three flags (`--data-dir`, `--listen`,
`--dev`) and dispatches subcommands (`serve`, `audit verify`, `rotate-key`,
`recover`, `healthcheck`, `version`); everything else is built in
`internal/app`.

## Module map

Every module is a package under `internal/` with a small exported surface.
Modules depend on interfaces, never on each other's concrete types.

| Package | Responsibility |
|---|---|
| `platform/clock`, `ids`, `httpx`, `logx`, `ratelimit` | No domain knowledge: injected time and ids, HTTP middleware and error shape, client address, logging, rate limiters |
| `store` | Opens SQLite (one writer, a read pool), runs the migrations in `store/migrations`, provides the transaction helper |
| `audit` | Append-only, hash-chained event log; one `Record` call inside the caller's transaction; verification and export |
| `secrets` | Write-only secret store under the master key (`Writer` sets and describes, `Opener` decrypts) |
| `settings` | Typed instance settings: registry, validation, audit |
| `identity` | Users, roles and the single authorization function `identity.Can`, sessions, API tokens, Google sign-in |
| `setup` | First-run wizard, setup code, recovery links |
| `connector` | The contract every database connector implements, the registry, the row fingerprint, safe dialing |
| `connector/postgres`, `connector/mysql` | One self-contained package per database product: driver, parser and validation, schema introspection, privilege check, dry run and execution |
| `connector/connectortest` | The conformance suite every connector must pass |
| `targets` | Connections (configured target databases): pools, passwords, policy, schema cache, connection tests |
| `changes` | Change requests, reviews, previews and the state machine |
| `executor` | Background loop that claims approved changes and runs them |
| `notify` | Outbound webhook notifications through a transactional outbox |
| `httpapi` | REST transport only, no business logic |
| `mcpserver` | MCP transport only, no business logic |
| `webui` | The embedded dashboard files |
| `app` | Composition root: builds every module and wires them together |
| `archtest` | Repository-wide architecture tests the linter cannot express |

Each module owns its SQL queries in its own sqlc package (for example
`changes/changesdb`, configured in `sqlc.yaml`). Migrations are one ordered
folder, `internal/store/migrations`.

The dashboard (`web/`) is React, TypeScript, Vite and Tailwind. Features live
in `web/src/features/<area>`, shared components in `web/src/ui`, and the API
client in `web/src/api`. The only place the dashboard knows about particular
databases is the editor dialect map in `web/src/ui/sqlDialect.ts`; connection
forms are rendered from the fields each connector declares.

## Import rules

Enforced by `depguard` in [`.golangci.yml`](../.golangci.yml) (`make lint`)
and by the tests in `internal/archtest`. Do not loosen them to make
something compile: declare the interface you need in the consuming package
and let `app` pass the implementation in.

1. `platform/*` (and `webui`) import nothing from `internal/` outside
   `platform`.
2. Domain modules never import `httpapi`, `mcpserver` or `app`.
3. Only `app` imports concrete connectors (`connector/postgres`,
   `connector/mysql`). Everything else uses the `connector` contract and the
   registry. Test files are exempt, so integration tests can drive a real
   connector.
4. Connector packages import only `connector`, `connector/connectortest` and
   `platform/*`, plus their own subpackages. They never touch the store,
   secrets or audit.
5. Only `targets`, `identity` and `notify` may hold a `secrets.Opener` and
   call `secrets.Value.Reveal`; `store`, `audit`, `changes` and `executor`
   may not import `secrets` at all; transports hold at most a
   `secrets.Writer`.
6. The transports `httpapi` and `mcpserver` do not import each other; `cmd/`
   imports only `app` and `platform/*`.

Also enforced: no code reads environment variables (forbidigo and
`archtest`), the wall clock is read only through `platform/clock`, output
goes through the injected logger, and nothing outside `identity` compares
roles.

## Adding a connector

A connector is a database product; a connection is one configured database
that uses it. Adding a product, say `example`, touches one new package and a
handful of one-line registrations.

1. **Create `internal/connector/example`.** Implement `connector.Connector`
   (in `internal/connector/connector.go`):
   - `Info()`: a stable id (`"example"`), display name, editor dialect name
     and default port.
   - `ConfigFields()`: the connection form (host, port, database, user,
     password as a `FieldSecret`, TLS mode, and so on). The dashboard renders
     it without new frontend code.
   - `ValidateConfig(cfg)`: normalize and check the non-secret fields.
   - `Validate(cfg, sql, policy)`: parse one statement with the product's
     own parser and enforce the rules of the plan (single-table `INSERT`,
     `UPDATE`, `DELETE`; a real `WHERE`; the function allowlist; no system
     schemas; reject anything it cannot parse). It must be pure and never
     touch a database.
   - `Open(ctx, cfg, password)`: return a `connector.Conn` with its own small
     pool. Check the host with `connector.CheckHost` in `ValidateConfig` and
     dial through `connector.SafeDialContext`, so link-local addresses stay
     refused before and after name resolution.

   And `connector.Conn`: `Ping`, `ServerInfo` (product, version, and whether
   the version is supported), `Privileges` (what the user can do and any
   excess rights, with the statements that remove them), `Schema`,
   `DryRun` (always rolls back) and `Execute` (locks matching rows, compares
   the fingerprint and row count with the expectation, commits only if
   nothing drifted). Label each run's session with `RunInfo.Label` without
   altering the statement text, and return `connector.ErrUnavailable` only
   for failures before the transaction began.

2. **Pass the conformance suite.** Add `conformance_test.go` that builds a
   `connectortest.Harness` (connector, validation config, your own
   validation corpus, a `NewTarget` that starts a seeded database with
   testcontainers, unreachable targets, a slow function, cell cases) and calls
   `connectortest.Run`. The seed fixture is documented at the top of
   `connectortest/harness.go`. Run it against the oldest and newest
   supported versions, as subtests named like `example14` and `example18`,
   and skip with a reason when Docker is unavailable or under `-short`.

3. **Register it** in `Connectors()` in `internal/app/app.go`: one entry in
   the list.

4. **Teach the linter.** In `.golangci.yml`, add the package to the
   `concrete-connectors` rule (exclude its files and deny importing it from
   elsewhere). The generic `connectors` rule already restricts its imports;
   if it has subpackages, give it its own rule like `connector-postgres`.

5. **Add the editor dialect** in `web/src/ui/sqlDialect.ts`: one entry
   mapping the `Info().Dialect` name to a CodeMirror dialect.

6. **Record versions and tests.** Add the supported versions to
   `docs/VERSIONS.md` ("Supported target databases"), the two matrix entries
   to the `integration` job in `.github/workflows/ci.yml`, and, if useful, a
   sample database to `deploy/dev/compose.yml`. Document the least-privilege
   user and how to find a run in the product's logs in
   `docs/self-hosting.md`.

Nothing else changes: targets, changes, the executor, both transports and
the dashboard work through the contract and the registry.
