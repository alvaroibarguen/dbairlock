import { createServer, type Server } from 'node:http'
import type { AddressInfo } from 'node:net'
import { networkInterfaces } from 'node:os'
import { expect, type Page, test } from './csp.ts'
import { MEMBER_STATE } from './state.ts'
import { reachable, SAMPLE_POSTGRES } from './targets.ts'

// Phase 9: the audit log page, chain verification in Settings, and
// notifications posted to a webhook receiver.

test('the audit log filters by URL, pages and opens an event', async ({ page }) => {
  await page.goto('/audit')
  // A table from 768px, a list below.
  const table = page.getByLabel('Audit events')
  await expect(table).toBeVisible()
  // Setup is in the log, and the filter keeps only setup events.
  await page.goto('/audit?action=setup.')
  await expect(table.getByRole('button', { name: /Setup completed, event \d+/ })).toBeVisible()
  for (const button of await table.getByRole('button').all()) {
    expect(await button.getAttribute('aria-label')).toMatch(/^Setup|^Recovery/)
  }
  // The filter survives a reload: the view is a link.
  await page.reload()
  await expect(page).toHaveURL(/action=setup\./)
  await expect(table.getByRole('button', { name: /Setup completed/ })).toBeVisible()

  // The event drawer shows the payload and the hashes.
  await table.getByRole('button', { name: /Setup completed, event \d+/ }).click()
  await expect(page).toHaveURL(/event=\d+/)
  const drawer = page.getByRole('dialog')
  await expect(drawer.getByRole('heading', { name: 'Setup completed' })).toBeVisible()
  await expect(drawer.getByRole('heading', { name: 'Payload' })).toBeVisible()
  await expect(drawer.getByText(/^[0-9a-f]{64}$/).first()).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page).not.toHaveURL(/event=/)

  // A filter that matches nothing says so.
  await page.goto('/audit?actor=nobody%40example.com')
  await expect(page.getByText('No events match these filters.')).toBeVisible()
  await page.getByRole('button', { name: 'Clear filters' }).click()
  await expect(table).toBeVisible()
})

test('a member reads the audit log but cannot export it', async ({ browser }) => {
  const ctx = await browser.newContext({ storageState: MEMBER_STATE })
  const page = await ctx.newPage()
  await page.goto('/audit')
  await expect(page.getByLabel('Audit events')).toBeVisible()
  await expect(page.getByRole('link', { name: 'Export' })).toHaveCount(0)
  expect((await page.request.post('/api/audit/verify')).status()).toBe(403)
  await ctx.close()
})

test('Verify now checks the hash chain from Settings', async ({ page }) => {
  await page.goto('/settings/system')
  await expect(page.getByText('Build commit')).toBeVisible()
  await page.getByRole('button', { name: 'Verify now' }).click()
  await expect(page.getByRole('status')).toContainText(/The chain is intact: [\d,]+ events checked/)
  await expect(page.getByText(/Last verified intact/)).toBeVisible()
  // The check is itself in the audit log.
  await page.goto('/audit?action=audit.verified')
  await expect(
    page.getByRole('button', { name: /Audit log verified, event \d+/ }).first(),
  ).toBeVisible()
})

test('the audit export downloads NDJSON', async ({ page }) => {
  const res = await page.request.get('/api/audit/export')
  expect(res.status()).toBe(200)
  expect(res.headers()['content-type']).toBe('application/x-ndjson')
  const lines = (await res.text()).trim().split('\n')
  expect(lines.length).toBeGreaterThan(3)
  for (const [i, line] of lines.entries()) {
    expect((JSON.parse(line) as { seq: number }).seq).toBe(i + 1)
  }
})

/** An address of this machine that is not loopback, which a non-dev server may post to. */
function lanAddress(): string | undefined {
  for (const list of Object.values(networkInterfaces())) {
    for (const a of list ?? []) {
      if (a.family === 'IPv4' && !a.internal && !a.address.startsWith('169.254.')) return a.address
    }
  }
  return undefined
}

interface Received {
  event?: string
  change?: number
  text?: string
}

async function startReceiver(
  host: string,
): Promise<{ server: Server; url: string; got: Received[] }> {
  const got: Received[] = []
  const server = createServer((req, res) => {
    let body = ''
    req.on('data', (c: Buffer) => (body += c.toString()))
    req.on('end', () => {
      try {
        got.push(JSON.parse(body) as Received)
      } catch {
        got.push({})
      }
      res.writeHead(204).end()
    })
  })
  await new Promise<void>((resolve) => server.listen(0, '0.0.0.0', resolve))
  const { port } = server.address() as AddressInfo
  return { server, url: `http://${host}:${port}/hooks/e2e-secret-token`, got }
}

async function setNotifications(page: Page, enabled: boolean) {
  const res = await page.request.patch('/api/settings', {
    data: { values: { 'notify.enabled': enabled } },
  })
  expect(res.status()).toBe(200)
}

test('notifications reach a webhook receiver as a change runs', async ({ page, browser }, info) => {
  // Settings are instance-wide: one project configures the webhook.
  test.skip(info.project.name !== 'desktop', 'runs once, in the desktop project')
  const host = lanAddress()
  test.skip(!host, 'no non-loopback address to receive on')
  test.skip(
    !(await reachable(SAMPLE_POSTGRES.host, SAMPLE_POSTGRES.port)),
    'the sample postgres from deploy/dev/compose.yml is not running',
  )
  const recv = await startReceiver(host ?? '')
  try {
    // Configure the webhook in Settings.
    await page.goto('/settings/notifications')
    await page.getByRole('switch', { name: 'Send notifications' }).click()
    const secret = page.getByRole('group', { name: 'Webhook URL' })
    await secret.getByRole('button', { name: /^(Set|Replace)$/ }).click()
    await secret.getByPlaceholder(/Value|New value/).fill(recv.url)
    for (const name of ['Change submitted for review', 'Change approved', 'Change executed']) {
      const box = page.getByRole('checkbox', { name })
      if (!(await box.isChecked())) await box.click()
    }
    await page.getByRole('button', { name: 'Save changes' }).click()
    await expect(page.getByText('Notifications settings saved.')).toBeVisible()
    // The URL is write-only: the page shows who set it, never the value.
    await expect(secret).toContainText('Set by')
    await expect(page.getByText(recv.url)).toHaveCount(0)
    const settings = await (await page.request.get('/api/settings')).text()
    expect(settings).not.toContain('e2e-secret-token')

    await page.getByRole('button', { name: 'Send test notification' }).click()
    await expect(page.getByText(/Delivered: the receiver answered 204/)).toBeVisible()
    await expect.poll(() => recv.got.some((b) => b.event === 'test')).toBe(true)

    // Run a change through: ada submits, bob approves, the server executes.
    const conn = await page.request.post('/api/connections', {
      data: {
        name: `e2e-notify-${Date.now()}`,
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
    const id = 100_000 + Math.floor(Math.random() * 800_000)
    const created = await page.request.post('/api/changes', {
      data: {
        connection_id: connectionId,
        title: `Notify ${id}`,
        statements: [
          `INSERT INTO users (id, email, name, status) VALUES (${id}, 'n${id}@example.com', 'N', 'active')`,
        ],
        submit: true,
      },
    })
    expect(created.status(), await created.text()).toBe(201)
    const change = (await created.json()) as { id: string; number: number }

    const ctx = await browser.newContext({ storageState: MEMBER_STATE })
    const bob = await ctx.newPage()
    await bob.goto(`/changes/${change.number}`)
    await bob.getByRole('button', { name: /^Approve/ }).click()
    await expect(bob.getByText(/^Executed/).first()).toBeVisible({ timeout: 20_000 })
    await ctx.close()

    const events = () => recv.got.filter((b) => b.change === change.number).map((b) => b.event)
    await expect
      .poll(events, { timeout: 20_000 })
      .toEqual(['change.submitted', 'change.approved', 'change.executed'])

    // The outcome is listed in Settings.
    await page.reload()
    await expect(
      page.getByRole('table', { name: 'Recent notifications' }).getByText('Delivered').first(),
    ).toBeVisible()
  } finally {
    await setNotifications(page, false)
    recv.server.close()
  }
})
