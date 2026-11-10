import { useQuery } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { Link } from 'react-router'
import { auditEventQuery, type AuditEvent } from '@/api/audit'
import { Badge, CodeBlock, Drawer, Skeleton } from '@/ui'
import { actionLabel } from './actionLabels'
import { stamp, subjectPath } from './format'

export interface EventDrawerProps {
  /** The event to show; the drawer is closed when undefined. */
  seq: number | undefined
  /** The event when it is on the current page, so no request is needed. */
  fromPage: AuditEvent | undefined
  onClose: () => void
}

/**
 * The full event: who, what, when, the payload and the hashes. The payload
 * is shown as JSON text only; nothing in it is ever interpreted as markup.
 */
export function EventDrawer({ seq, fromPage, onClose }: EventDrawerProps) {
  const fetched = useQuery({
    ...auditEventQuery(seq ?? 0),
    enabled: seq !== undefined && fromPage === undefined,
  })
  const event = fromPage ?? fetched.data ?? undefined

  return (
    <Drawer
      open={seq !== undefined}
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
      title={seq !== undefined ? `Event #${seq}` : 'Event'}
      className="w-[560px]"
    >
      {event ? (
        <EventDetail event={event} />
      ) : fetched.isPending && seq !== undefined && fromPage === undefined ? (
        <div className="flex flex-col gap-3 p-4" aria-busy="true" aria-label="Loading">
          <Skeleton className="w-2/3" />
          <Skeleton className="h-24 w-full" />
        </div>
      ) : (
        <p className="p-4 text-base text-dim">
          {fetched.isError ? fetched.error.message : `There is no event #${seq ?? ''}.`}
        </p>
      )}
    </Drawer>
  )
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-xs text-mute">{label}</dt>
      <dd className="min-w-0 font-mono text-sm break-all text-text tabular">{children}</dd>
    </>
  )
}

function EventDetail({ event: e }: { event: AuditEvent }) {
  const path = subjectPath(e)
  const payload = JSON.stringify(e.payload, null, 2)
  return (
    <div className="flex flex-col gap-5 p-4">
      <div className="flex flex-col gap-1">
        <h3 className="text-[15px] font-semibold text-text">{actionLabel(e.action)}</h3>
        <span className="font-mono text-xs text-mute">{e.action}</span>
      </div>
      <dl className="grid grid-cols-[96px_minmax(0,1fr)] gap-x-3 gap-y-2">
        <Row label="Time">
          {stamp(e.occurred_at)}
          <span className="block text-xs text-mute">{e.occurred_at}</span>
        </Row>
        <Row label="Actor">
          {e.actor.email ?? e.actor.user_id ?? (e.actor.via === 'system' ? 'dbairlock' : '—')}
          {e.actor.email && e.actor.user_id && (
            <span className="block text-xs text-mute">{e.actor.user_id}</span>
          )}
        </Row>
        <Row label="Channel">
          {e.actor.via === 'mcp' ? (
            <span className="inline-flex items-center gap-2">
              <span className="rounded-[3px] bg-violet/8 px-1.5 text-2xs font-medium tracking-[0.04em] text-violet">
                AGENT
              </span>
              mcp
            </span>
          ) : (
            e.actor.via
          )}
        </Row>
        {e.actor.api_token_id && (
          <Row label="Agent credential">
            {e.actor.api_token_id}
            <span className="block text-xs text-mute">API token or connected app</span>
          </Row>
        )}
        {e.actor.ip && <Row label="IP address">{e.actor.ip}</Row>}
        <Row label="Subject">
          {e.subject_type || e.subject_id ? (
            <>
              {e.subject_type && <Badge mono>{e.subject_type}</Badge>}{' '}
              {path ? (
                <Link to={path} className="text-blue hover:underline">
                  {e.subject_id}
                </Link>
              ) : (
                e.subject_id
              )}
            </>
          ) : (
            '—'
          )}
        </Row>
        <Row label="Event id">{e.id}</Row>
      </dl>
      <section aria-labelledby="payload-title" className="flex flex-col gap-2">
        <h4 id="payload-title" className="text-base font-medium small-caps text-dim">
          Payload
        </h4>
        <CodeBlock copyLabel="Copy payload">{payload}</CodeBlock>
      </section>
      <section aria-labelledby="hashes-title" className="flex flex-col gap-2">
        <h4 id="hashes-title" className="text-base font-medium small-caps text-dim">
          Hash chain
        </h4>
        <dl className="grid grid-cols-[96px_minmax(0,1fr)] gap-x-3 gap-y-2">
          <Row label="Previous">{e.prev_hash}</Row>
          <Row label="This event">{e.hash}</Row>
        </dl>
        <p className="text-xs text-mute">
          Each hash covers the previous one, so editing or deleting an event breaks every link after
          it. An admin can check the whole chain in Settings, System.
        </p>
      </section>
    </div>
  )
}
