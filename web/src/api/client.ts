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

export const SETUP_PATH = '/setup'

const redirectToSetup = () => window.location.assign(SETUP_PATH)
let onSetupRequired: () => void = redirectToSetup

/** Lets the app route "setup required" (503) through its router. */
export function setSetupRequiredHandler(handler: (() => void) | null): void {
  onSetupRequired = handler ?? redirectToSetup
}

/** Pages outside the signed-in app: a 401 there is expected, not a redirect. */
function isPublicPage(pathname: string): boolean {
  return (
    pathname === SIGN_IN_PATH ||
    pathname === SETUP_PATH ||
    pathname.startsWith(`${SETUP_PATH}/`) ||
    pathname === '/recover'
  )
}

/**
 * Converts every non-2xx response into a thrown ApiError (so callers only
 * ever see successful data). Sends the user to sign in on 401, and to the
 * setup wizard while the instance is not set up.
 */
export const errorMiddleware: Middleware = {
  async onResponse({ response }) {
    if (response.ok) return undefined
    const error = await apiErrorFromResponse(response)
    const { pathname, search } = window.location
    if (response.status === 401 && !isPublicPage(pathname)) {
      onUnauthorized(pathname + search)
    }
    if (error.code === 'setup_required' && !isPublicPage(pathname)) {
      onSetupRequired()
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
    // Same origin. Spelled out because a Request outside a browser (tests)
    // cannot resolve a relative URL.
    baseUrl: options.baseUrl ?? window.location.origin,
    credentials: 'same-origin',
    headers: { Accept: 'application/json' },
    // Look fetch up on each call rather than once at start-up, so a fetch
    // installed later (tests, instrumentation) is used.
    fetch: options.fetch ?? ((input: Request) => globalThis.fetch(input)),
  })
  client.use(errorMiddleware)
  return client
}

export const api = createApiClient()
