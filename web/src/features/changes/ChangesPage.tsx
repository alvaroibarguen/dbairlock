import { EmptyState, useDocumentTitle } from '@/ui'
import { ChangeQueue, QueueCount } from './ChangeQueue'
import { ChangesLayout } from './ChangesLayout'

/** /changes: the review queue, nothing selected yet. */
export function ChangesPage() {
  useDocumentTitle('Changes')
  return (
    <ChangesLayout
      queue={<ChangeQueue />}
      queueActions={<QueueCount />}
      detail={
        <EmptyState
          className="xl:h-full xl:justify-center"
          title="Select a change from the queue to review it."
        />
      }
      rows={
        <EmptyState
          title="Affected rows appear here once a change is selected."
          description="Each row is shown before and after the change."
        />
      }
    />
  )
}
