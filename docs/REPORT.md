# dbairlock build report

Written at the end of Phase 11 (2026-11-10). It records what was built, where the build departs from the plan, what is pinned below the latest release, and what is known not to work or was not verified. Detailed notes from the four Phase 11 workstreams are in [docs/report-notes/](report-notes/).

## What was built

| Phase | Result |
|---|---|
| 0. Tooling | Go 1.27.3, Node 26.13.0, pnpm 12.18.0; Go tools as `tool` directives in `go.mod`; every version checked against its registry on the day and recorded in [VERSIONS.md](VERSIONS.md). |
| 1. Skeleton | Contract-first API (`api/openapi.yaml` → oapi-codegen strict server and openapi-typescript client), `internal/platform` helpers, import boundaries enforced by depguard and an architecture test (no `os.Getenv` anywhere), the web app embedded in the binary, the Graphite Console design system and app shell. |
| 2. Store and audit | SQLite (modernc, WAL, `BEGIN IMMEDIATE`, goose migrations) and a hash-chained audit log written in the same transaction as every state change; `dbairlock audit verify`. |
| 3. Secrets and settings | AES-256-GCM secret store with a 0600 `master.key`, a `secrets.Value` type that redacts itself everywhere, `rotate-key`, and a typed settings registry edited only in the dashboard. |
| 4. Setup and identity | Browser setup with a one-time code from the log, Google sign-in (OIDC with PKCE), `__Host-` session cookies, API tokens (`dbal_pat_`), one authorization function (`identity.Can`), `dbairlock recover`, rate limits. |
| 5–6. Connectors | One `connector` contract, a Postgres connector (pg_query_go) and a MySQL connector (TiDB parser), each with its own validator, dry run with before/after row images, execution with a row fingerprint drift guard, and a shared conformance suite run against Postgres 14 and 18 and MySQL 8.4 and 9.7. Connections UI with privilege checks and exact GRANT/REVOKE fixes. |
| 7. Changes and executor | The change state machine, reviews bound to the SQL hash and the preview hash, preview freshness, the executor with drift handling, deferral while a target is down, and "outcome unknown" failures that are never retried. SQL editor (CodeMirror) and the three-pane review screen. |
| 8. MCP server | Stateless streamable-HTTP MCP endpoint with nine tools. Agents create and preview changes and get a review link, but never see row data and cannot approve. |
| 9. Audit, notifications, settings | Audit page with filters, verify and export; webhook notifications (generic or Slack) through an outbox; System settings; [configuration.md](configuration.md) lists every value. |
| 10. Packaging | 78 MB distroless image (amd64 and arm64), non-root and read-only root filesystem, `deploy/compose.yml`, a health check command, CI workflow, README with a screenshot, [self-hosting.md](self-hosting.md), [architecture.md](architecture.md), [security.md](security.md). |
| 11. Hardening | Safety rules mapped to tests and attacked on purpose (three bugs fixed, one of them serious), a secret leak test, strict CSP and headers, a route access test, 30-minute fuzz runs, fault tests, a load test with index fixes, the race detector in `make test`, axe on every page, keyboard and 375 px checks. |

## Phase 11 findings

**Fixed:**

- **Postgres validator bypass (serious).** Connections did not pin `standard_conforming_strings`. On a server or role with it off, `UPDATE users SET name = 'x\' WHERE id = 1 --' WHERE id > 0` validated and previewed as a one-row update but changed every row. Every transaction now sets it on; a server test reproduces the bypass on a misconfigured server.
- **Preview rows must equal changed rows.** Both connectors now refuse a dry run in which an `UPDATE` or `DELETE` changes a different number of rows than its `WHERE` matched. Side effect: tables with a `BEFORE` trigger that skips rows cannot be changed through dbairlock.
- **Clock changes.** A preview stamped in the future counted as fresh; the executor could claim a change whose approval had just expired; a preview refresh was refused after the clock stepped back. All three are fixed.
- **Read-only database file.** SQLite silently opened a 0400 `dbairlock.db` read-only, so the instance reported ready and failed every write. The store now restores 0600 and checks it can write at startup; otherwise it refuses to start.
- **Bad API tokens on `/mcp`** were not rate limited. They now back off per address.
- **Changes list at 50,000 changes** took 15.4 s for the first page. Two indexes (migration 00007) and a rewritten audit query bring every list request under 4 ms; `audit verify` over 500,000 events takes 3 s.
- **CSP** allowed `'unsafe-inline'` styles and had never been checked in a browser. It is now strict (`style-src 'self'` plus two hashes and a per-page nonce) and every Playwright test fails on any violation.
- **Accessibility.** Four colour tokens below 4.5:1 contrast, accessible names not matching visible text, scroll boxes unreachable by keyboard, focus lost after navigation and after dialogs, missing page titles and headings, a sideways scroll at 375 px, and demoting an admin without confirmation.

**Searched for and not found:** a way to edit an approved change, to approve one's own change (including a second token, MCP and a mid-review promotion), to run a change twice (concurrent triggers, restarts, clock jumps), to read a secret through any endpoint, error, log, export, MCP tool or panic, a data race, an unprotected route, HTML injection, or a fuzz crash in 112 million executions.

## Deviations from the plan

- **Connector contract** (Phases 5–6): `ValidateConfig` returns the normalized config, `Validate` takes the config, `Open` takes no limits (they go to each run), and the policy is not passed to the dry run or execution, so callers validate with the connection's current policy right before every run. MySQL results record the pinned `sql_mode` and server version. Phases 5 and 6 were committed together, as were Phases 1 and 2.
- **Extra permission and endpoint** (Phase 7): `change.administer` covers acting on other people's changes (cancelling them, triggering their execution) and never allows approving. `POST /api/changes/{id}/retry` lets an admin clear the wait of an approved change whose database could not be reached, so the executor tries again at once.
- **Change ids** are shown as `#N`, not `CHG-0142` as DESIGN.md's example suggests.
- **Agent tokens act only on changes their owner authored** (Phase 8), even when the owner is an admin, so a token cannot cancel or edit a teammate's change.
- **`--dev` adds `POST /api/dev/login`**, and a hidden setting `auth.oidc_issuer` accepts only Google or a loopback address. Both exist for tests (the fake OpenID provider); neither can point sign-in at a third party.
- **Design tokens.** Dark `--mute` and light `--mute`, `--amber` and `--green` were darkened or lightened by a few percent to pass contrast. DESIGN.md is updated; `docs/design/review-screen-reference.html` keeps the original values.
- **Test tooling reads one environment variable**, `DBAIRLOCK_BIN` in `web/e2e/global-setup.ts`, to run Playwright against the race-enabled binary. The server itself reads none.

## Versions below the latest

| What | Pinned to | Why |
|---|---|---|
| TypeScript for typescript-eslint and openapi-typescript | 6.0.3 | Both need the JavaScript compiler API that TypeScript 7 no longer ships. The app itself is checked with TypeScript 7.0.2. |
| typescript-eslint | 8.72.1 | 8.72.2 is younger than pnpm's minimum release age. |
| minimatch (under eslint) | 10.2.6 | Moderate advisory GHSA-g3hv-75wg-m249; the fix exists only in 11, and eslint requires 10. Lint-only, over globs from this repository. |
| PostgreSQL 14 as the oldest test target | 14 | Reaches end of life on 2026-11-12. Switch the oldest conformance and CI target to 15 after that date. |

Everything else was installed at its latest release; see [VERSIONS.md](VERSIONS.md).

## Known limitations

- **Always-true `WHERE` clauses that mention a column pass validation** (`id >= 0`, `coalesce(true, id = 1)`). The backstops are the per-connection row limit, the preview and the review.
- **A recreated Google account can approve its old self's changes.** Users are keyed by Google subject and email is not unique, so a Workspace admin who deletes and recreates an account gives that person a second dbairlock user. Suggested fix: refuse a first sign-in whose email belongs to an active user with a different subject.
- **Invisible characters in table names** (a zero-width space after `users`) are accepted as part of the name. The statement fails at the database unless such a table exists, but it looks like `users` on screen.
- **Clock stepping back past a change's creation** makes writes to that change fail with a generic internal error until the clock catches up. Nothing runs.
- **A MySQL COMMIT over a hung network** can take about a minute to fail. It still ends as "outcome unknown" and is never retried.
- **During an `/mcp` bad-token lock-out**, valid tokens from the same address also wait (agents behind one NAT).
- **A sonner upgrade that changes its CSS** breaks toast styling until the CSP hash is updated; `web/src/test/csp.test.ts` fails first and prints the new hash.
- **Images built by Compose** report version `dev (commit unknown)`; `make image` sets version, commit and date.
- At 375 px the Users table scrolls sideways inside its box.

## Not verified

- **Connecting a real Claude Code client** to a dev instance over MCP (the Phase 8 manual check). The MCP flow is tested end to end with the go-sdk client against the real handler.
- **The CI workflow** has never run on GitHub; nothing is pushed.
- **Browsers other than Chromium**, and screen readers by hand.
- **HSTS on a real HTTPS deployment**, a real full filesystem and a real read-only mount (simulated in tests), and concurrent load (requests were timed one at a time).
- **Hostile-SQL server tests** run only on Postgres 18 and MySQL 9.7.

## Suggested next steps

1. Refuse a sign-in whose email belongs to another active user (the recreated-account finding).
2. Reject identifiers containing invisible or confusable Unicode characters.
3. Move the oldest Postgres test target to 15 after 2026-11-12, and drop the TypeScript 6 hook once typescript-eslint and openapi-typescript support TypeScript 7.
4. Run the CI workflow on GitHub and connect Claude Code to a dev instance once the repository is pushed.
5. Likely features, not built: a schema browser, a read-only table browser, editor linting and formatting, table and column restrictions on previews, per-database permissions, per-token database scopes, MCP OAuth, a revert action using stored before images, more connectors, other sign-in providers, scheduled execution windows, and a Postgres metadata store for larger installations.
