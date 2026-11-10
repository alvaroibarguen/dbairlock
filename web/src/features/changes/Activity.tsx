import { useQuery } from '@tanstack/react-query'
import { type Change, type ChangeEvent, changeEventsQuery, sequenceEventsQuery } from '@/api'
import { Badge, Button, cn, Skeleton } from '@/ui'
import { PersonTag } from './AuthorTag'
import { dateTime, rows } from './format'

const outdatedText: Record<string, string> = {
  sql_changed: 'outdated: the SQL changed',
  preview_changed: 'outdated: the rows changed',
  reviewer_removed: 'does not count: reviewer removed',
}

/** Every review, newest last, with approvals that no longer count marked. */
export function Reviews({ change }: { change: Change }) {
  if (change.reviews.length === 0) {
    return <p className="text-base text-dim">No reviews yet.</p>
  }
  return (
    <ul className="flex flex-col gap-2">
      {change.reviews.map((r) => (
        <li
          key={r.id}
          className={cn(
            'flex flex-col gap-1.5 rounded-md border border-seam px-3 py-2.5',
            r.decision === 'approve' && !r.counts && 'opacity-70',
          )}
        >
          <div className="flex flex-wrap items-center gap-2">
            <PersonTag person={r.reviewer} />
            {r.decision === 'approve' ? (
              <Badge tone={r.counts ? 'green' : 'neutral'}>Approved</Badge>
            ) : (
              <Badge tone="red">Rejected</Badge>
            )}
            {r.outdated && <Badge tone="amber">{outdatedText[r.outdated] ?? 'outdated'}</Badge>}
            <span className="ml-auto font-mono text-xs text-mute tabular">
              {dateTime(r.created_at)} · rev {r.revision}
            </span>
          </div>
          {r.comment && <p className="text-base whitespace-pre-wrap text-text">{r.comment}</p>}
        </li>
      ))}
    </ul>
  )
}

/** Execution attempts, newest last. */
export function Executions({ change }: { change: Change }) {
  if (change.executions.length === 0) return null
  return (
    <ul className="flex flex-col gap-1.5">
      {change.executions.map((e) => (
        <li key={e.id} className="flex flex-wrap items-baseline gap-x-3 gap-y-1 text-base">
          <span className="font-mono text-xs text-mute tabular">
            attempt {e.attempt} · {dateTime(e.started_at)}
          </span>
          <span
            className={cn(
              e.outcome === 'executed' && 'text-green',
              (e.outcome === 'failed' || e.outcome === 'interrupted') && 'text-red',
              e.outcome === 'not_started' && 'text-amber',
              e.outcome === 'running' && 'text-blue',
            )}
          >
            {e.outcome === 'executed'
              ? `Executed: ${e.results.map((r) => rows(r.preview.rows_affected)).join(', ')}`
              : e.outcome === 'not_started'
                ? 'Could not reach the database; nothing ran'
                : e.outcome === 'running'
                  ? 'Running'
                  : e.outcome === 'interrupted'
                    ? 'Interrupted; outcome unknown'
                    : 'Failed'}
          </span>
          {e.error_message && (
            <span className="basis-full font-mono text-xs break-words text-dim">
              {e.error_message}
            </span>
          )}
        </li>
      ))}
    </ul>
  )
}

function actorName(e: ChangeEvent): string {
  if (e.actor.via === 'system') return 'dbairlock'
  const who = e.actor.email ?? e.actor.user_id ?? 'someone'
  return e.actor.via === 'mcp' ? `${who}'s agent` : who
}

function str(v: unknown): string {
  return typeof v === 'string' ? v : typeof v === 'number' ? String(v) : ''
}

/** One timeline entry in words. Payload text is shown as plain text. */
function describe(e: ChangeEvent): string {
  const p = e.payload
  switch (e.action) {
    case 'change.created':
      return p.sequence
        ? `created it as step ${str(p.step)} of sequence #${str(p.sequence)}`
        : 'created the change'
    case 'change.submitted':
      if (p.automatic) {
        return p.preview_error
          ? `Previewed after step ${Number(p.step) - 1} ran, and submitted for review. The preview failed: ${str(p.preview_error)}`
          : `Previewed after step ${Number(p.step) - 1} ran, and submitted for review`
      }
      return 'submitted it for review'
    case 'change.edited':
      return `edited it (revision ${str(p.revision)})`
    case 'change.preview_refreshed':
      return p.ok === false
        ? `updated the preview, which failed: ${str(p.error)}`
        : p.data_changed
          ? 'updated the preview: the affected rows changed'
          : 'updated the preview: the rows are unchanged'
    case 'change.reviewed':
      return p.decision === 'approve' ? 'approved it' : `rejected it: ${str(p.comment)}`
    case 'change.approved':
      return `Approved: ${str(p.approvals)} of ${str(p.required)} approvals`
    case 'change.rejected':
      return 'Rejected'
    case 'change.cancelled':
      return p.code === 'author_removed'
        ? 'Cancelled: the author was removed'
        : p.code === 'connection_archived'
          ? 'Cancelled: the connection was archived'
          : p.code === 'sequence_stopped'
            ? `Not run: ${str(p.reason)}`
            : `cancelled it${str(p.reason) ? `: ${str(p.reason)}` : ''}`
    case 'change.expired':
      return 'Expired: the approval lifetime ended'
    case 'change.returned_to_review':
      return 'Back in review: approvals no longer reach the required number'
    case 'change.approvals_voided':
      return 'An approval stopped counting: its reviewer was removed'
    case 'change.execute_requested':
      return 'asked to execute it now'
    case 'change.retry_requested':
      return 'asked to try the database again now'
    case 'execution.started':
      return `Execution started (attempt ${str(p.attempt)})`
    case 'execution.succeeded':
      return 'Executed and committed'
    case 'execution.failed':
      return `Execution failed: ${str(p.message)}`
    case 'execution.deferred':
      return `Could not reach the database (attempt ${str(p.attempt)}); nothing ran`
    case 'execution.interrupted':
      return 'Interrupted by a restart: outcome unknown, verify manually'
    case 'sequence.created':
      return `created the sequence with ${list(p.steps).length} steps`
    case 'sequence.submitted':
      return `submitted it: step 1 (#${str(p.change)}) is in review`
    case 'sequence.advanced':
      return `Step ${str(p.executed_step)} (#${str(p.executed_change)}) executed; step ${str(p.next_step)} is next`
    case 'sequence.completed':
      return `Completed: all ${str(p.steps)} steps executed`
    case 'sequence.stopped': {
      const applied = list(p.applied_steps)
      return `${str(p.message)} ${
        applied.length > 0
          ? `Applied and not rolled back: step ${applied.map(str).join(', ')}.`
          : 'No step was applied.'
      }${str(p.reason) ? ` Reason: ${str(p.reason)}` : ''}`
    }
    default:
      return e.action
  }
}

function list(v: unknown): unknown[] {
  return Array.isArray(v) ? v : []
}

/** The timeline of a change, or of a sequence: a filtered view of the audit log. */
export function Timeline({ id, sequence = false }: { id: string; sequence?: boolean }) {
  const ofChange = useQuery({ ...changeEventsQuery(id), enabled: !sequence })
  const ofSequence = useQuery({ ...sequenceEventsQuery(id), enabled: sequence })
  const events = sequence ? ofSequence : ofChange
  if (events.isPending) return <Skeleton className="h-16 w-full" />
  if (events.isError) {
    return (
      <div role="alert" className="flex flex-wrap items-center gap-3">
        <p className="text-base text-red">
          The activity could not be loaded: {events.error.message}
        </p>
        <Button size="sm" onClick={() => void events.refetch()}>
          Retry
        </Button>
      </div>
    )
  }
  if (events.data.length === 0) return <p className="text-base text-dim">No activity yet.</p>
  return (
    <ol className="flex flex-col">
      {events.data.map((e) => {
        const text = describe(e)
        // Sentences that start with a capital stand alone; the rest follow
        // the actor ("ada@example.com approved it").
        const standalone = e.actor.via === 'system' || /^[A-Z]/.test(text)
        return (
          <li
            key={e.seq}
            className="grid grid-cols-1 gap-x-3 border-l border-seam py-1.5 pl-3 text-base sm:grid-cols-[auto_minmax(0,1fr)]"
          >
            <span className="font-mono text-xs text-mute tabular">{dateTime(e.occurred_at)}</span>
            <span className="min-w-0 break-words text-dim">
              {!standalone && (
                <span className={e.actor.via === 'mcp' ? 'text-violet' : 'text-text'}>
                  {actorName(e)}{' '}
                </span>
              )}
              {text}
            </span>
          </li>
        )
      })}
    </ol>
  )
}
