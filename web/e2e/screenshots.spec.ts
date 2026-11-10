import { expect, test } from '@playwright/test'
import { MEMBER_STATE } from './state.ts'
import { db, reachable, SAMPLE_POSTGRES } from './targets.ts'

// Regenerates the screenshots in docs/images from seeded data. Skipped in a
// normal run; regenerate with `make screenshots`, which runs
//   DBAIRLOCK_SCREENSHOTS=1 pnpm exec playwright test --project=desktop screenshots.spec.ts
// against a freshly built binary and the sample targets of deploy/dev/compose.yml.

const out = '../docs/images'

const people = [
  { id: 4101, name: 'Priya N.', email: 'priya@example.com' },
  { id: 4102, name: 'Tomás O.', email: 'tomas@example.com' },
  { id: 4103, name: 'Mei L.', email: 'mei@example.com' },
]

test('review page', async ({ page, browser }, info) => {
  test.skip(!process.env.DBAIRLOCK_SCREENSHOTS, 'set DBAIRLOCK_SCREENSHOTS=1 (make screenshots)')
  test.skip(info.project.name !== 'desktop', 'desktop only')
  test.skip(
    !(await reachable(SAMPLE_POSTGRES.host, SAMPLE_POSTGRES.port)),
    'the sample Postgres from deploy/dev/compose.yml is not running',
  )

  // Three accounts a fraud rule suspended by mistake.
  const ids = people.map((p) => p.id).join(', ')
  db('postgres', `DELETE FROM users WHERE id IN (${ids})`)
  db(
    'postgres',
    `INSERT INTO users (id, email, name, status, updated_at) VALUES ${people
      .map((p) => `(${p.id}, '${p.email}', '${p.name}', 'suspended', now() - interval '2 hours')`)
      .join(', ')}`,
  )

  // Ada, the admin, adds the connection and submits two changes.
  const conn = await page.request.post('/api/connections', {
    data: {
      name: 'app-production',
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

  const submit = async (title: string, reason: string, sql: string) => {
    const res = await page.request.post('/api/changes', {
      data: { connection_id: connectionId, title, reason, statements: [sql], submit: true },
    })
    expect(res.status(), await res.text()).toBe(201)
    return ((await res.json()) as { number: number }).number
  }
  await submit(
    'Close stale open order for Dan',
    'Customer cancelled by phone; ticket SUP-2291.',
    `UPDATE orders SET status = 'cancelled' WHERE id = 4 AND status = 'open'`,
  )
  const number = await submit(
    'Reactivate accounts suspended by the fraud rule',
    'False positives from the velocity rule on 9 Nov; confirmed by the risk team in INC-412.',
    `UPDATE users SET status = 'active', updated_at = now() WHERE id IN (${ids}) AND status = 'suspended'`,
  )

  // Bob, a member, opens it for review.
  const ctx = await browser.newContext({
    storageState: MEMBER_STATE,
    viewport: { width: 1600, height: 900 },
    colorScheme: 'light',
  })
  const bob = await ctx.newPage()
  await bob.goto(`/changes/${number}`)
  await expect(
    bob.getByRole('heading', { level: 1, name: 'Reactivate accounts suspended by the fraud rule' }),
  ).toBeVisible()
  await expect(bob.getByText(/Preview taken .* ago/)).toBeVisible()
  await expect(bob.getByRole('button', { name: /^Approve/ })).toBeEnabled()
  await bob.screenshot({ path: `${out}/review.png` })
  await ctx.close()
})
