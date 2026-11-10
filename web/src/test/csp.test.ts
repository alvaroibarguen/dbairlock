import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

// The server's Content-Security-Policy allows no inline styles except, by
// hash, the stylesheet the toast library (sonner) inserts when it loads.
// This recomputes that hash from the installed sonner, so an upgrade that
// changes its CSS fails here, with the new hash, instead of silently
// unstyling toasts in production. The Playwright suite fails on any CSP
// violation in a real browser.
const policySource = readFileSync(
  join(import.meta.dirname, '../../../internal/platform/httpx/headers.go'),
  'utf8',
)

describe('content security policy', () => {
  it('allows the style element sonner inserts', async () => {
    const before = new Set(document.head.querySelectorAll('style'))
    await import('sonner')
    const added = [...document.head.querySelectorAll('style')].filter((s) => !before.has(s))
    expect(added.length).toBeGreaterThan(0)
    // sonner appends the element empty, then adds its text: both are checked.
    for (const text of ['', ...added.map((s) => s.textContent)]) {
      const hash = createHash('sha256').update(text).digest('base64')
      expect(policySource, `add 'sha256-${hash}' to style-src`).toContain(`'sha256-${hash}'`)
    }
  })

  it('has no unsafe source expressions', () => {
    for (const bad of ["'unsafe-inline'", "'unsafe-eval'", "'unsafe-hashes'"]) {
      expect(policySource).not.toContain(`"${bad}`)
      expect(policySource).not.toMatch(new RegExp(`src[^"\n]*${bad}`))
    }
  })
})
