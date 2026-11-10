import type { Change, ChangeStatement, ChangeSummary } from '@/api'

/** How a change is named everywhere: its number. */
export function changeRef(number: number): string {
  return `#${number}`
}

/** A short age: "now", "4m", "2h", "3d". */
export function shortAge(fromMs: number, nowMs: number): string {
  const s = Math.max(0, Math.floor((nowMs - fromMs) / 1000))
  if (s < 60) return 'now'
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m`
  const h = Math.floor(m / 60)
  if (h < 48) return `${h}h`
  return `${Math.floor(h / 24)}d`
}

/** A longer age in words: "12 seconds", "4 minutes", "2 hours". */
export function ageInWords(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000))
  if (s < 60) return s === 1 ? '1 second' : `${s} seconds`
  const m = Math.floor(s / 60)
  if (m < 60) return m === 1 ? '1 minute' : `${m} minutes`
  const h = Math.floor(m / 60)
  if (h < 48) return h === 1 ? '1 hour' : `${h} hours`
  const d = Math.floor(h / 24)
  return d === 1 ? '1 day' : `${d} days`
}

/**
 * A ticking duration: minutes and seconds under an hour ("2:46"), then
 * hours and minutes ("2h 18m"), then days and hours ("3d 4h").
 */
export function clockTime(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000))
  if (s < 3600) return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`
  const m = Math.floor(s / 60)
  const h = Math.floor(m / 60)
  if (h < 24) return m % 60 ? `${h}h ${m % 60}m` : `${h}h`
  const d = Math.floor(h / 24)
  return h % 24 ? `${d}d ${h % 24}h` : `${d}d`
}

/** A local time of day, "14:05". */
export function timeOfDay(iso: string): string {
  return new Date(iso).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
}

/** A local date and time, for the timeline and reviews. */
export function dateTime(iso: string): string {
  return new Date(iso).toLocaleString(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  })
}

export function rows(n: number): string {
  return n === 1 ? '1 row' : `${n} rows`
}

/** "UPDATE · 3 rows", or "2 statements · 5 rows". */
export function statementSummary(statements: readonly ChangeStatement[]): string {
  if (statements.length === 0) return 'no statements'
  const total = statements.reduce((n, s) => n + (s.expected_rows ?? 0), 0)
  const counted = statements.some((s) => s.expected_rows !== undefined)
  const first = statements[0]
  const what =
    statements.length === 1
      ? (first?.kind?.toUpperCase() ?? 'SQL')
      : `${statements.length} statements`
  return counted ? `${what} · ${rows(total)}` : what
}

/** The fully qualified table of a statement, "public.users". */
export function qualifiedTable(s: ChangeStatement): string {
  if (!s.table) return 'not parsed'
  return s.schema ? `${s.schema}.${s.table}` : s.table
}

/** True when an approved change is waiting for its database after a failed attempt. */
export function isWaiting(
  c: Pick<Change | ChangeSummary, 'status' | 'attempts' | 'next_attempt_at'>,
) {
  return c.status === 'approved' && c.attempts > 0 && c.next_attempt_at !== undefined
}

/** Plain-language outcome of a failed or ended change, or null. */
export function outcomeTitle(c: Change): string | null {
  switch (c.status) {
    case 'failed':
      switch (c.status_code) {
        case 'drift':
          return c.status_detail === 'count'
            ? 'Stopped: a different number of rows matched'
            : 'Stopped: the rows changed since review'
        case 'interrupted':
          return 'Interrupted: outcome unknown'
        case 'timeout':
          return 'Failed: timed out'
        case 'invalid':
          return 'Failed: no longer valid'
        default:
          return 'Failed'
      }
    case 'rejected':
      return 'Rejected'
    case 'expired':
      return 'Expired'
    case 'cancelled':
      return 'Cancelled'
    default:
      return null
  }
}
