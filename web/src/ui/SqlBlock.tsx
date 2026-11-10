import { useEffect, useState } from 'react'
import { cn } from './cn'
import type { SqlSegment } from './sqlHighlight'

export interface SqlBlockProps {
  sql: string
  /** The connector's editor dialect, for example "postgresql". */
  dialect: string
  className?: string
  'aria-label'?: string
}

/**
 * Read-only SQL with syntax colours. The text is always rendered as plain
 * text; colours arrive once the highlighter (loaded lazily with the editor
 * code) has split it into runs.
 */
export function SqlBlock({ sql, dialect, className, ...aria }: SqlBlockProps) {
  const [lines, setLines] = useState<{ sql: string; lines: SqlSegment[][] } | null>(null)
  useEffect(() => {
    let live = true
    void import('./sqlHighlight').then(({ highlightSql }) => {
      if (live) setLines({ sql, lines: highlightSql(sql, dialect) })
    })
    return () => {
      live = false
    }
  }, [sql, dialect])

  const highlighted = lines?.sql === sql ? lines.lines : null
  return (
    <pre
      aria-label={aria['aria-label']}
      className={cn(
        'overflow-x-auto rounded-md border border-seam bg-bg px-4 py-3.5 font-mono text-base leading-[1.7] whitespace-pre text-text tabular',
        className,
      )}
    >
      <code>
        {highlighted
          ? highlighted.map((line, i) => (
              <span key={i}>
                {i > 0 && '\n'}
                {line.map((seg, j) =>
                  seg.className ? (
                    <span key={j} className={seg.className}>
                      {seg.text}
                    </span>
                  ) : (
                    seg.text
                  ),
                )}
              </span>
            ))
          : sql}
      </code>
    </pre>
  )
}
