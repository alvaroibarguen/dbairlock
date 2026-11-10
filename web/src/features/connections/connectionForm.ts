import type {
  Connection,
  ConnectionCreate,
  ConnectionTestRequest,
  ConnectionUpdate,
  Connector,
  ConnectorField,
} from '@/api'
import type { SecretChange } from '@/ui'

/*
 * The connection form, derived entirely from the connector's declared
 * fields (GET /api/connectors). Nothing here knows about a particular
 * database: a connector's `secret` field is the connection's password,
 * every other field goes into `config`.
 */

export type ConfigValue = string | boolean

export interface PolicyValues {
  required_approvals: string
  max_rows: string
  approval_ttl_hours: string
  store_row_images: boolean
  auto_execute: boolean
  allowed_functions: string[]
}

export interface FormValues {
  connector: string
  name: string
  description: string
  config: Record<string, ConfigValue>
  password: SecretChange
  policy: PolicyValues
}

/** Bounds of the policy fields, as in api/openapi.yaml (ConnectionPolicy). */
export const policyBounds = {
  required_approvals: { min: 1, max: 5 },
  max_rows: { min: 1, max: 10000 },
  approval_ttl_hours: { min: 1, max: 720 },
} as const

export const NAME_PATTERN = /^[a-z0-9]([a-z0-9_-]{0,61}[a-z0-9])?$/
export const NAME_MESSAGE =
  'Use 1 to 63 lower-case letters, digits, hyphens and underscores, starting and ending with a letter or digit.'
export const FUNCTION_PATTERN = /^[a-z_][a-z0-9_]{0,62}$/
export const DESCRIPTION_MAX = 500

/** The fields that go into `config`: everything but the secret. */
export function configFields(c: Connector | undefined): ConnectorField[] {
  return c?.fields.filter((f) => f.type !== 'secret') ?? []
}

/** The connector's password field, if it declares one. */
export function secretField(c: Connector | undefined): ConnectorField | undefined {
  return c?.fields.find((f) => f.type === 'secret')
}

function toConfigValue(field: ConnectorField, value: unknown): ConfigValue {
  if (field.type === 'bool') return value === true
  if (typeof value === 'string') return value
  if (typeof value === 'number') return String(value)
  return ''
}

/** Initial config values: the saved value, else the field's default, else empty. */
export function initialConfig(c: Connector | undefined, saved?: Connection['config']) {
  const out: Record<string, ConfigValue> = {}
  for (const f of configFields(c)) {
    const v = saved && f.key in saved ? saved[f.key] : f.default
    out[f.key] = toConfigValue(f, v)
  }
  return out
}

/** The policy.default_* settings, when the viewer may read them. */
export interface PolicyDefaults {
  required_approvals?: number
  max_rows?: number
  approval_ttl_hours?: number
  store_row_images?: boolean
  auto_execute?: boolean
  allowed_functions: string[]
}

/**
 * A new connection's policy. Numbers left empty are filled by the server
 * from the policy.default_* settings.
 */
export function defaultPolicy(d: PolicyDefaults): PolicyValues {
  const num = (n: number | undefined) => (n === undefined ? '' : String(n))
  return {
    required_approvals: num(d.required_approvals),
    max_rows: num(d.max_rows),
    approval_ttl_hours: num(d.approval_ttl_hours),
    store_row_images: d.store_row_images ?? true,
    auto_execute: d.auto_execute ?? true,
    allowed_functions: [...d.allowed_functions],
  }
}

export function toFormValues(
  connectors: Connector[],
  connection?: Connection,
  defaults: PolicyDefaults = { allowed_functions: [] },
): FormValues {
  if (!connection) {
    const first = connectors[0]
    return {
      connector: first?.id ?? '',
      name: '',
      description: '',
      config: initialConfig(first),
      // Not '' (which opens the input with autofocus and scrolls the page to it).
      password: undefined,
      policy: defaultPolicy(defaults),
    }
  }
  const c = connectors.find((x) => x.id === connection.connector)
  const p = connection.policy
  return {
    connector: connection.connector,
    name: connection.name,
    description: connection.description,
    config: initialConfig(c, connection.config),
    password: undefined,
    policy: {
      required_approvals: String(p.required_approvals),
      max_rows: String(p.max_rows),
      approval_ttl_hours: String(p.approval_ttl_hours),
      store_row_images: p.store_row_images,
      auto_execute: p.auto_execute,
      allowed_functions: [...p.allowed_functions],
    },
  }
}

/** The config to send: trimmed strings, numbers for int fields, empty optional fields left out. */
export function toConfig(c: Connector | undefined, values: Record<string, ConfigValue>) {
  const out: Record<string, string | number | boolean> = {}
  for (const f of configFields(c)) {
    const v = values[f.key]
    if (f.type === 'bool') {
      out[f.key] = v === true
      continue
    }
    const s = typeof v === 'string' ? v.trim() : ''
    if (s === '') continue
    out[f.key] = f.type === 'int' && /^-?\d+$/.test(s) ? Number(s) : s
  }
  return out
}

function intOrUndefined(s: string): number | undefined {
  return /^\d+$/.test(s.trim()) ? Number(s.trim()) : undefined
}

function policyBody(p: PolicyValues) {
  return {
    required_approvals: intOrUndefined(p.required_approvals),
    max_rows: intOrUndefined(p.max_rows),
    approval_ttl_hours: intOrUndefined(p.approval_ttl_hours),
    store_row_images: p.store_row_images,
    auto_execute: p.auto_execute,
    allowed_functions: p.allowed_functions,
  }
}

export function toCreateBody(connectors: Connector[], v: FormValues): ConnectionCreate {
  const c = connectors.find((x) => x.id === v.connector)
  const body: ConnectionCreate = {
    name: v.name.trim(),
    description: v.description,
    connector: v.connector,
    config: toConfig(c, v.config),
    password: typeof v.password === 'string' ? v.password : '',
  }
  const policy = policyBody(v.policy)
  // Unset numbers fall back to the instance defaults on the server.
  body.policy = Object.fromEntries(Object.entries(policy).filter(([, val]) => val !== undefined))
  return body
}

/** Only what changed; `config` is sent whole when any of it changed. */
export function toUpdateBody(
  connectors: Connector[],
  v: FormValues,
  dirty: {
    name?: boolean
    description?: boolean
    config?: unknown
    password?: boolean
    policy?: unknown
  },
): ConnectionUpdate {
  const c = connectors.find((x) => x.id === v.connector)
  const body: ConnectionUpdate = {}
  if (dirty.name) body.name = v.name.trim()
  if (dirty.description) body.description = v.description
  if (dirty.config) body.config = toConfig(c, v.config)
  if (dirty.password && typeof v.password === 'string' && v.password !== '') {
    body.password = v.password
  }
  if (dirty.policy) body.policy = policyBody(v.policy)
  return body
}

/** A test of the settings as they are in the form, saved or not. */
export function toTestBody(
  connectors: Connector[],
  v: FormValues,
  connectionId?: string,
): ConnectionTestRequest {
  const c = connectors.find((x) => x.id === v.connector)
  const body: ConnectionTestRequest = { connector: v.connector, config: toConfig(c, v.config) }
  if (typeof v.password === 'string' && v.password !== '') body.password = v.password
  if (connectionId) body.connection_id = connectionId
  return body
}

/** The form field a server field error belongs to. */
export function formFieldFor(serverField: string): string | undefined {
  if (serverField === 'name' || serverField === 'description' || serverField === 'password') {
    return serverField
  }
  if (serverField.startsWith('policy.')) return serverField
  if (serverField.startsWith('config.')) {
    const key = serverField.slice('config.'.length)
    return key === 'password' ? 'password' : serverField
  }
  return undefined
}

/** "host:port/database" style summary of a config, from the connector's own fields. */
export function configSummary(c: Connector | undefined, config: Connection['config']): string {
  const fields = configFields(c)
  const str = (key: string) => {
    const v = config[key]
    return typeof v === 'string' || typeof v === 'number' ? String(v) : ''
  }
  const host = fields.find((f) => f.key === 'host') ? str('host') : ''
  const port = fields.find((f) => f.key === 'port') ? str('port') : ''
  const db = fields.find((f) => f.key === 'database') ? str('database') : ''
  const hostPort = host && port ? `${host}:${port}` : host
  return [hostPort, db].filter(Boolean).join('/')
}

export function policySummary(p: Connection['policy']): string {
  const approvals = `${p.required_approvals} approval${p.required_approvals === 1 ? '' : 's'}`
  return `${approvals} · ≤${p.max_rows.toLocaleString('en-US')} rows · ${p.approval_ttl_hours} h`
}
