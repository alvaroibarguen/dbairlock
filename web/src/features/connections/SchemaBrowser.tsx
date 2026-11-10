import { useQuery } from '@tanstack/react-query'
import { ChevronRight, KeyRound } from 'lucide-react'
import { useState } from 'react'
import { connectionSchemaQuery, isApiError } from '@/api'
import { Button, EmptyState, ErrorState, Input, Skeleton } from '@/ui'

/**
 * The tables, columns and primary keys of a connection, read from the
 * target (cached for a minute). The same data feeds editor completion.
 */
export function SchemaBrowser({ connectionId }: { connectionId: string }) {
  const query = useQuery(connectionSchemaQuery(connectionId))
  const [filter, setFilter] = useState('')

  if (query.isPending) {
    return (
      <div className="flex flex-col gap-2 px-6 py-5" aria-busy="true" aria-label="Loading schema">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-6 w-full" />
        <Skeleton className="h-6 w-full" />
      </div>
    )
  }
  if (query.isError) {
    return (
      <ErrorState
        title="The schema could not be read."
        message={query.error.message}
        code={isApiError(query.error) ? query.error.code : undefined}
        action={<Button onClick={() => void query.refetch()}>Retry</Button>}
      />
    )
  }

  const q = filter.trim().toLowerCase()
  const namespaces = query.data.namespaces
    .map((ns) => ({
      ...ns,
      tables: ns.tables.filter(
        (t) =>
          q === '' ||
          t.name.toLowerCase().includes(q) ||
          t.columns.some((c) => c.name.toLowerCase().includes(q)),
      ),
    }))
    .filter((ns) => ns.tables.length > 0)
  const tableCount = query.data.namespaces.reduce((n, ns) => n + ns.tables.length, 0)

  return (
    <div className="flex flex-col gap-4 px-6 py-5">
      <div className="flex flex-wrap items-center gap-3">
        <Input
          type="search"
          aria-label="Filter tables and columns"
          placeholder="Filter tables and columns"
          value={filter}
          onChange={(e) => setFilter(e.currentTarget.value)}
          className="w-full max-w-72"
        />
        <span className="font-mono text-xs text-mute tabular">
          {tableCount} table{tableCount === 1 ? '' : 's'}
        </span>
      </div>
      {tableCount === 0 ? (
        <EmptyState
          className="px-0"
          title="The database user can see no tables here."
          description="Grant it SELECT, INSERT, UPDATE and DELETE on the tables changes should reach."
        />
      ) : namespaces.length === 0 ? (
        <p className="text-base text-dim">No table or column matches “{filter}”.</p>
      ) : (
        namespaces.map((ns) => (
          <section key={ns.name} aria-label={`Schema ${ns.name}`} className="flex flex-col gap-1.5">
            <h3 className="font-mono text-xs text-mute">{ns.name}</h3>
            <ul className="flex flex-col overflow-hidden rounded-md border border-seam">
              {ns.tables.map((t) => (
                <li key={t.name} className="border-b border-seam last:border-b-0">
                  <details className="group" open={q !== ''}>
                    <summary className="flex h-8 cursor-pointer list-none items-center gap-2 px-3 text-base hover:bg-raise/60 focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-green [&::-webkit-details-marker]:hidden">
                      <ChevronRight
                        aria-hidden="true"
                        className="size-3.5 shrink-0 text-mute transition-transform duration-120 group-open:rotate-90"
                      />
                      <span className="min-w-0 truncate font-mono text-text">{t.name}</span>
                      <span className="ml-auto font-mono text-xs text-mute tabular">
                        {t.columns.length} col{t.columns.length === 1 ? '' : 's'}
                        {t.primary_key.length === 0 && ' · no primary key'}
                      </span>
                    </summary>
                    <table className="w-full border-t border-seam bg-bg text-left">
                      <caption className="sr-only">Columns of {t.name}</caption>
                      <thead className="sr-only">
                        <tr>
                          <th scope="col">Column</th>
                          <th scope="col">Type</th>
                          <th scope="col">Nullable</th>
                        </tr>
                      </thead>
                      <tbody>
                        {t.columns.map((c) => (
                          <tr key={c.name} className="h-7">
                            <td className="w-1/2 py-1 pr-2 pl-9 font-mono text-sm text-text">
                              <span className="inline-flex items-center gap-1.5">
                                {c.name}
                                {t.primary_key.includes(c.name) && (
                                  <KeyRound aria-label="primary key" className="size-3 text-mute" />
                                )}
                              </span>
                            </td>
                            <td className="py-1 pr-2 font-mono text-xs text-dim">{c.type}</td>
                            <td className="py-1 pr-3 text-right text-xs text-mute">
                              {c.nullable ? 'null' : 'not null'}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </details>
                </li>
              ))}
            </ul>
          </section>
        ))
      )}
    </div>
  )
}
