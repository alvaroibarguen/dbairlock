import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ChevronDown } from 'lucide-react'
import { type ReactNode, useId, useState } from 'react'
import { Link } from 'react-router'
import {
  type ChangeSummary,
  cancelSequence,
  isApiError,
  queryKeys,
  type Sequence,
  sequenceQuery,
} from '@/api'
import { Badge, Button, cn, ErrorState, PageHeader, Skeleton, toast } from '@/ui'
import { Timeline } from './Activity'
import { AuthorTag } from './AuthorTag'
import { ChangeDetail } from './ChangeDetail'
import { CancelDialog } from './ChangeDialogs'
import { changeRef, shortAge, statementSummary } from './format'
import { useConnectorInfo, useServerNow } from './hooks'
import { SEQUENCE_RULE, sequenceOutcome, stepState, type StepTone, stepTone } from './sequence'

const statusBadge: Record<Sequence['status'], { label: string; tone: 'amber' | 'green' | 'red' }> =
  {
    running: { label: 'Running', tone: 'amber' },
    completed: { label: 'Completed', tone: 'green' },
    stopped: { label: 'Stopped', tone: 'red' },
  }

const dotTone: Record<StepTone, string> = {
  green: 'bg-green',
  amber: 'bg-amber ring-4 ring-amber/20',
  red: 'bg-red',
  mute: 'bg-mute',
}

const textTone: Record<StepTone, string> = {
  green: 'text-green',
  amber: 'text-amber',
  red: 'text-red',
  mute: 'text-mute',
}

/**
 * A sequence: its steps as a vertical stepper, each expandable to the full
 * change detail. `selected` is the step (from 1) shown in the rows pane;
 * `onSelect` changes it.
 */
export function SequenceDetail({
  id,
  back,
  selected,
  onSelect,
}: {
  id: string
  back?: ReactNode
  selected?: number
  onSelect: (step: number) => void
}) {
  const query = useQuery(sequenceQuery(id))
  if (query.isPending) {
    return (
      <div className="flex flex-col gap-3 px-6 pt-5" aria-busy="true" aria-label="Loading sequence">
        <Skeleton className="h-4 w-48" />
        <Skeleton className="h-7 w-2/3" />
        <Skeleton className="h-32 w-full" />
      </div>
    )
  }
  if (query.isError) {
    const notFound = isApiError(query.error) && query.error.status === 404
    return (
      <>
        {back && (
          <PageHeader
            as="h1"
            title={
              <span className="flex items-center gap-2">
                {back}
                <span>Sequence</span>
              </span>
            }
          />
        )}
        <ErrorState
          className="flex-1 justify-center"
          title={notFound ? 'There is no such sequence.' : 'The sequence could not be loaded.'}
          message={notFound ? undefined : query.error.message}
          code={isApiError(query.error) ? query.error.code : undefined}
          action={
            notFound ? (
              <Button render={<Link to="/changes" />} nativeButton={false}>
                Back to the queue
              </Button>
            ) : (
              <Button onClick={() => void query.refetch()}>Retry</Button>
            )
          }
        />
      </>
    )
  }
  return (
    <Loaded
      seq={query.data}
      receivedAt={query.dataUpdatedAt}
      back={back}
      selected={selected ?? query.data.current_step}
      onSelect={onSelect}
    />
  )
}

function Loaded({
  seq,
  receivedAt,
  back,
  selected,
  onSelect,
}: {
  seq: Sequence
  receivedAt: number
  back?: ReactNode
  selected: number
  onSelect: (step: number) => void
}) {
  const queryClient = useQueryClient()
  const serverNow = useServerNow(seq.server_time, receivedAt)
  const [open, setOpen] = useState<Set<number>>(() => new Set([selected]))
  const [cancelOpen, setCancelOpen] = useState(false)
  const outcome = sequenceOutcome(seq)
  const badge = statusBadge[seq.status]

  const cancel = useMutation({
    mutationFn: (reason: string) => cancelSequence(seq.id, reason || undefined),
    onSuccess: (q) => {
      queryClient.setQueryData(sequenceQuery(seq.id).queryKey, q)
      void queryClient.invalidateQueries({ queryKey: queryKeys.changes.all })
      setCancelOpen(false)
      toast.success(`Sequence ${changeRef(q.number)} stopped.`)
    },
    onError: (err) =>
      toast.error(err instanceof Error ? err.message : 'The request did not complete. Try again.'),
  })

  const toggle = (step: number) => {
    const next = new Set(open)
    if (next.has(step)) {
      next.delete(step)
    } else {
      next.add(step)
      onSelect(step)
    }
    setOpen(next)
  }

  return (
    <div className="@container flex flex-col">
      <header className="border-b border-seam px-4 pt-[18px] pb-4 sm:px-6">
        <div className="flex items-center gap-2 font-mono text-xs text-mute tabular">
          {back}
          <span>{changeRef(seq.number)}</span>
          <span aria-hidden="true">/</span>
          <span>sequence of {seq.steps.length} steps</span>
        </div>
        <h1
          id="detail-title"
          className="mt-1.5 mb-3 text-2xl font-semibold tracking-[-0.01em] break-words text-text"
        >
          {seq.title}
        </h1>
        <div className="flex flex-wrap items-center gap-2 text-sm text-dim">
          <Badge tone={badge.tone}>{badge.label}</Badge>
          {seq.status === 'running' && (
            <Badge mono>
              step {seq.current_step} / {seq.steps.length}
            </Badge>
          )}
          <AuthorTag change={seq} />
          <span className="font-mono text-xs text-mute tabular">
            opened{' '}
            {shortAge(Date.parse(seq.created_at), serverNow) === 'now'
              ? 'just now'
              : `${shortAge(Date.parse(seq.created_at), serverNow)} ago`}
          </span>
        </div>
      </header>

      {seq.actions.cancel && (
        <div className="flex flex-wrap items-center gap-2 border-b border-seam px-4 py-3 sm:px-6">
          <span className="flex-1" />
          <span className="hidden text-xs text-mute @lg:inline">
            Steps already run stay applied
          </span>
          <Button variant="destructive" onClick={() => setCancelOpen(true)}>
            Cancel sequence
          </Button>
        </div>
      )}

      <div className="border-b border-seam px-4 py-3 sm:px-6">
        {outcome ? (
          <div
            role="status"
            className={cn(
              'flex flex-col gap-0.5 rounded-md border px-3 py-2.5',
              outcome.tone === 'green'
                ? 'border-green/35 bg-green/8 text-green'
                : 'border-red/35 bg-red/8 text-red',
            )}
          >
            <p className="text-base font-medium">{outcome.title}</p>
            <p className="text-base text-text">{outcome.body}</p>
          </div>
        ) : (
          <p className="rounded-md border border-seam bg-raise px-3 py-2.5 text-base text-dim">
            {SEQUENCE_RULE}
          </p>
        )}
      </div>

      {seq.reason && (
        <section className="px-4 pt-4 sm:px-6" aria-label="Reason">
          <h2 className="mb-2 text-base small-caps text-dim">Reason</h2>
          <p className="text-base whitespace-pre-wrap text-text">{seq.reason}</p>
        </section>
      )}

      <section className="px-4 pt-4 sm:px-6" aria-labelledby="steps-title">
        <h2
          id="steps-title"
          className="mb-2 flex items-baseline justify-between gap-3 text-base small-caps text-dim"
        >
          <span>Steps</span>
          <span className="truncate font-mono text-xs text-mute normal-case [font-variant:normal]">
            run in this order · one database each
          </span>
        </h2>
        <ol className="flex flex-col">
          {seq.steps.map((step, i) => (
            <Step
              key={step.id}
              seq={seq}
              step={step}
              index={i}
              last={i === seq.steps.length - 1}
              open={open.has(i + 1)}
              selected={selected === i + 1}
              onToggle={() => toggle(i + 1)}
            />
          ))}
        </ol>
      </section>

      <section className="px-4 pt-4 pb-6 sm:px-6" aria-label="Sequence activity">
        <h2 className="mb-2 text-base small-caps text-dim">Sequence activity</h2>
        <Timeline id={seq.id} sequence />
      </section>

      {seq.actions.cancel && (
        <CancelDialog
          open={cancelOpen}
          onOpenChange={setCancelOpen}
          number={seq.number}
          what="sequence"
          pending={cancel.isPending}
          onCancel={(reason) => cancel.mutate(reason)}
        />
      )}
    </div>
  )
}

/** One step of the stepper: its dot on the rail, a header button, and, open, the change. */
function Step({
  seq,
  step,
  index,
  last,
  open,
  selected,
  onToggle,
}: {
  seq: Sequence
  step: ChangeSummary
  index: number
  last: boolean
  open: boolean
  selected: boolean
  onToggle: () => void
}) {
  const connector = useConnectorInfo()(step.connection.connector)
  const current = seq.status === 'running' && seq.current_step === index + 1
  const tone = stepTone(step.status, current)
  const buttonId = useId()
  const panelId = useId()
  const state = stepState(seq, step, index)
  return (
    <li
      className={cn(
        'relative grid grid-cols-[20px_minmax(0,1fr)] gap-x-3',
        // The rail: a 1px seam from this dot down to the next.
        !last &&
          'before:absolute before:top-[22px] before:bottom-0 before:left-[9.5px] before:w-px before:bg-seam',
      )}
      aria-current={current ? 'step' : undefined}
    >
      <span className="flex justify-center pt-[15px]">
        <span aria-hidden="true" className={cn('size-[9px] rounded-full', dotTone[tone])} />
      </span>
      <div className={cn('min-w-0 pb-2', !last && 'pb-3')}>
        <h3>
          <button
            id={buttonId}
            type="button"
            aria-expanded={open}
            aria-controls={panelId}
            onClick={onToggle}
            className={cn(
              'grid w-full cursor-pointer grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 rounded-md border px-3 py-2 text-left transition-colors duration-120 ease-out',
              selected ? 'border-mute/40 bg-raise' : 'border-seam hover:bg-raise/60',
            )}
          >
            <span className="flex min-w-0 flex-col gap-0.5">
              <span className="flex min-w-0 flex-wrap items-center gap-x-2 font-mono text-xs text-mute tabular">
                <span className="text-dim">Step {index + 1}</span>
                <span>{changeRef(step.number)}</span>
                <span aria-hidden="true">/</span>
                <span className="truncate">{step.connection.name}</span>
                <span>{connector.name}</span>
              </span>
              <span className="truncate text-base text-text">{step.title}</span>
              <span className="flex min-w-0 flex-wrap items-center gap-x-2.5 font-mono text-[11.5px] tabular">
                <span className={textTone[tone]}>{state}</span>
                {step.statements.length > 0 && (
                  <span className="text-mute">{statementSummary(step.statements)}</span>
                )}
              </span>
            </span>
            <ChevronDown
              aria-hidden="true"
              className={cn(
                'size-4 text-mute transition-transform duration-160 ease-out motion-reduce:transition-none',
                open && 'rotate-180',
              )}
            />
          </button>
        </h3>
        {open && (
          <div
            id={panelId}
            role="region"
            aria-labelledby={buttonId}
            className="mt-2 overflow-hidden rounded-md border border-seam"
          >
            <ChangeDetail id={String(step.number)} embedded />
            <div className="border-t border-seam px-4 py-2.5 sm:px-6">
              <Link className="text-sm text-blue underline" to={`/changes/${step.number}`}>
                Open {changeRef(step.number)} on its own page
              </Link>
            </div>
          </div>
        )}
      </div>
    </li>
  )
}
