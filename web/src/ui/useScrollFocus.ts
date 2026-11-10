import { useCallback, useRef } from 'react'

/**
 * Makes a scroll container a Tab stop only while its content overflows, so
 * keyboard users can scroll it with the arrow keys (WCAG 2.1.1) without
 * adding empty Tab stops to containers that fit. Attach the returned ref to
 * the element with the overflow.
 */
export function useScrollFocus() {
  const observer = useRef<ResizeObserver | null>(null)
  return useCallback((el: HTMLElement | null) => {
    observer.current?.disconnect()
    observer.current = null
    if (!el) return
    const update = () => {
      const overflows = el.scrollWidth > el.clientWidth || el.scrollHeight > el.clientHeight
      if (overflows) el.tabIndex = 0
      else el.removeAttribute('tabindex')
    }
    update()
    if (typeof ResizeObserver === 'undefined') return
    observer.current = new ResizeObserver(update)
    observer.current.observe(el)
    // Content can grow without the container resizing.
    for (const child of Array.from(el.children)) observer.current.observe(child)
  }, [])
}
