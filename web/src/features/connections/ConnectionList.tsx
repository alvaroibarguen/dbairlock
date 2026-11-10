import { Link } from 'react-router'
import type { Connection, Connector } from '@/api'
import { cn } from '@/ui'
import { configSummary, policySummary } from './connectionForm'
import { dotClass, testStatus } from './status'

export interface ConnectionListProps {
  connections: Connection[]
  connectors: Connector[]
  selectedId?: string
}

/**
 * Connections grouped by connector: name, address, status of the last test
 * and a policy summary. Every connector can have any number of connections.
 */
export function ConnectionList({ connections, connectors, selectedId }: ConnectionListProps) {
  const groups = connectors
    .map((c) => ({ connector: c, items: connections.filter((x) => x.connector === c.id) }))
    .filter((g) => g.items.length > 0)

  return (
    <div className="flex flex-col">
      {groups.map(({ connector, items }) => (
        <section key={connector.id} aria-labelledby={`group-${connector.id}`}>
          <h2
            id={`group-${connector.id}`}
            className="flex h-8 items-center justify-between border-b border-seam bg-bg/40 px-4 text-xs font-medium small-caps text-dim"
          >
            <span>{connector.display_name}</span>
            <span className="font-mono text-mute tabular">{items.length}</span>
          </h2>
          <ul>
            {items.map((c) => {
              const status = testStatus(c.last_test)
              const selected = c.id === selectedId
              return (
                <li key={c.id} className="border-b border-seam">
                  <Link
                    to={`/connections/${c.id}`}
                    aria-current={selected ? 'page' : undefined}
                    className={cn(
                      'flex flex-col gap-1 px-4 py-2.5 transition-colors duration-120 ease-out',
                      'hover:bg-raise/60 focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-green',
                      selected && 'bg-raise hover:bg-raise',
                    )}
                  >
                    <span className="flex items-center gap-2">
                      <span
                        aria-hidden="true"
                        className={cn(
                          'inline-block size-[7px] shrink-0 rounded-full',
                          dotClass[status.tone],
                        )}
                      />
                      <span className="min-w-0 truncate font-mono text-base text-text">
                        {c.name}
                      </span>
                      <span className="sr-only">, {status.label}</span>
                    </span>
                    <span className="truncate pl-[15px] font-mono text-xs text-mute">
                      {configSummary(connector, c.config) || connector.display_name}
                    </span>
                    <span className="truncate pl-[15px] text-xs text-dim tabular">
                      {policySummary(c.policy)}
                    </span>
                  </Link>
                </li>
              )
            })}
          </ul>
        </section>
      ))}
    </div>
  )
}
