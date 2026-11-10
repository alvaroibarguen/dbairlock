import { useQuery } from '@tanstack/react-query'
import { Download, ListFilter } from 'lucide-react'
import { type SyntheticEvent, useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { can, isApiError, meQuery } from '@/api'
import {
  AUDIT_PAGE_SIZE,
  auditEventsQuery,
  auditExportUrl,
  type AuditEvent,
  type AuditFilters,
  type AuditVia,
} from '@/api/audit'
import {
  Button,
  cn,
  EmptyState,
  ErrorState,
  FormField,
  Input,
  MEDIUM,
  PageHeader,
  Pane,
  Select,
  Skeleton,
  Table,
  TBody,
  Td,
  Th,
  THead,
  Tr,
  useDocumentTitle,
  useMediaQuery,
} from '@/ui'
import { actionLabel, actionLabels, actionPrefixes } from './actionLabels'
import { EventDrawer } from './EventDrawer'
import { actorLabel, localToIso, stamp, subjectPath } from './format'

const viaOptions = [
  { value: 'any', label: 'Any channel' },
  { value: 'web', label: 'Web' },
  { value: 'mcp', label: 'MCP (agents)' },
  { value: 'cli', label: 'CLI' },
  { value: 'system', label: 'System' },
] as const

const subjectTypes = ['change', 'connection', 'user', 'api_token', 'notification', 'secret']

/** The URL keys that hold filters. Keeping them in the URL makes a view shareable. */
const FILTER_KEYS = ['actor', 'action', 'subject_type', 'subject_id', 'via', 'from', 'to'] as const

function isVia(v: string | null): v is AuditVia {
  return v === 'web' || v === 'mcp' || v === 'cli' || v === 'system'
}

/** The API filters from the URL. `from` and `to` are local datetime-local values there. */
function filtersFrom(params: URLSearchParams): AuditFilters {
  const f: AuditFilters = {}
  const text = (k: 'actor' | 'action' | 'subject_type' | 'subject_id') => {
    const v = params.get(k)?.trim()
    if (v) f[k] = v
  }
  text('actor')
  text('action')
  text('subject_type')
  text('subject_id')
  const via = params.get('via')
  if (isVia(via)) f.via = via
  const from = localToIso(params.get('from'))
  const to = localToIso(params.get('to'))
  if (from) f.from = from
  if (to) f.to = to
  return f
}

function positiveInt(v: string | null): number | undefined {
  const n = Number(v)
  return Number.isInteger(n) && n > 0 ? n : undefined
}

/** /audit: the hash-chained audit log, filterable, newest first. */
export function AuditPage() {
  useDocumentTitle('Audit log')
  const [params, setParams] = useSearchParams()
  const medium = useMediaQuery(MEDIUM, true)
  const [showFilters, setShowFilters] = useState(false)
  const me = useQuery(meQuery())
  const isAdmin = can(me.data, 'settings.manage')

  const filters = filtersFrom(params)
  const before = positiveInt(params.get('before'))
  const selected = positiveInt(params.get('event'))
  const query = useQuery(auditEventsQuery(filters, before))
  const active = FILTER_KEYS.filter((k) => params.get(k)).length

  const update = (fn: (p: URLSearchParams) => void, replace = false) => {
    const next = new URLSearchParams(params)
    fn(next)
    setParams(next, { replace })
  }
  const setFilter = (key: (typeof FILTER_KEYS)[number], value: string) =>
    update((p) => {
      if (value) p.set(key, value)
      else p.delete(key)
      p.delete('before')
    })
  const applyForm = (e: SyntheticEvent<HTMLFormElement>) => {
    e.preventDefault()
    const data = new FormData(e.currentTarget)
    update((p) => {
      for (const key of ['actor', 'action', 'subject_type', 'subject_id', 'from', 'to'] as const) {
        const v = data.get(key)
        const s = typeof v === 'string' ? v.trim() : ''
        if (s) p.set(key, s)
        else p.delete(key)
      }
      p.delete('before')
    })
  }
  const openEvent = (seq: number | undefined) =>
    update((p) => {
      if (seq === undefined) p.delete('event')
      else p.set('event', String(seq))
    })

  const events = query.data?.events ?? []
  const nextBefore = query.data?.next_before
  const exportHref = auditExportUrl(filters.from, filters.to)

  const filterForm = (
    <form
      // Keyed by the URL so Back and shared links refill the inputs.
      key={params.toString()}
      onSubmit={applyForm}
      aria-label="Filters"
      className="grid grid-cols-1 gap-3 border-b border-seam px-4 py-3 sm:grid-cols-2 lg:grid-cols-4"
    >
      <FormField label="Actor">
        <Input
          name="actor"
          mono
          defaultValue={params.get('actor') ?? ''}
          placeholder="email or user id"
          spellCheck={false}
        />
      </FormField>
      <FormField label="Action">
        <Input
          name="action"
          mono
          list="audit-actions"
          defaultValue={params.get('action') ?? ''}
          placeholder="change. or change.approved"
          spellCheck={false}
        />
      </FormField>
      <datalist id="audit-actions">
        {actionPrefixes.map((p) => (
          <option key={p.value} value={p.value}>
            {p.label}
          </option>
        ))}
        {Object.entries(actionLabels).map(([value, label]) => (
          <option key={value} value={value}>
            {label}
          </option>
        ))}
      </datalist>
      <FormField label="Subject">
        <div className="flex gap-2">
          <Input
            name="subject_type"
            mono
            list="audit-subjects"
            aria-label="Subject type"
            defaultValue={params.get('subject_type') ?? ''}
            placeholder="type"
            className="w-28 shrink-0"
            spellCheck={false}
          />
          <Input
            name="subject_id"
            mono
            aria-label="Subject id"
            defaultValue={params.get('subject_id') ?? ''}
            placeholder="id"
            spellCheck={false}
          />
        </div>
      </FormField>
      <datalist id="audit-subjects">
        {subjectTypes.map((s) => (
          <option key={s} value={s} />
        ))}
      </datalist>
      <FormField label="Channel">
        <Select
          aria-label="Channel"
          options={viaOptions}
          value={isVia(params.get('via')) ? (params.get('via') as AuditVia) : 'any'}
          onValueChange={(v) => setFilter('via', v === 'any' ? '' : v)}
        />
      </FormField>
      <FormField label="From">
        <Input
          name="from"
          type="datetime-local"
          step={1}
          mono
          defaultValue={params.get('from') ?? ''}
        />
      </FormField>
      <FormField label="Before">
        <Input
          name="to"
          type="datetime-local"
          step={1}
          mono
          defaultValue={params.get('to') ?? ''}
        />
      </FormField>
      <div className="flex items-end gap-2 sm:col-span-2">
        <Button type="submit" variant="primary">
          Apply
        </Button>
        <Button
          variant="ghost"
          disabled={active === 0}
          onClick={() => setParams(new URLSearchParams())}
        >
          Clear
        </Button>
      </div>
    </form>
  )

  return (
    <Pane className={cn(medium ? 'h-full' : 'flex-1')} aria-labelledby="audit-title">
      <PageHeader
        as="h1"
        title={<span id="audit-title">Audit log</span>}
        actions={
          <>
            {!medium && (
              <Button
                size="sm"
                variant="ghost"
                aria-expanded={showFilters}
                aria-controls="audit-filters"
                icon={<ListFilter aria-hidden="true" className="size-3.5" />}
                onClick={() => setShowFilters((s) => !s)}
              >
                Filters{active > 0 ? ` (${active})` : ''}
              </Button>
            )}
            {isAdmin && (
              <Button
                size="sm"
                icon={<Download aria-hidden="true" className="size-3.5" />}
                render={<a href={exportHref} download="dbairlock-audit.ndjson" />}
                nativeButton={false}
              >
                Export
              </Button>
            )}
          </>
        }
      />
      <div id="audit-filters" className={cn(!medium && !showFilters && 'hidden')}>
        {filterForm}
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto">
        {query.isPending ? (
          <div className="flex flex-col gap-2 px-4 py-4" aria-busy="true" aria-label="Loading">
            {Array.from({ length: 6 }, (_, i) => (
              <Skeleton key={i} className="h-6 w-full" />
            ))}
          </div>
        ) : query.isError ? (
          <ErrorState
            title="The audit log could not be loaded."
            message={query.error.message}
            code={isApiError(query.error) ? query.error.code : undefined}
            action={<Button onClick={() => void query.refetch()}>Retry</Button>}
          />
        ) : events.length === 0 ? (
          <EmptyState
            title={
              active > 0 || before
                ? 'No events match these filters.'
                : 'No audit events are recorded yet.'
            }
            description={active > 0 ? 'Widen the filters or clear them.' : undefined}
            action={
              active > 0 ? (
                <Button onClick={() => setParams(new URLSearchParams())}>Clear filters</Button>
              ) : undefined
            }
          />
        ) : medium ? (
          <EventTable events={events} selected={selected} onOpen={openEvent} />
        ) : (
          // Below 768px the table becomes a list, so the action stays in view.
          <EventList events={events} selected={selected} onOpen={openEvent} />
        )}
      </div>
      <nav
        aria-label="Pages"
        className="flex h-11 shrink-0 items-center justify-between gap-3 border-t border-seam px-4"
      >
        <span className="font-mono text-xs text-mute tabular">
          {events.length > 0
            ? `#${events[0]?.seq} – #${events[events.length - 1]?.seq} · ${events.length} of up to ${AUDIT_PAGE_SIZE}`
            : ''}
        </span>
        <div className="flex gap-2">
          <Button
            size="sm"
            variant="ghost"
            disabled={!before}
            onClick={() => update((p) => p.delete('before'))}
          >
            Newest
          </Button>
          <Button
            size="sm"
            disabled={nextBefore === undefined}
            onClick={() => update((p) => p.set('before', String(nextBefore)))}
          >
            Older
          </Button>
        </div>
      </nav>
      <EventDrawer
        seq={selected}
        fromPage={events.find((e) => e.seq === selected)}
        onClose={() => openEvent(undefined)}
      />
    </Pane>
  )
}

function EventTable({
  events,
  selected,
  onOpen,
}: {
  events: AuditEvent[]
  selected: number | undefined
  onOpen: (seq: number) => void
}) {
  return (
    <Table aria-label="Audit events">
      <THead>
        <Tr>
          <Th className="w-16 pl-4 text-right">Seq</Th>
          <Th>Time</Th>
          <Th>Actor</Th>
          <Th>Channel</Th>
          <Th>Action</Th>
          <Th className="pr-4">Subject</Th>
        </Tr>
      </THead>
      <TBody>
        {events.map((e) => {
          const path = subjectPath(e)
          return (
            <Tr
              key={e.seq}
              selected={e.seq === selected}
              className="cursor-pointer hover:bg-raise/60"
              onClick={() => onOpen(e.seq)}
            >
              <Td className="pl-4 text-right text-mute">{e.seq}</Td>
              <Td>{stamp(e.occurred_at)}</Td>
              <Td className={cn('max-w-64 truncate', e.actor.via === 'mcp' && 'text-violet')}>
                {actorLabel(e)}
              </Td>
              <Td className={cn(e.actor.via === 'mcp' && 'text-violet')}>{e.actor.via}</Td>
              <Td>
                <button
                  type="button"
                  aria-label={`${actionLabel(e.action)}, event ${e.seq}: show details`}
                  className="cursor-pointer rounded-sm font-sans text-base text-text hover:underline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
                  onClick={(ev) => {
                    ev.stopPropagation()
                    onOpen(e.seq)
                  }}
                >
                  {actionLabel(e.action)}
                </button>
                {actionLabels[e.action] && (
                  <span className="ml-2 text-xs text-mute">{e.action}</span>
                )}
              </Td>
              <Td className="max-w-72 truncate pr-4">
                {e.subject_type && <span className="text-mute">{e.subject_type} </span>}
                {path ? (
                  <Link
                    to={path}
                    className="text-blue hover:underline focus-visible:outline-2 focus-visible:outline-green"
                    onClick={(ev) => ev.stopPropagation()}
                  >
                    {e.subject_id}
                  </Link>
                ) : (
                  e.subject_id
                )}
              </Td>
            </Tr>
          )
        })}
      </TBody>
    </Table>
  )
}

function EventList({
  events,
  selected,
  onOpen,
}: {
  events: AuditEvent[]
  selected: number | undefined
  onOpen: (seq: number) => void
}) {
  return (
    <ul aria-label="Audit events" className="flex flex-col">
      {events.map((e) => (
        <li key={e.seq} className="border-b border-seam">
          {/* Named by its visible text, so speech input can say what it sees. */}
          <button
            type="button"
            aria-current={e.seq === selected || undefined}
            onClick={() => onOpen(e.seq)}
            className={cn(
              'flex w-full cursor-pointer flex-col gap-0.5 px-4 py-2.5 text-left transition-colors duration-120',
              'hover:bg-raise/60 focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-green',
              e.seq === selected && 'bg-raise',
            )}
          >
            <span className="flex items-baseline justify-between gap-3">
              <span className="truncate text-base text-text">{actionLabel(e.action)}</span>
              <span className="shrink-0 font-mono text-xs text-mute tabular">#{e.seq}</span>
            </span>
            <span className="truncate font-mono text-xs text-dim tabular">
              {stamp(e.occurred_at)} ·{' '}
              <span className={cn(e.actor.via === 'mcp' && 'text-violet')}>
                {actorLabel(e)} via {e.actor.via}
              </span>
            </span>
            {e.subject_id && (
              <span className="truncate font-mono text-xs text-mute">
                {e.subject_type} {e.subject_id}
              </span>
            )}
          </button>
        </li>
      ))}
    </ul>
  )
}
