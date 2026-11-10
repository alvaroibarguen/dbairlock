import { expect, type Page, test } from '@playwright/test'
import { expectAccessible } from './axe.ts'
import { MEMBER_STATE } from './state.ts'
import { db, reachable, SAMPLE_POSTGRES } from './targets.ts'

// Phase 11: axe on every page, in the dark and the light theme, on the
// desktop (1280px) and the mobile (375px) projects. The setup wizard is
// checked inside setup.spec.ts, the only time it can be shown.

// Each axe pass takes a second or two, and most tests run several.
test.describe.configure({ timeout: 90_000 })

async function settle(page: Page) {
  await expect(page.locator('[aria-busy="true"]')).toHaveCount(0)
}

test('sign-in and recovery pages', async ({ browser }) => {
  const ctx = await browser.newContext({ storageState: { cookies: [], origins: [] } })
  const page = await ctx.newPage()
  await page.goto('/signin')
  await expect(page.getByRole('button', { name: 'Sign in with Google' })).toBeVisible()
  await expectAccessible(page, 'sign-in')
  await page.goto('/signin?error=domain_not_allowed')
  await expectAccessible(page, 'sign-in with an error')
  await page.goto('/recover')
  await expect(page.getByText('This link has no recovery code.')).toBeVisible()
  await expectAccessible(page, 'recover without a code')
  await ctx.close()
})

test('changes queue, empty detail and the new change editor', async ({ page }) => {
  await page.goto('/changes')
  await settle(page)
  await expectAccessible(page, 'changes queue')
  for (const view of ['mine', 'all']) {
    await page.goto(`/changes?view=${view}`)
    await settle(page)
    await expectAccessible(page, `changes queue, ${view}`)
  }
  await page.goto('/changes/new')
  await expect(page.getByLabel('Title')).toBeVisible()
  await expectAccessible(page, 'new change')
  await page.goto('/changes/999999')
  await settle(page)
  await expectAccessible(page, 'a change that does not exist')
})

test('a change with rows, as its author and as a reviewer', async ({ page, browser }, info) => {
  test.skip(
    !(await reachable(SAMPLE_POSTGRES.host, SAMPLE_POSTGRES.port)),
    'the sample Postgres from deploy/dev/compose.yml is not running',
  )
  const id = 100_000 + Math.floor(Math.random() * 800_000)
  db(
    'postgres',
    `INSERT INTO users (id, email, name, status) VALUES (${id}, 'a11y${id}@example.com', 'Axe', 'active')`,
  )
  const conn = await page.request.post('/api/connections', {
    data: {
      name: `e2e-a11y-${info.project.name}-${id}`,
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
  const res = await page.request.post('/api/changes', {
    data: {
      connection_id: connectionId,
      title: `Rename user ${id}`,
      reason: 'Accessibility check.',
      statements: [`UPDATE users SET name = 'Axe after' WHERE id = ${id}`],
      submit: true,
    },
  })
  expect(res.status(), await res.text()).toBe(201)
  const number = ((await res.json()) as { number: number }).number

  // The author's view.
  await page.goto(`/changes/${number}`)
  await expect(page.getByRole('heading', { level: 1, name: `Rename user ${id}` })).toBeVisible()
  await settle(page)
  await expectAccessible(page, 'change detail, author')
  if (info.project.name === 'mobile') {
    await page.getByRole('tab', { name: 'Affected rows' }).click()
    await expectAccessible(page, 'affected rows tab')
  }

  // The reviewer's view, and the reject dialog.
  const ctx = await browser.newContext({ storageState: MEMBER_STATE })
  const bob = await ctx.newPage()
  await bob.goto(`/changes/${number}`)
  await expect(bob.getByRole('button', { name: /^Approve/ })).toBeEnabled()
  await settle(bob)
  await expectAccessible(bob, 'change detail, reviewer')
  await bob.getByRole('button', { name: 'Reject' }).click()
  await expect(bob.getByRole('dialog', { name: 'Reject this change?' })).toBeVisible()
  await expectAccessible(bob, 'reject dialog')
  await bob.keyboard.press('Escape')
  await ctx.close()

  // Edit opens the editor with the statement and a preview.
  await page.goto(`/changes/new`)
  await page.getByRole('combobox', { name: 'Connection' }).click()
  await page
    .getByRole('option', { name: new RegExp(`e2e-a11y-${info.project.name}-${id}`) })
    .click()
  await page.getByLabel('Title').fill('Preview check')
  const editor = page.getByRole('textbox', { name: 'SQL of statement 1' })
  await editor.click()
  await page.keyboard.type(`UPDATE users SET name = 'x' WHERE id = ${id}`)
  await page.keyboard.press('ControlOrMeta+Enter')
  await expect(page.getByText('Every statement is valid and the dry run succeeded.')).toBeVisible()
  await expectAccessible(page, 'new change with a preview')
})

test('audit log and an event', async ({ page }) => {
  await page.goto('/audit')
  await expect(page.getByLabel('Audit events')).toBeVisible()
  await settle(page)
  await expectAccessible(page, 'audit log')
  await page
    .getByLabel('Audit events')
    .getByRole('button', { name: /(event |#)\d+/ })
    .first()
    .click()
  await expect(page.getByRole('dialog')).toBeVisible()
  await expectAccessible(page, 'audit event drawer')
  await page.keyboard.press('Escape')
  await page.goto('/audit?actor=nobody%40example.com')
  await expect(page.getByText('No events match these filters.')).toBeVisible()
  await expectAccessible(page, 'audit log, no matches')
})

test('connections', async ({ page }) => {
  await page.goto('/connections')
  await settle(page)
  await expectAccessible(page, 'connections')
  await page.goto('/connections/new')
  await expect(page.getByRole('button', { name: 'Save connection' })).toBeVisible()
  await expectAccessible(page, 'new connection')
  const list = await page.request.get('/api/connections')
  const items = ((await list.json()) as { connections: { id: string }[] }).connections
  if (items[0]) {
    await page.goto(`/connections/${items[0].id}`)
    await settle(page)
    await expectAccessible(page, 'connection detail')
    await page.getByRole('tab', { name: 'Schema' }).click()
    await settle(page)
    await expectAccessible(page, 'connection schema')
  }
})

test('connect an agent', async ({ page }) => {
  await page.goto('/agents')
  await settle(page)
  await expectAccessible(page, 'agents')
})

const sections = [
  'general',
  'sign-in',
  'users',
  'api-tokens',
  'review-policy',
  'limits',
  'notifications',
  'system',
  'appearance',
]

for (const s of sections) {
  test(`settings: ${s}`, async ({ page }) => {
    await page.goto(`/settings/${s}`)
    await settle(page)
    await expect(page.locator('#section-title')).toBeVisible()
    await expectAccessible(page, `settings ${s}`)
  })
}

test('settings dialogs', async ({ page }) => {
  await page.goto('/settings/api-tokens')
  await page.getByRole('button', { name: 'Create token' }).click()
  await expect(page.getByRole('dialog', { name: 'Create an API token' })).toBeVisible()
  await expectAccessible(page, 'create token dialog')
  await page.keyboard.press('Escape')

  // Taking admin rights away asks first; keeping them changes nothing.
  await page.goto('/settings/users')
  await page.getByRole('combobox', { name: 'Role of ada@example.com' }).click()
  await page.getByRole('option', { name: 'Member' }).click()
  const confirm = page.getByRole('dialog', { name: 'Make yourself a member?' })
  await expect(confirm).toBeVisible()
  await expectAccessible(page, 'demote confirmation')
  await confirm.getByRole('button', { name: 'Keep as admin' }).click()
  await expect(confirm).toBeHidden()
  await expect(page.getByRole('combobox', { name: 'Role of ada@example.com' })).toContainText(
    'Admin',
  )
})

test('settings as a member', async ({ browser }) => {
  const ctx = await browser.newContext({ storageState: MEMBER_STATE })
  const page = await ctx.newPage()
  await page.goto('/settings')
  await settle(page)
  await expectAccessible(page, 'member settings')
  await ctx.close()
})

test('not found and error states', async ({ page }) => {
  await page.goto('/no-such-page')
  await expect(page.getByText('There is no page at this address.')).toBeVisible()
  await expectAccessible(page, 'not found')

  // A failing API shows the error state, not a blank pane.
  await page.route('**/api/changes?**', (route) =>
    route.fulfill({
      status: 500,
      json: { code: 'internal', message: 'The server could not complete the request.' },
    }),
  )
  await page.goto('/changes')
  await expect(page.getByRole('button', { name: /Retry|Try again/ }).first()).toBeVisible()
  await expectAccessible(page, 'queue error')
})

test('user menu and mobile navigation', async ({ page }, info) => {
  await page.goto('/changes')
  await settle(page)
  if (info.project.name === 'mobile') {
    await page.getByRole('button', { name: 'Open navigation' }).click()
    await expect(page.getByRole('dialog', { name: 'Navigation' })).toBeVisible()
    await expectAccessible(page, 'navigation drawer')
    await page.keyboard.press('Escape')
  }
  await page.getByRole('button', { name: 'User menu' }).click()
  await expect(page.getByRole('menu')).toBeVisible()
  await expectAccessible(page, 'user menu')
})
