import { useCallback, useSyncExternalStore } from 'react'

const noop = () => undefined

/** Breakpoints from DESIGN.md: panes collapse below 1280px, the rail becomes a top bar below 768px. */
export const WIDE = '(min-width: 1280px)'
export const MEDIUM = '(min-width: 768px)'

/** Whether a media query matches, updating live. False where matchMedia is missing. */
export function useMediaQuery(query: string, fallback = false): boolean {
  const subscribe = useCallback(
    (onChange: () => void) => {
      if (typeof window.matchMedia !== 'function') return noop
      const media = window.matchMedia(query)
      media.addEventListener('change', onChange)
      return () => media.removeEventListener('change', onChange)
    },
    [query],
  )
  return useSyncExternalStore(
    subscribe,
    () => (typeof window.matchMedia === 'function' ? window.matchMedia(query).matches : fallback),
    () => fallback,
  )
}
