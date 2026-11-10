import { Plus } from 'lucide-react'
import { Link } from 'react-router'
import { Button, EmptyState, Tooltip, useDocumentTitle } from '@/ui'
import { ChangeQueue, QueueCount } from './ChangeQueue'
import { ChangesLayout } from './ChangesLayout'

export function NewChangeButton() {
  return (
    <Tooltip content="New change">
      <Button
        variant="ghost"
        size="icon"
        className="-mr-2 size-8"
        aria-label="New change"
        render={<Link to="/changes/new" />}
        nativeButton={false}
        icon={<Plus aria-hidden="true" className="size-4" />}
      />
    </Tooltip>
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
