import { useQuery } from '@tanstack/react-query'
import { ArrowLeft } from 'lucide-react'
import { Link, useParams } from 'react-router'
import { changeQuery } from '@/api'
import { Button, EmptyState, MEDIUM, Skeleton, useMediaQuery } from '@/ui'
import { AffectedRows, RowsLegend } from './AffectedRows'
import { ChangeDetail } from './ChangeDetail'
import { ChangeQueue, QueueCount } from './ChangeQueue'
import { ChangesLayout } from './ChangesLayout'
import { NewChangeButton } from './ChangesPage'
import { useQueueView } from './hooks'

function RowsPane({ id }: { id: string }) {
  const query = useQuery(changeQuery(id))
  if (query.isPending) return <Skeleton className="m-4 h-32" />
  if (query.isError) return <EmptyState title="No affected rows to show." />
  return <AffectedRows change={query.data} />
}

/** /changes/:id: the queue, the change under review, and its affected rows. */
export function ChangeDetailPage() {
  const { id = '' } = useParams()
  const medium = useMediaQuery(MEDIUM, true)
  const view = useQueueView()
  const back = !medium && (
    <Button
      variant="ghost"
      size="icon"
      className="-my-2 -ml-2 size-8"
      aria-label="Back to the queue"
      render={<Link to={view === 'needs_my_review' ? '/changes' : `/changes?view=${view}`} />}
      nativeButton={false}
      icon={<ArrowLeft aria-hidden="true" className="size-4" />}
    />
  )
  return (
    <ChangesLayout
      mobileFocus="detail"
      queue={<ChangeQueue selected={id} />}
      queueActions={
        <>
          <QueueCount />
          <NewChangeButton />
        </>
      }
      detail={<ChangeDetail key={id} id={id} back={back} />}
      rows={<RowsPane id={id} />}
      rowsActions={<RowsLegend />}
    />
  )
}
