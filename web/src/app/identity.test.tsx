import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter } from 'react-router'
import { RouterProvider } from 'react-router/dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ApiToken, Me, User } from '@/api'
import { Providers } from './Providers'
import { createQueryClient } from './queryClient'
import { routes } from './router'

const json = (status: number, body?: unknown) =>
  body === undefined
    ? new Response(null, { status })
    : new Response(JSON.stringify(body), {
        status,
        headers: { 'Content-Type': 'application/json' },
      })

const ada: User = {
  id: 'u1',
  email: 'ada@example.com',
  name: 'Ada Lovelace',
  role: 'admin',
  created_at: '2026-11-09T10:00:00Z',
  last_sign_in_at: '2026-11-09T11:00:00Z',
}
const bob: User = {
  id: 'u2',
  email: 'bob@example.com',
  name: 'Bob',
  role: 'member',
  created_at: '2026-11-09T10:00:00Z',
}
const adminMe: Me = {
  user: ada,
  permissions: [
    'change.submit',
    'change.review',
    'database.manage',
    'settings.manage',
    'user.manage',
    'audit.read',
  ],
}
const memberMe: Me = {
  user: bob,
  permissions: ['change.submit', 'change.review', 'audit.read'],
}
const token: ApiToken = {
  id: 't1',
  name: 'laptop agent',
  prefix: 'dbal_pat_AbCd',
  owner_id: 'u1',
  owner_email: 'ada@example.com',
  created_at: '2026-11-09T10:00:00Z',
}

type Route = (req: Request, body: unknown) => Response | undefined

interface Call {
  method: string
  path: string
  body: unknown
}

/** A fake API: `route` answers what it knows; meta and me have defaults. */
function mockApi(options: { me?: Me; meta?: object; route?: Route } = {}) {
  const calls: Call[] = []
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
    const req = input instanceof Request ? input : new Request(String(input), init)
    const url = new URL(req.url, 'http://localhost')
    const text = req.method === 'GET' ? '' : await req.text()
    const body: unknown = text ? JSON.parse(text) : undefined
    calls.push({ method: req.method, path: url.pathname + url.search, body })
    const answer = options.route?.(req, body)
    if (answer) return answer
    if (url.pathname === '/api/meta') {
      return json(200, {
        version: 'test',
        setupState: 'ready',
        instanceName: 'Acme dbairlock',
        devLogin: false,
        ...options.meta,
      })
    }
    if (url.pathname === '/api/me') return json(200, options.me ?? adminMe)
    return json(404, { error: { code: 'not_found', message: 'Not found.' } })
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

afterEach(() => {
  vi.restoreAllMocks()
})

describe('sign-in page', () => {
  it('shows the instance name, one button and a plain reason for a refusal', async () => {
    mockApi()
    renderAt('/signin?error=account_removed&next=%2Fchanges%2F7')
    expect(await screen.findByText('Acme dbairlock')).toBeInTheDocument()
    expect(screen.getByRole('alert')).toHaveTextContent(/access to dbairlock has been removed/)
    // A link rendered as a button (Base UI gives it role="button").
    expect(screen.getByRole('button', { name: 'Sign in with Google' })).toHaveAttribute(
      'href',
      '/api/auth/google/start?next=%2Fchanges%2F7',
    )
    expect(screen.queryByRole('form', { name: 'Development sign-in' })).toBeNull()
  })

  it('explains a wrong domain and never sends the user off-site afterwards', async () => {
    mockApi()
    renderAt('/signin?error=domain_not_allowed&next=%2F%2Fevil.example.com')
    expect(await screen.findByRole('alert')).toHaveTextContent(/not in a domain allowed/)
    expect(screen.getByRole('button', { name: 'Sign in with Google' })).toHaveAttribute(
      'href',
      '/api/auth/google/start',
    )
  })

  it('offers the development sign-in only in dev mode', async () => {
    const calls = mockApi({
      meta: { devLogin: true },
      route: (req) => {
        if (new URL(req.url).pathname === '/api/dev/login') return json(204)
        return undefined
      },
    })
    renderAt('/signin')
    const form = await screen.findByRole('form', { name: 'Development sign-in' })
    await userEvent.click(within(form).getByRole('button', { name: 'Sign in as admin' }))
    await waitFor(() =>
      expect(calls.find((c) => c.path === '/api/dev/login')?.body).toEqual({
        email: 'admin@dev.localhost',
        role: 'admin',
      }),
    )
  })
})

describe('setup wizard', () => {
  it('walks from the code to the sign-in settings to the final Google sign-in', async () => {
    let session = false
    const calls = mockApi({
      meta: { setupState: 'setup_required' },
      route: (req) => {
        const path = new URL(req.url).pathname
        if (path === '/api/setup/session') {
          return session
            ? json(200, { kind: 'setup' })
            : json(401, { error: { code: 'setup_session_required', message: 'Enter the code.' } })
        }
        if (path === '/api/setup/verify') {
          session = true
          return json(204)
        }
        if (path === '/api/setup/signin' && req.method === 'GET') {
          return json(200, {
            public_url: '',
            suggested_public_url: 'https://dbairlock.example.com',
            client_id: '',
            client_secret: { is_set: false },
            allowed_domains: [],
            redirect_uri: '',
          })
        }
        if (path === '/api/setup/signin' && req.method === 'PUT') {
          return json(200, {
            public_url: 'https://dbairlock.example.com',
            suggested_public_url: 'https://dbairlock.example.com',
            client_id: 'cid',
            client_secret: { is_set: true, set_by: 'system', set_at: '2026-11-09T10:00:00Z' },
            allowed_domains: ['example.com'],
            redirect_uri: 'https://dbairlock.example.com/api/auth/google/callback',
          })
        }
        return undefined
      },
    })
    renderAt('/setup')
    const code = await screen.findByLabelText('Setup code')
    await userEvent.type(code, 'abcde-fghij-klmno-pqrst')
    await userEvent.click(screen.getByRole('button', { name: 'Continue' }))

    // The redirect URI follows the public URL, ready to copy.
    const url = await screen.findByLabelText('Public URL')
    expect(url).toHaveValue('https://dbairlock.example.com')
    expect(
      screen.getByText('https://dbairlock.example.com/api/auth/google/callback'),
    ).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Copy URI' })).toBeInTheDocument()

    // Every field is required before anything is sent.
    await userEvent.click(screen.getByRole('button', { name: 'Save and continue' }))
    expect(await screen.findByText('Enter at least one domain.')).toBeInTheDocument()
    expect(calls.some((c) => c.method === 'PUT')).toBe(false)

    await userEvent.type(screen.getByLabelText('Google client ID'), 'cid')
    const secret = screen.getByRole('group', { name: 'Google client secret' })
    await userEvent.type(within(secret).getByPlaceholderText('Value'), 's3cret')
    await userEvent.type(screen.getByLabelText('Allowed domains'), 'Example.com{Enter}')
    expect(screen.getByRole('button', { name: 'Remove example.com' })).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Save and continue' }))

    const finish = await screen.findByRole('button', { name: 'Sign in with Google to finish' })
    expect(finish).toHaveAttribute('href', '/api/setup/google/start')
    expect(calls.find((c) => c.method === 'PUT')?.body).toEqual({
      public_url: 'https://dbairlock.example.com',
      client_id: 'cid',
      client_secret: 's3cret',
      allowed_domains: ['example.com'],
    })
    expect(calls.find((c) => c.path === '/api/setup/verify')?.body).toEqual({
      code: 'abcde-fghij-klmno-pqrst',
    })
  })
})

describe('users and tokens', () => {
  it('hides admin sections from members', async () => {
    mockApi({
      me: memberMe,
      route: (req) => {
        if (new URL(req.url).pathname === '/api/tokens') return json(200, { tokens: [] })
        return undefined
      },
    })
    renderAt('/settings')
    const nav = await screen.findByRole('navigation', { name: 'Settings sections' })
    const names = within(nav)
      .getAllByRole('link')
      .map((l) => l.textContent)
    expect(names).toEqual(['API tokens', 'Appearance'])
    expect(await screen.findByText('You have no active tokens.')).toBeInTheDocument()
  })

  it('states what removing a user does and removes them', async () => {
    let removed = false
    const calls = mockApi({
      route: (req) => {
        const path = new URL(req.url).pathname
        if (path === '/api/users') {
          return json(200, {
            users: [
              ada,
              removed
                ? { ...bob, removed_at: '2026-11-09T12:00:00Z', removed_by: 'ada@example.com' }
                : bob,
            ],
          })
        }
        if (path === '/api/users/u2/remove') {
          removed = true
          return json(200, { ...bob, removed_at: '2026-11-09T12:00:00Z' })
        }
        return undefined
      },
    })
    renderAt('/settings/users')
    const table = await screen.findByRole('table', { name: 'Users' })
    const row = within(table).getByText('bob@example.com').closest('tr')
    if (!row) throw new Error('no row')
    expect(within(table).getByText('you')).toBeInTheDocument()
    await userEvent.click(within(row).getByRole('button', { name: 'Remove' }))
    const dialog = await screen.findByRole('dialog', { name: 'Remove bob@example.com?' })
    expect(dialog).toHaveTextContent(/Signs them out of every browser/)
    expect(dialog).toHaveTextContent(/Revokes all their API tokens and connected apps permanently/)
    expect(dialog).toHaveTextContent(/Cancels their open changes/)
    await userEvent.click(within(dialog).getByRole('button', { name: 'Remove user' }))
    await waitFor(() => expect(calls.some((c) => c.path === '/api/users/u2/remove')).toBe(true))
    await userEvent.click(await screen.findByRole('button', { name: /Removed 1/ }))
    const removedTable = await screen.findByRole('table', { name: 'Removed users' })
    expect(within(removedTable).getByRole('button', { name: 'Restore' })).toBeInTheDocument()
  })

  it('shows a new token once, with its MCP configuration, and revokes tokens', async () => {
    let tokens: ApiToken[] = [token]
    const calls = mockApi({
      route: (req) => {
        const path = new URL(req.url).pathname
        if (path === '/api/tokens' && req.method === 'GET') return json(200, { tokens })
        if (path === '/api/tokens' && req.method === 'POST') {
          const created = { ...token, id: 't2', name: 'ci bot', prefix: 'dbal_pat_ZzZz' }
          tokens = [created, ...tokens]
          return json(201, {
            token: created,
            secret: 'dbal_pat_ZzZzSECRETSECRET',
            mcp_url: 'https://dbairlock.example.com/mcp',
          })
        }
        if (path === '/api/tokens/t1' && req.method === 'DELETE') {
          tokens = tokens.filter((t) => t.id !== 't1')
          return json(204)
        }
        return undefined
      },
    })
    renderAt('/settings/api-tokens')
    expect(await screen.findByText('dbal_pat_AbCd…')).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: 'Create token' }))
    const dialog = await screen.findByRole('dialog', { name: 'Create an API token' })
    await userEvent.type(within(dialog).getByLabelText('Name'), 'ci bot')
    await userEvent.click(within(dialog).getByRole('button', { name: 'Create token' }))

    const shown = await screen.findByRole('dialog', { name: 'Token "ci bot" created' })
    expect(shown).toHaveTextContent('dbal_pat_ZzZzSECRETSECRET')
    expect(shown).toHaveTextContent(/shown only once/)
    expect(shown).toHaveTextContent('"Authorization": "Bearer dbal_pat_ZzZzSECRETSECRET"')
    expect(shown).toHaveTextContent('"url": "https://dbairlock.example.com/mcp"')
    expect(within(shown).getByRole('button', { name: 'Copy token' })).toBeInTheDocument()
    expect(calls.find((c) => c.method === 'POST')?.body).toEqual({
      name: 'ci bot',
      expires_in_days: 90,
    })
    await userEvent.click(within(shown).getByRole('button', { name: 'Done' }))
    await waitFor(() => expect(screen.queryByText('dbal_pat_ZzZzSECRETSECRET')).toBeNull())

    const table = await screen.findByRole('table', { name: 'API tokens' })
    const row = within(table).getByText('laptop agent').closest('tr')
    if (!row) throw new Error('no row')
    await userEvent.click(within(row).getByRole('button', { name: 'Revoke' }))
    const confirm = await screen.findByRole('dialog', { name: 'Revoke "laptop agent"?' })
    await userEvent.click(within(confirm).getByRole('button', { name: 'Revoke token' }))
    await waitFor(() => expect(screen.queryByText('laptop agent')).toBeNull())
  })
})
