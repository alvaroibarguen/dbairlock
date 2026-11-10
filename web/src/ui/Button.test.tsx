import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { Button } from './Button'

describe('Button', () => {
  it('calls onClick when enabled', async () => {
    const onClick = vi.fn()
    render(<Button onClick={onClick}>Approve</Button>)
    await userEvent.click(screen.getByRole('button', { name: 'Approve' }))
    expect(onClick).toHaveBeenCalledOnce()
  })

  it('ignores clicks when disabled', async () => {
    const onClick = vi.fn()
    render(
      <Button disabled onClick={onClick}>
        Approve
      </Button>,
    )
    const button = screen.getByRole('button', { name: 'Approve' })
    expect(button).toBeDisabled()
    await userEvent.click(button)
    expect(onClick).not.toHaveBeenCalled()
  })

  it('is busy, inert and still focusable while loading', async () => {
    const onClick = vi.fn()
    render(
      <Button loading onClick={onClick}>
        Executing
      </Button>,
    )
    const button = screen.getByRole('button', { name: 'Executing' })
    expect(button).toHaveAttribute('aria-busy', 'true')
    expect(button).toHaveAttribute('aria-disabled', 'true')
    await userEvent.tab()
    expect(button).toHaveFocus()
    await userEvent.click(button)
    expect(onClick).not.toHaveBeenCalled()
  })

  it('renders the keyboard hint without changing the accessible name', () => {
    render(
      <Button variant="primary" shortcut="⌘↵">
        Approve & execute
      </Button>,
    )
    const button = screen.getByRole('button', { name: 'Approve & execute' })
    expect(button).toHaveTextContent('⌘↵')
    expect(button).toHaveAttribute('data-variant', 'primary')
  })
})
