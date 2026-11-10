// How MCP clients reach dbairlock: the endpoint and ready-to-paste client
// configuration, shared by the Connect an agent page and the token dialog.

/** Stands in for the API token in the configuration shown before one exists. */
export const TOKEN_PLACEHOLDER = 'dbal_pat_YOUR_TOKEN'

/** The MCP endpoint of the instance whose dashboard is at origin. */
export function mcpUrl(origin: string): string {
  return `${origin.replace(/\/+$/, '')}/mcp`
}

/** The Claude Code command that adds dbairlock as an HTTP MCP server. */
export function claudeCodeCommand(url: string, token: string = TOKEN_PLACEHOLDER): string {
  return `claude mcp add --transport http dbairlock ${url} \\\n  --header "Authorization: Bearer ${token}"`
}

/** A JSON MCP client configuration (the mcpServers shape). */
export function genericConfig(url: string, token: string = TOKEN_PLACEHOLDER): string {
  return JSON.stringify(
    {
      mcpServers: {
        dbairlock: { type: 'http', url, headers: { Authorization: `Bearer ${token}` } },
      },
    },
    null,
    2,
  )
}
