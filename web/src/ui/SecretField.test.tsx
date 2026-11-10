import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { type SecretChange, SecretField, type SecretStatusView } from './SecretField'

function Harness({
  status,
  onChange,
}: {
  status: SecretStatusView
  onChange?: (v: SecretChange) => void
}) {
  const [value, setValue] = useState<SecretChange>(undefined)
  return (
    <>
      <SecretField
        label="Client secret"
        status={status}
        value={value}
        onChange={(v) => {
          setValue(v)
          onChange?.(v)
        }}
      />
      <output data-testid="value">
        {value === undefined ? 'unchanged' : JSON.stringify(value)}
      </output>
    </>
  )
}

const setStatus: SecretStatusView = {
  isSet: true,
  setBy: 'admin@example.com',
  setAt: '2026-11-09T10:00:00Z',
}

describe('SecretField', () => {
  it('shows "Not set" and offers Set but not Clear', () => {
    render(<Harness status={{ isSet: false }} />)
    expect(screen.getByRole('group', { name: 'Client secret' })).toHaveTextContent('Not set')
    expect(screen.getByRole('button', { name: 'Set' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Clear' })).not.toBeInTheDocument()
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument()
  })

  it('shows who set it and when, with Replace and Clear', () => {
    render(<Harness status={setStatus} />)
    const group = screen.getByRole('group', { name: 'Client secret' })
    expect(group).toHaveTextContent(/Set by admin@example\.com on/)
    expect(group.querySelector('time')).toHaveAttribute('datetime', '2026-11-09T10:00:00Z')
    expect(screen.getByRole('button', { name: 'Replace' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Clear' })).toBeInTheDocument()
  })

  it('Replace reveals an empty new-password input with no show toggle', async () => {
    const onChange = vi.fn()
    const { container } = render(<Harness status={setStatus} onChange={onChange} />)
    await userEvent.click(screen.getByRole('button', { name: 'Replace' }))
    const input = container.querySelector('input')
    if (!input) throw new Error('no input after Replace')
    expect(input).toHaveAttribute('type', 'password')
    expect(input).toHaveAttribute('autocomplete', 'new-password')
    expect(input).toHaveValue('')
    expect(input).toHaveFocus()
    expect(screen.queryByRole('button', { name: /show|reveal|view/i })).not.toBeInTheDocument()

    await userEvent.type(input, 'n3w')
    expect(screen.getByTestId('value')).toHaveTextContent('"n3w"')
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(screen.getByTestId('value')).toHaveTextContent('unchanged')
    expect(container.querySelector('input')).toBeNull()
    expect(screen.getByRole('button', { name: 'Replace' })).toHaveFocus()
  })

  it('Clear marks the secret for clearing until saved, and Undo reverts', async () => {
    render(<Harness status={setStatus} />)
    await userEvent.click(screen.getByRole('button', { name: 'Clear' }))
    expect(screen.getByTestId('value')).toHaveTextContent('null')
    expect(screen.getByText('Will be cleared when you save.')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Undo' }))
    expect(screen.getByTestId('value')).toHaveTextContent('unchanged')
  })
})
