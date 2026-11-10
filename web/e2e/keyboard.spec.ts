/// <reference lib="dom" />
// (The DOM types are for the callbacks that run in the page.)
import { expect, type Locator, type Page, test } from '@playwright/test'
import { MEMBER_STATE } from './state.ts'
import { db, reachable, SAMPLE_POSTGRES } from './targets.ts'

// Phase 11: the main flow by keyboard alone, at 1280px (desktop project)
// and 375px (mobile project). No clicks: every step is Tab, Enter, arrows
// and typing.

/** Presses Tab until the target has focus; fails if it is never reached. */
async function tabTo(page: Page, target: Locator, max = 60) {
  for (let i = 0; i < max; i++) {
    if (
      await target.evaluate(
        (el) => el === document.activeElement || el.contains(document.activeElement),
      )
    ) {
      return
    }
    await page.keyboard.press('Tab')
  }
  await expect(target, `not reachable with Tab in ${max} presses`).toBeFocused()
}

test('write, submit and approve a change by keyboard alone', async ({ page, browser }, info) => {
  test.skip(
    !(await reachable(SAMPLE_POSTGRES.host, SAMPLE_POSTGRES.port)),
    'the sample Postgres from deploy/dev/compose.yml is not running',
  )
  const id = 100_000 + Math.floor(Math.random() * 800_000)
  db(
    'postgres',
    `INSERT INTO users (id, email, name, status) VALUES (${id}, 'kb${id}@example.com', 'Keys', 'active')`,
  )
  const name = `kb-${info.project.name}-${id}`
  const conn = await page.request.post('/api/connections', {
    data: {
      name,
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
    },
  })
  expect(conn.status(), await conn.text()).toBe(201)

  // Ada reaches the editor from the navigation, by keyboard.
  await page.goto('/changes')
  await page.keyboard.press('Tab')
  await expect(page.getByRole('link', { name: 'Skip to content' })).toBeFocused()
  if (info.project.name === 'mobile') {
    await tabTo(page, page.getByRole('button', { name: 'Open navigation' }))
    await page.keyboard.press('Enter')
    const nav = page.getByRole('dialog', { name: 'Navigation' })
    await expect(nav).toBeVisible()
    await tabTo(page, nav.getByRole('link', { name: 'New change' }))
    await page.keyboard.press('Enter')
    await expect(nav).toBeHidden()
  } else {
    await tabTo(
      page,
      page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'New change' }),
    )
    await page.keyboard.press('Enter')
  }
  await expect(page).toHaveURL(/\/changes\/new$/)

  // The form, field by field.
  const connection = page.getByRole('combobox', { name: 'Connection' })
  await tabTo(page, connection)
  await page.keyboard.press('Enter')
  const option = page.getByRole('option', { name: new RegExp(`^${name} `) })
  await expect(option).toBeVisible()
  for (let i = 0; i < 50 && !(await option.evaluate((el) => el === document.activeElement)); i++) {
    await page.keyboard.press('ArrowDown')
  }
  await expect(option).toBeFocused()
  await page.keyboard.press('Enter')
  await expect(connection).toContainText(name)
  await tabTo(page, page.getByLabel('Title'))
  await page.keyboard.type(`Keyboard ${id}`)
  await tabTo(page, page.getByLabel('Reason'))
  await page.keyboard.type('Checked by keyboard only.')
  await tabTo(page, page.getByRole('textbox', { name: 'SQL of statement 1' }))
  await page.keyboard.type(`UPDATE users SET name = 'Typed' WHERE id = ${id}`)
  // Escape closes completion; Mod+Enter runs the preview.
  await page.keyboard.press('Escape')
  await page.keyboard.press('ControlOrMeta+Enter')
  await expect(page.getByText('Every statement is valid and the dry run succeeded.')).toBeVisible()
  await tabTo(page, page.getByRole('button', { name: 'Submit for review' }))
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(/\/changes\/\d+$/)
  const number = page.url().split('/').pop() ?? ''

  // Bob finds it in his queue and approves it with Mod+Enter.
  const ctx = await browser.newContext({ storageState: MEMBER_STATE })
  const bob = await ctx.newPage()
  await bob.goto('/changes')
  await bob.keyboard.press('Tab')
  await bob.keyboard.press('Enter')
  await expect(bob.locator('#main')).toBeFocused()
  const row = bob
    .getByRole('list', { name: 'Changes' })
    .getByRole('link', { name: new RegExp(`Keyboard ${id}`) })
  await tabTo(bob, row)
  await bob.keyboard.press('Enter')
  await expect(bob).toHaveURL(new RegExp(`/changes/${number}$`))
  await expect(bob.getByRole('heading', { level: 1, name: `Keyboard ${id}` })).toBeVisible()
  // Focus is never lost to <body> after the screen changes.
  expect(await bob.evaluate(() => document.activeElement !== document.body)).toBe(true)
  await expect(bob.getByRole('button', { name: /^Approve/ })).toBeEnabled()
  await bob.keyboard.press('ControlOrMeta+Enter')
  await expect(bob.getByText(/^Executed/).first()).toBeVisible({ timeout: 20_000 })
  expect(db('postgres', `SELECT name FROM users WHERE id = ${id}`)).toBe('Typed')
  await ctx.close()
})

test('reject by keyboard: the dialog traps focus and Escape returns it', async ({
  page,
  browser,
}, info) => {
  test.skip(
    !(await reachable(SAMPLE_POSTGRES.host, SAMPLE_POSTGRES.port)),
    'the sample Postgres from deploy/dev/compose.yml is not running',
  )
  const id = 100_000 + Math.floor(Math.random() * 800_000)
  db(
    'postgres',
    `INSERT INTO users (id, email, name, status) VALUES (${id}, 'kbr${id}@example.com', 'Keep', 'active')`,
  )
  const conn = await page.request.post('/api/connections', {
    data: {
      name: `kb-reject-${info.project.name}-${id}`,
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
    },
  })
  expect(conn.status(), await conn.text()).toBe(201)
  const connectionId = ((await conn.json()) as { id: string }).id
  const res = await page.request.post('/api/changes', {
    data: {
      connection_id: connectionId,
      title: `Reject by keyboard ${id}`,
      statements: [`DELETE FROM users WHERE id = ${id}`],
      submit: true,
    },
  })
  expect(res.status(), await res.text()).toBe(201)
  const number = ((await res.json()) as { number: number }).number

  const ctx = await browser.newContext({ storageState: MEMBER_STATE })
  const bob = await ctx.newPage()
  await bob.goto(`/changes/${number}`)
  const reject = bob.getByRole('button', { name: 'Reject', exact: true })
  await tabTo(bob, reject)
  await bob.keyboard.press('Enter')
  const dialog = bob.getByRole('dialog', { name: 'Reject this change?' })
  await expect(dialog).toBeVisible()
  // Focus starts inside the dialog and Tab stays there.
  await expect(dialog.getByLabel('Why are you rejecting it?')).toBeFocused()
  for (let i = 0; i < 8; i++) {
    await bob.keyboard.press('Tab')
    expect(await dialog.evaluate((el) => el.contains(document.activeElement))).toBe(true)
  }
  await bob.keyboard.press('Escape')
  await expect(dialog).toBeHidden()
  await expect(reject).toBeFocused()

  await bob.keyboard.press('Enter')
  await expect(dialog).toBeVisible()
  await bob.keyboard.type('Not needed any more.')
  await tabTo(bob, dialog.getByRole('button', { name: 'Reject change' }))
  await bob.keyboard.press('Enter')
  await expect(bob.getByText('Rejected by Bob Member')).toBeVisible()
  await ctx.close()
})

test('an audit event opens and closes by keyboard, returning focus', async ({ page }) => {
  await page.goto('/audit')
  const event = page
    .getByLabel('Audit events')
    .getByRole('button', { name: /(event |#)\d+/ })
    .first()
  await tabTo(page, event)
  await page.keyboard.press('Enter')
  const drawer = page.getByRole('dialog')
  await expect(drawer.getByRole('heading', { name: 'Payload' })).toBeVisible()
  expect(await drawer.evaluate((el) => el.contains(document.activeElement))).toBe(true)
  await page.keyboard.press('Escape')
  await expect(drawer).toBeHidden()
  await expect(event).toBeFocused()
})

test('settings sections and the user menu by keyboard', async ({ page }, info) => {
  await page.goto('/settings/general')
  const sections = page.getByRole('navigation', { name: 'Settings sections' })
  await tabTo(page, sections.getByRole('link', { name: 'Appearance' }))
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(/\/settings\/appearance$/)
  await tabTo(page, page.getByRole('combobox', { name: 'Theme' }))

  // The user menu opens with Enter and closes with Escape, returning focus.
  await page.goto('/changes')
  const menuButton = page.getByRole('button', { name: 'User menu' })
  await tabTo(page, menuButton, info.project.name === 'mobile' ? 20 : 60)
  await page.keyboard.press('Enter')
  await expect(page.getByRole('menu')).toBeVisible()
  await expect(page.getByRole('menuitem', { name: 'Sign out', exact: true })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('menu')).toBeHidden()
  await expect(menuButton).toBeFocused()
})
