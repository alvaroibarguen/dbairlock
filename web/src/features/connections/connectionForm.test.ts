import { describe, expect, it } from 'vitest'
import type { Connection, Connector } from '@/api'
import {
  configSummary,
  formFieldFor,
  policySummary,
  toConfig,
  toCreateBody,
  toFormValues,
  toTestBody,
  toUpdateBody,
} from './connectionForm'

// A made-up connector: the form must work for any declared fields.
const acme: Connector = {
  id: 'acme',
  display_name: 'AcmeDB',
  dialect: 'sql',
  default_port: 7000,
  fields: [
    { key: 'host', label: 'Host', type: 'string', required: true },
    { key: 'port', label: 'Port', type: 'int', required: true, default: 7000 },
    { key: 'database', label: 'Database', type: 'string', required: true },
    { key: 'secret_word', label: 'Secret word', type: 'secret', required: true },
    { key: 'mode', label: 'Mode', type: 'enum', required: true, default: 'b', options: ['a', 'b'] },
    { key: 'compress', label: 'Compress', type: 'bool', required: false },
    { key: 'ca', label: 'CA', type: 'text', required: false },
  ],
}

const saved: Connection = {
  id: 'c1',
  name: 'prod',
  description: 'Prod',
  connector: 'acme',
  config: { host: 'db', port: 7001, database: 'app', mode: 'a', compress: true },
  policy: {
    required_approvals: 2,
    max_rows: 50,
    approval_ttl_hours: 24,
    store_row_images: false,
    auto_execute: true,
    allowed_functions: ['lower'],
  },
  password: { is_set: true, set_by: 'ada@example.com', set_at: '2026-01-01T00:00:00Z' },
  created_by: 'ada@example.com',
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
}

describe('connection form', () => {
  it('starts a new connection from the field defaults and the policy defaults', () => {
    const v = toFormValues([acme], undefined, { allowed_functions: ['now'], max_rows: 100 })
    expect(v.connector).toBe('acme')
    expect(v.config).toEqual({
      host: '',
      port: '7000',
      database: '',
      mode: 'b',
      compress: false,
      ca: '',
    })
    expect(v.password).toBeUndefined()
    expect(v.policy.max_rows).toBe('100')
    expect(v.policy.required_approvals).toBe('')
    expect(v.policy.allowed_functions).toEqual(['now'])
  })

  it('never puts the secret field in config', () => {
    const v = toFormValues([acme])
    expect('secret_word' in v.config).toBe(false)
    v.config.host = ' db.internal '
    v.password = 'p'
    const body = toCreateBody([acme], { ...v, name: ' prod ' })
    expect(body).toMatchObject({
      name: 'prod',
      connector: 'acme',
      password: 'p',
      config: { host: 'db.internal', port: 7000, mode: 'b', compress: false },
    })
    expect(body.config).not.toHaveProperty('secret_word')
    expect(body.config).not.toHaveProperty('ca')
    // Empty policy numbers are left to the server's defaults.
    expect(body.policy).not.toHaveProperty('max_rows')
    expect(body.policy).toHaveProperty('allowed_functions', [])
  })

  it('edits only what changed and never sends an empty password', () => {
    const v = toFormValues([acme], saved)
    expect(v.password).toBeUndefined()
    expect(v.config.port).toBe('7001')
    expect(toUpdateBody([acme], v, { name: true })).toEqual({ name: 'prod' })
    expect(toUpdateBody([acme], { ...v, password: '' }, { password: true })).toEqual({})
    const body = toUpdateBody([acme], { ...v, password: 'new' }, { password: true, config: {} })
    expect(body.password).toBe('new')
    expect(body.config).toEqual({
      host: 'db',
      port: 7001,
      database: 'app',
      mode: 'a',
      compress: true,
    })
  })

  it('tests a saved connection with its saved password', () => {
    const v = toFormValues([acme], saved)
    expect(toTestBody([acme], v, 'c1')).toEqual({
      connector: 'acme',
      config: toConfig(acme, v.config),
      connection_id: 'c1',
    })
  })

  it('maps server field errors onto form fields', () => {
    expect(formFieldFor('name')).toBe('name')
    expect(formFieldFor('config.port')).toBe('config.port')
    expect(formFieldFor('config.password')).toBe('password')
    expect(formFieldFor('policy.max_rows')).toBe('policy.max_rows')
    expect(formFieldFor('config')).toBeUndefined()
  })

  it('summarizes a connection', () => {
    expect(configSummary(acme, saved.config)).toBe('db:7001/app')
    expect(policySummary(saved.policy)).toBe('2 approvals · ≤50 rows · 24 h')
  })
})
