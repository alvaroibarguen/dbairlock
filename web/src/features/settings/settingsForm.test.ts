import { describe, expect, it } from 'vitest'
import type { Settings } from '@/api'
import {
  formName,
  groupSchema,
  hiddenSettings,
  settingsGroups,
  toFormValues,
  toUpdate,
} from './settingsForm'

const settings: Settings = {
  values: {
    'instance.name': 'dbairlock',
    'instance.public_url': '',
    'instance.trusted_proxies': [],
    'auth.oidc_issuer': 'https://accounts.google.com',
    'auth.google.client_id': '',
    'auth.allowed_domains': ['example.com'],
    'auth.session_hours': 12,
    'mcp.oauth_enabled': true,
    'policy.default_required_approvals': 1,
    'policy.default_max_rows': 100,
    'policy.default_approval_ttl_hours': 72,
    'policy.default_store_row_images': true,
    'policy.default_auto_execute': true,
    'policy.preview_max_age_minutes': 5,
    'limits.statement_timeout_ms': 10000,
    'limits.lock_timeout_ms': 3000,
    'limits.mcp_calls_per_minute': 60,
    'limits.mcp_previews_per_hour': 30,
    'limits.mcp_submissions_per_hour': 20,
    'notify.enabled': false,
    'notify.format': 'generic',
    'notify.events': ['change.submitted', 'change.approved'],
  },
  secrets: {
    'auth.google.client_secret': { is_set: false },
    'notify.webhook_url': { is_set: false },
  },
}

const group = (id: string) => {
  const g = settingsGroups.find((x) => x.id === id)
  if (!g) throw new Error(id)
  return g
}

function messages(id: string, values: Record<string, unknown>): Record<string, string> {
  const g = group(id)
  const result = groupSchema(g).safeParse({ ...toFormValues(g, settings), ...values })
  const out: Record<string, string> = {}
  if (!result.success) for (const i of result.error.issues) out[String(i.path[0])] = i.message
  return out
}

describe('settings form', () => {
  it('covers every registered setting exactly once', () => {
    const keys = settingsGroups.flatMap((g) =>
      g.fields.filter((f) => f.kind !== 'secret').map((f) => f.key),
    )
    expect([...keys, ...hiddenSettings].sort()).toEqual(Object.keys(settings.values).sort())
    const secrets = settingsGroups.flatMap((g) =>
      g.fields.filter((f) => f.kind === 'secret').map((f) => f.key),
    )
    expect([...secrets].sort()).toEqual(Object.keys(settings.secrets).sort())
  })

  it('uses form names without dots', () => {
    expect(formName({ kind: 'int', key: 'auth.session_hours' })).toBe('auth__session_hours')
    expect(formName({ kind: 'secret', key: 'notify.webhook_url' })).toBe(
      'secret__notify__webhook_url',
    )
  })

  it('mirrors the server rules and messages', () => {
    expect(messages('sign-in', { auth__session_hours: '0' })).toEqual({
      auth__session_hours: 'Must be between 1 and 720.',
    })
    expect(messages('sign-in', { auth__session_hours: '1.5' })).toEqual({
      auth__session_hours: 'Must be a whole number.',
    })
    expect(messages('sign-in', { auth__allowed_domains: 'example.com\nnot a domain' })).toEqual({
      auth__allowed_domains: '"not a domain" is not a domain name, for example example.com.',
    })
    expect(messages('sign-in', { auth__google__client_id: 'has space' })).toEqual({
      auth__google__client_id: 'Must not contain spaces.',
    })
    expect(messages('sign-in', { secret__auth__google__client_secret: '' })).toEqual({
      secret__auth__google__client_secret: 'Enter a value, or cancel.',
    })
    expect(messages('general', { instance__name: '   ' })).toEqual({
      instance__name: 'A value is required.',
    })
    expect(messages('general', { instance__public_url: 'ftp://x.example' })).toHaveProperty(
      'instance__public_url',
    )
    expect(messages('general', { instance__public_url: 'https://x.example/?a=1' })).toEqual({
      instance__public_url: 'Must not contain credentials, a query or a fragment.',
    })
    expect(messages('review-policy', { policy__default_required_approvals: '6' })).toEqual({
      policy__default_required_approvals: 'Must be between 1 and 5.',
    })
    expect(messages('general', { instance__public_url: 'https://dbairlock.example.com' })).toEqual(
      {},
    )
  })

  it('sends only changed settings, and secrets as replace or clear', () => {
    const g = group('sign-in')
    const values = {
      ...toFormValues(g, settings),
      auth__session_hours: '24',
      auth__allowed_domains: 'example.com\ncorp.example.org',
      secret__auth__google__client_secret: 'new-secret',
    }
    expect(toUpdate(g, values, { auth__session_hours: true, auth__allowed_domains: true })).toEqual(
      {
        values: {
          'auth.session_hours': 24,
          'auth.allowed_domains': ['example.com', 'corp.example.org'],
        },
        secrets: { 'auth.google.client_secret': 'new-secret' },
      },
    )
    expect(
      toUpdate(g, { ...toFormValues(g, settings), secret__auth__google__client_secret: null }, {}),
    ).toEqual({
      secrets: { 'auth.google.client_secret': null },
    })
    expect(toUpdate(g, toFormValues(g, settings), {})).toEqual({})
  })
})
