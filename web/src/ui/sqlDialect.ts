import {
  MySQL,
  PostgreSQL,
  type SQLConfig,
  type SQLDialect,
  type SQLNamespace,
  StandardSQL,
} from '@codemirror/lang-sql'

/**
 * Editor dialects, keyed by the `dialect` a connector declares in its info
 * (`GET /api/connectors`). This is the one place the dashboard knows about
 * particular databases: adding a connector adds one entry here.
 */
const dialects: Readonly<Record<string, SQLDialect>> = {
  postgresql: PostgreSQL,
  mysql: MySQL,
}

/** The CodeMirror SQL dialect for a connector's dialect; standard SQL when unknown. */
export function sqlDialect(dialect: string): SQLDialect {
  return dialects[dialect] ?? StandardSQL
}

/** The shape of `GET /api/connections/{id}/schema`, as far as completion needs it. */
export interface CompletionSchema {
  namespaces: readonly {
    name: string
    tables: readonly { name: string; columns: readonly { name: string }[] }[]
  }[]
}

/**
 * Configuration for `sql()` from `@codemirror/lang-sql`: the connector's
 * dialect and, when the schema is known, completion of namespaces, tables
 * and columns. Tables in `defaultNamespace` also complete unqualified.
 */
export function sqlConfig(
  dialect: string,
  schema?: CompletionSchema,
  defaultNamespace?: string,
): SQLConfig {
  const config: SQLConfig = { dialect: sqlDialect(dialect), upperCaseKeywords: true }
  if (!schema) return config
  const namespaces: Record<string, SQLNamespace> = {}
  for (const ns of schema.namespaces) {
    const tables: Record<string, SQLNamespace> = {}
    for (const t of ns.tables) tables[t.name] = t.columns.map((c) => c.name)
    namespaces[ns.name] = tables
  }
  config.schema = namespaces
  const only = schema.namespaces.length === 1 ? schema.namespaces[0]?.name : undefined
  const def = defaultNamespace ?? only
  if (def !== undefined && def in namespaces) config.defaultSchema = def
  return config
}
