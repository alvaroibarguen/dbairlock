import { CircleAlert, CircleCheck, TriangleAlert } from 'lucide-react'
import type { ConnectionTestResult } from '@/api'
import { Badge, CodeBlock, cn } from '@/ui'

const time = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'medium' })

function when(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '' : time.format(d)
}

/**
 * The outcome of "Test connection": connectivity, the server version, what
 * the database user may do, and, prominently, any privileges beyond what
 * dbairlock needs with the exact statements that remove them.
 */
export function TestResultView({ result }: { result: ConnectionTestResult }) {
  const excess = result.privileges?.excess ?? []
  const fixes = excess.map((e) => e.fix).filter((f): f is string => Boolean(f))

  return (
    <div className="flex flex-col gap-3" aria-live="polite">
      <div
        role={result.ok ? 'status' : 'alert'}
        className={cn(
          'flex items-start gap-2 rounded-md border px-3 py-2.5 text-base',
          result.ok ? 'border-green/35 bg-green/8 text-green' : 'border-red/35 bg-red/8 text-red',
        )}
      >
        {result.ok ? (
          <CircleCheck aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
        ) : (
          <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
        )}
        <div className="flex min-w-0 flex-col gap-0.5">
          <span>
            {result.ok
              ? `Connected${result.server ? ` to ${result.server.product} ${result.server.version}` : ''}.`
              : 'Could not use this connection.'}
          </span>
          {result.message && (
            <span className="font-mono text-xs break-words text-dim">{result.message}</span>
          )}
          <time dateTime={result.tested_at} className="font-mono text-xs text-mute tabular">
            Tested {when(result.tested_at)}
          </time>
        </div>
      </div>

      {result.warnings.length > 0 && (
        <ul className="flex flex-col gap-2">
          {result.warnings.map((w) => (
            <li
              key={w}
              className="flex items-start gap-2 rounded-md border border-amber/35 bg-amber/8 px-3 py-2.5 text-base text-amber"
            >
              <TriangleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
              <span>{w}</span>
            </li>
          ))}
        </ul>
      )}

      {result.privileges && (
        <div className="flex flex-col gap-2">
          <h4 className="text-base font-medium small-caps text-dim">
            Privileges of{' '}
            <span className="font-mono tracking-normal text-text [font-variant:normal]">
              {result.privileges.user}
            </span>
          </h4>
          <div className="flex flex-wrap gap-1.5">
            {(
              [
                ['SELECT', result.privileges.can_select],
                ['INSERT', result.privileges.can_insert],
                ['UPDATE', result.privileges.can_update],
                ['DELETE', result.privileges.can_delete],
              ] as const
            ).map(([name, ok]) => (
              <Badge key={name} mono tone={ok ? 'green' : 'neutral'}>
                {ok ? name : `no ${name}`}
              </Badge>
            ))}
          </div>
          {excess.length > 0 && (
            <div className="flex flex-col gap-2 rounded-md border border-red/35 p-3">
              <p className="text-base text-red">
                Excess privileges: dbairlock needs only SELECT, INSERT, UPDATE and DELETE.
              </p>
              <ul className="flex flex-col gap-1.5">
                {excess.map((e) => (
                  <li key={e.privilege} className="flex flex-col gap-0.5">
                    <span className="font-mono text-sm text-text">{e.privilege}</span>
                    <span className="text-xs text-dim">{e.detail}</span>
                  </li>
                ))}
              </ul>
              {fixes.length > 0 && (
                <>
                  <p className="text-xs text-dim">Run these on the database to remove them:</p>
                  <CodeBlock copyLabel="statements">{fixes.join('\n')}</CodeBlock>
                </>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  )
}
