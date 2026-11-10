# Versions

Re-verified on 2026-11-09 with `go list -m -f '{{.Version}}' <module>@latest`, `npm view <pkg> version` and endoflife.date. Update this file whenever a dependency is added or bumped, and record every pin with its reason.

## Toolchain

| Component | Version | Notes |
|---|---|---|
| Go | 1.27.3 | |
| Node | 26.13.0 LTS | Node 26 no longer ships corepack |
| pnpm | 12.18.0 | Installed with `npm install -g pnpm@latest` into the per-user Node prefix |
| gcc | 13.3.0 | Needed by `pg_query_go` (cgo) |
| Docker | 29.7.2 (Docker Desktop, WSL integration), Compose v5.5.1 | Required from Phase 5 |

## Go tools (`tool` directive in `go.mod`, run with `go tool <name>`)

| Tool | Version |
|---|---|
| `github.com/sqlc-dev/sqlc` | v1.31.1 |
| `github.com/oapi-codegen/oapi-codegen/v2` | v2.8.2 |
| `github.com/pressly/goose/v3` | v3.29.0 |
| `github.com/golangci/golangci-lint/v2` | v2.15.0 |
| `golang.org/x/vuln` (govulncheck) | v1.9.0 |

## Go modules

| Module | Version |
|---|---|
| `github.com/modelcontextprotocol/go-sdk` | v1.8.0 |
| `github.com/pganalyze/pg_query_go/v6` | v6.2.5 |
| `github.com/pingcap/tidb/pkg/parser` (with its `test_driver` literal driver; does not pull in the rest of TiDB) | v0.0.0-20261110033632-5cf03a50bc23 |
| `github.com/jackc/pgx/v5` | v5.12.0 |
| `github.com/go-sql-driver/mysql` | v1.10.1 |
| `modernc.org/sqlite` | v1.62.0 |
| `github.com/pressly/goose/v3` (library, embedded migrations) | v3.29.0 |
| `github.com/coreos/go-oidc/v3` | v3.21.0 |
| `golang.org/x/oauth2` | v0.38.0 |
| `golang.org/x/time` | v0.16.0 |
| `github.com/go-jose/go-jose/v4` (signs ID tokens in the fake OpenID provider for tests; go-oidc already depends on it) | v4.1.9 |
| `github.com/google/uuid` | v1.6.0 |
| `github.com/testcontainers/testcontainers-go` | v0.45.0 |
| `github.com/testcontainers/testcontainers-go/modules/postgres` | v0.45.0 |
| `github.com/testcontainers/testcontainers-go/modules/mysql` | v0.45.0 |
| `github.com/oapi-codegen/runtime` (used by the generated server) | v1.8.0 |
| `github.com/oapi-codegen/nullable` (omitted vs `null` in PATCH bodies) | v1.2.0 |
| `github.com/getkin/kin-openapi` (contract tests read `api/openapi.yaml`) | v0.153.0 |

## npm packages

Installed in `web/` with `pnpm add <pkg>@latest` on 2026-11-09. pnpm 12 refuses releases younger than its supply-chain minimum age, so three packages published the same day resolved to the previous release: vite 8.3.7 (published 2026-11-09), `@vitejs/plugin-react` 6.3.0 (2026-11-09) and typescript-eslint 8.72.2 (2026-11-09). Re-run `pnpm add <pkg>@latest` after they age in.

| Package | Version |
|---|---|
| react, react-dom | 19.3.1 |
| vite | 8.3.6 |
| `@vitejs/plugin-react` | 6.2.1 |
| typescript | 7.0.2 (6.0.3 for lint and codegen tools, see Pins) |
| tailwindcss, `@tailwindcss/vite` | 4.3.5 |
| react-router | 8.5.1 |
| `@tanstack/react-query` | 5.107.1 |
| `@tanstack/react-table` | 9.3.1 |
| `@base-ui/react` | 1.9.0 |
| lucide-react | 1.70.0 |
| react-hook-form | 7.91.0 |
| `@hookform/resolvers` | 7.0.5 |
| zod | 4.6.7 |
| openapi-typescript | 7.13.0 |
| openapi-fetch | 0.17.0 |
| sonner | 2.0.8 |
| tailwind-merge | 3.7.0 |
| `@codemirror/lang-sql` (editor dialect mapping, `web/src/ui/sqlDialect.ts`) | 6.10.0 |
| geist (self-hosted Geist and Geist Mono) | 1.9.2 |
| vitest | 5.1.0 |
| jsdom | 31.0.0 |
| `@testing-library/react` | 16.3.3 |
| `@testing-library/dom` | 10.4.2 |
| `@testing-library/user-event` | 14.6.8 |
| `@testing-library/jest-dom` | 7.0.1 |
| `@playwright/test` | 1.65.0 |
| eslint | 10.14.0 |
| `@eslint/js` | 10.0.1 |
| typescript-eslint | 8.72.1 |
| eslint-plugin-react-hooks | 7.1.1 |
| eslint-plugin-react-refresh | 0.5.7 |
| globals | 17.14.0 |
| prettier | 3.9.10 |
| prettier-plugin-tailwindcss | 0.8.1 |
| `@uiw/react-codemirror` (SQL editor, phase 7) | 4.25.12 |
| `@codemirror/view` | 6.43.15 |
| `@codemirror/state` | 6.7.6 |
| `@codemirror/language` | 6.13.1 |
| `@codemirror/autocomplete` | 6.20.4 |
| `@codemirror/lint` | 6.9.7 |
| `@lezer/highlight` | 1.2.5 |
| `@types/node` | 26.6.6 |
| `@types/react`, `@types/react-dom` | 19.3.0 |

The `codemirror` meta-package is not installed: `@uiw/react-codemirror` brings its own basic setup, and the editor imports the `@codemirror/*` packages it uses directly.

## Supported target databases

Every Postgres major version still maintained by the PostgreSQL project, and every MySQL LTS release still maintained by Oracle, as of 2026-11-09.

| Database | Version (latest patch) | End of life | Tested |
|---|---|---|---|
| PostgreSQL 18 | 18.6 | 2030-11-14 | newest |
| PostgreSQL 17 | 17.11 | 2029-11-08 | |
| PostgreSQL 16 | 16.15 | 2028-11-09 | |
| PostgreSQL 15 | 15.19 | 2027-11-11 | |
| PostgreSQL 14 | 14.24 | 2026-11-12 | oldest (EOL in 3 days; switch the oldest test target to 15 after that) |
| MySQL 9.7 LTS | 9.7.4 | 2034-04-30 | newest |
| MySQL 8.4 LTS | 8.4.13 | 2032-04-30 | oldest |

MySQL 26.10 is the newest Innovation release, not an LTS, so it is not a supported target. MySQL 8.0 reached end of life on 2026-04-30.

## Pins

| What | Pinned to | Reason |
|---|---|---|
| TypeScript used by typescript-eslint and openapi-typescript | 6.0.3 (newest 6.x) | TypeScript 7 is the native compiler and ships no JavaScript compiler API (`require('typescript')` exposes only the version). typescript-eslint 8.72 (peer range `>=4.8.4 <6.1.0`) and openapi-typescript 7.13 (peer `^5.x`, builds its output with `ts.factory`) both need that API. `web/.pnpmfile.cjs` replaces their `typescript` peer with a private `npm:typescript@6.0.3` dependency. The app itself is type-checked by TypeScript 7.0.2 (`tsc -b` in `pnpm build`). Remove the hook once both tools support TypeScript 7. |
| geist peer dependency on `next` | removed | geist declares `next` as a peer for its `next/font` helpers. dbairlock uses only the font files, so `web/.pnpmfile.cjs` drops the peer instead of installing Next.js. |
