import { spawn, spawnSync } from 'node:child_process'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { startFakeOidc } from './fake-oidc.ts'

export const PORT = 18080

/**
 * Starts the fake OpenID provider and the real binary (`make e2e` builds it
 * first) with a throwaway data directory, and hands the tests the setup code
 * the binary prints to its log.
 */
export default async function globalSetup() {
  startDevTargets()
  const idp = await startFakeOidc()
  const dataDir = mkdtempSync(join(tmpdir(), 'dbairlock-e2e-'))
  // `make test-all` points DBAIRLOCK_BIN at a race-enabled build.
  const server = spawn(
    process.env.DBAIRLOCK_BIN ?? '../bin/dbairlock',
    ['serve', '--data-dir', dataDir, '--listen', `127.0.0.1:${PORT}`],
    { stdio: ['ignore', 'ignore', 'pipe'] },
  )

  let log = ''
  const code = await new Promise<string>((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`no setup code in the log:\n${log}`)), 30_000)
    server.stderr.on('data', (chunk: Buffer) => {
      log += chunk.toString()
      const m = /Code:\s+(\S+)/.exec(log)
      if (m?.[1]) {
        clearTimeout(timer)
        resolve(m[1])
      }
    })
    server.on('exit', (status) => reject(new Error(`dbairlock exited (${status}):\n${log}`)))
  })
  for (let i = 0; i < 100; i++) {
    try {
      if ((await fetch(`http://127.0.0.1:${PORT}/healthz`)).ok) break
    } catch {
      // not listening yet
    }
    await new Promise((r) => setTimeout(r, 100))
  }

  process.env.E2E_SETUP_CODE = code
  process.env.E2E_OIDC_ISSUER = idp.issuer

  return async () => {
    if (server.exitCode === null && server.signalCode === null) {
      const exited = new Promise((r) => server.once('exit', r))
      server.kill('SIGTERM')
      await exited
    }
    await idp.close()
    // A race-enabled binary reports data races on stderr; fail the run.
    if (log.includes('WARNING: DATA RACE')) {
      throw new Error(`the server reported a data race:\n${log}`)
    }
  }
}

/**
 * Starts the sample Postgres and MySQL targets from deploy/dev/compose.yml
 * (left running afterwards, like `docker compose up -d`). Without Docker the
 * connection tests skip themselves (see e2e/targets.ts).
 */
function startDevTargets() {
  const res = spawnSync(
    'docker',
    ['compose', '-f', '../deploy/dev/compose.yml', 'up', '-d', '--wait', '--wait-timeout', '180'],
    { stdio: ['ignore', 'ignore', 'pipe'], encoding: 'utf8', timeout: 240_000 },
  )
  if (res.status !== 0) {
    console.warn(`Sample targets not started; connection tests will skip.\n${res.stderr}`)
  }
}
