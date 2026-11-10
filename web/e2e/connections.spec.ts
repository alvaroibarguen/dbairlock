import { expect, type Page, test } from './csp.ts'
import { reachable, SAMPLE_MYSQL, SAMPLE_POSTGRES } from './targets.ts'

// These run signed in as the admin created by setup.spec.ts, against the
// sample targets of deploy/dev/compose.yml (started by global-setup.ts).

async function choose(page: Page, label: string, option: string) {
  await page.getByRole('combobox', { name: label }).click()
  await page.getByRole('option', { name: option, exact: true }).click()
}

async function fillPassword(page: Page, value: string) {
  const group = page.getByRole('group', { name: 'Password' })
  const input = group.getByPlaceholder(/Value|New value/)
  if (!(await input.isVisible())) await group.getByRole('button', { name: /Set|Replace/ }).click()
  await input.fill(value)
}

test('add the sample Postgres, see the excess-privilege warning, test it and see its schema', async ({
  page,
}, info) => {
  test.skip(
    !(await reachable(SAMPLE_POSTGRES.host, SAMPLE_POSTGRES.port)),
    'the sample Postgres from deploy/dev/compose.yml is not running',
  )
  const name = `sample-pg-${info.project.name}`
  await page.goto('/connections')
  await page.getByRole('button', { name: 'Add connection' }).first().click()
  await expect(page).toHaveURL(/\/connections\/new$/)

  // The accepted risk is stated above the form.
  await expect(page.getByRole('note')).toContainText(
    'Everyone who can sign in will be able to see rows from any table this database user can read',
  )

  await choose(page, 'Database type', 'PostgreSQL')
  await page.getByLabel('Name', { exact: true }).fill(name)
  await page.getByLabel('Description').fill('Sample app database: users, orders')
  await page.getByLabel('Host').fill(SAMPLE_POSTGRES.host)
  await page.getByLabel('Port').fill(String(SAMPLE_POSTGRES.port))
  await page.getByLabel('Database', { exact: true }).fill(SAMPLE_POSTGRES.database)
  await expect(page.getByLabel('Default schema')).toHaveValue('public')
  await choose(page, 'TLS', 'disable')

  // The superuser: connects, with a prominent warning and the fix.
  await page.getByLabel('Username').fill(SAMPLE_POSTGRES.super.user)
  await fillPassword(page, SAMPLE_POSTGRES.super.password)
  await page.getByRole('button', { name: 'Test connection' }).click()
  await expect(page.getByText(/Connected to PostgreSQL/)).toBeVisible()
  await expect(
    page.getByText(/has privileges beyond SELECT, INSERT, UPDATE and DELETE/),
  ).toBeVisible()
  await expect(page.getByText(/ALTER ROLE "dbairlock_super" NOSUPERUSER;/)).toBeVisible()

  // The least-privilege user: no warning.
  await page.getByLabel('Username').fill(SAMPLE_POSTGRES.app.user)
  await fillPassword(page, SAMPLE_POSTGRES.app.password)
  await page.getByRole('button', { name: 'Test connection' }).click()
  await expect(page.getByText(/Connected to PostgreSQL/)).toBeVisible()
  await expect(page.getByText(/privileges beyond/)).toHaveCount(0)

  await page.getByRole('button', { name: 'Save connection' }).click()
  await expect(page).toHaveURL(/\/connections\/[0-9a-f-]{36}$/)
  await expect(page.getByText(`${name} added.`)).toBeVisible()

  // The saved connection: test it (recorded), then browse its schema.
  await page.getByRole('button', { name: 'Test connection' }).click()
  await expect(page.getByText('Tests the saved settings and records the result.')).toBeVisible()
  await expect(
    page.getByRole('status').filter({ hasText: 'Connected to PostgreSQL' }),
  ).toBeVisible()
  await page.getByRole('tab', { name: 'Schema' }).click()
  await expect(page.getByRole('region', { name: 'Schema public' })).toBeVisible()
  await page.getByText('users', { exact: true }).click()
  await expect(page.getByRole('table', { name: 'Columns of users' })).toContainText('email')
  await expect(page.getByText('audit_notes', { exact: true })).toBeVisible()

  // It is listed under its connector.
  await page.goto('/connections')
  await expect(page.getByRole('region', { name: 'PostgreSQL' })).toContainText(name)
})

test('add the sample MySQL through the same form and test it', async ({ page }, info) => {
  test.skip(
    !(await reachable(SAMPLE_MYSQL.host, SAMPLE_MYSQL.port)),
    'the sample MySQL from deploy/dev/compose.yml is not running',
  )
  const name = `sample-mysql-${info.project.name}`
  await page.goto('/connections/new')
  await choose(page, 'Database type', 'MySQL')
  await page.getByLabel('Name', { exact: true }).fill(name)
  await page.getByLabel('Host').fill(SAMPLE_MYSQL.host)
  await page.getByLabel('Port').fill(String(SAMPLE_MYSQL.port))
  await page.getByLabel('Database', { exact: true }).fill(SAMPLE_MYSQL.database)
  await page.getByLabel('Username').fill(SAMPLE_MYSQL.app.user)
  await choose(page, 'TLS', 'disabled')
  await fillPassword(page, SAMPLE_MYSQL.app.password)
  await page.getByRole('button', { name: 'Test connection' }).click()
  await expect(page.getByText(/Connected to MySQL/)).toBeVisible()
  await expect(page.getByText(/privileges beyond/)).toHaveCount(0)

  await page.getByRole('button', { name: 'Save connection' }).click()
  await expect(page).toHaveURL(/\/connections\/[0-9a-f-]{36}$/)
  await page.getByRole('button', { name: 'Test connection' }).click()
  await expect(page.getByRole('status').filter({ hasText: 'Connected to MySQL' })).toBeVisible()
  await page.getByRole('tab', { name: 'Schema' }).click()
  await expect(page.getByText('legacy_notes', { exact: true })).toBeVisible()
})

test('names must be well formed and unique across connectors', async ({ page }, info) => {
  const name = `dup-${info.project.name}`
  const res = await page.request.post('/api/connections', {
    data: {
      name,
      connector: 'postgres',
      config: { host: '127.0.0.1', port: 1, database: 'app', username: 'u', tls_mode: 'disable' },
      password: 'p',
    },
  })
  expect(res.status()).toBe(201)

  await page.goto('/connections/new')
  await choose(page, 'Database type', 'MySQL')
  const nameInput = page.getByLabel('Name', { exact: true })
  await nameInput.fill('Not A Slug')
  await page.getByLabel('Host').click()
  await expect(page.getByText(/Use 1 to 63 lower-case letters/)).toBeVisible()

  // The same name on another connector is refused too, on the field.
  await nameInput.fill(name)
  await page.getByLabel('Host').fill('127.0.0.1')
  await page.getByLabel('Database', { exact: true }).fill('app')
  await page.getByLabel('Username').fill('u')
  await fillPassword(page, 'p')
  await page.getByRole('button', { name: 'Save connection' }).click()
  await expect(page.getByText(/Another connection already has this name/)).toBeVisible()
  await expect(page).toHaveURL(/\/connections\/new$/)
})
