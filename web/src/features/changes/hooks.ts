import { useQuery } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { useSearchParams } from 'react-router'
import { type ChangeView, connectorsQuery } from '@/api'

/** The selected queue tab, kept in the URL so it survives navigation. */
export function useQueueView(): ChangeView {
  const [params] = useSearchParams()
  const v = params.get('view')
  return v === 'mine' || v === 'all' ? v : 'needs_my_review'
}

export interface ConnectorInfo {
  name: string
  dialect: string
}

/** Display name and editor dialect of a connector, from GET /api/connectors. */
export function useConnectorInfo(): (id: string) => ConnectorInfo {
  const connectors = useQuery(connectorsQuery())
  return (id) => {
    const c = connectors.data?.connectors.find((x) => x.id === id)
    return { name: c?.display_name ?? id, dialect: c?.dialect ?? '' }
  }
}

/**
 * The server's clock, ticking every second. `serverTime` is the time the
 * server put in a response and `receivedAt` the browser's time when it
 * arrived (TanStack Query's dataUpdatedAt), so ages are counted on the
 * server's clock whatever the browser's clock says.
 */
export function useServerNow(serverTime: string | undefined, receivedAt: number): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(t)
  }, [])
  const offset = serverTime ? Date.parse(serverTime) - receivedAt : 0
  return Math.max(now, receivedAt) + offset
}
