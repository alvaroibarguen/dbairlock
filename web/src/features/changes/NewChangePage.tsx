import { PageHeader, Pane, EmptyState } from '@/ui'

/** /changes/new. The SQL editor arrives in phase 7. */
export function NewChangePage() {
  return (
    <Pane className="h-full">
      <PageHeader as="h1" title="New change" />
      <EmptyState
        className="flex-1 justify-center"
        title="Submitting changes is not available yet."
        description="The SQL editor and preview arrive with the changes API."
      />
    </Pane>
  )
}
