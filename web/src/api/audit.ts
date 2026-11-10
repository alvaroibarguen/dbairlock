import { keepPreviousData, queryOptions } from '@tanstack/react-query'
import { api } from './client'
import { ApiError } from './errors'
import type { components } from './schema'

export type AuditEvent = components['schemas']['AuditEvent']
export type AuditVia = components['schemas']['AuditVia']
export type AuditVerification = components['schemas']['AuditVerification']
export type SystemInfo = components['schemas']['SystemInfo']
export type Notification = components['schemas']['Notification']
export type NotificationTestResult = components['schemas']['NotificationTestResult']

/** Audit log filters. Every field is optional; empty means "any". */
export interface AuditFilters {
  actor?: string
  action?: string
  subject_type?: string
  subject_id?: string
  via?: AuditVia
  /** ISO date-time, inclusive. */
  from?: string
  /** ISO date-time, exclusive. */
  to?: string
}

/** Events per page of the audit log. */
export const AUDIT_PAGE_SIZE = 100

function data<T>(d: T | undefined): T {
  if (d === undefined)
    throw new ApiError(0, 'invalid_response', 'The server returned an empty response.')
  return d
}

export const auditKeys = {
  all: ['audit'] as const,
  list: (filters: AuditFilters, before?: number) => ['audit', 'list', filters, before] as const,
  verification: () => ['audit', 'verification'] as const,
  system: () => ['system'] as const,
  notifications: () => ['notifications'] as const,
}

/** One page of audit events, newest first, starting below `before`. */
export const auditEventsQuery = (filters: AuditFilters, before?: number) =>
  queryOptions({
    queryKey: auditKeys.list(filters, before),
    queryFn: async ({ signal }) => {
      const { data: d } = await api.GET('/api/audit', {
        signal,
        params: { query: { ...filters, before, limit: AUDIT_PAGE_SIZE } },
      })
      return data(d)
    },
    placeholderData: keepPreviousData,
  })

/** One event by sequence number (for a shared link to an event outside the page). */
export const auditEventQuery = (seq: number) =>
  queryOptions({
    queryKey: ['audit', 'event', seq] as const,
    queryFn: async ({ signal }) => {
      const { data: d } = await api.GET('/api/audit', {
        signal,
        params: { query: { before: seq + 1, limit: 1 } },
      })
      const e = data(d).events[0]
      return e?.seq === seq ? e : null
    },
    staleTime: Infinity,
  })

/** The running or most recent chain verification. Admin only. */
export const auditVerificationQuery = () =>
  queryOptions({
    queryKey: auditKeys.verification(),
    queryFn: async ({ signal }) => {
      const { data: d } = await api.GET('/api/audit/verify', { signal })
      return data(d)
    },
    // Poll while a check runs; it reports progress as it goes.
    refetchInterval: (q) => (q.state.data?.state === 'running' ? 1000 : false),
  })

/** Starts a chain verification (or joins the running one). */
export async function verifyAudit(): Promise<AuditVerification> {
  const { data: d } = await api.POST('/api/audit/verify')
  return data(d)
}

/** URL that downloads the audit log as NDJSON for a time range. Admin only. */
export function auditExportUrl(from?: string, to?: string): string {
  const q = new URLSearchParams()
  if (from) q.set('from', from)
  if (to) q.set('to', to)
  const s = q.toString()
  return s ? `/api/audit/export?${s}` : '/api/audit/export'
}

/** The System page. Admin only. */
export const systemQuery = () =>
  queryOptions({
    queryKey: auditKeys.system(),
    queryFn: async ({ signal }) => {
      const { data: d } = await api.GET('/api/system', { signal })
      return data(d)
    },
  })

/** Recent notifications and their delivery outcome. Admin only. */
export const notificationsQuery = () =>
  queryOptions({
    queryKey: auditKeys.notifications(),
    queryFn: async ({ signal }) => {
      const { data: d } = await api.GET('/api/notifications', { signal })
      return data(d).notifications
    },
    refetchInterval: 5_000,
  })

/** Posts a test notification to the saved webhook. */
export async function sendTestNotification(): Promise<NotificationTestResult> {
  const { data: d } = await api.POST('/api/notifications/test')
  return data(d)
}
