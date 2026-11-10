# dbairlock

A self-hosted service where engineers and AI agents submit row-level SQL changes for human review, after which the server executes them. Go backend, React and Tailwind frontend, MCP server, SQLite metadata store.

## How to work in this repo

- **Follow the plan in `plans/`.** Start with [plans/PLAN.md](plans/PLAN.md) for the overview and safety rules, then work through the phase files in order. The `plans/` folder is gitignored, so it exists only on the owner's machine.
- **One phase at a time.** Each phase file ends with checks. All of them must pass before you commit and start the next phase.
- **Record progress.** When a phase is done, tick it off below so a later session knows where to resume.
- **Latest versions only.** Add dependencies with `go get <module>@latest` or `pnpm add <pkg>@latest`, never with a version typed from memory. Read the docs for the installed version before using an API.
- **Respect module boundaries.** Each database connector is its own package behind the `connector` contract. The import rules in the plan are enforced by the linter; do not loosen them.
- **Everything is configured in the dashboard.** No environment variables, no config files. The only process flags are `--data-dir`, `--listen` and `--dev`.
- **Secrets are write-only.** Nothing may return or log a secret value.
- **Fail closed.** Unparseable SQL is rejected. An execution with an uncertain outcome is marked failed and never retried.
- **Audit in the same transaction** as every state change.
- **Do not push, publish images, or create remote repos** unless the owner asks.
- **Follow [DESIGN.md](DESIGN.md)** (Graphite Console) for every screen, and [PRODUCT.md](PRODUCT.md) for product truth.
- **Edit files with editor tools**, not shell scripts (no `python3 - <<EOF`, `sed -i` or `cat > file`).

## Settled

- Go module path: `github.com/alvaroibarguen/dbairlock`.
- License: MIT.

## Progress

- [x] Phase 0: Tooling
- [x] Phase 1: Skeleton
- [x] Phase 2: Store and audit
- [x] Phase 3: Secrets and settings
- [x] Phase 4: Setup and identity (the fake OpenID provider for tests is `internal/identity/oidctest` and `web/e2e/fake-oidc.ts`; the hidden setting `auth.oidc_issuer` accepts only Google or a loopback address)
- [x] Phase 5: Connector contract and Postgres (sample targets: `docker compose -f deploy/dev/compose.yml up -d`; the e2e suite starts them and skips the connection tests without Docker)
- [x] Phase 6: MySQL connector
- [x] Phase 7: Changes and executor (integration tests in `internal/app/changes_test.go` start their own Postgres and MySQL containers; the e2e suite signs in a second user, bob, saved as `web/test-results/.auth/member.json`)
- [x] Phase 8: MCP server (`internal/mcpserver`, stateless streamable HTTP; the end-to-end test is `internal/app/mcp_test.go`, driving the go-sdk client against the real handler; agent setup is in `docs/agents.md`)
- [x] Phase 9: Audit page, notifications and remaining settings (every configurable value is listed in `docs/configuration.md`; the e2e notification test posts to a receiver on this machine's non-loopback address, because only `--dev` allows loopback webhooks)
- [x] Phase 10: Packaging (`deploy/Dockerfile` cross-compiles cgo for arm64 with Debian's cross gcc; `deploy/compose.yml` builds the image on first `up`; the image's health check is `dbairlock healthcheck`, probing `/readyz`; `make screenshots` regenerates `docs/images/review.png`; self-hosting, security and architecture docs are in `docs/`)
- [x] Phase 11: Hardening (`make test` runs the race detector; `make test-all` adds Playwright against a race-enabled binary; `make load` is the 50,000-change load test; the final report is `docs/REPORT.md`, with workstream notes in `docs/report-notes/`)
