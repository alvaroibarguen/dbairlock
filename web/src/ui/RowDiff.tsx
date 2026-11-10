import { ChevronsLeftRight, ChevronsRightLeft } from 'lucide-react'
import { type ReactNode, useState } from 'react'
import { cn } from './cn'
import { useScrollFocus } from './useScrollFocus'

/** A cell value as the server sends it (see the Cell schema). Always shown as text. */
export type CellValue = unknown

/** The rows one statement affects (a RowPreview). */
export interface RowImage {
  columns: readonly string[]
  primary_key: readonly string[]
  has_primary_key: boolean
  rows_affected: number
  before?: readonly (readonly CellValue[])[]
  after?: readonly (readonly CellValue[])[]
  truncated: boolean
  row_images: boolean
}

export interface RowDiffProps {
  kind: 'insert' | 'update' | 'delete'
  image: RowImage
  /** The preview before the last update, when it found different data. */
  previous?: RowImage
  /** Accessible name of the table, for example "Statement 1: UPDATE public.users". */
  caption: string
  className?: string
}

/** Longest value shown before "show all". */
const CLIP = 64

interface Tagged {
  type: string
  value: string
  size: number
  truncated?: boolean
}

function isTagged(v: unknown): v is Tagged {
  return (
    typeof v === 'object' &&
    v !== null &&
    !Array.isArray(v) &&
    typeof (v as Tagged).type === 'string' &&
    typeof (v as Tagged).value === 'string' &&
    typeof (v as Tagged).size === 'number'
  )
}

/** The text of a value, for display and comparison. Never interpreted as markup. */
function cellText(v: CellValue): string {
  if (v === null || v === undefined) return 'NULL'
  if (isTagged(v)) return v.value
  if (typeof v === 'string') return v
  if (typeof v === 'number' || typeof v === 'boolean') return String(v)
  return JSON.stringify(v)
}

const sameCell = (a: CellValue, b: CellValue) => JSON.stringify(a) === JSON.stringify(b)

function Cell({ value }: { value: CellValue }) {
  const [open, setOpen] = useState(false)
  if (value === null || value === undefined) {
    return <span className="small-caps text-mute italic">null</span>
  }
  const tagged = isTagged(value) ? value : null
  const text = cellText(value)
  const long = text.length > CLIP
  const shown = long && !open ? text.slice(0, CLIP) + '…' : text
  return (
    <span className="inline-flex max-w-full items-baseline gap-1.5">
      <span className={cn('whitespace-pre', open && 'break-all whitespace-pre-wrap')}>
        {tagged?.type === 'bytes' && <span className="text-mute">0x·b64 </span>}
        {shown}
      </span>
      {tagged?.truncated && (
        <span className="text-2xs text-mute" title={`${tagged.size} bytes, cut for display`}>
          {tagged.size}B cut
        </span>
      )}
      {long && (
        <button
          type="button"
          className="inline-flex cursor-pointer items-center rounded-sm text-mute hover:text-text"
          aria-label={open ? 'Show less' : 'Show the whole value'}
          aria-expanded={open}
          onClick={() => setOpen((o) => !o)}
        >
          {open ? (
            <ChevronsRightLeft aria-hidden="true" className="size-3" />
          ) : (
            <ChevronsLeftRight aria-hidden="true" className="size-3" />
          )}
        </button>
      )}
    </span>
  )
}

/** The primary key of a row, as text, for pairing before and after. */
function keyOf(row: readonly CellValue[], keyIdx: readonly number[]): string {
  return JSON.stringify(keyIdx.map((i) => row[i]))
}

interface Line {
  side: 'before' | 'after' | 'insert' | 'delete'
  row: readonly CellValue[]
  /** Column indexes whose value differs between before and after. */
  changed: ReadonlySet<number>
  /** The row differs from the previous preview. */
  fresh: boolean
  last: boolean
}

/**
 * The rows a statement affects. For an UPDATE, rows are paired by primary
 * key: the before row, then the after row, with changed values in red
 * (struck through) and green. A DELETE lists the removed rows; an INSERT the
 * new ones. The key column stays in view while the table scrolls sideways.
 */
export function RowDiff({ kind, image, previous, caption, className }: RowDiffProps) {
  const scrollRef = useScrollFocus()
  const cols = image.columns
  const keyIdx = image.primary_key.map((k) => cols.indexOf(k)).filter((i) => i >= 0)
  const order = [...keyIdx, ...cols.map((_, i) => i).filter((i) => !keyIdx.includes(i))]

  if (!image.row_images) {
    return (
      <Note className={className}>
        This connection does not store row images. The statement affects{' '}
        <span className="font-mono text-text tabular">{image.rows_affected}</span>{' '}
        {image.rows_affected === 1 ? 'row' : 'rows'}.
      </Note>
    )
  }

  const prevRows = new Set(
    (previous?.before ?? previous?.after ?? []).map((r) => JSON.stringify(r)),
  )
  const isFresh = (row: readonly CellValue[]) =>
    previous !== undefined && !prevRows.has(JSON.stringify(row))

  const lines: Line[] = []
  const changedCols = new Set<number>()
  if (kind === 'update') {
    const after = new Map((image.after ?? []).map((r) => [keyOf(r, keyIdx), r]))
    for (const b of image.before ?? []) {
      const a = keyIdx.length > 0 ? after.get(keyOf(b, keyIdx)) : undefined
      const changed = new Set<number>()
      if (a) b.forEach((v, i) => !sameCell(v, a[i]) && changed.add(i))
      changed.forEach((i) => changedCols.add(i))
      const fresh = isFresh(b)
      lines.push({ side: 'before', row: b, changed, fresh, last: !a })
      if (a) lines.push({ side: 'after', row: a, changed, fresh, last: true })
    }
  } else if (kind === 'delete') {
    for (const b of image.before ?? []) {
      lines.push({ side: 'delete', row: b, changed: new Set(), fresh: isFresh(b), last: true })
    }
  } else {
    for (const a of image.after ?? []) {
      lines.push({ side: 'insert', row: a, changed: new Set(), fresh: false, last: true })
    }
  }
  const gone =
    previous !== undefined && kind !== 'insert'
      ? (previous.before ?? []).filter(
          (r) => !(image.before ?? []).some((b) => JSON.stringify(b) === JSON.stringify(r)),
        ).length
      : 0
  const shown = kind === 'insert' ? (image.after?.length ?? 0) : (image.before?.length ?? 0)

  return (
    <div className={cn('flex min-w-0 flex-col', className)}>
      {!image.has_primary_key && (
        <Note tone="amber">
          This table has no primary key, so the whole row is compared at execution. Any change to
          these rows before execution will stop this change.
        </Note>
      )}
      {kind === 'update' && !image.has_primary_key && (
        <Note>Without a primary key the rows after the change cannot be shown.</Note>
      )}
      <div ref={scrollRef} className="relative min-w-0 overflow-x-auto">
        <table className="w-full border-collapse font-mono text-sm tabular">
          <caption className="sr-only">{caption}</caption>
          <thead>
            <tr>
              <th scope="col" className={cn(thClass, 'sticky left-0 z-10 w-14 bg-panel')}>
                <span className="sr-only">Side</span>
              </th>
              {order.map((i, n) => (
                <th
                  key={cols[i]}
                  scope="col"
                  className={cn(
                    thClass,
                    n === 0 && keyIdx.length > 0 && 'sticky left-14 z-10 bg-panel',
                    changedCols.has(i) && 'text-dim',
                  )}
                >
                  {cols[i]}
                  {keyIdx.includes(i) && <span className="ml-1 text-2xs text-mute">pk</span>}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {lines.map((line, n) => (
              <Row key={n} line={line} order={order} keyIdx={keyIdx} />
            ))}
          </tbody>
        </table>
      </div>
      <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 border-t border-seam px-4 py-3 font-mono text-xs text-mute tabular">
        <span>
          {shown} of {image.rows_affected} {image.rows_affected === 1 ? 'row' : 'rows'}
          {kind === 'update' &&
            ` · ${changedCols.size} ${changedCols.size === 1 ? 'column changes' : 'columns change'}`}
          {gone > 0 && (
            <span className="text-amber">
              {' '}
              · {gone} {gone === 1 ? 'row' : 'rows'} no longer matched
            </span>
          )}
        </span>
        <span>{keyIdx.length > 0 ? `pk: ${image.primary_key.join(', ')}` : 'no primary key'}</span>
        {image.truncated && (
          <span className="basis-full font-sans">
            Some values are cut for display; execution compares the complete values.
          </span>
        )}
      </div>
    </div>
  )
}

const thClass =
  'border-b border-seam px-3 py-2 text-left font-sans text-xs font-medium whitespace-nowrap text-mute small-caps'

const sideLabel: Record<Line['side'], string> = {
  before: 'before',
  after: 'after',
  insert: 'insert',
  delete: 'delete',
}

function Row({
  line,
  order,
  keyIdx,
}: {
  line: Line
  order: readonly number[]
  keyIdx: readonly number[]
}) {
  const { side, row, changed, fresh, last } = line
  const top = side === 'before' || side === 'insert' || side === 'delete'
  return (
    <tr
      className={cn(
        last && 'border-b border-seam',
        fresh && top && 'shadow-[inset_2px_0_0_var(--amber)]',
      )}
      data-side={side}
    >
      <td
        className={cn(
          'sticky left-0 z-[1] w-14 bg-panel px-3 text-2xs whitespace-nowrap',
          top ? 'pt-3' : 'pt-1.5',
          last ? 'pb-3' : 'pb-1.5',
          side === 'after' || side === 'insert'
            ? 'text-green'
            : side === 'delete'
              ? 'text-red'
              : 'text-mute',
        )}
      >
        {sideLabel[side]}
        {fresh && top && <span className="sr-only"> (changed since the last preview)</span>}
      </td>
      {order.map((i, n) => {
        const isKey = keyIdx.includes(i)
        const diff = changed.has(i)
        return (
          <td
            key={i}
            className={cn(
              'px-3 align-top whitespace-nowrap',
              top ? 'pt-3' : 'pt-1.5',
              last ? 'pb-3' : 'pb-1.5',
              n === 0 && keyIdx.length > 0 && 'sticky left-14 z-[1] bg-panel',
              isKey ? 'text-text' : 'text-dim',
              side === 'before' && diff && 'text-red line-through decoration-red/50',
              side === 'after' &&
                diff &&
                'bg-green/8 text-green shadow-[inset_2px_0_0_var(--green)]',
              side === 'delete' && !isKey && 'text-red/90 line-through decoration-red/40',
              side === 'insert' && 'text-green',
            )}
          >
            <Cell value={row[i]} />
            {diff && (
              <span className="sr-only">{side === 'before' ? ' (old value)' : ' (new value)'}</span>
            )}
          </td>
        )
      })}
    </tr>
  )
}

function Note({
  children,
  tone = 'neutral',
  className,
}: {
  children: ReactNode
  tone?: 'neutral' | 'amber'
  className?: string
}) {
  return (
    <p
      className={cn(
        'border-b border-seam px-4 py-2.5 text-xs',
        tone === 'amber' ? 'bg-amber/8 text-amber' : 'text-dim',
        className,
      )}
    >
      {children}
    </p>
  )
}
