# Versions

Re-verified on 2026-11-09 with `go list -m -f '{{.Version}}' <module>@latest`, `npm view <pkg> version` and endoflife.date. Update this file whenever a dependency is added or bumped, and record every pin with its reason.

## Toolchain

| Component | Version | Notes |
|---|---|---|
| Go | 1.27.3 | |
| Node | 26.13.0 LTS | Node 26 no longer ships corepack |
| pnpm | 12.18.0 | Installed with `npm install -g pnpm@latest` into the per-user Node prefix |
| gcc | 13.3.0 | Needed by `pg_query_go` (cgo) |
| Docker | Docker Desktop, WSL integration | Required from Phase 5 |

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
| `github.com/pingcap/tidb/pkg/parser` | v0.0.0-20261109175506-c9f7aed139f6 |
| `github.com/jackc/pgx/v5` | v5.12.0 |
| `github.com/go-sql-driver/mysql` | v1.10.1 |
| `modernc.org/sqlite` | v1.62.0 |
| `github.com/coreos/go-oidc/v3` | v3.21.0 |
| `golang.org/x/oauth2` | v0.38.0 |
| `golang.org/x/time` | v0.16.0 |
| `github.com/google/uuid` | v1.6.0 |
| `github.com/testcontainers/testcontainers-go` | v0.45.0 |

## npm packages

| Package | Version |
|---|---|
| react, react-dom | 19.3.1 |
| vite | 8.3.7 |
| `@vitejs/plugin-react` | 6.3.0 |
| typescript | 7.0.2 |
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
| codemirror | 6.0.2 |
| `@codemirror/lang-sql` | 6.10.0 |
| `@uiw/react-codemirror` | 4.25.12 |
| tailwind-merge | 3.7.0 |
| sonner | 2.0.8 |
| vitest | 5.1.0 |
| `@testing-library/react` | 16.3.3 |
| `@playwright/test` | 1.65.0 |
| eslint | 10.14.0 |
| typescript-eslint | 8.72.2 |
| prettier | 3.9.10 |

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

None.
