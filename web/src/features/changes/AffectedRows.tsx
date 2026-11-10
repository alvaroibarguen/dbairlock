import type { Change, RowPreview } from '@/api'
import { EmptyState, RowDiff } from '@/ui'
import { qualifiedTable, rows } from './format'

/** The before/after legend for the rows pane header. */
export function RowsLegend() {
  return (
    <span className="flex gap-3.5 font-mono text-[11.5px] text-mute normal-case [font-variant:normal]">
      <span className="flex items-center gap-1.5">
        <i aria-hidden="true" className="inline-block size-2 rounded-[2px] bg-red" />
        before
      </span>
      <span className="flex items-center gap-1.5">
        <i aria-hidden="true" className="inline-block size-2 rounded-[2px] bg-green" />
        after
      </span>
    </span>
  )
}

/**
 * The rows every statement affects, before and after. Once a change has
 * executed, the rows its execution changed; until then, the preview's, with
 * rows that differ from the previous preview marked.
 */
export function AffectedRows({ change }: { change: Change }) {
  const executed = change.executions.find((e) => e.outcome === 'executed')
  const items = change.statements.map((s) => {
    const result = executed?.results.find((r) => r.position === s.position)
    const image: RowPreview | undefined = result?.preview ?? s.preview
    return {
      s,
      image,
      previous: change.preview_changed && !result ? s.previous_preview : undefined,
    }
  })
  if (items.every((i) => !i.image)) {
    return (
      <EmptyState
        title={
          change.preview_error
            ? 'There is no preview: the dry run failed.'
            : 'There is no preview of this change yet.'
        }
        description={change.preview_error}
      />
    )
  }
  return (
    <div className="flex flex-col">
      {executed && (
        <p className="border-b border-seam px-4 py-2.5 text-xs text-green">
          The rows as the execution changed them.
        </p>
      )}
      {items.map(({ s, image, previous }) => (
        <section
          key={s.position}
          aria-label={`Statement ${s.position + 1} rows`}
          className="border-b border-seam last:border-b-0"
        >
          {items.length > 1 && (
            <h3 className="flex items-center justify-between gap-3 border-b border-seam bg-bg/40 px-4 py-2 font-mono text-xs text-dim">
              <span>
                {s.position + 1}. {s.kind?.toUpperCase()} {qualifiedTable(s)}
              </span>
              {image && <span className="text-mute">{rows(image.rows_affected)}</span>}
            </h3>
          )}
          {image && s.kind ? (
            <RowDiff
              kind={s.kind}
              image={image}
              previous={previous}
              caption={`Statement ${s.position + 1}: ${s.kind.toUpperCase()} ${qualifiedTable(s)}`}
            />
          ) : (
            <p className="px-4 py-3 text-base text-dim">
              {s.issue?.message ?? 'This statement has no preview.'}
            </p>
          )}
        </section>
      ))}
    </div>
  )
}
