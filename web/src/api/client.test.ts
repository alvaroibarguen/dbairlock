import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApiClient, setUnauthorizedHandler } from './client'
import { ApiError } from './errors'

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })

function clientReturning(response: Response | Error) {
  const fetchMock = vi.fn<typeof fetch>(() =>
    response instanceof Error ? Promise.reject(response) : Promise.resolve(response),
  )
  return createApiClient({ baseUrl: 'http://localhost', fetch: fetchMock })
}

async function rejection(promise: Promise<unknown>): Promise<ApiError> {
  try {
    await promise
  } catch (error) {
    if (error instanceof ApiError) return error
    throw error
  }
  throw new Error('expected the request to fail')
}

describe('api client error middleware', () => {
  afterEach(() => {
    setUnauthorizedHandler(null)
  })

  it('returns data for successful responses', async () => {
    const api = clientReturning(json(200, { version: 'v0.1.0', setupState: 'ready' }))
    const { data } = await api.GET('/api/meta')
    expect(data).toEqual({ version: 'v0.1.0', setupState: 'ready' })
  })

  it('turns the error envelope into a typed ApiError', async () => {
    const api = clientReturning(
      json(404, { error: { code: 'not_found', message: 'Change CHG-0142 does not exist.' } }),
    )
    const error = await rejection(api.GET('/api/meta'))
    expect(error).toMatchObject({
      status: 404,
      code: 'not_found',
      message: 'Change CHG-0142 does not exist.',
    })
  })

  it('reports responses without an envelope as invalid_response', async () => {
    const api = clientReturning(new Response('<html>Bad gateway</html>', { status: 502 }))
    const error = await rejection(api.GET('/api/meta'))
    expect(error).toMatchObject({ status: 502, code: 'invalid_response' })
  })

  it('reports fetch failures as network_error', async () => {
    const api = clientReturning(new TypeError('Failed to fetch'))
    const error = await rejection(api.GET('/api/meta'))
    expect(error).toMatchObject({ status: 0, code: 'network_error' })
  })

  it('sends the user to sign in on 401, remembering where they were', async () => {
    window.history.replaceState(null, '', '/changes/CHG-0142?tab=rows')
    const onUnauthorized = vi.fn()
    setUnauthorizedHandler(onUnauthorized)
    const api = clientReturning(
      json(401, { error: { code: 'unauthenticated', message: 'Sign in to continue.' } }),
    )
    const error = await rejection(api.GET('/api/meta'))
    expect(error.isUnauthorized).toBe(true)
    expect(onUnauthorized).toHaveBeenCalledWith('/changes/CHG-0142?tab=rows')
  })

  it('does not redirect again when already on the sign-in page', async () => {
    window.history.replaceState(null, '', '/signin')
    const onUnauthorized = vi.fn()
    setUnauthorizedHandler(onUnauthorized)
    const api = clientReturning(
      json(401, { error: { code: 'unauthenticated', message: 'Sign in to continue.' } }),
    )
    await rejection(api.GET('/api/meta'))
    expect(onUnauthorized).not.toHaveBeenCalled()
  })
})
