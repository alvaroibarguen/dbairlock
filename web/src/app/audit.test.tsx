import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter } from 'react-router'
import { RouterProvider } from 'react-router/dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Me } from '@/api'
import type { AuditEvent, AuditVerification } from '@/api/audit'
import { Providers } from './Providers'
import { createQueryClient } from './queryClient'
import { routes } from './router'

const me: Me = {
  user: {
    id: 'u1',
    email: 'ada@example.com',
    name: 'Ada',
    role: 'admin',
    created_at: '2026-11-09T10:00:00Z',
  },
  permissions: ['change.submit', 'change.review', 'settings.manage', 'user.manage', 'audit.read'],
}

const hash = (c: string) => c.repeat(64)

function event(seq: number, over: Partial<AuditEvent> = {}): AuditEvent {
  return {
    seq,
    id: `evt-${seq}`,
    occurred_at: '2026-11-09T10:00:00Z',
    actor: { via: 'web', user_id: 'u1', email: 'ada@example.com', ip: '10.0.0.1' },
    action: 'change.approved',
    subject_type: 'change',
    subject_id: 'c-42',
    payload: { number: 42 },
    prev_hash: hash('a'),
    hash: hash('b'),
    ...over,
  }
}

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

interface Api {
  events?: (q: URLSearchParams) => unknown
  verification?: AuditVerification
}

function mockApi({ events, verification }: Api) {
  const calls: URL[] = []
  vi.spyOn(globalThis, 'fetch').mockImplementation((input, init) => {
    const req = input instanceof Request ? input : new Request(String(input), init)
    const url = new URL(req.url, 'http://localhost')
    calls.push(url)
    const answer = (status: number, body: unknown) => Promise.resolve(json(status, body))
    switch (url.pathname) {
      case '/api/meta':
        return answer(200, {
          version: 't',
          setupState: 'ready',
          instanceName: 'x',
          devLogin: false,
        })
      case '/api/me':
        return answer(200, me)
      case '/api/audit':
        return answer(200, events?.(url.searchParams) ?? { events: [] })
      case '/api/audit/verify':
        return answer(200, verification ?? { state: 'none', checked: 0 })
      case '/api/system':
        return answer(200, {
          version: 'v1.2.3',
          commit: 'abc123',
          data_dir: '/data',
          store_bytes: 2_500_000,
          dev: false,
          counts: { users: 3, connections: 2, changes: 17 },
          migration: { current: 6, latest: 6, pending: false },
        })
    }
    return answer(404, { error: { code: 'not_found', message: 'Not found.' } })
  })
  return calls
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

afterEach(() => vi.restoreAllMocks())

describe('audit page', () => {
  it('lists events with labels and links subjects to their pages', async () => {
    mockApi({
      events: () => ({
        events: [
          event(2),
          event(1, { action: 'brand.new_action', subject_type: '', subject_id: '' }),
        ],
      }),
    })
    renderAt('/audit')
    const table = await screen.findByRole('table', { name: 'Audit events' })
    expect(within(table).getByRole('button', { name: /Change approved, event 2/ })).toBeVisible()
    // Unknown actions fall back to the raw name.
    expect(within(table).getByRole('button', { name: /brand\.new_action, event 1/ })).toBeVisible()
    expect(within(table).getByRole('link', { name: 'c-42' })).toHaveAttribute(
      'href',
      '/changes/c-42',
    )
  })

  it('keeps filters in the URL and sends them to the server', async () => {
    const calls = mockApi({ events: () => ({ events: [event(5)], next_before: 5 }) })
    const router = renderAt('/audit?action=change.&via=mcp')
    await screen.findByRole('table', { name: 'Audit events' })
    const lastAudit = () => calls.filter((u) => u.pathname === '/api/audit').at(-1)
    expect(lastAudit()?.searchParams.get('action')).toBe('change.')
    expect(lastAudit()?.searchParams.get('via')).toBe('mcp')

    await userEvent.type(screen.getByLabelText('Actor'), 'bob@example.com')
    await userEvent.click(screen.getByRole('button', { name: 'Apply' }))
    await waitFor(() => expect(router.state.location.search).toContain('actor=bob%40example.com'))
    expect(router.state.location.search).toContain('action=change.')

    await userEvent.click(screen.getByRole('button', { name: 'Older' }))
    await waitFor(() => expect(router.state.location.search).toContain('before=5'))
    await waitFor(() => expect(lastAudit()?.searchParams.get('before')).toBe('5'))
  })

  it('shows the full event, payload as plain text, in a drawer', async () => {
    mockApi({
      events: () => ({
        events: [event(7, { payload: { title: '<img src=x onerror=alert(1)>', number: 7 } })],
      }),
    })
    const router = renderAt('/audit')
    await userEvent.click(await screen.findByRole('button', { name: /event 7: show details/ }))
    const drawer = await screen.findByRole('dialog')
    expect(router.state.location.search).toContain('event=7')
    expect(within(drawer).getByText(/"title": "<img src=x onerror=alert\(1\)>"/)).toBeVisible()
    expect(drawer.querySelector('img')).toBeNull()
    expect(within(drawer).getByText(hash('b'))).toBeVisible()
    expect(within(drawer).getByText('10.0.0.1')).toBeVisible()
  })
})

describe('system settings', () => {
  it('names the first broken link after a verification', async () => {
    mockApi({
      verification: {
        state: 'done',
        checked: 12,
        ok: false,
        problem: { seq: 13, reason: 'hash mismatch: the event was modified' },
        finished_at: '2026-11-09T10:00:00Z',
      },
    })
    renderAt('/settings/system')
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('The chain is broken at event #13: hash mismatch')
    expect(
      within(alert).getByRole('link', { name: 'Open event #13 in the audit log' }),
    ).toHaveAttribute('href', '/audit?before=14&event=13')
    expect(screen.getByText('Never verified from the dashboard.')).toBeVisible()
    expect(screen.getByText('abc123')).toBeVisible()
    expect(screen.getByText('2.4 MB')).toBeVisible()
  })
})
