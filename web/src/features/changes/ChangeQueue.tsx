import { useQuery } from '@tanstack/react-query'
import type { KeyboardEvent } from 'react'
import { Link, useSearchParams } from 'react-router'
import { type ChangeSummary, type ChangeView, changesQuery, isApiError } from '@/api'
import { Button, type ChangeStatus, cn, EmptyState, ErrorState, Skeleton, StatusDot } from '@/ui'
import { changeRef, isWaiting, shortAge, statementSummary } from './format'
import { useQueueView } from './hooks'
import { stepLabel } from './sequence'

const views: { value: ChangeView; label: string }[] = [
  { value: 'needs_my_review', label: 'Needs my review' },
  { value: 'mine', label: 'Mine' },
  { value: 'all', label: 'All' },
]

const empty: Record<ChangeView, string> = {
  needs_my_review: 'Nothing is waiting for your review.',
  mine: 'You have not submitted any changes yet.',
  all: 'No changes have been submitted yet.',
}

const edge: Partial<Record<ChangeStatus, string>> = {
  pending_review: 'shadow-[inset_2px_0_0_var(--amber)]',
  approved: 'shadow-[inset_2px_0_0_var(--green)]',
  executed: 'shadow-[inset_2px_0_0_var(--green)]',
  failed: 'shadow-[inset_2px_0_0_var(--red)]',
  rejected: 'shadow-[inset_2px_0_0_var(--red)]',
}

/** What the meta line says about a change's state. */
function stateText(c: ChangeSummary): string {
  if (isWaiting(c)) return `waiting · attempt ${c.attempts}`
  switch (c.status) {
    case 'draft':
      return 'draft'
    case 'pending_review':
    case 'approved':
      return statementSummary(c.statements)
    case 'failed':
      return c.status_code === 'drift' ? `drift · ${c.status_detail ?? ''}` : 'failed'
    default:
      return c.status.replace('_', ' ')
  }
}

/** Arrow keys move between queue rows. */
function onListKey(e: KeyboardEvent<HTMLUListElement>) {
  if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return
  const links = Array.from(e.currentTarget.querySelectorAll<HTMLAnchorElement>('a[data-row]'))
  const i = links.findIndex((l) => l === document.activeElement)
  const next = links[e.key === 'ArrowDown' ? i + 1 : Math.max(i - 1, 0)]
  if (next) {
    e.preventDefault()
    next.focus()
  }
}

export function QueueCount() {
  const list = useQuery(changesQuery('needs_my_review'))
  if (list.data === undefined) return null
  return (
    <span className="font-mono text-text normal-case tabular [font-variant:normal]">
      {list.data.needs_my_review} pending
    </span>
  )
}

/**
 * The review queue: tabs, then one row per change, newest first. A sequence
 * is one row, shown as its current step. `selected` is the open change's
 * number or id, `selectedSequence` the open sequence's.
 */
export function ChangeQueue({
  selected,
  selectedSequence,
}: {
  selected?: string
  selectedSequence?: string
}) {
  const view = useQueueView()
  const [, setParams] = useSearchParams()
  const list = useQuery(changesQuery(view))
  const nowMs = list.dataUpdatedAt

  return (
    <div className="flex flex-col">
      <div
        role="group"
        aria-label="Show"
        className="flex gap-1 overflow-x-auto border-b border-seam px-3 py-2.5"
      >
        {views.map((v) => (
          <button
            key={v.value}
            type="button"
            aria-pressed={view === v.value}
            onClick={() => setParams(v.value === 'needs_my_review' ? {} : { view: v.value })}
            className={cn(
              'flex h-6 shrink-0 cursor-pointer items-center gap-1.5 rounded-sm border px-2 text-xs whitespace-nowrap transition-colors duration-120 ease-out',
              view === v.value
                ? 'border-mute/40 bg-raise text-text'
                : 'border-seam text-dim hover:text-text',
            )}
          >
            {v.label}
            {v.value === 'needs_my_review' && list.data && view === v.value && (
              <span className="font-mono text-2xs text-mute tabular">
                {list.data.needs_my_review}
              </span>
            )}
          </button>
        ))}
      </div>
      {list.isPending ? (
        <div className="flex flex-col gap-2 p-4" aria-busy="true" aria-label="Loading changes">
          <Skeleton className="h-12 w-full" />
          <Skeleton className="h-12 w-full" />
          <Skeleton className="h-12 w-full" />
        </div>
      ) : list.isError ? (
        <ErrorState
          title="Changes could not be loaded."
          message={list.error.message}
          code={isApiError(list.error) ? list.error.code : undefined}
          action={<Button onClick={() => void list.refetch()}>Retry</Button>}
        />
      ) : list.data.changes.length === 0 ? (
        <EmptyState
          title={empty[view]}
          description={
            view === 'needs_my_review'
              ? 'Changes your teammates or their agents submit appear here.'
              : undefined
          }
          action={
            <Button variant="primary" render={<Link to="/changes/new" />} nativeButton={false}>
              New change
            </Button>
          }
        />
      ) : (
        <ul aria-label="Changes" className="flex flex-col" onKeyDown={onListKey}>
          {list.data.changes.map((c) => {
            const seq = c.sequence
            const isSel = seq
              ? selectedSequence === String(seq.number) || selectedSequence === seq.id
              : selected === String(c.number) || selected === c.id
            const agent = c.via === 'mcp'
            const search = view === 'needs_my_review' ? '' : `?view=${view}`
            return (
              <li key={c.id}>
                <Link
                  data-row
                  to={{
                    pathname: seq ? `/sequences/${seq.number}` : `/changes/${c.number}`,
                    search,
                  }}
                  aria-current={isSel ? 'page' : undefined}
                  className={cn(
                    'grid grid-cols-[10px_minmax(0,1fr)_auto] gap-x-2.5 gap-y-0.5 border-b border-seam px-4 pt-2.5 pb-[11px] transition-colors duration-120 ease-out',
                    'hover:bg-raise/60 focus-visible:-outline-offset-2',
                    isSel && cn('bg-raise', edge[c.status]),
                  )}
                >
                  <StatusDot status={c.status} className="mt-1.5" />
                  <span className="truncate text-text">{seq ? seq.title : c.title}</span>
                  <span className="font-mono text-xs text-mute tabular">
                    {changeRef(seq ? seq.number : c.number)}
                  </span>
                  <span className="col-start-2 col-end-4 flex min-w-0 items-center gap-2.5 font-mono text-[11.5px] text-mute tabular">
                    {seq && (
                      <span className="shrink-0 rounded-sm border border-seam px-1 text-dim">
                        {stepLabel(seq)}
                      </span>
                    )}
                    <span className="truncate">{c.connection.name}</span>
                    <span
                      className={cn(
                        'shrink-0',
                        c.status === 'failed' && 'text-red',
                        isWaiting(c) && 'text-amber',
                      )}
                    >
                      {stateText(c)}
                    </span>
                    {agent && (
                      <span className="shrink-0 text-2xs font-medium tracking-[0.04em] text-violet">
                        AGENT
                      </span>
                    )}
                    <span className="ml-auto shrink-0">
                      {shortAge(Date.parse(c.submitted_at ?? c.created_at), nowMs)}
                    </span>
                  </span>
                  <span className="sr-only">
                    {`${seq ? `, sequence, step ${seq.step} of ${seq.steps}` : ''}, ${c.status.replace('_', ' ')}${agent ? `, submitted by an agent of ${c.author.email}` : `, by ${c.author.email}`}`}
                  </span>
                </Link>
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}
