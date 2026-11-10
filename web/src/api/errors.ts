import type { components } from './schema'

export type ErrorEnvelope = components['schemas']['Error']
export type FieldError = components['schemas']['FieldError']
export type ErrorDetails = components['schemas']['ErrorDetails']

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
  /** Structured detail some errors carry, for example every statement's problem. */
  readonly details: ErrorDetails | undefined

  constructor(
    status: number,
    code: string,
    message: string,
    fields: readonly FieldError[] = [],
    details?: ErrorDetails,
  ) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.fields = fields
    this.details = details
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

const signInErrors: Record<string, string> = {
  domain_not_allowed:
    'This Google account is not in a domain allowed to sign in. Use your work account, or ask an admin to add your domain.',
  account_removed:
    'Your access to dbairlock has been removed. Ask an admin if you think this is a mistake.',
  email_unverified: 'Google has not verified the email address of this account.',
  not_configured: 'Google sign-in is not configured yet. An admin has to finish setup first.',
  rate_limited: 'Too many sign-in attempts from your network. Wait a minute and try again.',
  provider_error: 'Google did not complete the sign-in. Try again.',
  flow_expired: 'The sign-in took too long or was started in another browser. Try again.',
  provider_unreachable:
    'The dbairlock server could not reach Google. Check its network access and try again.',
  setup_session_expired: 'Your setup session has ended. Enter the setup code again.',
  setup_complete: 'Setup is already complete. Sign in instead.',
}

/**
 * The message for an `error` code the server puts on /signin or /setup
 * after a failed Google sign-in. Unknown codes (failed verification steps)
 * share one message; the code is shown next to it for support.
 */
export function signInErrorMessage(code: string): string {
  return signInErrors[code] ?? 'The sign-in could not be verified, so it was refused. Try again.'
}

/** The envelope's details, kept only if the server sent an object. */
function details(body: ErrorEnvelope): ErrorDetails | undefined {
  const d: unknown = body.error.details
  return typeof d === 'object' && d !== null ? body.error.details : undefined
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
    return new ApiError(response.status, body.error.code, body.error.message, fields, details(body))
  }
  return new ApiError(
    response.status,
    'invalid_response',
    `The server returned ${response.status} ${response.statusText}`.trim() + '.',
  )
}
