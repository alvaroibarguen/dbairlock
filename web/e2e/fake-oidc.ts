import { createHash, generateKeyPairSync, randomBytes, sign } from 'node:crypto'
import { createServer, type IncomingMessage, type ServerResponse } from 'node:http'
import type { AddressInfo } from 'node:net'

/**
 * A fake Google for the end-to-end suite: OpenID discovery, an account
 * chooser page, the authorization code flow with PKCE, RS256 ID tokens and
 * JWKS. dbairlock accepts a non-Google issuer only on a loopback address,
 * which is where this listens.
 */

export const CLIENT_ID = 'e2e-client.apps.googleusercontent.com'
export const CLIENT_SECRET = 'e2e-client-secret'

export interface Account {
  sub: string
  email: string
  name: string
  hd?: string
}

export const accounts: Account[] = [
  { sub: '1001', email: 'ada@example.com', name: 'Ada Lovelace', hd: 'example.com' },
  { sub: '1002', email: 'bob@example.com', name: 'Bob Member', hd: 'example.com' },
  { sub: '1003', email: 'eve@other.test', name: 'Eve Outsider', hd: 'other.test' },
]

interface Grant {
  account: Account
  nonce: string
  challenge: string
  redirect: string
  used: boolean
}

const b64url = (b: Buffer | string) => Buffer.from(b).toString('base64url')

export interface FakeOidc {
  issuer: string
  close: () => Promise<void>
}

export async function startFakeOidc(port = 0): Promise<FakeOidc> {
  const { privateKey, publicKey } = generateKeyPairSync('rsa', { modulusLength: 2048 })
  const jwk = { ...publicKey.export({ format: 'jwk' }), kid: 'e2e', alg: 'RS256', use: 'sig' }
  const grants = new Map<string, Grant>()
  let issuer = ''

  const idToken = (g: Grant) => {
    const now = Math.floor(Date.now() / 1000)
    const header = b64url(JSON.stringify({ alg: 'RS256', typ: 'JWT', kid: 'e2e' }))
    const claims = b64url(
      JSON.stringify({
        iss: issuer,
        aud: CLIENT_ID,
        sub: g.account.sub,
        email: g.account.email,
        email_verified: true,
        name: g.account.name,
        ...(g.account.hd ? { hd: g.account.hd } : {}),
        nonce: g.nonce,
        iat: now,
        exp: now + 3600,
      }),
    )
    const signature = sign('sha256', Buffer.from(`${header}.${claims}`), privateKey)
    return `${header}.${claims}.${b64url(signature)}`
  }

  /** Writes a response; returns true so handlers can `return send(...)`. */
  const send = (
    res: ServerResponse,
    status: number,
    body: unknown,
    type = 'application/json',
  ): true => {
    res.writeHead(status, { 'Content-Type': type })
    res.end(typeof body === 'string' ? body : JSON.stringify(body))
    return true
  }

  const readForm = async (req: IncomingMessage) => {
    const chunks: Buffer[] = []
    for await (const c of req) chunks.push(c as Buffer)
    return new URLSearchParams(Buffer.concat(chunks).toString())
  }

  const server = createServer((req, res) => {
    void (async () => {
      const url = new URL(req.url ?? '/', issuer)
      if (url.pathname === '/.well-known/openid-configuration') {
        return send(res, 200, {
          issuer,
          authorization_endpoint: `${issuer}/authorize`,
          token_endpoint: `${issuer}/token`,
          jwks_uri: `${issuer}/jwks`,
          response_types_supported: ['code'],
          subject_types_supported: ['public'],
          id_token_signing_alg_values_supported: ['RS256'],
        })
      }
      if (url.pathname === '/jwks') return send(res, 200, { keys: [jwk] })
      if (url.pathname === '/authorize') {
        // An account chooser, like Google's: one link per account.
        const links = accounts
          .map((a) => {
            const q = new URLSearchParams(url.searchParams)
            q.set('account', a.sub)
            return `<li><a href="/approve?${q.toString()}">${a.email}</a></li>`
          })
          .join('')
        return send(
          res,
          200,
          `<!doctype html><title>Fake Google</title><h1>Choose an account</h1><ul>${links}</ul>`,
          'text/html',
        )
      }
      if (url.pathname === '/approve') {
        const q = url.searchParams
        const account = accounts.find((a) => a.sub === q.get('account'))
        if (!account || q.get('client_id') !== CLIENT_ID)
          return send(res, 400, 'bad request', 'text/plain')
        const code = randomBytes(16).toString('base64url')
        grants.set(code, {
          account,
          nonce: q.get('nonce') ?? '',
          challenge: q.get('code_challenge') ?? '',
          redirect: q.get('redirect_uri') ?? '',
          used: false,
        })
        const back = new URL(q.get('redirect_uri') ?? '')
        back.searchParams.set('code', code)
        back.searchParams.set('state', q.get('state') ?? '')
        res.writeHead(302, { Location: back.toString() })
        return res.end()
      }
      if (url.pathname === '/token' && req.method === 'POST') {
        const form = await readForm(req)
        let id = form.get('client_id')
        let secret = form.get('client_secret')
        const auth = req.headers.authorization
        if (auth?.startsWith('Basic ')) {
          const [u = '', p = ''] = Buffer.from(auth.slice(6), 'base64').toString().split(':')
          id = decodeURIComponent(u)
          secret = decodeURIComponent(p)
        }
        if (id !== CLIENT_ID || secret !== CLIENT_SECRET) {
          return send(res, 401, { error: 'invalid_client' })
        }
        const g = grants.get(form.get('code') ?? '')
        const verifier = form.get('code_verifier') ?? ''
        const challenge = createHash('sha256').update(verifier).digest('base64url')
        if (!g || g.used || g.challenge !== challenge || g.redirect !== form.get('redirect_uri')) {
          return send(res, 400, { error: 'invalid_grant' })
        }
        g.used = true
        return send(res, 200, {
          access_token: randomBytes(16).toString('base64url'),
          token_type: 'Bearer',
          expires_in: 3600,
          id_token: idToken(g),
        })
      }
      send(res, 404, 'not found', 'text/plain')
    })()
  })

  await new Promise<void>((resolve) => server.listen(port, '127.0.0.1', resolve))
  issuer = `http://127.0.0.1:${(server.address() as AddressInfo).port}`
  return {
    issuer,
    close: () => new Promise<void>((resolve) => server.close(() => resolve())),
  }
}
