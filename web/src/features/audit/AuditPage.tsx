import { EmptyState, PageHeader, Pane } from '@/ui'

/** /audit. The hash-chained audit log arrives in phase 9. */
export function AuditPage() {
  return (
    <Pane className="h-full">
      <PageHeader as="h1" title="Audit log" />
      <EmptyState
        className="flex-1 justify-center"
        title="No audit events are recorded yet."
        description="Every change, approval and setting update will be listed here."
      />
    </Pane>
  )
}
