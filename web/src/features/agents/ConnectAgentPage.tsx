import { KeyRound } from 'lucide-react'
import type { ReactNode } from 'react'
import { Link } from 'react-router'
import { Button, CodeBlock, PageHeader, Pane } from '@/ui'
import { claudeCodeCommand, genericConfig, mcpUrl, TOKEN_PLACEHOLDER } from '@/api/mcp'

/** /agents: how to connect an AI agent to dbairlock over MCP. */
export function ConnectAgentPage() {
  const url = mcpUrl(window.location.origin)
  return (
    <Pane className="h-full">
      <PageHeader
        as="h1"
        title="Connect an agent"
        actions={
          <Button
            size="sm"
            variant="primary"
            icon={<KeyRound aria-hidden="true" className="size-3.5" />}
            render={<Link to="/settings/api-tokens" />}
            nativeButton={false}
          >
            Create a token
          </Button>
        }
      />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="flex max-w-3xl flex-col gap-8 px-6 py-6">
          <ol className="flex list-decimal flex-col gap-1.5 pl-5 text-base text-dim marker:font-mono marker:text-mute">
            <li>
              <Link className="text-blue underline" to="/settings/api-tokens">
                Create an API token
              </Link>{' '}
              in Settings. It acts as you, and is shown once.
            </li>
            <li>Add dbairlock to your agent as an MCP server with the token, as shown below.</li>
            <li>
              Ask the agent for a data change. It submits the change for review and gives you the
              link; another person approves it before it runs.
            </li>
          </ol>

          <Section
            title="Claude Code"
            hint={
              <>
                Run in a terminal, replacing <code className="font-mono">{TOKEN_PLACEHOLDER}</code>{' '}
                with your token. Add <code className="font-mono">--scope user</code> to use it in
                every project.
              </>
            }
          >
            <CodeBlock copyLabel="Copy command">{claudeCodeCommand(url)}</CodeBlock>
          </Section>

          <Section
            title="Other MCP clients"
            hint="Any client that supports streamable HTTP with headers, for example in .mcp.json or the client's MCP settings."
          >
            <CodeBlock copyLabel="Copy configuration">{genericConfig(url)}</CodeBlock>
          </Section>

          <Section title="What agents can do">
            <ul className="flex list-disc flex-col gap-1 pl-5 text-base text-dim">
              <li>List connections and read schemas: tables, columns, types and keys.</li>
              <li>
                Preview and submit single-table INSERT, UPDATE and DELETE changes. They see row
                counts, never row data.
              </li>
              <li>Follow and cancel the changes submitted with your token.</li>
              <li>They cannot approve or run anything. Changes they submit are authored by you.</li>
            </ul>
          </Section>
        </div>
      </div>
    </Pane>
  )
}

function Section({
  title,
  hint,
  children,
}: {
  title: string
  hint?: ReactNode
  children: ReactNode
}) {
  return (
    <section className="flex flex-col gap-2">
      <h2 className="text-lg font-medium text-text">{title}</h2>
      {hint && <p className="text-base text-dim">{hint}</p>}
      {children}
    </section>
  )
}
