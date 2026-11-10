import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter } from 'react-router'
import { RouterProvider } from 'react-router/dom'
import { describe, expect, it, vi } from 'vitest'
import type { Me, Settings } from '@/api'
import { Providers } from './Providers'
import { createQueryClient } from './queryClient'
import { routes } from './router'

const settings: Settings = {
  values: {
    'instance.name': 'dbairlock',
    'instance.public_url': '',
    'instance.trusted_proxies': [],
    'auth.oidc_issuer': 'https://accounts.google.com',
    'auth.google.client_id': '123.apps.googleusercontent.com',
    'auth.allowed_domains': ['example.com'],
    'auth.session_hours': 12,
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
  },
  secrets: {
    'auth.google.client_secret': {
      is_set: true,
      set_by: 'admin@example.com',
      set_at: '2026-11-09T10:00:00Z',
    },
    'notify.webhook_url': { is_set: false },
  },
}

const me: Me = {
  user: {
    id: 'u1',
    email: 'admin@example.com',
    name: 'Ada Admin',
    role: 'admin',
    created_at: '2026-11-09T10:00:00Z',
  },
  permissions: [
    'change.submit',
    'change.review',
    'database.manage',
    'settings.manage',
    'user.manage',
    'audit.read',
  ],
}

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

/** A fake API: GET returns the settings; PATCH answers with `patch` and records the body. */
function mockApi(patch: (body: unknown) => Response = () => json(200, settings)) {
  const patches: unknown[] = []
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
    const req = input instanceof Request ? input : new Request(String(input), init)
    const path = new URL(req.url, 'http://localhost').pathname
    if (path === '/api/settings' && req.method === 'GET') return json(200, settings)
    if (path === '/api/settings' && req.method === 'PATCH') {
      const body: unknown = await req.json()
      patches.push(body)
      return patch(body)
    }
    if (path === '/api/meta') {
      return json(200, {
        version: 'test',
        setupState: 'ready',
        instanceName: 'dbairlock',
        devLogin: false,
      })
    }
    if (path === '/api/me') return json(200, me)
    return json(404, { error: { code: 'not_found', message: 'Not found.' } })
  })
  return patches
}

function renderAt(path: string) {
  const router = createMemoryRouter(routes, { initialEntries: [path] })
  render(
    <Providers client={createQueryClient()}>
      <RouterProvider router={router} />
    </Providers>,
  )
  return router
}

describe('settings page', () => {
  it('lists the sections and opens General by default', async () => {
    mockApi()
    renderAt('/settings')
    const nav = await screen.findByRole('navigation', { name: 'Settings sections' })
    for (const name of [
      'General',
      'Sign-in',
      'Review policy',
      'Limits',
      'Notifications',
      'Appearance',
    ]) {
      expect(within(nav).getByRole('link', { name })).toBeInTheDocument()
    }
    expect(await screen.findByLabelText('Instance name')).toHaveValue('dbairlock')
  })

  it('shows secret status, never a value, and validates before sending', async () => {
    const patches = mockApi()
    renderAt('/settings/sign-in')
    const secret = await screen.findByRole('group', { name: 'Google client secret' })
    expect(secret).toHaveTextContent(/Set by admin@example\.com on/)
    expect(screen.getByRole('note')).toHaveTextContent(/lock everyone out/)

    const hours = screen.getByLabelText('Session length')
    await userEvent.clear(hours)
    await userEvent.type(hours, '0')
    await userEvent.click(screen.getByRole('button', { name: 'Save changes' }))
    expect(await screen.findByText('Must be between 1 and 720.')).toBeInTheDocument()
    expect(patches).toHaveLength(0)

    await userEvent.clear(hours)
    await userEvent.type(hours, '24')
    await userEvent.click(within(secret).getByRole('button', { name: 'Replace' }))
    await userEvent.type(within(secret).getByPlaceholderText('New value'), 'n3w-secret')
    await userEvent.click(screen.getByRole('button', { name: 'Save changes' }))
    await waitFor(() => expect(patches).toHaveLength(1))
    expect(patches[0]).toEqual({
      values: { 'auth.session_hours': 24 },
      secrets: { 'auth.google.client_secret': 'n3w-secret' },
    })
    // After saving, the secret is back to its status line with no input.
    await waitFor(() => expect(within(secret).queryByPlaceholderText('New value')).toBeNull())
  })

  it('shows server field errors on their fields', async () => {
    mockApi(() =>
      json(422, {
        error: {
          code: 'validation_failed',
          message: 'Some settings are invalid. Nothing was changed.',
          fields: [
            {
              field: 'instance.public_url',
              message: 'Must not contain credentials, a query or a fragment.',
            },
          ],
        },
      }),
    )
    renderAt('/settings/general')
    const url = await screen.findByLabelText('Public URL')
    await userEvent.type(url, 'https://dbairlock.example.com')
    await userEvent.click(screen.getByRole('button', { name: 'Save changes' }))
    expect(
      await screen.findByText('Must not contain credentials, a query or a fragment.'),
    ).toBeInTheDocument()
  })

  it('clears a secret with null', async () => {
    const patches = mockApi()
    renderAt('/settings/sign-in')
    const secret = await screen.findByRole('group', { name: 'Google client secret' })
    await userEvent.click(within(secret).getByRole('button', { name: 'Clear' }))
    await userEvent.click(screen.getByRole('button', { name: 'Save changes' }))
    await waitFor(() =>
      expect(patches).toEqual([{ secrets: { 'auth.google.client_secret': null } }]),
    )
  })
})
