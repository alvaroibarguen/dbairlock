# Connecting AI agents

dbairlock runs an MCP server at `/mcp` on the same address as the
dashboard, over streamable HTTP. An agent connected to it can find the right
database and table, check SQL, and submit a change for review. A person
approves the change in the dashboard before dbairlock runs it.

## Setup

An agent signs in one of two ways:

- **OAuth (recommended for people).** The agent opens your browser on
  dbairlock, you allow it on a consent page, and it receives its own
  short-lived tokens. Nothing is copied or pasted. Needs
  `instance.public_url` set and **Settings → Sign-in → Agents can sign in
  with OAuth** on (the default).
- **API token (for CI, scripts and clients without OAuth).** Create one in
  **Settings → API tokens**. It starts with `dbal_pat_`, acts as you, and is
  shown only once.

The **Connect an agent** page in the dashboard shows the commands below
with your instance's address filled in. Then ask the agent for a data
change: it gives you a review link, someone other than you approves it
there, and dbairlock runs it.

### Claude Code with OAuth

```sh
claude mcp add --transport http dbairlock https://dbairlock.example.com/mcp
```

Then, in Claude Code, run `/mcp`, choose **dbairlock** and
**Authenticate** (or run `claude mcp login dbairlock` in a shell). Claude
Code finds dbairlock's authorization server from the `401` the MCP endpoint
answers, registers itself, and opens your browser. Sign in with Google if
you are not signed in already, check the consent page (the client's name,
the `localhost` address on your computer the sign-in is sent to, and what
it can and cannot do) and choose **Allow**. Claude Code refreshes its token
by itself from then on.

- Add `--scope user` to make it available in every project. `--scope
  project` is safe here too: `.mcp.json` then holds only the URL, and each
  person signs in for themselves.
- Over SSH, `claude mcp login dbairlock --no-browser` prints the URL to open
  on your own machine; paste the redirect URL back when it asks.
- `claude mcp logout dbairlock` revokes the sign-in at dbairlock (Claude
  Code calls the revocation endpoint). You can also disconnect it in
  **Settings → API tokens → Connected apps**.
- Headless runs (`claude -p`) cannot sign in; sign in once interactively
  first, or use an API token.

### Claude Code with an API token

```sh
claude mcp add --transport http dbairlock https://dbairlock.example.com/mcp \
  --header "Authorization: Bearer dbal_pat_YOUR_TOKEN"
```

Do not commit a token: use `--scope local` or `--scope user`. When an
`Authorization` header is configured, Claude Code uses it and never starts
OAuth.

### Other clients

Clients that implement MCP authorization (OAuth 2.1 with protected
resource metadata, dynamic client registration and PKCE) need only the URL:

```json
{ "mcpServers": { "dbairlock": { "type": "http", "url": "https://dbairlock.example.com/mcp" } } }
```

Any other client that supports streamable HTTP with custom headers works
with an API token. The usual JSON configuration:

```json
{
  "mcpServers": {
    "dbairlock": {
      "type": "http",
      "url": "https://dbairlock.example.com/mcp",
      "headers": { "Authorization": "Bearer dbal_pat_YOUR_TOKEN" }
    }
  }
}
```

The server is stateless: every request stands alone and carries the token.
A request without a valid token gets `401` with a `WWW-Authenticate: Bearer`
header that names the protected resource metadata while OAuth is available;
while the instance is still in setup mode, `/mcp` answers `503`.

### How OAuth works here

dbairlock is the authorization server for its own MCP endpoint, following
the MCP authorization specification (revision 2026-07-28):

| What | Where |
|---|---|
| Protected resource metadata (RFC 9728) | `/.well-known/oauth-protected-resource/mcp` (also at the root, describing the instance) |
| Authorization server metadata (RFC 8414) | `/.well-known/oauth-authorization-server` |
| Dynamic client registration (RFC 7591) | `POST /oauth/register` |
| Authorization (consent page at `/oauth/consent`) | `GET /oauth/authorize` |
| Token: `authorization_code` with PKCE `S256`, `refresh_token` | `POST /oauth/token` |
| Revocation (RFC 7009) | `POST /oauth/revoke` |

- Clients are public (no secret) and register **loopback redirect URIs
  only**: `http://127.0.0.1`, `http://localhost` or `http://[::1]` with a
  plain path; any port is accepted at authorization time (RFC 8252), the
  host and path must match exactly.
- The token is bound to the MCP endpoint (`resource`, RFC 8707). The
  issuer, the endpoints and the resource all come from
  `instance.public_url`; changing it invalidates every OAuth token.
- Authorization responses carry `iss` (RFC 9207).
- One scope, `mcp`: exactly the tools below. `offline_access` is accepted;
  a refresh token is always issued.
- Codes live 60 seconds and work once; access tokens live an hour;
  refresh tokens rotate on every use and expire after 30 days unused.
  Presenting a used refresh token, or a code a second time, revokes the
  whole sign-in.
- Client ID metadata documents are not supported (see
  [security.md](security.md#oauth-sign-in-for-agents) for why); clients fall
  back to dynamic registration, as Claude Code does.

## What agents can do

| Tool | What it does |
|---|---|
| `list_connections` | Every connection: name, connector, description, schemas with table counts, max rows per change, approvals required. An unreachable database is listed with `schema_status: unavailable`. |
| `list_tables` | One connection's tables by schema, with column counts and primary keys. |
| `get_schema` | Columns, types, nullability and primary keys; one table, or up to 50 tables. |
| `find_tables` | Tables and columns whose names contain a word, across all connections. |
| `preview_change` | Validates statements and dry-runs them in a rolled-back transaction. Reports each statement's kind, table and affected row count, or what is wrong and where. Saves nothing. |
| `submit_change` | Creates and submits a change in one call. Returns its number, the affected row counts and the review URL. |
| `get_change` | Status, reviews and comments, approvals, and how execution went. For a step of a sequence, also the sequence and step. |
| `list_changes` | The token owner's changes. A sequence is listed once, as its current step, with `sequence`, `step` and `steps`. |
| `cancel_change` | Cancels one of the token owner's changes that has not run. Cancelling a step of a sequence stops the sequence. |
| `submit_sequence` | Submits 2 to 10 steps that run in order, each a change on its own connection (Postgres and MySQL mixed). Every step's SQL is validated now; only step 1 is dry-run and goes to review now. Returns the sequence number, each step's change number and the review URL. |
| `get_sequence` | Every step's number and status, which step is open, and, if it stopped, why and which steps were applied. |
| `cancel_sequence` | Stops one of the token owner's running sequences: the open step and every later step are cancelled. |

### Sequences

Use a sequence when a change must run only after another one has run, for
example copy a customer to the billing database, then mark it moved in the
application database. Each step is atomic on its own database; **the
sequence as a whole is not**. Steps are reviewed one at a time: when a step
has executed, the next one is dry-run against the data as it is then and
goes to review with the usual rules (a person other than the token's owner
approves, the preview must be fresh). If a step fails, is rejected, expires
or is cancelled, the sequence stops, the later steps are cancelled, and the
steps that already ran stay applied. Nothing is rolled back across
databases, so write each step so that stopping after it leaves the data in a
state a person can finish by hand.

## What agents cannot do

An agent signed in with OAuth is treated exactly like an API token of the
person who allowed it: the same tools, restrictions, rate limits and audit
events. "The token's owner" below means that person too.

- **Approve or run anything.** There is no tool for it, and the changes
  module refuses reviews made with a token. Changes an agent submits are
  authored by the token's owner, so the owner cannot approve them either.
- **See row data.** Agents get row counts, never row images or cell values,
  and database error messages (which can quote values) are reduced to their
  error code. A person reviewing the change sees the rows in the dashboard.
- **See other people's changes.** `get_change`, `list_changes`,
  `cancel_change`, `get_sequence` and `cancel_sequence` work only on changes
  and sequences the token's owner authored, even when the owner is an admin.
- **Run anything but row changes.** Only single-table `INSERT`, `UPDATE` and
  `DELETE` are accepted, and `UPDATE` and `DELETE` need a `WHERE` clause.

A token can reach every connection its owner can; per-token restrictions are
a likely next feature. Row counts for chosen `WHERE` clauses can reveal facts
about the data (for example whether a row with some email exists); this is
the same exposure as submitting a change, and is controlled by the rate
limits and the audit log.

## Retries, limits and the audit log

- Calling `submit_change` again with the same SQL while that change is open
  returns the same change with `already_existed: true`, so retrying after a
  timeout never creates a duplicate. `submit_sequence` does the same for a
  running sequence with the same steps (connections and SQL, in order).
- Each token may make 60 tool calls per minute, 30 `preview_change` calls per
  hour (each one is a real dry run with brief row locks on the database) and
  20 `submit_change` and `submit_sequence` calls per hour (together). Admins change these in
  **Settings → Limits**. A call over a limit returns an error saying when to
  try again.
  An agent signed in with OAuth has the same limits, counted per sign-in
  (connected app).
- Every tool call is recorded in the audit log as `mcp.tool_called`, with the
  tool, the agent's name, the connection, the change number and the
  statement count; its subject is the API token or, for OAuth, the
  connected app (`oauth_grant`). The SQL is recorded with the change itself.
  OAuth registrations, approvals, token issues and revocations are recorded
  as `oauth.*` events. A change shows the agent as "AGENT Claude Code via
  ada@example.com".
- Removing a person, revoking a token or disconnecting an app stops its
  agents on their next request. Turning off **Agents can sign in with
  OAuth** refuses every OAuth token at once; API tokens keep working.

## Suggested agent instructions

Add something like this to your project's agent instructions (for example
`CLAUDE.md` or `AGENTS.md`):

```markdown
## Production data changes

Never change production data directly. To change rows in a production
database, use the dbairlock MCP server: find the table with list_connections
and find_tables, check the SQL with preview_change, submit it with
submit_change, and give me the review link it returns. Someone else must
approve it before it runs. Do not poll get_change in a loop.
```
