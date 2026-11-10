import { useQuery } from '@tanstack/react-query'
import { ArrowLeft } from 'lucide-react'
import { Link, useParams, useSearchParams } from 'react-router'
import { changeQuery, sequenceQuery } from '@/api'
import { Button, EmptyState, MEDIUM, Skeleton, useDocumentTitle, useMediaQuery } from '@/ui'
import { AffectedRows, RowsLegend } from './AffectedRows'
import { ChangeQueue, QueueCount } from './ChangeQueue'
import { ChangesLayout } from './ChangesLayout'
import { changeRef } from './format'
import { useQueueView } from './hooks'
import { SequenceDetail } from './SequenceDetail'

/** The rows of the selected step (from 1), or of the current one. */
function RowsPane({ id, step }: { id: string; step?: number }) {
  const seq = useQuery(sequenceQuery(id))
  const n = step ?? seq.data?.current_step
  const stepChange = seq.data?.steps[(n ?? 1) - 1]
  const change = useQuery({
    ...changeQuery(String(stepChange?.number ?? '')),
    enabled: stepChange !== undefined,
  })
  if (seq.isPending || (stepChange && change.isPending)) return <Skeleton className="m-4 h-32" />
  if (seq.isError || change.isError || !stepChange || !change.data) {
    return <EmptyState title="The step could not be loaded, so there are no rows to show." />
  }
  const waiting = change.data.status === 'draft'
  return (
    <div className="flex flex-col">
      <p className="border-b border-seam px-4 py-2.5 font-mono text-xs text-dim tabular">
        Step {n} · {changeRef(stepChange.number)} · {stepChange.connection.name}
      </p>
      {waiting ? (
        <EmptyState
          title={`Step ${n} has no preview yet.`}
          description={`It is previewed after step ${(n ?? 1) - 1} has run, because its rows may depend on it.`}
        />
      ) : (
        <AffectedRows change={change.data} />
      )}
    </div>
  )
}

/** /sequences/:id: the queue, the sequence's stepper, and the selected step's rows. */
export function SequenceDetailPage() {
  const { id = '' } = useParams()
  const [params, setParams] = useSearchParams()
  useDocumentTitle(`Sequence #${id}`)
  const medium = useMediaQuery(MEDIUM, true)
  const view = useQueueView()
  const stepParam = Number(params.get('step'))
  const step = Number.isInteger(stepParam) && stepParam > 0 ? stepParam : undefined
  const select = (n: number) => {
    const next = new URLSearchParams(params)
    next.set('step', String(n))
    setParams(next, { replace: true })
  }
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
      title="Sequence"
      mobileFocus="detail"
      queue={<ChangeQueue selectedSequence={id} />}
      queueActions={<QueueCount />}
      detail={<SequenceDetail key={id} id={id} back={back} selected={step} onSelect={select} />}
      rows={<RowsPane id={id} step={step} />}
      rowsActions={<RowsLegend />}
    />
  )
}
