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
