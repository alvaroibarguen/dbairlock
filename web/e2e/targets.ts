import { spawnSync } from 'node:child_process'
import { connect } from 'node:net'

/** The sample targets from deploy/dev/compose.yml. Passwords are for local development only. */
export const SAMPLE_POSTGRES = {
  host: '127.0.0.1',
  port: 55432,
  database: 'app',
  app: { user: 'dbairlock_app', password: 'app-secret' },
  super: { user: 'dbairlock_super', password: 'super-secret' },
}

export const SAMPLE_MYSQL = {
  host: '127.0.0.1',
  port: 53306,
  database: 'app',
  app: { user: 'dbairlock_app', password: 'app-secret' },
}

const compose = ['compose', '-f', '../deploy/dev/compose.yml', 'exec', '-T']

/** Runs SQL on a sample target as its superuser and returns the output. */
export function db(connector: 'postgres' | 'mysql', sql: string): string {
  const args =
    connector === 'postgres'
      ? [...compose, 'postgres', 'psql', '-U', 'postgres', '-d', 'app', '-tAc', sql]
      : [...compose, 'mysql', 'mysql', '-uroot', '-proot-secret', 'app', '-N', '-e', sql]
  const res = spawnSync('docker', args, { encoding: 'utf8', timeout: 60_000 })
  if (res.status !== 0) throw new Error(`${sql}: ${res.stderr}`)
  return res.stdout.trim()
}

/** Whether something accepts TCP connections on host:port. */
export function reachable(host: string, port: number): Promise<boolean> {
  return new Promise((resolve) => {
    const socket = connect({ host, port, timeout: 2000 })
    const done = (ok: boolean) => {
      socket.destroy()
      resolve(ok)
    }
    socket.once('connect', () => done(true))
    socket.once('error', () => done(false))
    socket.once('timeout', () => done(false))
  })
}
