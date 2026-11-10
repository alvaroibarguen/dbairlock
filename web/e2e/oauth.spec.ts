import { createHash, randomBytes } from 'node:crypto'
import { createServer, type Server } from 'node:http'
import type { AddressInfo } from 'node:net'
import { expectAccessible } from './axe.ts'
import { expect, type Page, test } from './csp.ts'

// OAuth sign-in of MCP clients: the consent page (allow and deny), its
// error page, sign-in on the way, and the connected apps list. A small HTTP
// server on a loopback port stands in for the MCP client's callback.

test.describe.configure({ timeout: 90_000 })

interface Callback {
  url: string
  server: Server
  received: () => Promise<URLSearchParams>
}

/** Listens on 127.0.0.1 like a native client waiting for the redirect. */
async function callbackServer(): Promise<Callback> {
  const arrived: URLSearchParams[] = []
  const waiting: ((q: URLSearchParams) => void)[] = []
  const server = createServer((req, res) => {
    const q = new URL(req.url ?? '/', 'http://127.0.0.1').searchParams
    res.writeHead(200, { 'Content-Type': 'text/html' })
    res.end('<!doctype html><title>Signed in</title><p>You can close this tab.</p>')
    const waiter = waiting.shift()
    if (waiter) waiter(q)
    else arrived.push(q)
  })
  await new Promise<void>((r) => server.listen(0, '127.0.0.1', r))
  const { port } = server.address() as AddressInfo
  return {
    url: `http://127.0.0.1:${port}/callback`,
    server,
    received: () => {
      const q = arrived.shift()
      return q ? Promise.resolve(q) : new Promise((r) => waiting.push(r))
    },
  }
}

function pkce() {
  const verifier = randomBytes(32).toString('base64url')
  const challenge = createHash('sha256').update(verifier).digest('base64url')
  return { verifier, challenge }
}

async function register(page: Page, name: string, redirect: string): Promise<string> {
  const r = await page.request.post('/oauth/register', {
    data: { client_name: name, redirect_uris: [redirect], token_endpoint_auth_method: 'none' },
  })
  expect(r.status()).toBe(201)
  return ((await r.json()) as { client_id: string }).client_id
}

function authorizeURL(client: string, redirect: string, challenge: string, state: string) {
  const q = new URLSearchParams({
    response_type: 'code',
    client_id: client,
    redirect_uri: redirect,
    code_challenge: challenge,
    code_challenge_method: 'S256',
    state,
    scope: 'mcp',
    resource: 'http://127.0.0.1:18080/mcp',
  })
  return `/oauth/authorize?${q.toString()}`
}

test('consent page: allow, deny, and the connected app', async ({ page }, info) => {
  const cb = await callbackServer()
  try {
    const name = `E2E agent ${info.project.name} ${randomBytes(3).toString('hex')}`
    const client = await register(page, name, cb.url)
    const { verifier, challenge } = pkce()

    await page.goto(authorizeURL(client, cb.url, challenge, 'state-allow'))
    await expect(page).toHaveURL(/\/oauth\/consent\?request=/)
    await expect(page.getByText(name)).toBeVisible()
    await expect(page.getByText('Unverified name')).toBeVisible()
    await expect(page.getByText(new URL(cb.url).host).first()).toBeVisible()
    await expect(page.getByRole('heading', { name: 'It will not be able to' })).toBeVisible()
    await expectAccessible(page, 'oauth consent')

    await page.getByRole('button', { name: 'Allow' }).click()
    await page.waitForURL(`${cb.url}?**`)
    const allowed = await cb.received()
    expect(allowed.get('state')).toBe('state-allow')
    expect(allowed.get('iss')).toBe('http://127.0.0.1:18080')
    const code = allowed.get('code') ?? ''
    expect(code).toMatch(/^dbal_oac_/)

    // The client exchanges the code and calls /mcp with the token.
    const token = await page.request.post('/oauth/token', {
      form: {
        grant_type: 'authorization_code',
        client_id: client,
        code,
        redirect_uri: cb.url,
        code_verifier: verifier,
      },
    })
    expect(token.status()).toBe(200)
    const { access_token } = (await token.json()) as { access_token: string }
    const mcp = await page.request.post('/mcp', {
      headers: {
        Authorization: `Bearer ${access_token}`,
        Accept: 'application/json, text/event-stream',
      },
      data: {
        jsonrpc: '2.0',
        id: 1,
        method: 'initialize',
        params: {
          protocolVersion: '2025-06-18',
          capabilities: {},
          clientInfo: { name: 'e2e', version: '1' },
        },
      },
    })
    expect(mcp.status()).toBe(200)

    // Deny: the client learns access_denied and gets no code.
    await page.goto(authorizeURL(client, cb.url, pkce().challenge, 'state-deny'))
    await page.getByRole('button', { name: 'Deny' }).click()
    await page.waitForURL(`${cb.url}?**`)
    const denied = await cb.received()
    expect(denied.get('error')).toBe('access_denied')
    expect(denied.get('state')).toBe('state-deny')
    expect(denied.get('code')).toBeNull()

    // The app is listed under Settings → API tokens, and can be disconnected.
    await page.goto('/settings/api-tokens')
    const apps = page.getByRole('table', { name: 'Connected apps' })
    const row = apps.getByRole('row').filter({ hasText: name })
    await expect(row).toBeVisible()
    await expectAccessible(page, 'connected apps')
    await row.getByRole('button', { name: 'Disconnect' }).click()
    const dialog = page.getByRole('dialog')
    await dialog.getByRole('button', { name: 'Disconnect app' }).click()
    await expect(page.getByText(`"${name}" was disconnected.`)).toBeVisible()
    await expect(apps.getByRole('row').filter({ hasText: name })).toHaveCount(0)

    const after = await page.request.post('/mcp', {
      headers: { Authorization: `Bearer ${access_token}` },
      data: {},
    })
    expect(after.status()).toBe(401)
  } finally {
    cb.server.close()
  }
})

test('consent page: sign-in first, and errors', async ({ page, browser }) => {
  const cb = await callbackServer()
  try {
    const client = await register(page, 'E2E signed-out agent', cb.url)
    const ctx = await browser.newContext({ storageState: { cookies: [], origins: [] } })
    const anon = await ctx.newPage()
    await anon.goto(authorizeURL(client, cb.url, pkce().challenge, 's'))
    await expect(anon).toHaveURL(/\/signin\?next=%2Foauth%2Fconsent%3Frequest%3D/)
    await ctx.close()

    // A redirect URI the client did not register is shown here, never followed.
    await page.goto(authorizeURL(client, 'http://127.0.0.1:1/elsewhere', pkce().challenge, 's'))
    await expect(page).toHaveURL(/\/oauth\/consent\?error=invalid_redirect_uri/)
    await expect(page.getByRole('alert')).toContainText('did not register')
    await expectAccessible(page, 'oauth consent error')

    await page.goto('/oauth/consent?request=no-such-request')
    await expect(page.getByRole('alert')).toContainText('expired or was already answered')
  } finally {
    cb.server.close()
  }
})
