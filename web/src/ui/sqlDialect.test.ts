import { MySQL, PostgreSQL, StandardSQL } from '@codemirror/lang-sql'
import { describe, expect, it } from 'vitest'
import { sqlConfig, sqlDialect } from './sqlDialect'

describe('sqlDialect', () => {
  it('maps each connector dialect to its CodeMirror dialect', () => {
    expect(sqlDialect('postgresql')).toBe(PostgreSQL)
    expect(sqlDialect('mysql')).toBe(MySQL)
    expect(sqlDialect('oracle')).toBe(StandardSQL)
  })

  it('builds completion from a schema', () => {
    const schema = {
      namespaces: [
        {
          name: 'public',
          tables: [{ name: 'users', columns: [{ name: 'id' }, { name: 'email' }] }],
        },
        { name: 'billing', tables: [{ name: 'invoices', columns: [{ name: 'total' }] }] },
      ],
    }
    const config = sqlConfig('postgresql', schema, 'public')
    expect(config.dialect).toBe(PostgreSQL)
    expect(config.schema).toEqual({
      public: { users: ['id', 'email'] },
      billing: { invoices: ['total'] },
    })
    expect(config.defaultSchema).toBe('public')
  })

  it('uses the only namespace as the default (a MySQL database)', () => {
    const config = sqlConfig('mysql', { namespaces: [{ name: 'app', tables: [] }] })
    expect(config.defaultSchema).toBe('app')
    expect(sqlConfig('mysql').schema).toBeUndefined()
  })
})
