import createClient, { type Middleware } from 'openapi-fetch'
import { ApiError, apiErrorFromResponse } from './errors'
import type { paths } from './schema'

export const SIGN_IN_PATH = '/signin'

type UnauthorizedHandler = (returnTo: string) => void

/** Default: a full navigation to the sign-in page, remembering where the user was. */
const redirectToSignIn: UnauthorizedHandler = (returnTo) => {
  const target = `${SIGN_IN_PATH}?next=${encodeURIComponent(returnTo)}`
  window.location.assign(target)
}

let onUnauthorized: UnauthorizedHandler = redirectToSignIn

/** Lets the app route 401s through its router instead of a full page load. */
export function setUnauthorizedHandler(handler: UnauthorizedHandler | null): void {
  onUnauthorized = handler ?? redirectToSignIn
}

/**
 * Converts every non-2xx response into a thrown ApiError (so callers only
 * ever see successful data), and sends the user to sign in on 401.
 */
export const errorMiddleware: Middleware = {
  async onResponse({ response }) {
    if (response.ok) return undefined
    const error = await apiErrorFromResponse(response)
    if (response.status === 401) {
      const here = window.location.pathname + window.location.search
      if (window.location.pathname !== SIGN_IN_PATH) onUnauthorized(here)
    }
    throw error
  },
  onError({ error }) {
    if (error instanceof ApiError) return error
    if (error instanceof DOMException && error.name === 'AbortError') return error
    return new ApiError(0, 'network_error', 'Could not reach the dbairlock server.')
  },
}

/** Creates a typed client for the dbairlock REST API. The app uses the shared `api`. */
export function createApiClient(options: { baseUrl?: string; fetch?: typeof fetch } = {}) {
  const client = createClient<paths>({
    baseUrl: options.baseUrl ?? '',
    credentials: 'same-origin',
    headers: { Accept: 'application/json' },
    ...(options.fetch ? { fetch: options.fetch } : {}),
  })
  client.use(errorMiddleware)
  return client
}

export const api = createApiClient()
