import { useQuery } from '@tanstack/react-query'
import { type Change, type ChangeEvent, changeEventsQuery } from '@/api'
import { Badge, cn, Skeleton } from '@/ui'
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
      return 'created the change'
    case 'change.submitted':
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
    default:
      return e.action
  }
}

/** The change's timeline: a filtered view of the audit log. */
export function Timeline({ id }: { id: string }) {
  const events = useQuery(changeEventsQuery(id))
  if (events.isPending) return <Skeleton className="h-16 w-full" />
  if (events.isError) return <p className="text-base text-red">{events.error.message}</p>
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
