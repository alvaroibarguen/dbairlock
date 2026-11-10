import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter } from 'react-router'
import { RouterProvider } from 'react-router/dom'
import { describe, expect, it, vi } from 'vitest'
import { Providers } from './Providers'
import { createQueryClient } from './queryClient'
import { routes } from './router'

function renderAt(path: string) {
  // The shell asks for /api/meta; no server in tests.
  vi.spyOn(globalThis, 'fetch').mockRejectedValue(new TypeError('offline'))
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
    expect(screen.getByText('No changes are waiting for review.')).toBeInTheDocument()
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
