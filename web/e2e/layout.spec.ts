/// <reference lib="dom" />
// (The DOM types are for the callbacks that run in the page.)
import { expect, type Page, test } from '@playwright/test'
import { reachable, SAMPLE_POSTGRES } from './targets.ts'

// Phase 11: at 375px no page scrolls sideways and nothing sticks out of the
// viewport. Wide content (SQL, row tables) scrolls inside its own box.
// Runs in the mobile project only. Set A11Y_SHOTS=<dir> to also save a
// screenshot of each page for review.

/**
 * Elements whose right edge passes the viewport, unless an ancestor clips or
 * scrolls them (overflow other than visible) and itself fits.
 */
async function overflowing(page: Page): Promise<string[]> {
  return page.evaluate(() => {
    const width = document.documentElement.clientWidth
    const out: string[] = []
    if (document.documentElement.scrollWidth > width) {
      out.push(`document scrolls sideways: ${document.documentElement.scrollWidth}px > ${width}px`)
    }
    // Only ancestors in the containing-block chain clip: an absolutely
    // positioned element escapes every overflow box up to its offsetParent.
    const absolute = (el: Element) => getComputedStyle(el).position === 'absolute'
    const contained = (el: Element): boolean => {
      let skipTo = absolute(el) ? (el as HTMLElement).offsetParent : null
      for (let p = el.parentElement; p && p !== document.body; p = p.parentElement) {
        if (skipTo) {
          if (p !== skipTo) continue
          skipTo = null
        }
        if (getComputedStyle(p).overflowX !== 'visible') {
          return p.getBoundingClientRect().right <= width + 1
        }
        if (absolute(p)) skipTo = p.offsetParent
      }
      return false
    }
    for (const el of Array.from(document.body.querySelectorAll('*'))) {
      const r = el.getBoundingClientRect()
      if (r.width === 0 || r.height === 0 || r.right <= width + 1) continue
      if (getComputedStyle(el).position === 'fixed') continue
      if (contained(el)) continue
      const id = el.id ? `#${el.id}` : ''
      const cls =
        typeof el.className === 'string' ? `.${el.className.split(' ').slice(0, 3).join('.')}` : ''
      out.push(`${el.tagName.toLowerCase()}${id}${cls} right=${Math.round(r.right)}`)
      if (out.length > 10) break
    }
    return out
  })
}

async function check(page: Page, name: string) {
  await expect(page.locator('[aria-busy="true"]')).toHaveCount(0)
  expect(await overflowing(page), name).toEqual([])
  const dir = process.env.A11Y_SHOTS
  if (dir) {
    await page.screenshot({ path: `${dir}/${name}.png`, fullPage: false })
  }
}

const pages: [string, string, string][] = [
  ['queue', '/changes', 'Review queue'],
  ['queue-all', '/changes?view=all', 'Review queue'],
  ['new-change', '/changes/new', 'New change'],
  ['missing-change', '/changes/999999', 'There is no such change.'],
  ['audit', '/audit', 'Audit log'],
  ['connections', '/connections', 'Connections'],
  ['new-connection', '/connections/new', 'Add a connection'],
  ['agents', '/agents', 'Connect an agent'],
  ['settings-general', '/settings/general', 'General'],
  ['settings-sign-in', '/settings/sign-in', 'Sign-in'],
  ['settings-users', '/settings/users', 'Users'],
  ['settings-tokens', '/settings/api-tokens', 'API tokens'],
  ['settings-review-policy', '/settings/review-policy', 'Policy defaults'],
  ['settings-limits', '/settings/limits', 'Limits'],
  ['settings-notifications', '/settings/notifications', 'Notifications'],
  ['settings-system', '/settings/system', 'System'],
  ['settings-appearance', '/settings/appearance', 'Appearance'],
  ['not-found', '/no-such-page', 'There is no page at this address.'],
]

test.describe('375px', () => {
  test.skip(({ viewport }) => (viewport?.width ?? 0) > 400, 'mobile project only')

  for (const [name, path, text] of pages) {
    test(`${name} fits the screen`, async ({ page }) => {
      await page.goto(path)
      await expect(page.getByText(text, { exact: false }).first()).toBeVisible()
      await check(page, name)
    })
  }

  test('sign-in fits the screen', async ({ browser }) => {
    const ctx = await browser.newContext({
      storageState: { cookies: [], origins: [] },
      viewport: { width: 375, height: 812 },
    })
    const page = await ctx.newPage()
    await page.goto('/signin?error=domain_not_allowed')
    await expect(page.getByRole('button', { name: 'Sign in with Google' })).toBeVisible()
    await check(page, 'signin')
    await ctx.close()
  })

  test('a change with rows, its rows tab and the editor with a preview fit the screen', async ({
    page,
  }) => {
    test.skip(
      !(await reachable(SAMPLE_POSTGRES.host, SAMPLE_POSTGRES.port)),
      'the sample Postgres from deploy/dev/compose.yml is not running',
    )
    const connName = `e2e-layout-${Date.now()}`
    const conn = await page.request.post('/api/connections', {
      data: {
        name: connName,
        connector: 'postgres',
        config: {
          host: SAMPLE_POSTGRES.host,
          port: SAMPLE_POSTGRES.port,
          database: SAMPLE_POSTGRES.database,
          default_schema: 'public',
          username: SAMPLE_POSTGRES.app.user,
          tls_mode: 'disable',
        },
        password: SAMPLE_POSTGRES.app.password,
        policy: { auto_execute: false },
      },
    })
    expect(conn.status(), await conn.text()).toBe(201)
    const connectionId = ((await conn.json()) as { id: string }).id
    // A long statement over the seeded rows, so the SQL and the row table
    // are wider than the screen.
    const sql =
      "UPDATE users SET status = 'active', name = upper(name) WHERE id IN (1, 2, 3) AND status IS NOT NULL AND email LIKE '%@%'"
    const res = await page.request.post('/api/changes', {
      data: {
        connection_id: connectionId,
        title:
          'A rather long title that has to wrap on a phone screen without pushing anything out',
        reason: 'Layout check.',
        statements: [sql],
        submit: true,
      },
    })
    expect(res.status(), await res.text()).toBe(201)
    const number = ((await res.json()) as { number: number }).number

    await page.goto(`/changes/${number}`)
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
    await check(page, 'change-detail')
    await page.getByRole('tab', { name: 'Affected rows' }).click()
    await check(page, 'change-rows')

    await page.goto(`/connections/${connectionId}`)
    await check(page, 'connection-detail')
    await page.getByRole('tab', { name: 'Schema' }).click()
    await check(page, 'connection-schema')

    await page.goto(`/changes/new`)
    await page.getByRole('combobox', { name: 'Connection' }).click()
    await page.getByRole('option', { name: new RegExp(`^${connName} `) }).click()
    await page.getByLabel('Title').fill('Preview at 375px')
    await page.getByRole('textbox', { name: 'SQL of statement 1' }).click()
    await page.keyboard.type(sql)
    await page.keyboard.press('Escape')
    await page.keyboard.press('ControlOrMeta+Enter')
    await expect(page.getByRole('status').filter({ hasText: /dry run|need fixing/ })).toBeVisible()
    await check(page, 'new-change-preview')
  })
})
