import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { CircleAlert, CircleCheck } from 'lucide-react'
import type { ReactNode } from 'react'
import { Link } from 'react-router'
import { isApiError } from '@/api'
import {
  auditKeys,
  auditVerificationQuery,
  type AuditVerification,
  systemQuery,
  verifyAudit,
} from '@/api/audit'
import { Badge, Button, ErrorState, Skeleton, Spinner } from '@/ui'
import { formatTime } from './format'

const count = new Intl.NumberFormat()

function bytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`
}

function Item({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-1 gap-x-4 gap-y-0.5 py-2 sm:grid-cols-[180px_minmax(0,1fr)]">
      <dt className="text-base text-dim">{label}</dt>
      <dd className="min-w-0 font-mono text-sm break-all text-text tabular">{children}</dd>
    </div>
  )
}

/** Settings → System: what this instance is and whether its audit log is intact. Admin only. */
export function SystemSection() {
  const system = useQuery(systemQuery())
  if (system.isPending) {
    return (
      <div className="flex max-w-xl flex-col gap-3 px-6 py-6" aria-busy="true" aria-label="Loading">
        {Array.from({ length: 6 }, (_, i) => (
          <Skeleton key={i} className="h-5 w-full" />
        ))}
      </div>
    )
  }
  if (system.isError) {
    return (
      <ErrorState
        title="System status could not be loaded."
        message={system.error.message}
        code={isApiError(system.error) ? system.error.code : undefined}
        action={<Button onClick={() => void system.refetch()}>Retry</Button>}
      />
    )
  }
  const s = system.data
  return (
    <div className="flex max-w-2xl flex-col gap-8 px-6 py-6">
      <section aria-labelledby="integrity-title" className="flex flex-col gap-3">
        <h3 id="integrity-title" className="text-base font-medium small-caps text-dim">
          Audit log integrity
        </h3>
        <VerifyPanel />
      </section>
      <section aria-labelledby="instance-title" className="flex flex-col gap-1">
        <h3 id="instance-title" className="text-base font-medium small-caps text-dim">
          Instance
        </h3>
        <dl className="divide-y divide-seam">
          <Item label="Version">
            {s.version}
            {s.dev && (
              <Badge tone="amber" className="ml-2 font-sans">
                Dev mode
              </Badge>
            )}
          </Item>
          <Item label="Build commit">{s.commit}</Item>
          <Item label="Data directory">{s.data_dir}</Item>
          <Item label="Store size">{bytes(s.store_bytes)}</Item>
          <Item label="Migrations">
            {s.migration.current} of {s.migration.latest}
            {s.migration.pending ? (
              <Badge tone="amber" className="ml-2 font-sans">
                Newer migration pending
              </Badge>
            ) : (
              <span className="ml-2 font-sans text-xs text-mute">up to date</span>
            )}
          </Item>
          <Item label="Users">{count.format(s.counts.users)}</Item>
          <Item label="Connections">{count.format(s.counts.connections)}</Item>
          <Item label="Changes">{count.format(s.counts.changes)}</Item>
        </dl>
      </section>
    </div>
  )
}

/** The last successful verification, a Verify now button, and the outcome. */
function VerifyPanel() {
  const queryClient = useQueryClient()
  const status = useQuery(auditVerificationQuery())
  const verify = useMutation({
    mutationFn: verifyAudit,
    onSuccess: (v) => {
      queryClient.setQueryData(auditKeys.verification(), v)
      void queryClient.invalidateQueries({ queryKey: auditKeys.system() })
      void queryClient.invalidateQueries({ queryKey: auditKeys.all })
    },
  })
  const v: AuditVerification | undefined = status.data
  const running = v?.state === 'running' || verify.isPending

  return (
    <div className="flex flex-col gap-3">
      <p className="text-base text-dim">
        Every event stores the hash of the one before it. Verifying recomputes the chain and names
        the first event that was edited, removed or reordered.
      </p>
      <div className="flex flex-wrap items-center gap-3">
        <Button variant="primary" loading={running} onClick={() => verify.mutate()}>
          Verify now
        </Button>
        <span className="text-base text-dim" aria-live="polite">
          {running ? (
            <span className="inline-flex items-center gap-2">
              <Spinner className="size-3.5" />
              Checking… <span className="font-mono tabular">
                {count.format(v?.checked ?? 0)}
              </span>{' '}
              events so far
            </span>
          ) : v?.last_success ? (
            <>
              Last verified intact{' '}
              <span className="font-mono text-text tabular">{formatTime(v.last_success.at)}</span>{' '}
              by {v.last_success.by} ({count.format(v.last_success.checked)} events)
            </>
          ) : status.isPending ? (
            ''
          ) : (
            <span className="text-amber">Never verified from the dashboard.</span>
          )}
        </span>
      </div>
      {verify.isError && (
        <p role="alert" className="text-base text-red">
          {verify.error.message}
        </p>
      )}
      {!running && v?.state === 'done' && <Outcome v={v} />}
      {!running && v?.state === 'error' && (
        <p role="alert" className="text-base text-red">
          {v.message ?? 'The check could not run.'}
        </p>
      )}
    </div>
  )
}

function Outcome({ v }: { v: AuditVerification }) {
  if (v.ok) {
    return (
      <p
        role="status"
        className="flex items-start gap-2 rounded-md border border-green/35 bg-green/8 px-3 py-2.5 text-base text-green"
      >
        <CircleCheck aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
        <span>
          The chain is intact: {count.format(v.checked)} events checked
          {v.finished_at ? ` at ${formatTime(v.finished_at)}` : ''}.
        </span>
      </p>
    )
  }
  const seq = v.problem?.seq
  return (
    <div
      role="alert"
      className="flex items-start gap-2 rounded-md border border-red/35 bg-red/8 px-3 py-2.5 text-base text-red"
    >
      <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
      <div className="flex flex-col gap-1">
        <span>
          The chain is broken at event <span className="font-mono tabular">#{seq}</span>:{' '}
          {v.problem?.reason}.
        </span>
        <span className="text-dim">
          Events before #{seq} verified. Restore the database from a backup taken before the change,
          and find out who had access to the data directory.
        </span>
        {seq !== undefined && (
          <Link
            to={`/audit?before=${seq + 1}&event=${seq}`}
            className="text-blue underline-offset-2 hover:underline"
          >
            Open event #{seq} in the audit log
          </Link>
        )}
      </div>
    </div>
  )
}
