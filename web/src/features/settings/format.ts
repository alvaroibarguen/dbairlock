const dateTime = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' })
const dateOnly = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium' })

/** A timestamp for a table cell, or `fallback` when there is none. */
export function formatTime(iso: string | undefined, fallback = '—'): string {
  if (!iso) return fallback
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? fallback : dateTime.format(d)
}

/** A date without the time, or `fallback`. */
export function formatDate(iso: string | undefined, fallback = '—'): string {
  if (!iso) return fallback
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? fallback : dateOnly.format(d)
}
