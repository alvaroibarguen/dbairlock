import { RefreshCw, TriangleAlert } from 'lucide-react'
import type { ReactNode } from 'react'
import type { Change } from '@/api'
import { Button, cn } from '@/ui'
import { ageInWords, clockTime, rows } from './format'
import { type Freshness, type RefreshNotice, staleMessage } from './freshness'

/**
 * The preview line of a change in review: when the preview was taken,
 * counting up live, and the Update preview button. Past the limit it turns
 * into a warning banner; after an update it says what the update found.
 */
export function FreshnessStrip({
  change,
  freshness,
  refreshing,
  onRefresh,
  notice,
}: {
  change: Change
  freshness: Freshness
  refreshing: boolean
  onRefresh?: () => void
  notice: RefreshNotice | null
}) {
  const button = onRefresh && (
    <Button
      size="sm"
      onClick={onRefresh}
      loading={refreshing}
      icon={<RefreshCw aria-hidden="true" className="size-3.5" />}
    >
      Update preview
    </Button>
  )
  const failed = change.preview_error
  return (
    <div className="flex flex-col gap-2 border-b border-seam px-4 py-3 sm:px-6">
      {notice?.kind === 'changed' && (
        <p
          role="alert"
          className="rounded-md border border-amber/35 bg-amber/8 px-3 py-2 text-base font-medium text-amber"
        >
          The affected rows have changed since the last preview. Earlier approvals no longer count;
          rows that differ are marked.
        </p>
      )}
      {notice?.kind === 'unchanged' && (
        <p role="status" className="text-base text-green">
          Preview updated. The affected rows are unchanged.
        </p>
      )}
      {notice?.kind === 'refused' && (
        <p role="alert" className="text-base text-amber">
          {notice.message}
        </p>
      )}
      {failed ? (
        <div
          role="alert"
          className="flex flex-wrap items-center gap-x-4 gap-y-2 rounded-md border border-red/35 bg-red/8 px-3 py-2"
        >
          <p className="min-w-[14rem] flex-1 text-base text-red">
            The last preview failed: {failed} This change cannot be approved until the author edits
            it or a new preview succeeds.
          </p>
          {button}
        </div>
      ) : freshness.stale ? (
        <div
          role="alert"
          className="flex flex-wrap items-center gap-x-4 gap-y-2 rounded-md border border-amber/35 bg-amber/8 px-3 py-2"
        >
          <TriangleAlert aria-hidden="true" className="size-4 shrink-0 text-amber" />
          <p className="min-w-[14rem] flex-1 text-base text-amber">{staleMessage(freshness)}</p>
          {button}
        </div>
      ) : (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
          <p
            className={cn('min-w-0 flex-1 text-base', freshness.ending ? 'text-amber' : 'text-dim')}
          >
            Preview taken {ageInWords(freshness.age ?? 0)} ago
            <span className="ml-2 font-mono text-xs text-mute tabular">
              fresh for {clockTime(freshness.maxAge - (freshness.age ?? 0))}
            </span>
          </p>
          {button}
        </div>
      )}
    </div>
  )
}

/** Rows affected, fingerprint and preview age, with the guards execution applies. */
export function PreviewStats({ change, freshness }: { change: Change; freshness: Freshness }) {
  const total = change.statements.reduce((n, s) => n + (s.expected_rows ?? 0), 0)
  const fp = change.statements.find((s) => s.fingerprint)?.fingerprint
  const live = change.status === 'pending_review'
  const pct =
    freshness.age === null
      ? 0
      : Math.max(0, Math.min(100, 100 - (freshness.age / freshness.maxAge) * 100))
  const tone = freshness.stale ? 'text-red' : freshness.ending ? 'text-amber' : 'text-green'
  const bar = freshness.stale ? 'bg-red' : freshness.ending ? 'bg-amber' : 'bg-green'
  // Sized by the pane, not the window: the detail pane is narrow beside
  // the queue and the rows.
  return (
    <div className="@container">
      <div
        className={cn(
          'grid grid-cols-1 overflow-hidden rounded-md border border-seam bg-bg',
          live ? '@md:grid-cols-[1fr_1.3fr_1.4fr]' : '@md:grid-cols-[1fr_1.3fr]',
        )}
      >
        <Stat label="Rows affected">
          <span className="text-lg font-medium">{total}</span>
        </Stat>
        <Stat label="Fingerprint">
          <span className="text-[15px] whitespace-nowrap" title={fp}>
            {fp ? `sha256 ${fp.slice(0, 4)}…${fp.slice(-4)}` : 'count only'}
          </span>
        </Stat>
        {live && (
          <Stat label="Preview age">
            {freshness.age !== null ? (
              <>
                <span className={cn('text-lg font-medium', tone)}>
                  {clockTime(freshness.age)}{' '}
                  <span className="text-xs font-normal text-mute">
                    / fresh until {clockTime(freshness.maxAge)}
                  </span>
                </span>
                {/* A meter flush along the cell's bottom edge: it takes no
                    height, so all three cells keep the same padding. */}
                <span
                  aria-hidden="true"
                  className="absolute inset-x-0 bottom-0 block h-[3px] bg-seam"
                >
                  <span
                    className={cn('block h-[3px] transition-[width] duration-120', bar)}
                    style={{ width: `${pct}%` }}
                  />
                </span>
              </>
            ) : (
              <span className="text-lg font-medium text-mute">—</span>
            )}
          </Stat>
        )}
      </div>
      <ul className="mt-3 grid grid-cols-1 gap-x-4 gap-y-1.5 text-sm text-dim @md:grid-cols-2">
        {change.statements.map((s) => (
          <Guard key={s.position}>
            {change.statements.length > 1 ? `Statement ${s.position + 1}: ` : ''}
            {s.expected_rows !== undefined
              ? `row count must equal ${rows(s.expected_rows)}`
              : 'not previewed'}
          </Guard>
        ))}
        <Guard>
          {fp
            ? 'Fingerprint re-checked in the transaction'
            : 'Counts re-checked in the transaction'}
        </Guard>
        <Guard>Each statement changes one table</Guard>
        <Guard>Rolls back on any drift</Guard>
      </ul>
    </div>
  )
}

function Stat({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="relative flex flex-col border-seam px-3.5 py-3 not-first:border-t @md:not-first:border-t-0 @md:not-first:border-l">
      <span className="text-xs small-caps text-mute">{label}</span>
      <span className="mt-0.5 font-mono tabular">{children}</span>
    </div>
  )
}

function Guard({ children }: { children: ReactNode }) {
  return (
    <li className="flex gap-2">
      <span aria-hidden="true" className="font-mono text-green">
        ✓
      </span>
      <span className="first-letter:uppercase">{children}</span>
    </li>
  )
}
