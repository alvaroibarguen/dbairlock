import { describe, expect, it } from 'vitest'
import type { Change } from '@/api'
import { clockTime, shortAge, statementSummary } from './format'
import { freshnessOf, staleMessage } from './freshness'

const at = Date.parse('2026-03-02T09:00:00Z')
const change = (over: Partial<Change>) =>
  ({ previewed_at: new Date(at).toISOString(), preview_max_age_seconds: 300, ...over }) as Change

describe('freshness', () => {
  it('counts the age on the given clock and turns stale past the limit', () => {
    expect(freshnessOf(change({}), at + 60_000)).toMatchObject({
      age: 60_000,
      stale: false,
      ending: false,
    })
    expect(freshnessOf(change({}), at + 250_000)).toMatchObject({ stale: false, ending: true })
    const stale = freshnessOf(change({}), at + 12 * 60_000)
    expect(stale.stale).toBe(true)
    expect(staleMessage(stale)).toBe(
      'This preview is 12 minutes old. Approving requires a preview from the last 5 minutes, so you review the data as it is now.',
    )
  })

  it('takes the limit from the setting', () => {
    const f = freshnessOf(change({ preview_max_age_seconds: 600 }), at + 7 * 60_000)
    expect(f.stale).toBe(false)
    expect(staleMessage({ ...f, age: 11 * 60_000 })).toContain('from the last 10 minutes')
  })

  it('treats a missing preview as stale', () => {
    expect(freshnessOf(change({ previewed_at: undefined }), at).stale).toBe(true)
  })
})

describe('format', () => {
  it('formats ages and summaries', () => {
    expect(clockTime(166_000)).toBe('2:46')
    expect(shortAge(at, at + 4 * 60_000)).toBe('4m')
    expect(shortAge(at, at + 3 * 3_600_000)).toBe('3h')
    expect(
      statementSummary([
        {
          position: 0,
          sql: '',
          schema: 'public',
          table: 'users',
          has_primary_key: true,
          kind: 'update',
          expected_rows: 3,
        },
      ]),
    ).toBe('UPDATE · 3 rows')
  })
})
