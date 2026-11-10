import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter } from 'react-router'
import { RouterProvider } from 'react-router/dom'
import { describe, expect, it, vi } from 'vitest'
import { Providers } from './Providers'
import { createQueryClient } from './queryClient'
import { routes } from './router'

function renderAt(path: string) {
  // No server in tests: the queue is empty, everything else is offline.
  vi.spyOn(globalThis, 'fetch').mockImplementation((input: RequestInfo | URL) => {
    const url = input instanceof Request ? input.url : String(input)
    if (new URL(url).pathname === '/api/changes') {
      return Promise.resolve(
        new Response(JSON.stringify({ changes: [], needs_my_review: 0 }), {
          headers: { 'Content-Type': 'application/json' },
        }),
      )
    }
    return Promise.reject(new TypeError('offline'))
  })
  const router = createMemoryRouter(routes, { initialEntries: [path] })
  render(
    <Providers client={createQueryClient()}>
      <RouterProvider router={router} />
    </Providers>,
  )
  return router
}

describe('app shell', () => {
  it('shows the rail and the empty three-pane changes screen', async () => {
    renderAt('/changes')
    const nav = await screen.findByRole('navigation', { name: 'Main' })
    for (const label of ['Changes', 'New change', 'Audit log', 'Connections', 'Settings']) {
      expect(within(nav).getByRole('link', { name: label })).toBeInTheDocument()
    }
    expect(within(nav).getByRole('link', { name: 'Changes' })).toHaveAttribute(
      'aria-current',
      'page',
    )
    expect(await screen.findByText('Nothing is waiting for your review.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /^Needs my review/ })).toHaveAttribute(
      'aria-pressed',
      'true',
    )
    expect(screen.getByText('Select a change from the queue to review it.')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: /Affected rows/ })).toBeInTheDocument()
  })

  it('navigates from the rail by keyboard', async () => {
    const router = renderAt('/changes')
    const nav = await screen.findByRole('navigation', { name: 'Main' })
    within(nav).getByRole('link', { name: 'Audit log' }).focus()
    await userEvent.keyboard('{Enter}')
    expect(router.state.location.pathname).toBe('/audit')
    expect(await screen.findByRole('heading', { level: 1, name: 'Audit log' })).toBeInTheDocument()
  })

  it('redirects / to /changes', async () => {
    const router = renderAt('/')
    await screen.findByRole('navigation', { name: 'Main' })
    expect(router.state.location.pathname).toBe('/changes')
  })
})
