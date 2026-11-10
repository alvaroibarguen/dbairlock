import { render, screen, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { RowDiff, type RowImage } from './RowDiff'
import { highlightSql } from './sqlHighlight'
import { byteToIndex } from './textOffset'

const image = (over: Partial<RowImage>): RowImage => ({
  columns: ['id', 'name', 'status'],
  primary_key: ['id'],
  has_primary_key: true,
  rows_affected: 2,
  truncated: false,
  row_images: true,
  ...over,
})

describe('RowDiff', () => {
  it('pairs before and after rows by key and marks changed values', () => {
    render(
      <RowDiff
        kind="update"
        caption="Statement 1"
        image={image({
          before: [
            [2, 'Bob', 'active'],
            [1, 'Ann', 'active'],
          ],
          after: [
            [1, 'Zed', 'active'],
            [2, 'Zed', 'active'],
          ],
        })}
      />,
    )
    const table = screen.getByRole('table', { name: 'Statement 1' })
    const [header, before2, after2, ...rest] = within(table).getAllByRole('row')
    if (!header || !before2 || !after2) throw new Error('rows missing')
    expect([before2, after2, ...rest].map((r) => r.getAttribute('data-side'))).toEqual([
      'before',
      'after',
      'before',
      'after',
    ])
    // Row 2 before, then row 2 after.
    expect(before2).toHaveTextContent('2Bob')
    expect(after2).toHaveTextContent('2Zed')
    expect(within(before2).getByText('Bob').closest('td')).toHaveClass('line-through')
    expect(within(after2).getByText('Zed').closest('td')).toHaveClass('text-green')
    // Unchanged values are not marked.
    expect(within(after2).getByText('active').closest('td')).not.toHaveClass('text-green')
    expect(screen.getByText(/2 of 2 rows · 1 column changes/)).toBeInTheDocument()
  })

  it('shows nulls, never interprets markup, and expands long values', () => {
    const long = '<b>x</b>'.repeat(20)
    render(
      <RowDiff
        kind="delete"
        caption="Statement 1"
        image={image({ rows_affected: 1, before: [[1, null, long]] })}
      />,
    )
    expect(screen.getByText('null')).toBeInTheDocument()
    expect(document.querySelector('b')).toBeNull()
    expect(screen.getByText(/^<b>x<\/b>/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Show the whole value' })).toBeInTheDocument()
  })

  it('warns about tables without a primary key and missing row images', () => {
    const { rerender } = render(
      <RowDiff
        kind="delete"
        caption="s"
        image={image({ primary_key: [], has_primary_key: false, before: [[1, 'a', 'b']] })}
      />,
    )
    expect(screen.getByText(/This table has no primary key/)).toBeInTheDocument()
    rerender(<RowDiff kind="delete" caption="s" image={image({ row_images: false })} />)
    expect(screen.getByText(/does not store row images/)).toBeInTheDocument()
  })

  it('marks rows that differ from the previous preview', () => {
    render(
      <RowDiff
        kind="delete"
        caption="s"
        image={image({
          before: [
            [1, 'Ann', 'x'],
            [5, 'Eve', 'x'],
          ],
        })}
        previous={image({
          before: [
            [1, 'Ann', 'x'],
            [3, 'Cat', 'x'],
          ],
        })}
      />,
    )
    expect(screen.getAllByText('(changed since the last preview)')).toHaveLength(1)
    expect(screen.getByText(/1 row no longer matched/)).toBeInTheDocument()
  })
})

describe('highlightSql', () => {
  it('colours keywords, strings, numbers and calls, keeping the text', () => {
    const text = "UPDATE users SET name = lower('Zed'), n = 2 WHERE id = 1"
    const lines = highlightSql(text, 'postgresql')
    expect(
      lines
        .flat()
        .map((s) => s.text)
        .join(''),
    ).toBe(text)
    const cls = (t: string) => lines.flat().find((s) => s.text === t)?.className
    expect(cls('UPDATE')).toBe('text-blue')
    expect(cls("'Zed'")).toBe('text-green')
    expect(cls('2')).toBe('text-amber')
    expect(cls('lower')).toBe('text-violet')
  })

  it('does not take the table of an INSERT for a call', () => {
    const lines = highlightSql('INSERT INTO users (id) VALUES (1)', 'postgresql')
    expect(
      lines.flat().some((s) => s.text.includes('users') && s.className === 'text-violet'),
    ).toBe(false)
  })
})

describe('byteToIndex', () => {
  it('converts UTF-8 byte offsets to string indexes', () => {
    expect(byteToIndex('abc', 2)).toBe(2)
    expect(byteToIndex("'é' x", 4)).toBe(3)
    expect(byteToIndex('😀 x', 5)).toBe(3)
    expect(byteToIndex('abc', -1)).toBe(0)
  })
})
