import type { AuditEvent } from '@/api/audit'

const pad = (n: number) => String(n).padStart(2, '0')

/** A local timestamp for a table cell: 2026-11-10 09:41:03. */
export function stamp(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

/** Who did it, in words: the email, else the user id, else the channel. */
export function actorLabel(e: AuditEvent): string {
  if (e.actor.email) return e.actor.email
  if (e.actor.user_id) return e.actor.user_id
  return e.actor.via === 'system' ? 'dbairlock' : e.actor.via
}

/** The dashboard page of an event's subject, if it has one. */
export function subjectPath(e: AuditEvent): string | undefined {
  if (!e.subject_id) return undefined
  switch (e.subject_type) {
    case 'change':
      return `/changes/${encodeURIComponent(e.subject_id)}`
    case 'connection':
      return `/connections/${encodeURIComponent(e.subject_id)}`
    default:
      return undefined
  }
}

/**
 * A datetime-local input value (local time, no zone) as an ISO instant, or
 * undefined when empty or invalid.
 */
export function localToIso(value: string | null): string | undefined {
  if (!value) return undefined
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? undefined : d.toISOString()
}
