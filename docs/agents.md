# Connecting AI agents

dbairlock runs an MCP server at `/mcp` on the same address as the
dashboard, over streamable HTTP. An agent connected to it can find the right
database and table, check SQL, and submit a change for review. A person
approves the change in the dashboard before dbairlock runs it.

## Setup

1. In the dashboard, open **Settings → API tokens** and create a token. It
   starts with `dbal_pat_`, acts as you, and is shown only once. The
   **Connect an agent** page in the dashboard shows the commands below with
   your instance's address filled in.
2. Add dbairlock to the agent as an MCP server that sends the token as a
   bearer token.
3. Ask the agent for a data change. It gives you a review link; someone
   other than you approves it there, and dbairlock runs it.

### Claude Code

```sh
claude mcp add --transport http dbairlock https://dbairlock.example.com/mcp \
  --header "Authorization: Bearer dbal_pat_YOUR_TOKEN"
```

Add `--scope user` to make it available in every project, or
`--scope project` to write it to the project's `.mcp.json` (do not commit a
token: use a per-person scope instead).

### Other clients

Any MCP client that supports streamable HTTP with custom headers works.
The usual JSON configuration:

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
header; while the instance is still in setup mode, `/mcp` answers `503`.

## What agents can do

| Tool | What it does |
|---|---|
| `list_connections` | Every connection: name, connector, description, schemas with table counts, max rows per change, approvals required. An unreachable database is listed with `schema_status: unavailable`. |
| `list_tables` | One connection's tables by schema, with column counts and primary keys. |
| `get_schema` | Columns, types, nullability and primary keys; one table, or up to 50 tables. |
| `find_tables` | Tables and columns whose names contain a word, across all connections. |
| `preview_change` | Validates statements and dry-runs them in a rolled-back transaction. Reports each statement's kind, table and affected row count, or what is wrong and where. Saves nothing. |
| `submit_change` | Creates and submits a change in one call. Returns its number, the affected row counts and the review URL. |
| `get_change` | Status, reviews and comments, approvals, and how execution went. |
| `list_changes` | The token owner's changes. |
| `cancel_change` | Cancels one of the token owner's changes that has not run. |

## What agents cannot do

- **Approve or run anything.** There is no tool for it, and the changes
  module refuses reviews made with a token. Changes an agent submits are
  authored by the token's owner, so the owner cannot approve them either.
- **See row data.** Agents get row counts, never row images or cell values,
  and database error messages (which can quote values) are reduced to their
  error code. A person reviewing the change sees the rows in the dashboard.
- **See other people's changes.** `get_change`, `list_changes` and
  `cancel_change` work only on changes the token's owner authored, even when
  the owner is an admin.
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
  timeout never creates a duplicate.
- Each token may make 60 tool calls per minute, 30 `preview_change` calls per
  hour (each one is a real dry run with brief row locks on the database) and
  20 `submit_change` calls per hour. Admins change these in
  **Settings → Limits**. A call over a limit returns an error saying when to
  try again.
- Every tool call is recorded in the audit log as `mcp.tool_called`, with the
  tool, the connection, the change number and the statement count. The SQL is
  recorded with the change itself.
- Removing a person, or revoking a token, stops its agents on their next
  request.

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
