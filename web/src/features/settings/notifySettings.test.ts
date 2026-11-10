import { describe, expect, it } from 'vitest'
import type { Settings } from '@/api'
import { groupSchema, settingsGroups, toFormValues, toUpdate } from './settingsForm'

const notifications = settingsGroups.find((g) => g.id === 'notifications')
if (!notifications) throw new Error('no notifications group')

const values = {
  'notify.enabled': true,
  'notify.format': 'slack',
  'notify.events': ['change.approved', 'change.submitted'],
} as unknown as Settings['values']

const settings = {
  values,
  secrets: {
    'auth.google.client_secret': { is_set: false },
    'notify.webhook_url': {
      is_set: true,
      set_by: 'ada@example.com',
      set_at: '2026-11-09T10:00:00Z',
    },
  },
} as Settings

describe('notification settings', () => {
  it('loads the format and the events', () => {
    const v = toFormValues(notifications, settings)
    expect(v.notify__format).toBe('slack')
    expect(v.notify__events).toEqual(['change.approved', 'change.submitted'])
    expect(v.secret__notify__webhook_url).toBeUndefined()
  })

  it('validates like the server', () => {
    const schema = groupSchema(notifications)
    const base = toFormValues(notifications, settings)
    expect(schema.safeParse(base).success).toBe(true)
    const bad = schema.safeParse({ ...base, notify__format: 'teams' })
    expect(bad.error?.issues[0]?.message).toBe('Must be one of: generic, slack.')
    expect(schema.safeParse({ ...base, notify__events: ['change.created'] }).success).toBe(false)
    expect(schema.safeParse({ ...base, notify__events: [] }).success).toBe(true)
  })

  it('sends events in the server order and the URL as a secret', () => {
    const v = {
      ...toFormValues(notifications, settings),
      notify__format: 'generic',
      notify__events: ['change.failed', 'change.submitted'],
      secret__notify__webhook_url: 'https://hooks.example.com/x',
    }
    expect(toUpdate(notifications, v, { notify__format: true, notify__events: [true] })).toEqual({
      values: {
        'notify.format': 'generic',
        'notify.events': ['change.submitted', 'change.failed'],
      },
      secrets: { 'notify.webhook_url': 'https://hooks.example.com/x' },
    })
  })
})
