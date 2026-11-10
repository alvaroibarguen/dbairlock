import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, CirclePlay, Pencil, RotateCw, Send } from 'lucide-react'
import { type ReactNode, useEffect, useId, useState } from 'react'
import { Link, useNavigate } from 'react-router'
import {
  type Change,
  approveChange,
  cancelChange,
  changeQuery,
  executeChange,
  isApiError,
  queryKeys,
  refreshChangePreview,
  rejectChange,
  retryChange,
  submitChange,
} from '@/api'
import {
  Badge,
  Button,
  cn,
  ErrorState,
  PageHeader,
  Skeleton,
  SqlBlock,
  StatusBadge,
  toast,
  Tooltip,
  typing,
} from '@/ui'
import { Executions, Reviews, Timeline } from './Activity'
import { AuthorTag } from './AuthorTag'
import { CancelDialog, RejectDialog } from './ChangeDialogs'
import {
  changeRef,
  dateTime,
  isWaiting,
  outcomeTitle,
  qualifiedTable,
  rows,
  shortAge,
  timeOfDay,
} from './format'
import { freshnessOf, type RefreshNotice, staleMessage } from './freshness'
import { useConnectorInfo, useServerNow } from './hooks'
import { FreshnessStrip, PreviewStats } from './PreviewPanel'

/** Codes after which the reviewer must look at the latest preview first. */
const reloadCodes = new Set(['preview_stale', 'preview_changed', 'sql_changed', 'preview_failed'])

function ago(fromMs: number, nowMs: number): string {
  const a = shortAge(fromMs, nowMs)
  return a === 'now' ? 'just now' : `${a} ago`
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : 'The request did not complete. Try again.'
}

/**
 * The change review page: who submitted it, its status, the SQL, the
 * preview and its freshness, the actions the viewer may take, reviews and
 * the timeline. The SQL and the rows come first; agent-written text is
 * plain text.
 */
export function ChangeDetail({ id, back }: { id: string; back?: ReactNode }) {
  const query = useQuery(changeQuery(id))
  if (query.isPending) {
    return (
      <div className="flex flex-col gap-3 px-6 pt-5" aria-busy="true" aria-label="Loading change">
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
        {/* On a phone this is the whole screen: keep the way back and a page heading. */}
        {back && (
          <PageHeader
            as="h1"
            title={
              <span className="flex items-center gap-2">
                {back}
                <span>Change</span>
              </span>
            }
          />
        )}
        <ErrorState
          className="flex-1 justify-center"
          title={notFound ? 'There is no such change.' : 'The change could not be loaded.'}
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
  return <Loaded id={id} change={query.data} receivedAt={query.dataUpdatedAt} back={back} />
}

function Loaded({
  id,
  change,
  receivedAt,
  back,
}: {
  id: string
  change: Change
  receivedAt: number
  back?: ReactNode
}) {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const connector = useConnectorInfo()(change.connection.connector)
  const serverNow = useServerNow(change.server_time, receivedAt)
  const freshness = freshnessOf(change, serverNow)
  const [notice, setNotice] = useState<RefreshNotice | null>(null)
  const [rejectOpen, setRejectOpen] = useState(false)
  const [cancelOpen, setCancelOpen] = useState(false)
  const approveHint = useId()

  const update = (c: Change) => {
    queryClient.setQueryData(changeQuery(id).queryKey, c)
    void queryClient.invalidateQueries({ queryKey: queryKeys.changes.all })
  }

  const approve = useMutation({
    mutationFn: () => approveChange(change),
    onSuccess: (c) => {
      update(c)
      setNotice(null)
      toast.success(
        c.status === 'approved'
          ? `${changeRef(c.number)} approved.`
          : 'Your approval was recorded.',
      )
    },
    onError: (err) => {
      if (isApiError(err) && reloadCodes.has(err.code)) {
        // Never retried: show why and load the latest preview.
        setNotice({ kind: 'refused', message: err.message })
        void queryClient.invalidateQueries({ queryKey: queryKeys.changes.detail(id) })
      } else {
        toast.error(errorMessage(err))
      }
    },
  })
  const refresh = useMutation({
    mutationFn: () => refreshChangePreview(change.id),
    onSuccess: (r) => {
      update(r.change)
      setNotice(r.ok ? { kind: r.data_changed ? 'changed' : 'unchanged' } : null)
    },
    onError: (err) => toast.error(errorMessage(err)),
  })
  const reject = useMutation({
    mutationFn: (comment: string) => rejectChange(change.id, comment),
    onSuccess: (c) => {
      update(c)
      setRejectOpen(false)
      toast.success(`${changeRef(c.number)} rejected.`)
    },
  })
  const cancel = useMutation({
    mutationFn: (reason: string) => cancelChange(change.id, reason || undefined),
    onSuccess: (c) => {
      update(c)
      setCancelOpen(false)
      toast.success(`${changeRef(c.number)} cancelled.`)
    },
    onError: (err) => toast.error(errorMessage(err)),
  })
  const submit = useMutation({
    mutationFn: () => submitChange(change.id, change.revision),
    onSuccess: (c) => {
      if (c.already_existed) {
        toast.info(`You already have an open change with this SQL: ${changeRef(c.number)}.`)
        void navigate(`/changes/${c.number}`)
        return
      }
      update(c)
      toast.success(`${changeRef(c.number)} submitted for review.`)
    },
    onError: (err) => {
      toast.error(errorMessage(err))
      void queryClient.invalidateQueries({ queryKey: queryKeys.changes.detail(id) })
    },
  })
  const execute = useMutation({
    mutationFn: () => executeChange(change.id),
    onSuccess: (c) => update(c),
    onError: (err) => toast.error(errorMessage(err)),
  })
  const retry = useMutation({
    mutationFn: () => retryChange(change.id),
    onSuccess: (c) => update(c),
    onError: (err) => toast.error(errorMessage(err)),
  })

  const a = change.actions
  const pending = change.status === 'pending_review'
  const blocked = change.preview_error
    ? 'The last preview failed. This change cannot be approved until the author edits it or a new preview succeeds.'
    : freshness.stale
      ? staleMessage(freshness)
      : null
  const canApprove = a.approve && blocked === null && !approve.isPending
  const final = change.approvals + 1 >= change.required_approvals
  const approveLabel = final && change.auto_execute ? 'Approve & execute' : 'Approve'

  useEffect(() => {
    if (!canApprove) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Enter' && (e.metaKey || e.ctrlKey) && !typing()) {
        e.preventDefault()
        approve.mutate()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [canApprove, approve])

  const author = (
    <>
      {a.edit && (
        <Button
          icon={<Pencil aria-hidden="true" className="size-3.5" />}
          render={<Link to={`/changes/${change.number}/edit`} />}
          nativeButton={false}
        >
          Edit
        </Button>
      )}
      {a.submit && (
        <Button
          variant="primary"
          icon={<Send aria-hidden="true" className="size-3.5" />}
          loading={submit.isPending}
          onClick={() => submit.mutate()}
        >
          Submit for review
        </Button>
      )}
      {a.execute_now && (
        <Button
          variant="primary"
          icon={<CirclePlay aria-hidden="true" className="size-3.5" />}
          loading={execute.isPending}
          onClick={() => execute.mutate()}
        >
          Execute now
        </Button>
      )}
      {a.retry_now && (
        <Button
          icon={<RotateCw aria-hidden="true" className="size-3.5" />}
          loading={retry.isPending}
          onClick={() => retry.mutate()}
        >
          Try now
        </Button>
      )}
    </>
  )
  const hasActions =
    a.approve || a.reject || a.edit || a.submit || a.execute_now || a.retry_now || a.cancel

  return (
    <div className="@container flex flex-col">
      <header className="border-b border-seam px-4 pt-[18px] pb-4 sm:px-6">
        <div className="flex items-center gap-2 font-mono text-xs text-mute tabular">
          {back}
          <span>{changeRef(change.number)}</span>
          <span aria-hidden="true">/</span>
          <span className="truncate">{change.connection.name}</span>
          <span>{connector.name}</span>
        </div>
        <h1
          id="detail-title"
          className="mt-1.5 mb-3 text-2xl font-semibold tracking-[-0.01em] break-words text-text"
        >
          {change.title}
        </h1>
        <div className="flex flex-wrap items-center gap-2 text-sm text-dim">
          <StatusBadge status={change.status} />
          {(pending || change.status === 'approved') && (
            <Badge mono>
              {change.approvals} / {change.required_approvals} approvals
            </Badge>
          )}
          <AuthorTag change={change} />
          <span className="font-mono text-xs text-mute tabular">
            {change.submitted_at
              ? `opened ${ago(Date.parse(change.submitted_at), serverNow)}`
              : `drafted ${ago(Date.parse(change.created_at), serverNow)}`}
          </span>
        </div>
      </header>

      {hasActions && (
        <div className="flex flex-wrap items-center gap-2 border-b border-seam px-4 py-3 sm:px-6">
          {a.approve &&
            (canApprove ? (
              <Button
                variant="primary"
                shortcut="⌘↵"
                icon={<Check aria-hidden="true" className="size-3.5" />}
                loading={approve.isPending}
                onClick={() => approve.mutate()}
              >
                {approveLabel}
              </Button>
            ) : (
              <Tooltip content={blocked ?? 'Approving…'}>
                <Button
                  variant="primary"
                  disabled
                  focusableWhenDisabled
                  loading={approve.isPending}
                  aria-describedby={approveHint}
                  icon={<Check aria-hidden="true" className="size-3.5" />}
                >
                  {approveLabel}
                </Button>
              </Tooltip>
            ))}
          {blocked && a.approve && (
            <span id={approveHint} className="sr-only">
              {blocked}
            </span>
          )}
          {author}
          <span className="flex-1" />
          {a.reject && (
            <>
              <span className="hidden text-xs text-mute @lg:inline">Rejecting ends the change</span>
              <Button variant="destructive" onClick={() => setRejectOpen(true)}>
                Reject
              </Button>
            </>
          )}
          {a.cancel && !a.reject && (
            <Button variant="destructive" onClick={() => setCancelOpen(true)}>
              Cancel change
            </Button>
          )}
        </div>
      )}

      <StatusNotice
        change={change}
        onCopy={() => void navigate('/changes/new', { state: { from: change } })}
      />

      {pending && (
        <FreshnessStrip
          change={change}
          freshness={freshness}
          refreshing={refresh.isPending}
          onRefresh={a.refresh_preview ? () => refresh.mutate() : undefined}
          notice={notice}
        />
      )}

      <Section
        label={
          change.statements.length === 1 ? 'Statement' : `Statements (${change.statements.length})`
        }
        aside={change.statements.length > 1 ? 'run in this order · one transaction' : undefined}
      >
        <div className="flex flex-col gap-3">
          {change.statements.map((s) => (
            <div key={s.position} className="flex flex-col gap-1.5">
              {change.statements.length > 1 && (
                <p className="flex justify-between font-mono text-xs text-mute">
                  <span>
                    {s.position + 1}. {s.kind?.toUpperCase()} {qualifiedTable(s)}
                  </span>
                  {s.expected_rows !== undefined && <span>{rows(s.expected_rows)}</span>}
                </p>
              )}
              <SqlBlock
                sql={s.sql}
                dialect={connector.dialect}
                aria-label={`Statement ${s.position + 1}`}
              />
              {s.issue && (
                <p className="text-sm text-red">
                  {s.issue.message}
                  <span className="ml-2 font-mono text-xs text-mute">{s.issue.code}</span>
                </p>
              )}
            </div>
          ))}
        </div>
      </Section>

      {change.statements.some((s) => s.preview) && (
        <Section label="Preview">
          <PreviewStats change={change} freshness={freshness} />
        </Section>
      )}

      {change.reason && (
        <Section label="Reason">
          <p className="text-base whitespace-pre-wrap text-text">{change.reason}</p>
        </Section>
      )}

      <Section
        label="Approvals"
        aside={`${change.required_approvals} required · ${change.auto_execute ? 'runs when approved' : 'runs on Execute now'}`}
      >
        <Reviews change={change} />
      </Section>

      {change.executions.length > 0 && (
        <Section label="Execution">
          <Executions change={change} />
        </Section>
      )}

      <Section label="Activity" className="pb-6">
        <Timeline id={change.id} />
      </Section>

      {a.reject && (
        <RejectDialog
          open={rejectOpen}
          onOpenChange={setRejectOpen}
          pending={reject.isPending}
          error={reject.error ? errorMessage(reject.error) : undefined}
          onReject={(comment) => reject.mutate(comment)}
        />
      )}
      {a.cancel && (
        <CancelDialog
          open={cancelOpen}
          onOpenChange={setCancelOpen}
          number={change.number}
          pending={cancel.isPending}
          onCancel={(reason) => cancel.mutate(reason)}
        />
      )}
    </div>
  )
}

function Section({
  label,
  aside,
  className,
  children,
}: {
  label: string
  aside?: string
  className?: string
  children: ReactNode
}) {
  return (
    <section className={cn('px-4 pt-4 sm:px-6', className)} aria-label={label}>
      <h2 className="mb-2 flex items-baseline justify-between gap-3 text-base small-caps text-dim">
        <span>{label}</span>
        {aside && (
          <span className="truncate font-mono text-xs text-mute normal-case [font-variant:normal]">
            {aside}
          </span>
        )}
      </h2>
      {children}
    </section>
  )
}

/** Where the change stands, in one plain sentence, when there is something to say. */
function StatusNotice({ change, onCopy }: { change: Change; onCopy: () => void }) {
  const c = change
  let tone: 'green' | 'amber' | 'red' | 'blue' | 'neutral' = 'neutral'
  let title: string | null = outcomeTitle(c)
  let body: ReactNode = c.status_message ?? null
  let action: ReactNode = null

  switch (c.status) {
    case 'draft':
      title = 'Draft: not submitted for review yet.'
      body = c.preview_error ? `The last dry run found problems: ${c.preview_error}` : null
      tone = c.preview_error ? 'amber' : 'neutral'
      break
    case 'pending_review':
      if (c.status_code !== 'approvals_lost') return null
      title = 'Back in review'
      tone = 'amber'
      break
    case 'approved':
      if (isWaiting(c)) {
        tone = 'amber'
        title = 'Approved. Waiting for the database'
        body = `Could not connect (attempt ${c.attempts}): ${c.last_attempt_error ?? ''} Next try at ${timeOfDay(c.next_attempt_at ?? '')}.`
      } else if (!c.auto_execute && !c.execute_requested) {
        tone = 'green'
        title = 'Approved. Waiting for someone to execute it'
        body =
          'This connection does not run approved changes automatically. The author or an admin starts it with Execute now.'
      } else {
        tone = 'green'
        title = 'Approved. It runs in a moment.'
        body = null
      }
      break
    case 'executing':
      tone = 'blue'
      title = 'Executing on the database now.'
      body = null
      break
    case 'executed':
      tone = 'green'
      title = `Executed${c.executed_at ? ` ${dateTime(c.executed_at)}` : ''}.`
      body = 'Every statement matched what was reviewed, and the transaction was committed.'
      break
    case 'failed':
      tone = 'red'
      if (c.status_code === 'drift') {
        action = (
          <Button size="sm" onClick={onCopy}>
            Create a new change from this one
          </Button>
        )
      }
      break
    case 'rejected': {
      tone = 'red'
      const r = [...c.reviews].reverse().find((x) => x.decision === 'reject')
      title = r ? `Rejected by ${r.reviewer.name || r.reviewer.email}` : 'Rejected'
      body = r?.comment ?? c.status_message
      break
    }
    default:
      break
  }
  if (!title) return null
  const tones = {
    green: 'border-green/35 bg-green/8 text-green',
    amber: 'border-amber/35 bg-amber/8 text-amber',
    red: 'border-red/35 bg-red/8 text-red',
    blue: 'border-blue/35 bg-blue/8 text-blue',
    neutral: 'border-seam bg-raise text-dim',
  }
  return (
    <div className="border-b border-seam px-4 py-3 sm:px-6">
      <div
        role="status"
        className={cn(
          'flex flex-wrap items-center gap-x-4 gap-y-2 rounded-md border px-3 py-2.5',
          tones[tone],
        )}
      >
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <p className="text-base font-medium">{title}</p>
          {body && <p className="text-base whitespace-pre-wrap text-text">{body}</p>}
        </div>
        {action}
      </div>
    </div>
  )
}
