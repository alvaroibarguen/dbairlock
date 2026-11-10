import { type Browser, expect, type Page, test } from '@playwright/test'
import { MEMBER_STATE } from './state.ts'
import { db, reachable, SAMPLE_MYSQL, SAMPLE_POSTGRES } from './targets.ts'

// The core flow on both connectors, against the sample targets of
// deploy/dev/compose.yml: ada (the admin from setup.spec.ts) writes and
// submits a change, bob reviews it, and the server executes it. Each test
// works on rows it inserts with ids of its own, so projects and reruns do
// not interfere.

function freshId(): number {
  return 100_000 + Math.floor(Math.random() * 800_000)
}

function insertUser(connector: 'postgres' | 'mysql', id: number, name: string) {
  db(
    connector,
    `INSERT INTO users (id, email, name, status) VALUES (${id}, 'e2e${id}@example.com', '${name}', 'active')`,
  )
}

const targets = [
  {
    connector: 'postgres' as const,
    sample: SAMPLE_POSTGRES,
    config: (s: typeof SAMPLE_POSTGRES) => ({
      host: s.host,
      port: s.port,
      database: s.database,
      default_schema: 'public',
      username: s.app.user,
      tls_mode: 'disable',
    }),
  },
  {
    connector: 'mysql' as const,
    sample: SAMPLE_MYSQL,
    config: (s: typeof SAMPLE_MYSQL) => ({
      host: s.host,
      port: s.port,
      database: s.database,
      username: s.app.user,
      tls_mode: 'disabled',
    }),
  },
]

/** Opens a new browser as bob, a member, signed in by setup.spec.ts. */
async function signInBob(browser: Browser): Promise<Page> {
  const ctx = await browser.newContext({ storageState: MEMBER_STATE })
  return ctx.newPage()
}

async function createConnection(
  page: Page,
  t: (typeof targets)[number],
  name: string,
  policy: Record<string, unknown> = {},
) {
  const res = await page.request.post('/api/connections', {
    data: {
      name,
      connector: t.connector,
      config: t.config(t.sample as never),
      password: t.sample.app.password,
      policy,
    },
  })
  expect(res.status(), await res.text()).toBe(201)
  return ((await res.json()) as { id: string }).id
}

/** Submits a change through the API as the page's user and returns its number. */
async function submitByApi(page: Page, connectionId: string, title: string, sql: string) {
  const res = await page.request.post('/api/changes', {
    data: { connection_id: connectionId, title, statements: [sql], submit: true },
  })
  expect(res.status(), await res.text()).toBe(201)
  return ((await res.json()) as { number: number }).number
}

for (const t of targets) {
  test.describe(t.connector, () => {
    test.beforeEach(async () => {
      test.skip(
        !(await reachable(t.sample.host, t.sample.port)),
        `the sample ${t.connector} from deploy/dev/compose.yml is not running`,
      )
    })

    test('author writes and submits, a teammate approves, the server executes', async ({
      page,
      browser,
    }, info) => {
      const id = freshId()
      insertUser(t.connector, id, 'Before')
      const conn = `e2e-${t.connector}-flow-${info.project.name}-${id}`
      await createConnection(page, t, conn)

      // Ada writes the change with schema completion, previews and submits.
      await page.goto('/changes/new')
      await page.getByRole('combobox', { name: 'Connection' }).click()
      await page.getByRole('option', { name: new RegExp(`^${conn} `) }).click()
      await page.getByLabel('Title').fill(`Rename user ${id}`)
      await page.getByLabel('Reason').fill('Support ticket: wrong name.')
      const editor = page.getByRole('textbox', { name: 'SQL of statement 1' })
      await editor.click()
      await page.keyboard.type('UPDATE use')
      await expect(page.getByRole('option', { name: /^users/ }).first()).toBeVisible()
      // CodeMirror ignores Enter for a moment after the list opens.
      await page.waitForTimeout(300)
      await page.keyboard.press('Enter')
      await page.keyboard.type(` SET name = 'After' WHERE id = ${id}`)
      await expect(editor).toContainText(`UPDATE users SET name = 'After' WHERE id = ${id}`)
      await page.keyboard.press('ControlOrMeta+Enter')
      await expect(
        page.getByText('Every statement is valid and the dry run succeeded.'),
      ).toBeVisible()
      await expect(page.getByRole('table', { name: /Statement 1: UPDATE/ })).toContainText('After')
      await page.getByRole('button', { name: 'Submit for review' }).click()
      await expect(page).toHaveURL(/\/changes\/\d+$/)
      const number = page.url().split('/').pop() ?? ''
      await expect(page.getByRole('heading', { level: 1, name: `Rename user ${id}` })).toBeVisible()
      // The author has no approve button at all.
      await expect(page.getByRole('button', { name: /^Approve/ })).toHaveCount(0)

      // Bob approves; the change runs at once.
      const bob = await signInBob(browser)
      await bob.goto(`/changes/${number}`)
      await expect(bob.getByText(/Preview taken .* ago/)).toBeVisible()
      await bob.getByRole('button', { name: /^Approve/ }).click()
      await expect(bob.getByText(/^Executed/).first()).toBeVisible({ timeout: 20_000 })
      expect(db(t.connector, `SELECT name FROM users WHERE id = ${id}`)).toBe('After')
      await bob.context().close()
    })

    test('a stale preview blocks approval until it is updated', async ({ page, browser }, info) => {
      const id = freshId()
      insertUser(t.connector, id, 'Stale')
      const conn = await createConnection(
        page,
        t,
        `e2e-${t.connector}-stale-${info.project.name}-${id}`,
        {
          auto_execute: false,
        },
      )
      const number = await submitByApi(
        page,
        conn,
        `Stale ${id}`,
        `UPDATE users SET name = 'Fresh' WHERE id = ${id}`,
      )

      const bob = await signInBob(browser)
      await bob.clock.install()
      await bob.goto(`/changes/${number}`)
      const approve = bob.getByRole('button', { name: /^Approve/ })
      await expect(approve).toBeEnabled()
      // Advance the clock past the limit: the page's own, and the server's
      // as the page sees it (ages are counted on the server clock, which the
      // detail response reports).
      await bob.route(`**/api/changes/${number}`, async (route) => {
        const res = await route.fetch()
        const body = (await res.json()) as { server_time: string }
        body.server_time = new Date(Date.parse(body.server_time) + 6 * 60_000).toISOString()
        await route.fulfill({ response: res, json: body })
      })
      await bob.clock.fastForward('06:00')
      const banner = bob.getByRole('alert').filter({ hasText: /This preview is \d+ minutes old/ })
      await expect(banner).toContainText('Approving requires a preview from the last 5 minutes')
      await expect(approve).toBeDisabled()
      await bob.unroute(`**/api/changes/${number}`)
      await banner.getByRole('button', { name: 'Update preview' }).click()
      await expect(bob.getByText('Preview updated. The affected rows are unchanged.')).toBeVisible()
      await expect(banner).toHaveCount(0)
      await expect(approve).toBeEnabled()
      await approve.click()
      await expect(bob.getByText('Approved. Waiting for someone to execute it')).toBeVisible()
      await bob.context().close()
    })

    test('a rejection shows its comment to the author', async ({ page, browser }, info) => {
      const id = freshId()
      insertUser(t.connector, id, 'Keep')
      const conn = await createConnection(
        page,
        t,
        `e2e-${t.connector}-reject-${info.project.name}-${id}`,
      )
      const number = await submitByApi(
        page,
        conn,
        `Reject ${id}`,
        `DELETE FROM users WHERE id = ${id}`,
      )

      const bob = await signInBob(browser)
      await bob.goto(`/changes/${number}`)
      await bob.getByRole('button', { name: 'Reject' }).click()
      const dialog = bob.getByRole('dialog', { name: 'Reject this change?' })
      await dialog.getByLabel('Why are you rejecting it?').fill('This user still has open orders.')
      await dialog.getByRole('button', { name: 'Reject change' }).click()
      await bob.context().close()

      await page.goto(`/changes/${number}`)
      await expect(page.getByText('Rejected by Bob Member')).toBeVisible()
      await expect(page.getByText('This user still has open orders.').first()).toBeVisible()
      expect(db(t.connector, `SELECT count(*) FROM users WHERE id = ${id}`)).toBe('1')
    })

    test('drift is explained and nothing is changed', async ({ page, browser }, info) => {
      const id = freshId()
      insertUser(t.connector, id, 'Original')
      const conn = await createConnection(
        page,
        t,
        `e2e-${t.connector}-drift-${info.project.name}-${id}`,
        {
          auto_execute: false,
        },
      )
      const number = await submitByApi(
        page,
        conn,
        `Drift ${id}`,
        `UPDATE users SET name = 'Planned' WHERE id = ${id}`,
      )

      const bob = await signInBob(browser)
      await bob.goto(`/changes/${number}`)
      await bob.getByRole('button', { name: /^Approve/ }).click()
      await expect(bob.getByText('Approved. Waiting for someone to execute it')).toBeVisible()
      await bob.context().close()

      // Someone changes the value the change would overwrite.
      db(t.connector, `UPDATE users SET name = 'Changed elsewhere' WHERE id = ${id}`)
      await page.goto(`/changes/${number}`)
      await page.getByRole('button', { name: 'Execute now' }).click()
      await expect(page.getByText('Stopped: the rows changed since review')).toBeVisible({
        timeout: 20_000,
      })
      await expect(
        page
          .getByText(
            'The rows this change would affect, or the values it would overwrite, are no longer the ones that were reviewed, so nothing was changed.',
          )
          .first(),
      ).toBeVisible()
      await expect(
        page.getByRole('button', { name: 'Create a new change from this one' }),
      ).toBeVisible()
      expect(db(t.connector, `SELECT name FROM users WHERE id = ${id}`)).toBe('Changed elsewhere')
    })
  })
}
