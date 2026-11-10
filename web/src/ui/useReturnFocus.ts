import { useLayoutEffect, useRef } from 'react'

/**
 * For a dialog or drawer opened from state rather than by its own trigger:
 * remembers the element that had focus when it opened and returns a Base UI
 * `finalFocus` callback that puts focus back there on close (or leaves the
 * default when that element is gone). Layout effects run before Base UI
 * moves focus into the popup. With a trigger, Base UI already does this.
 */
export function useReturnFocus(open: boolean | undefined, hasTrigger: boolean) {
  const opener = useRef<HTMLElement | null>(null)
  useLayoutEffect(() => {
    if (!open || hasTrigger) return
    const el = document.activeElement
    opener.current = el instanceof HTMLElement && el !== document.body ? el : null
  }, [open, hasTrigger])
  return hasTrigger ? undefined : () => (opener.current?.isConnected ? opener.current : true)
}
