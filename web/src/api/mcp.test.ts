import { describe, expect, it } from 'vitest'
import {
  claudeCodeCommand,
  claudeCodeOAuthCommand,
  genericConfig,
  genericOAuthConfig,
  mcpUrl,
} from './mcp'

describe('agent configuration', () => {
  it('builds the endpoint from the dashboard origin', () => {
    expect(mcpUrl('https://dbairlock.example.com')).toBe('https://dbairlock.example.com/mcp')
    expect(mcpUrl('http://localhost:8080/')).toBe('http://localhost:8080/mcp')
  })

  it('writes the Claude Code command with the token header', () => {
    expect(claudeCodeCommand('https://db.example.com/mcp', 'dbal_pat_abc')).toBe(
      'claude mcp add --transport http dbairlock https://db.example.com/mcp \\\n' +
        '  --header "Authorization: Bearer dbal_pat_abc"',
    )
  })

  it('writes the OAuth configuration without any token', () => {
    expect(claudeCodeOAuthCommand('https://db.example.com/mcp')).toBe(
      'claude mcp add --transport http dbairlock https://db.example.com/mcp',
    )
    expect(JSON.parse(genericOAuthConfig('https://db.example.com/mcp'))).toEqual({
      mcpServers: { dbairlock: { type: 'http', url: 'https://db.example.com/mcp' } },
    })
  })

  it('writes a generic mcpServers configuration', () => {
    expect(JSON.parse(genericConfig('https://db.example.com/mcp', 'dbal_pat_abc'))).toEqual({
      mcpServers: {
        dbairlock: {
          type: 'http',
          url: 'https://db.example.com/mcp',
          headers: { Authorization: 'Bearer dbal_pat_abc' },
        },
      },
    })
  })
})
