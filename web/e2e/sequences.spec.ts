/// <reference lib="dom" />
// (The DOM types are for the callbacks that run in the page.)
import { expectAccessible } from './axe.ts'
import { type Browser, expect, type Locator, type Page, test } from './csp.ts'
import { MEMBER_STATE } from './state.ts'
import { db, reachable, SAMPLE_MYSQL, SAMPLE_POSTGRES } from './targets.ts'

// Sequences: ordered changes across databases. Ada builds a Postgres then
// MySQL sequence in the editor, bob approves step 1, step 2 is previewed
// and comes to review, bob approves it, and both run. A rejected step stops
// a sequence, and the page says which steps were applied. Each test works on
// rows it inserts with ids of its own.

function freshId(): number {
  return 100_000 + Math.floor(Math.random() * 800_000)
}

function insertUser(connector: 'postgres' | 'mysql', id: number, name: string) {
  db(
    connector,
    `INSERT INTO users (id, email, name, status) VALUES (${id}, 'seq${id}@example.com', '${name}', 'active')`,
  )
}

async function createConnections(page: Page, suffix: string) {
  const make = async (name: string, data: Record<string, unknown>) => {
    const res = await page.request.post('/api/connections', { data: { name, ...data } })
    expect(res.status(), await res.text()).toBe(201)
    return ((await res.json()) as { id: string }).id
  }
  const pgName = `seq-pg-${suffix}`
  const myName = `seq-my-${suffix}`
  const pg = await make(pgName, {
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
  })
  const my = await make(myName, {
    connector: 'mysql',
    config: {
      host: SAMPLE_MYSQL.host,
      port: SAMPLE_MYSQL.port,
      database: SAMPLE_MYSQL.database,
      username: SAMPLE_MYSQL.app.user,
      tls_mode: 'disabled',
    },
    password: SAMPLE_MYSQL.app.password,
  })
  return { pg, my, pgName, myName }
}

async function signInBob(browser: Browser): Promise<Page> {
  const ctx = await browser.newContext({ storageState: MEMBER_STATE })
  return ctx.newPage()
}

/** Settles transitions and live updates before an axe run. */
async function settle(page: Page) {
  await page.waitForTimeout(150)
}

/** The expanded content of step n of the open sequence. */
function stepPanel(page: Page, n: number): Locator {
  return page.getByRole('region', { name: new RegExp(`^Step ${n} `) })
}

/** The header button of step n, which expands and collapses it. */
function stepButton(page: Page, n: number): Locator {
  return page.getByRole('button', { name: new RegExp(`^Step ${n} `) })
}

/** Presses Tab until the target has focus; fails if it is never reached. */
async function tabTo(page: Page, target: Locator, max = 80) {
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

/** Opens a focused select with Enter and picks an option with the arrow keys. */
async function chooseByKeyboard(page: Page, select: Locator, option: RegExp) {
  await expect(select).toBeFocused()
  await page.keyboard.press('Enter')
  const item = page.getByRole('option', { name: option })
  await expect(item).toBeVisible()
  for (let i = 0; i < 80 && !(await item.evaluate((el) => el === document.activeElement)); i++) {
    await page.keyboard.press('ArrowDown')
  }
  await expect(item).toBeFocused()
  await page.keyboard.press('Enter')
}

test.beforeEach(async () => {
  test.skip(
    !(await reachable(SAMPLE_POSTGRES.host, SAMPLE_POSTGRES.port)) ||
      !(await reachable(SAMPLE_MYSQL.host, SAMPLE_MYSQL.port)),
    'the sample databases from deploy/dev/compose.yml are not running',
  )
})

test('a two-step sequence across Postgres and MySQL is reviewed and runs in order', async ({
  page,
  browser,
}, info) => {
  const id = freshId()
  insertUser('postgres', id, 'Before')
  insertUser('mysql', id, 'Before')
  const { pgName, myName } = await createConnections(page, `${info.project.name}-${id}`)

  // Ada starts a change, then turns it into a sequence with Add step.
  await page.goto('/changes/new')
  await page.getByRole('combobox', { name: 'Connection' }).click()
  await page.getByRole('option', { name: new RegExp(`^${pgName} `) }).click()
  await page.getByLabel('Title', { exact: true }).fill(`Move user ${id}`)
  await page.getByLabel('Reason').fill('Ticket 31: the user moves to billing.')
  await page.getByRole('textbox', { name: 'SQL of statement 1' }).click()
  await page.keyboard.type(`UPDATE users SET name = 'Moved' WHERE id = ${id}`)
  await page.getByRole('button', { name: 'Add step' }).click()

  await expect(page.getByText(/^Steps run in order\. Each step is reviewed/)).toBeVisible()
  const steps = page.getByRole('list', { name: 'Steps' })
  await expect(steps.getByRole('listitem')).toHaveCount(2)
  // Step 1 kept the connection and the SQL.
  await expect(page.getByRole('textbox', { name: 'SQL of step 1, statement 1' })).toContainText(
    `WHERE id = ${id}`,
  )
  await page.getByRole('combobox', { name: 'Connection of step 2' }).click()
  await page.getByRole('option', { name: new RegExp(`^${myName} `) }).click()
  await page.getByLabel('Title of step 2 (optional)').fill('Mark the customer in billing')
  await page.getByRole('textbox', { name: 'SQL of step 2, statement 1' }).click()
  await page.keyboard.type(`UPDATE users SET status = 'billing' WHERE id = ${id}`)

  // Only step 1 can be dry-run now.
  await page.getByRole('button', { name: 'Preview step 1' }).click()
  await expect(page.getByText('Step 1 is valid and its dry run succeeded.')).toBeVisible()
  await settle(page)
  await expectAccessible(page, 'sequence editor')

  await page.getByRole('button', { name: 'Submit sequence' }).click()
  await expect(page).toHaveURL(/\/sequences\/\d+/)
  const number = /\/sequences\/(\d+)/.exec(page.url())?.[1] ?? ''
  await expect(page.getByRole('heading', { level: 1, name: `Move user ${id}` })).toBeVisible()
  await expect(stepButton(page, 1)).toContainText('In review')
  await expect(stepButton(page, 2)).toContainText('Waiting for step 1')
  // The author cannot approve.
  await expect(page.getByRole('button', { name: /^Approve/ })).toHaveCount(0)

  // Bob sees it once in his queue, as step 1/2.
  const bob = await signInBob(browser)
  await bob.goto('/changes')
  const row = bob.getByRole('link', { name: new RegExp(`^Move user ${id}`) })
  await expect(row).toHaveCount(1)
  await expect(row).toContainText('step 1/2')
  await bob.goto(`/sequences/${number}`)
  await expect(stepPanel(bob, 1)).toBeVisible()
  // Step 2 waits: it shows its SQL but cannot be reviewed.
  await stepButton(bob, 2).click()
  await expect(
    stepPanel(bob, 2).getByText(/Waiting for step 1\. Not previewed yet\./),
  ).toBeVisible()
  await expect(stepPanel(bob, 2).getByRole('button', { name: /^Approve/ })).toHaveCount(0)
  await settle(bob)
  await expectAccessible(bob, 'sequence, reviewer, step 1 in review')

  await stepPanel(bob, 1)
    .getByRole('button', { name: /^Approve/ })
    .click()
  // Step 1 runs; step 2 is previewed and comes to review.
  await expect(stepButton(bob, 1)).toContainText('Executed', { timeout: 20_000 })
  await expect(stepButton(bob, 2)).toContainText('In review', { timeout: 20_000 })
  expect(db('postgres', `SELECT name FROM users WHERE id = ${id}`)).toBe('Moved')
  expect(db('mysql', `SELECT status FROM users WHERE id = ${id}`)).toBe('active')
  const approve2 = stepPanel(bob, 2).getByRole('button', { name: /^Approve/ })
  await expect(approve2).toBeEnabled({ timeout: 20_000 })
  await approve2.click()
  await expect(bob.getByText('Completed: all 2 steps executed.')).toBeVisible({ timeout: 20_000 })
  expect(db('mysql', `SELECT status FROM users WHERE id = ${id}`)).toBe('billing')
  await bob.context().close()

  await page.reload()
  await expect(page.getByText('Completed: all 2 steps executed.')).toBeVisible()
})

test('a rejected step stops the sequence and the page names the applied steps', async ({
  page,
  browser,
}, info) => {
  const id = freshId()
  insertUser('postgres', id, 'Keep')
  insertUser('mysql', id, 'Keep')
  const { pg, my } = await createConnections(page, `stop-${info.project.name}-${id}`)
  const res = await page.request.post('/api/sequences', {
    data: {
      title: `Stop ${id}`,
      reason: 'Three steps, the second will be rejected.',
      steps: [
        { connection_id: pg, statements: [`UPDATE users SET name = 'First' WHERE id = ${id}`] },
        { connection_id: my, statements: [`UPDATE users SET name = 'Second' WHERE id = ${id}`] },
        { connection_id: pg, statements: [`UPDATE users SET status = 'third' WHERE id = ${id}`] },
      ],
    },
  })
  expect(res.status(), await res.text()).toBe(201)
  const seq = (await res.json()) as { number: number; steps: { number: number }[] }

  const bob = await signInBob(browser)
  await bob.goto(`/sequences/${seq.number}`)
  await stepPanel(bob, 1)
    .getByRole('button', { name: /^Approve/ })
    .click()
  await expect(stepButton(bob, 2)).toContainText('In review', { timeout: 20_000 })
  await stepButton(bob, 2).click()
  await stepPanel(bob, 2).getByRole('button', { name: 'Reject' }).click()
  const dialog = bob.getByRole('dialog', { name: 'Reject this change?' })
  await expect(dialog).toContainText('the sequence stops')
  await dialog.getByLabel('Why are you rejecting it?').fill('Billing already has it.')
  await dialog.getByRole('button', { name: 'Reject change' }).click()
  await bob.context().close()

  await page.goto(`/sequences/${seq.number}`)
  const banner = page.getByRole('status').filter({ hasText: 'Stopped: step 2 was rejected.' })
  await expect(banner).toBeVisible()
  await expect(banner).toContainText(
    `Step 1 (#${seq.steps[0]?.number} on seq-pg-stop-${info.project.name}-${id}) was applied and is not rolled back. Step 3 did not run.`,
  )
  await expect(stepButton(page, 3)).toContainText('Not run: the sequence stopped')
  await expect(page.getByRole('button', { name: 'Cancel sequence' })).toHaveCount(0)
  expect(db('postgres', `SELECT name || '/' || status FROM users WHERE id = ${id}`)).toBe(
    'First/active',
  )
  expect(db('mysql', `SELECT name FROM users WHERE id = ${id}`)).toBe('Keep')
  await settle(page)
  await expectAccessible(page, 'stopped sequence')

  // A step page links back to its sequence.
  await page.goto(`/changes/${seq.steps[2]?.number}`)
  await expect(page.getByText('Not run: the sequence stopped before this step.')).toBeVisible()
  await page.getByRole('link', { name: `#${seq.number}` }).click()
  await expect(page).toHaveURL(new RegExp(`/sequences/${seq.number}\\?step=3`))
})

test('steps are built, reordered and opened by keyboard alone', async ({ page }, info) => {
  const id = freshId()
  insertUser('postgres', id, 'Keys')
  const { pg, pgName, myName } = await createConnections(page, `kb-${info.project.name}-${id}`)

  await page.goto('/changes/new')
  // From the first control of the form on, by keyboard.
  const first = page.getByRole('combobox', { name: 'Connection' })
  await tabTo(page, first, 120)
  await chooseByKeyboard(page, first, new RegExp(`^${pgName} `))
  await tabTo(page, page.getByLabel('Title', { exact: true }))
  await page.keyboard.type(`Keyboard ${id}`)
  await tabTo(page, page.getByRole('textbox', { name: 'SQL of statement 1' }))
  await page.keyboard.type(`UPDATE users SET name = 'One' WHERE id = ${id}`)
  // Escape leaves the editor's Tab handling; Tab then moves on.
  await page.keyboard.press('Escape')
  await tabTo(page, page.getByRole('button', { name: 'Add step' }))
  await page.keyboard.press('Enter')
  const second = page.getByRole('combobox', { name: 'Connection of step 2' })
  await tabTo(page, second)
  await chooseByKeyboard(page, second, new RegExp(`^${myName} `))

  // Move step 2 up: the MySQL step becomes step 1.
  await tabTo(page, page.getByRole('button', { name: 'Move step 2 up' }), 120)
  await page.keyboard.press('Enter')
  await expect(page.getByRole('combobox', { name: 'Connection of step 1' })).toContainText(myName)
  await expect(page.getByRole('combobox', { name: 'Connection of step 2' })).toContainText(pgName)
  await expect(page.getByRole('textbox', { name: 'SQL of step 2, statement 1' })).toContainText(
    `WHERE id = ${id}`,
  )
  // And back.
  await page.getByRole('button', { name: 'Move step 2 up' }).focus()
  await page.keyboard.press('Enter')
  await expect(page.getByRole('combobox', { name: 'Connection of step 1' })).toContainText(pgName)
  // Removing step 2 turns it back into a single change.
  await page.getByRole('button', { name: 'Remove step 2', exact: true }).focus()
  await page.keyboard.press('Enter')
  await expect(page.getByRole('list', { name: 'Steps' })).toHaveCount(0)
  await expect(page.getByRole('textbox', { name: 'SQL of statement 1' })).toContainText(
    `WHERE id = ${id}`,
  )

  // The stepper opens and closes from the keyboard.
  const res = await page.request.post('/api/sequences', {
    data: {
      title: `Keyboard stepper ${id}`,
      steps: [
        { connection_id: pg, statements: [`UPDATE users SET name = 'K1' WHERE id = ${id}`] },
        { connection_id: pg, statements: [`UPDATE users SET name = 'K2' WHERE id = ${id}`] },
      ],
    },
  })
  expect(res.status(), await res.text()).toBe(201)
  const seq = (await res.json()) as { number: number }
  await page.goto(`/sequences/${seq.number}`)
  const header = stepButton(page, 2)
  await expect(header).toHaveAttribute('aria-expanded', 'false')
  await tabTo(page, header, 120)
  await page.keyboard.press('Enter')
  await expect(header).toHaveAttribute('aria-expanded', 'true')
  await expect(stepPanel(page, 2)).toContainText('Waiting for step 1')
  await page.keyboard.press('Space')
  await expect(header).toHaveAttribute('aria-expanded', 'false')
  // The author cancels the sequence from the keyboard.
  await tabTo(page, page.getByRole('button', { name: 'Cancel sequence' }), 120)
  await page.keyboard.press('Enter')
  const dialog = page.getByRole('dialog', { name: `Cancel sequence #${seq.number}?` })
  await expect(dialog).toContainText('Steps already run stay applied')
  await dialog.getByRole('button', { name: 'Cancel sequence' }).focus()
  await page.keyboard.press('Enter')
  await expect(page.getByText('Stopped: step 1 was cancelled.', { exact: true })).toBeVisible()
  await expect(page.getByText('No step was applied, so no database was changed.')).toBeVisible()
})
