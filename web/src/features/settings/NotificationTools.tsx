import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Send } from 'lucide-react'
import type { Settings } from '@/api'
import {
  auditKeys,
  type Notification,
  notificationsQuery,
  type NotificationTestResult,
  sendTestNotification,
} from '@/api/audit'
import { Badge, type BadgeTone, Button, Table, TBody, Td, Th, THead, Tr } from '@/ui'
import { formatTime } from './format'
import { notifyEvents } from './settingsForm'

const statusMeta: Record<Notification['status'], { label: string; tone: BadgeTone }> = {
  pending: { label: 'Retrying', tone: 'amber' },
  delivered: { label: 'Delivered', tone: 'green' },
  gave_up: { label: 'Dropped', tone: 'red' },
}

const eventLabel = (e: string) => notifyEvents.find((x) => x.value === e)?.label ?? e

/**
 * Below the Notifications form: a test send to the saved webhook, and the
 * recent notifications with their delivery outcome.
 */
export function NotificationTools({ settings }: { settings: Settings }) {
  const queryClient = useQueryClient()
  const urlSet = settings.secrets['notify.webhook_url'].is_set
  const recent = useQuery(notificationsQuery())
  const test = useMutation<NotificationTestResult>({
    mutationFn: sendTestNotification,
    onSettled: () => void queryClient.invalidateQueries({ queryKey: auditKeys.notifications() }),
  })

  return (
    <div className="flex max-w-3xl flex-col gap-6 border-t border-seam px-6 py-6">
      <section aria-labelledby="test-title" className="flex flex-col gap-2">
        <h3 id="test-title" className="text-base font-medium small-caps text-dim">
          Test
        </h3>
        <p className="text-base text-dim">
          Posts a test message to the saved webhook URL in the saved format, even while sending is
          off. Save changes first.
        </p>
        <div className="flex flex-wrap items-center gap-3">
          <Button
            icon={<Send aria-hidden="true" className="size-3.5" />}
            loading={test.isPending}
            disabled={!urlSet}
            onClick={() => test.mutate()}
          >
            Send test notification
          </Button>
          {!urlSet && <span className="text-xs text-mute">Save a webhook URL first.</span>}
          <span aria-live="polite" className="text-base">
            {test.data &&
              (test.data.ok ? (
                <span className="text-green">{test.data.message}</span>
              ) : (
                <span className="text-red">Not delivered. {test.data.message}</span>
              ))}
            {test.isError && <span className="text-red">{test.error.message}</span>}
          </span>
        </div>
      </section>
      <section aria-labelledby="recent-title" className="flex flex-col gap-2">
        <h3 id="recent-title" className="text-base font-medium small-caps text-dim">
          Recent notifications
        </h3>
        {recent.isError ? (
          <p className="text-base text-red">{recent.error.message}</p>
        ) : (recent.data ?? []).length === 0 ? (
          <p className="text-base text-dim">
            {recent.isPending ? 'Loading…' : 'Nothing has been sent yet.'}
          </p>
        ) : (
          <div className="rounded-md border border-seam">
            <Table aria-label="Recent notifications">
              <THead>
                <Tr>
                  <Th className="pl-3">Queued</Th>
                  <Th>Event</Th>
                  <Th>Status</Th>
                  <Th className="text-right">Attempts</Th>
                  <Th className="pr-3">Last result</Th>
                </Tr>
              </THead>
              <TBody>
                {(recent.data ?? []).map((n) => (
                  <Tr key={n.id}>
                    <Td className="pl-3">{formatTime(n.created_at)}</Td>
                    <Td className="font-sans text-text">{eventLabel(n.event)}</Td>
                    <Td>
                      {n.status === 'pending' && n.attempts === 0 ? (
                        <Badge>Queued</Badge>
                      ) : (
                        <Badge tone={statusMeta[n.status].tone}>{statusMeta[n.status].label}</Badge>
                      )}
                    </Td>
                    <Td className="text-right">{n.attempts}</Td>
                    <Td className="max-w-80 truncate pr-3 font-sans" title={n.last_error}>
                      {n.status === 'delivered'
                        ? `HTTP ${n.last_status ?? ''}`
                        : n.status === 'pending'
                          ? n.attempts === 0
                            ? 'Waiting to be sent.'
                            : `${n.last_error ?? ''} Next try ${formatTime(n.next_attempt_at)}.`
                          : (n.last_error ?? '')}
                    </Td>
                  </Tr>
                ))}
              </TBody>
            </Table>
          </div>
        )}
      </section>
    </div>
  )
}
