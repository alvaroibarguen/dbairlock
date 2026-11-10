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

## Container base images (`deploy/Dockerfile`)

Pinned by tag and multi-arch index digest, looked up on 2026-11-10 with `docker buildx imagetools inspect <image>`. To bump, re-run that command and update both the Dockerfile `ARG` and this table.

| Stage | Image | Digest |
|---|---|---|
| web | `node:26-trixie-slim` (Node 26.13.0) | `sha256:5adad64f9a1fca2d51972caa65983634c8a774eb300d72b20f7f2a0f60368072` |
| build | `golang:1.27.3-trixie` | `sha256:af1bfc2614ebb1dbd31af514b78740fd678850e76498762b425ecee4c5c6ea80` |
| runtime | `gcr.io/distroless/base-nossl-debian13:nonroot` (glibc, CA certificates, tzdata; uid 65532) | `sha256:e6b0f82595aa1edde72c8054de7a9dca30f0f8f54820c37a167c0ddfc21c5c67` |

The build and runtime stages are both Debian 13 (trixie), so the binary's glibc matches the runtime's. Measured on 2026-11-10 with both connectors linked: 78 MB for linux/amd64 (`docker image ls`), and the linux/arm64 image cross-compiles with `gcc-aarch64-linux-gnu` and runs under emulation. The image installs pnpm 12.18.0 with `npm install -g` (no corepack in Node 26).

## GitHub Actions (`.github/`)

Major tags, latest releases on 2026-11-10: `actions/checkout@v7`, `actions/setup-go@v7`, `actions/setup-node@v7`, `actions/cache@v6`, `actions/upload-artifact@v7`, `docker/setup-buildx-action@v4`, `docker/build-push-action@v7`; in the commented publish outline also `docker/login-action@v4` and `docker/metadata-action@v6`.

## Go tools (`tool` directive in `go.mod`, run with `go tool <name>`)

| Tool | Version |
|---|---|
| `github.com/sqlc-dev/sqlc` | v1.31.1 |
| `github.com/oapi-codegen/oapi-codegen/v2` | v2.8.2 |
| `github.com/pressly/goose/v3` | v3.29.0 |
| `github.com/golangci/golangci-lint/v2` | v2.15.0 |
| `golang.org/x/vuln` (govulncheck) | v1.9.0 |

## Go modules

Checked against `go.mod` on 2026-11-10 (phase 11): `go mod tidy` changes nothing, every direct module below is imported by non-generated code or tests, and `go list -m -u` reports no newer release of any of them except a newer daily pseudo-version of the TiDB parser, which has no releases of its own; bump it with `go get github.com/pingcap/tidb/pkg/parser@latest` when there is a reason to.

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

Installed in `web/` with `pnpm add <pkg>@latest` on 2026-11-09. pnpm 12 refuses releases younger than its supply-chain minimum age, so three packages published the same day resolved to the previous release. Re-tried on 2026-11-10 (phase 11): vite 8.3.7 and `@vitejs/plugin-react` 6.3.0 have aged in and are installed; typescript-eslint 8.72.2 (published 2026-11-09 17:08 UTC) still resolves to 8.72.1. Re-run `pnpm add -D typescript-eslint@latest` once it ages in.

Checked against `web/pnpm-lock.yaml` (`pnpm list --depth 0`) on 2026-11-10: every version below is the one installed.

| Package | Version |
|---|---|
| react, react-dom | 19.3.1 |
| vite | 8.3.7 |
| `@vitejs/plugin-react` | 6.3.0 |
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
| `@uiw/react-codemirror` (SQL editor, phase 7) | 4.25.12 |
| `@codemirror/view` | 6.43.15 |
| `@codemirror/state` | 6.7.6 |
| `@codemirror/language` | 6.13.1 |
| `@codemirror/autocomplete` | 6.20.4 |
| `@codemirror/lint` | 6.9.7 |
| `@lezer/highlight` | 1.2.5 |
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
| typescript-eslint | 8.72.1 (8.72.2 held back by pnpm's minimum release age, see above) |
| eslint-plugin-react-hooks | 7.1.1 |
| eslint-plugin-react-refresh | 0.5.7 |
| globals | 17.14.0 |
| prettier | 3.9.10 |
| prettier-plugin-tailwindcss | 0.8.1 |
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

## Vulnerability scans

Run on 2026-11-10 (phase 11). Re-run both before each release.

| Scan | Result | Decision |
|---|---|---|
| `go tool govulncheck ./...` | No vulnerabilities found. | |
| `pnpm audit` (in `web/`) | 1 moderate: [GHSA-g3hv-75wg-m249](https://github.com/advisories/GHSA-g3hv-75wg-m249), minimatch denial of service through brace/globstar amplification, minimatch 10.2.6, fixed in 11.0.1. | **Accepted, documented.** The only path is the lint toolchain (eslint 10.14.0 and `@eslint/config-array` require `minimatch ^10`, and no 10.x fix exists). It runs on developer machines and in CI over glob patterns written in this repository, never on attacker input, and it is not in the shipped binary or image. Forcing minimatch 11 under eslint with an override would be an untested major-version swap inside a dev tool. Remove this entry when eslint moves to minimatch 11. |
