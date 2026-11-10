import { EmptyState, PageHeader, Pane } from '@/ui'

/** /connections. Connection management arrives in phase 5. */
export function ConnectionsPage() {
  return (
    <Pane className="h-full">
      <PageHeader as="h1" title="Connections" />
      <EmptyState
        className="flex-1 justify-center"
        title="No database connections are configured."
        description="Adding Postgres and MySQL connections arrives in a later release."
      />
    </Pane>
  )
}
