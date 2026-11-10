import { QueryClient } from '@tanstack/react-query'
import { isApiError } from '@/api'

/** Retries only failures that may be transient: network errors and 5xx, at most twice. */
function shouldRetry(failureCount: number, error: unknown): boolean {
  if (failureCount >= 2) return false
  if (!isApiError(error)) return false
  return error.status === 0 || error.status >= 500
}

export function createQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: shouldRetry, staleTime: 30_000 },
      mutations: { retry: false },
    },
  })
}
