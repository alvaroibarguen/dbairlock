import { Plus } from 'lucide-react'
import { Link } from 'react-router'
import { Button, EmptyState, useDocumentTitle } from '@/ui'
import { ChangeQueue, QueueCount } from './ChangeQueue'
import { ChangesLayout } from './ChangesLayout'

/** The queue header's way to start a change: labelled, in the primary colour. */
export function NewChangeButton() {
  return (
    <Button
      variant="primary"
      size="sm"
      render={<Link to="/changes/new" />}
      nativeButton={false}
      icon={<Plus aria-hidden="true" className="size-3.5" />}
    >
      New change
    </Button>
  )
}

/** /changes: the review queue, nothing selected yet. */
export function ChangesPage() {
  useDocumentTitle('Changes')
  return (
    <ChangesLayout
      queue={<ChangeQueue />}
      queueActions={
        <>
          <QueueCount />
          <NewChangeButton />
        </>
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
