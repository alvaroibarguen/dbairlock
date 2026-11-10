import { Link } from 'react-router'
import { Button, EmptyState } from '@/ui'
import { ChangesLayout } from './ChangesLayout'

/** /changes. The queue is empty until the changes API exists (phase 7). */
export function ChangesPage() {
  return (
    <ChangesLayout
      queue={
        <EmptyState
          title="No changes are waiting for review."
          description="Changes submitted by your team or their agents appear here."
          action={
            <Button variant="primary" render={<Link to="/changes/new" />} nativeButton={false}>
              New change
            </Button>
          }
        />
      }
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
