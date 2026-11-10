import { queryOptions } from '@tanstack/react-query'
import { api } from './client'
import { ApiError } from './errors'

/**
 * Query keys, one factory per resource. Keys are hierarchical so a whole
 * resource can be invalidated with its root key, for example
 * `queryClient.invalidateQueries({ queryKey: queryKeys.changes.all })`.
 */
export const queryKeys = {
  meta: () => ['meta'] as const,
  changes: {
    all: ['changes'] as const,
    list: (filters: Record<string, unknown> = {}) => ['changes', 'list', filters] as const,
    detail: (id: string) => ['changes', 'detail', id] as const,
  },
  connections: {
    all: ['connections'] as const,
    detail: (id: string) => ['connections', 'detail', id] as const,
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

function requireData<T>(data: T | undefined): T {
  if (data === undefined) {
    throw new ApiError(0, 'invalid_response', 'The server returned an empty response.')
  }
  return data
}
