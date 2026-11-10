# Configuration

dbairlock is configured in the dashboard. There are no environment variables
and no configuration files (a test, `internal/archtest`, fails the build if
any code reads the environment). Every value the server reads that changes
its behaviour is one of four kinds:

- **Setting**: instance-wide, edited in **Settings**, validated and audited
  (`setting.changed`). Stored in the database; the default applies until it
  is changed.
- **Connection field**: per database, edited on the connection's page.
- **Process flag**: `--data-dir`, `--listen` and `--dev`, the only three.
- **Constant**: compiled in, with the reason it is not configurable.

Secrets (the Google client secret, connection passwords and the webhook URL)
are write-only: the dashboard shows whether one is set, by whom and when,
never its value.

## Process flags

| Flag | Default | Effect |
|---|---|---|
| `--data-dir` | `./data` (the container image passes `/data`) | Holds `dbairlock.db` and `master.key` (see [operations.md](operations.md)). |
| `--listen` | `:8080` | Address of the HTTP server (dashboard, `/api`, `/mcp`). |
| `--dev` | off | Development only: binds to loopback, enables the dev sign-in, allows notifications to loopback and link-local receivers. |

## Settings

| Key | Default | Settings page | Effect |
|---|---|---|---|
| `instance.name` | `dbairlock` | General | Shown in the dashboard and on the sign-in page. |
| `instance.public_url` | empty | General | Base of sign-in redirects, review links given to agents and links in notifications. |
| `instance.trusted_proxies` | none | Limits | CIDR ranges whose `X-Forwarded-For` header gives the client address. |
| `auth.google.client_id` | empty | Sign-in | Google OAuth client. |
| `auth.google.client_secret` | not set | Sign-in | Secret. |
| `auth.allowed_domains` | none | Sign-in | Google Workspace domains whose accounts may sign in. |
| `auth.session_hours` | 12 | Sign-in | Idle length of a sign-in, 1 to 720 hours. |
| `auth.oidc_issuer` | Google | hidden | Google's issuer; another issuer is accepted only on a loopback address, for tests. |
| `policy.default_required_approvals` | 1 | Policy defaults | Default for connections created afterwards. |
| `policy.default_max_rows` | 100 | Policy defaults | Default for connections created afterwards. |
| `policy.default_approval_ttl_hours` | 72 | Policy defaults | Default for connections created afterwards. |
| `policy.default_store_row_images` | on | Policy defaults | Default for connections created afterwards. |
| `policy.default_auto_execute` | on | Policy defaults | Default for connections created afterwards. |
| `policy.preview_max_age_minutes` | 5 | Policy defaults | Maximum age of the preview an approval needs; applies to every connection immediately. |
| `limits.statement_timeout_ms` | 10000 | Limits | Statement timeout on targets, for dry runs and executions. |
| `limits.lock_timeout_ms` | 3000 | Limits | Lock timeout on targets. |
| `limits.mcp_calls_per_minute` | 60 | Limits | MCP tool calls per API token per minute. |
| `limits.mcp_previews_per_hour` | 30 | Limits | `preview_change` calls per token per hour. |
| `limits.mcp_submissions_per_hour` | 20 | Limits | `submit_change` calls per token per hour. |
| `notify.enabled` | off | Notifications | Whether events are queued for the webhook. |
| `notify.webhook_url` | not set | Notifications | Secret: chat webhook URLs embed a credential. |
| `notify.format` | `generic` | Notifications | `generic` (one flat JSON object) or `slack` (Slack incoming webhook body). |
| `notify.events` | all five | Notifications | Which of change submitted, approved, rejected, executed and failed are sent. |

## Connection fields

Edited on each connection's page; the `policy.default_*` settings only fill
in the form for a new connection.

| Field | Effect |
|---|---|
| Name, description | Shown to people and to agents (`list_connections`). |
| Connector | Postgres or MySQL. |
| Host, port, database, user, TLS mode, default schema (Postgres) | How dbairlock reaches the database. |
| Password | Secret. |
| Required approvals (1 to 5) | Approvals a change needs; the author never counts. |
| Maximum rows (1 to 10,000) | A statement affecting more rows is rejected at submission. |
| Approval lifetime (1 to 720 hours) | A change still in review after this long expires. |
| Store row images | Keep before and after rows with each change. |
| Run automatically when approved | Otherwise someone starts the run. |
| Allowed functions | Overrides the connector's default function allowlist. |

## Constants

| Value | Where | Why it is not configurable |
|---|---|---|
| Accepted statements: single-table `INSERT`, `UPDATE`, `DELETE` | connectors | The product's safety rule (PLAN 5.1). |
| 20 statements per change, 64 KB per statement | `changes` | Bounds what one review can cover; a larger change should be split. |
| Title 200, reason and comments 4,000 characters | `changes` | Input bounds. |
| 10 seconds between preview refreshes of one change | `changes` | A refresh is a real dry run on the target. |
| Executor poll every 5 seconds; retry an unreachable target after 30 s, 1, 2, 5 minutes, then every 5 minutes | `executor` | PLAN 5.6; the approval lifetime is what ends the retries. |
| Session absolute maximum 30 days | `identity` | However active, a sign-in ends; the idle length is the setting. |
| Sign-in flow 10 minutes; setup session 30 minutes; recovery code 15 minutes | `identity`, `setup` | Short-lived by design. |
| Sign-in and setup-code rate limits | `identity`, `setup` | Brute-force protection must not be switchable off. |
| Schema cache 60 seconds | `targets` | Freshness of editor completion; not worth a setting. |
| Connection test 30 seconds; connect timeout 10 seconds; pool idle 5 minutes, lifetime 30 minutes | `targets`, connectors | Operational defaults that every target tolerates. |
| Cell values shown up to 4 KB | `connector` | Display truncation; complete values are fingerprinted regardless. |
| Request bodies 1 MB (`/api`), 256 KB (`/mcp`) | `httpx`, `mcpserver` | Abuse limits. |
| Audit payload strings 8 KB; audit page size up to 500 | `audit` | Bound the size of one event and one page. |
| Notification attempt timeout 5 seconds, no redirects | `notify` | A receiver must never hold up the instance. |
| Notification retries: 30 seconds, doubling to at most 1 hour, given up after 24 hours | `notify` | Notifications are a convenience, not the audit trail. |
| Delivered and dropped notifications listed for 30 days | `notify` | Housekeeping. |
| Shutdown grace 20 seconds | `app` | Must stay below orchestrators' default of 30 seconds (Kubernetes); `deploy/compose.yml` raises Docker's 10-second default to 30 with `stop_grace_period`. |
| SQLite temporary storage in memory | `store` | The container's root filesystem is read-only; only `/data` is writable. |

## Notifications

When enabled, each selected event is written to an outbox in the same
transaction as the change it reports, so nothing is sent for a change that
rolled back, and a slow or failing receiver never delays a review or an
execution. A background worker posts each event with up to a 5-second
timeout, retries failures with exponential backoff, and gives up after 24
hours (recorded as `notification.gave_up` in the audit log). **Notifications
may be delayed or dropped; the audit log is the record.**

The body holds the event, change number, title, database name, author,
reviewer (for approvals and rejections), status and a link built from
`instance.public_url`. It never holds SQL, row data, reasons or comments.

Receivers on loopback or link-local addresses (including cloud metadata
services) are refused unless the server runs with `--dev`; private addresses
are allowed. Redirects are not followed.
