import type { components } from './schema'

export type ErrorEnvelope = components['schemas']['Error']
export type FieldError = components['schemas']['FieldError']

/** Error codes produced by the client itself rather than the server. */
export type ClientErrorCode = 'network_error' | 'invalid_response'

/**
 * Every failed API call rejects with an ApiError. `code` is the server's
 * stable machine-readable code (for example `not_found`) or a client code;
 * `message` is safe to show to the user.
 */
export class ApiError extends Error {
  readonly status: number
  readonly code: string
  /** One entry per invalid field on a `validation_failed` error; empty otherwise. */
  readonly fields: readonly FieldError[]

  constructor(status: number, code: string, message: string, fields: readonly FieldError[] = []) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.fields = fields
  }

  get isUnauthorized(): boolean {
    return this.status === 401
  }
}

export function isApiError(value: unknown): value is ApiError {
  return value instanceof ApiError
}

function isErrorEnvelope(value: unknown): value is ErrorEnvelope {
  if (typeof value !== 'object' || value === null || !('error' in value)) return false
  const body = value.error
  return (
    typeof body === 'object' &&
    body !== null &&
    typeof (body as { code?: unknown }).code === 'string' &&
    typeof (body as { message?: unknown }).message === 'string'
  )
}

function isFieldError(value: unknown): value is FieldError {
  return (
    typeof value === 'object' &&
    value !== null &&
    typeof (value as { field?: unknown }).field === 'string' &&
    typeof (value as { message?: unknown }).message === 'string'
  )
}

/** Builds an ApiError from a failed response, reading the error envelope when present. */
export async function apiErrorFromResponse(response: Response): Promise<ApiError> {
  let body: unknown
  try {
    body = await response.clone().json()
  } catch {
    body = undefined
  }
  if (isErrorEnvelope(body)) {
    const fields = Array.isArray(body.error.fields) ? body.error.fields.filter(isFieldError) : []
    return new ApiError(response.status, body.error.code, body.error.message, fields)
  }
  return new ApiError(
    response.status,
    'invalid_response',
    `The server returned ${response.status} ${response.statusText}`.trim() + '.',
  )
}
