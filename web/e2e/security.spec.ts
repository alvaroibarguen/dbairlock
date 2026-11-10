import { expect, test } from './csp.ts'

// Every test in this suite fails on a Content-Security-Policy violation (see
// csp.ts). These check the headers a real browser receives and that the
// violation check itself works.

test('the dashboard is served with the security headers', async ({ page }) => {
  const res = await page.goto('/changes')
  const h = res?.headers() ?? {}
  const csp = h['content-security-policy'] ?? ''
  expect(csp).toContain("default-src 'self'")
  expect(csp).toContain("script-src 'self';")
  expect(csp).toContain("frame-ancestors 'none'")
  expect(csp).not.toContain('unsafe-inline')
  expect(csp).not.toContain('unsafe-eval')
  expect(h['x-content-type-options']).toBe('nosniff')
  expect(h['x-frame-options']).toBe('DENY')
  expect(h['referrer-policy']).toBe('no-referrer')
  expect(h['permissions-policy']).toContain('camera=()')
  // The e2e instance's public URL is plain http, so no HSTS.
  expect(h['strict-transport-security']).toBeUndefined()
  await expect(page.getByRole('button', { name: /^Needs my review/ })).toBeVisible()
})

test('inline script and style are blocked, and the check notices', async ({
  page,
  cspViolations,
}) => {
  await page.goto('/changes')
  await expect(page.getByRole('button', { name: /^Needs my review/ })).toBeVisible()
  const ran = await page.evaluate(async () => {
    const w = window as unknown as { injected?: boolean }
    const s = document.createElement('script')
    s.textContent = 'window.injected = true'
    document.body.append(s)
    const st = document.createElement('style')
    st.textContent = 'body { color: red }'
    document.head.append(st)
    await new Promise((r) => setTimeout(r, 100))
    return w.injected === true
  })
  expect(ran).toBe(false)
  await expect.poll(() => cspViolations.length).toBeGreaterThanOrEqual(2)
  expect(cspViolations.join('\n')).toMatch(/script-src/)
  expect(cspViolations.join('\n')).toMatch(/style-src/)
  cspViolations.length = 0 // expected here; any other violation still fails
})

test('a toast renders with its styles under the policy', async ({ page }) => {
  await page.goto('/settings')
  // Toasts are styled by sonner's stylesheet, allowed by hash: the toaster
  // region exists and the library's rules apply to it.
  const position = await page.evaluate(() => {
    const el = document.querySelector('[data-sonner-toaster]')
    return el ? getComputedStyle(el).position : null
  })
  // The toaster mounts its list lazily; if it is not there yet, the stylesheet
  // must still be present and applied.
  if (position !== null) expect(position).toBe('fixed')
  const sheet = await page.evaluate(() =>
    [...document.styleSheets].some((s) => {
      try {
        return [...s.cssRules].some((r) => r.cssText.includes('data-sonner-toaster'))
      } catch {
        return false
      }
    }),
  )
  expect(sheet).toBe(true)
})
