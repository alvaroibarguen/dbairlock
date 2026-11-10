import { queryOptions } from '@tanstack/react-query'
import { api } from './client'
import { ApiError } from './errors'
import type { components } from './schema'

export type Settings = components['schemas']['Settings']
export type SettingValues = components['schemas']['SettingValues']
export type SettingsUpdate = components['schemas']['SettingsUpdate']
export type SecretStatus = components['schemas']['SecretStatus']
export type SettingKey = keyof SettingValues
export type SecretSlot = keyof components['schemas']['SettingsSecretStatus']
export type Me = components['schemas']['Me']
export type User = components['schemas']['User']
export type Role = components['schemas']['Role']
export type Permission = components['schemas']['Permission']
export type ApiToken = components['schemas']['ApiToken']
export type TokenCreated = components['schemas']['TokenCreated']
export type TokenExpiryDays = NonNullable<components['schemas']['TokenCreate']['expires_in_days']>
export type SetupSignIn = components['schemas']['SetupSignIn']
export type SetupSignInUpdate = components['schemas']['SetupSignInUpdate']
export type SetupSessionKind = components['schemas']['SetupSession']['kind']
export type Connector = components['schemas']['Connector']
export type ConnectorField = components['schemas']['ConnectorField']
export type Connection = components['schemas']['Connection']
export type ConnectionCreate = components['schemas']['ConnectionCreate']
export type ConnectionUpdate = components['schemas']['ConnectionUpdate']
export type ConnectionPolicy = components['schemas']['ConnectionPolicy']
export type ConnectionTestRequest = components['schemas']['ConnectionTestRequest']
export type ConnectionTestResult = components['schemas']['ConnectionTestResult']
export type DatabaseSchema = components['schemas']['Schema']

/**
 * Query keys, one factory per resource. Keys are hierarchical so a whole
 * resource can be invalidated with its root key, for example
 * `queryClient.invalidateQueries({ queryKey: queryKeys.changes.all })`.
 */
export const queryKeys = {
  meta: () => ['meta'] as const,
  me: () => ['me'] as const,
  settings: () => ['settings'] as const,
  users: () => ['users'] as const,
  tokens: (all: boolean) => ['tokens', { all }] as const,
  tokensAll: ['tokens'] as const,
  setup: {
    session: () => ['setup', 'session'] as const,
    signIn: () => ['setup', 'signin'] as const,
  },
  changes: {
    all: ['changes'] as const,
    list: (filters: Record<string, unknown> = {}) => ['changes', 'list', filters] as const,
    detail: (id: string) => ['changes', 'detail', id] as const,
  },
  connectors: () => ['connectors'] as const,
  connections: {
    all: ['connections'] as const,
    list: () => ['connections', 'list'] as const,
    detail: (id: string) => ['connections', 'detail', id] as const,
    schema: (id: string) => ['connections', 'schema', id] as const,
  },
  audit: {
    all: ['audit'] as const,
    list: (filters: Record<string, unknown> = {}) => ['audit', 'list', filters] as const,
  },
}

/** Instance metadata (version, setup state). Public; needed before sign-in. */
export const metaQuery = () =>
  queryOptions({
    queryKey: queryKeys.meta(),
    queryFn: async ({ signal }) => {
      // The error middleware throws on every non-2xx response.
      const { data } = await api.GET('/api/meta', { signal })
      return requireData(data)
    },
    staleTime: 5 * 60_000,
  })

/** Instance settings and the status (never the value) of related secrets. Admin only. */
export const settingsQuery = () =>
  queryOptions({
    queryKey: queryKeys.settings(),
    queryFn: async ({ signal }) => {
      const { data } = await api.GET('/api/settings', { signal })
      return requireData(data)
    },
  })

/**
 * Applies a settings update atomically and returns the settings afterwards.
 * Omitted fields are unchanged; for a secret, a string replaces it and
 * `null` clears it. Rejects with an ApiError carrying `fields` on 422.
 */
export async function updateSettings(body: SettingsUpdate): Promise<Settings> {
  const { data } = await api.PATCH('/api/settings', { body })
  return requireData(data)
}

/** The signed-in user and their permissions. A 401 sends the browser to sign in. */
export const meQuery = () =>
  queryOptions({
    queryKey: queryKeys.me(),
    queryFn: async ({ signal }) => {
      const { data } = await api.GET('/api/me', { signal })
      return requireData(data)
    },
    staleTime: 60_000,
  })

/** Whether the signed-in user may do something. The server checks every request anyway. */
export function can(me: Me | undefined, permission: Permission): boolean {
  return me?.permissions.includes(permission) ?? false
}

/** URL that starts a Google sign-in and returns to `next` afterwards. */
export function googleSignInUrl(next?: string): string {
  return next ? `/api/auth/google/start?next=${encodeURIComponent(next)}` : '/api/auth/google/start'
}

/** URL that starts the Google sign-in which finishes setup or confirms a recovery. */
export const SETUP_GOOGLE_URL = '/api/setup/google/start'

export async function signOut(): Promise<void> {
  await api.POST('/api/auth/signout')
}

export async function signOutEverywhere(): Promise<void> {
  await api.POST('/api/auth/signout-everywhere')
}

export async function devLogin(email: string, role: Role): Promise<void> {
  await api.POST('/api/dev/login', { body: { email, role } })
}

/** Every user, removed ones included. Admin only. */
export const usersQuery = () =>
  queryOptions({
    queryKey: queryKeys.users(),
    queryFn: async ({ signal }) => {
      const { data } = await api.GET('/api/users', { signal })
      return requireData(data).users
    },
  })

export async function setUserRole(id: string, role: Role): Promise<User> {
  const { data } = await api.PATCH('/api/users/{id}', { params: { path: { id } }, body: { role } })
  return requireData(data)
}

export async function removeUser(id: string): Promise<User> {
  const { data } = await api.POST('/api/users/{id}/remove', { params: { path: { id } } })
  return requireData(data)
}

export async function restoreUser(id: string): Promise<User> {
  const { data } = await api.POST('/api/users/{id}/restore', { params: { path: { id } } })
  return requireData(data)
}

/** Active API tokens: the caller's, or with `all` every user's (admin only). */
export const tokensQuery = (all: boolean) =>
  queryOptions({
    queryKey: queryKeys.tokens(all),
    queryFn: async ({ signal }) => {
      const { data } = await api.GET('/api/tokens', { signal, params: { query: { all } } })
      return requireData(data).tokens
    },
  })

/** Creates a token. The response carries the token itself, shown once. */
export async function createToken(name: string, expiresInDays: TokenExpiryDays | null) {
  const { data } = await api.POST('/api/tokens', {
    body: expiresInDays === null ? { name } : { name, expires_in_days: expiresInDays },
  })
  return requireData(data)
}

export async function revokeToken(id: string): Promise<void> {
  await api.DELETE('/api/tokens/{id}', { params: { path: { id } } })
}

/** The setup or recovery session this browser holds; rejects with 401 if none. */
export const setupSessionQuery = () =>
  queryOptions({
    queryKey: queryKeys.setup.session(),
    queryFn: async ({ signal }) => {
      const { data } = await api.GET('/api/setup/session', { signal })
      return requireData(data).kind
    },
    retry: false,
    staleTime: 0,
  })

export const setupSignInQuery = () =>
  queryOptions({
    queryKey: queryKeys.setup.signIn(),
    queryFn: async ({ signal }) => {
      const { data } = await api.GET('/api/setup/signin', { signal })
      return requireData(data)
    },
  })

export async function verifySetupCode(code: string): Promise<void> {
  await api.POST('/api/setup/verify', { body: { code } })
}

export async function verifyRecoveryCode(code: string): Promise<void> {
  await api.POST('/api/setup/recover', { body: { code } })
}

export async function saveSetupSignIn(body: SetupSignInUpdate): Promise<SetupSignIn> {
  const { data } = await api.PUT('/api/setup/signin', { body })
  return requireData(data)
}

/** Every connector with the fields of its connection form, and the default function allowlist. */
export const connectorsQuery = () =>
  queryOptions({
    queryKey: queryKeys.connectors(),
    queryFn: async ({ signal }) => {
      const { data } = await api.GET('/api/connectors', { signal })
      return requireData(data)
    },
    staleTime: Infinity,
  })

/** Connections that are not archived, ordered by connector and name. */
export const connectionsQuery = () =>
  queryOptions({
    queryKey: queryKeys.connections.list(),
    queryFn: async ({ signal }) => {
      const { data } = await api.GET('/api/connections', { signal })
      return requireData(data).connections
    },
  })

export const connectionQuery = (id: string) =>
  queryOptions({
    queryKey: queryKeys.connections.detail(id),
    queryFn: async ({ signal }) => {
      const { data } = await api.GET('/api/connections/{id}', {
        signal,
        params: { path: { id } },
      })
      return requireData(data)
    },
  })

/** Tables, columns and keys of a connection, for the schema browser and editor completion. */
export const connectionSchemaQuery = (id: string) =>
  queryOptions({
    queryKey: queryKeys.connections.schema(id),
    queryFn: async ({ signal }) => {
      const { data } = await api.GET('/api/connections/{id}/schema', {
        signal,
        params: { path: { id } },
      })
      return requireData(data)
    },
    staleTime: 60_000,
    retry: false,
  })

export async function createConnection(body: ConnectionCreate): Promise<Connection> {
  const { data } = await api.POST('/api/connections', { body })
  return requireData(data)
}

export async function updateConnection(id: string, body: ConnectionUpdate): Promise<Connection> {
  const { data } = await api.PATCH('/api/connections/{id}', { params: { path: { id } }, body })
  return requireData(data)
}

export async function archiveConnection(id: string): Promise<Connection> {
  const { data } = await api.POST('/api/connections/{id}/archive', { params: { path: { id } } })
  return requireData(data)
}

/** Tests settings before they are saved. A failure to connect is a result with `ok: false`. */
export async function testConnectionSettings(
  body: ConnectionTestRequest,
): Promise<ConnectionTestResult> {
  const { data } = await api.POST('/api/connections/test', { body })
  return requireData(data)
}

/** Tests a saved connection and records the outcome as its last test. */
export async function testConnection(id: string): Promise<ConnectionTestResult> {
  const { data } = await api.POST('/api/connections/{id}/test', { params: { path: { id } } })
  return requireData(data)
}

function requireData<T>(data: T | undefined): T {
  if (data === undefined) {
    throw new ApiError(0, 'invalid_response', 'The server returned an empty response.')
  }
  return data
}
