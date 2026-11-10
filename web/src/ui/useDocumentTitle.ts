import { useEffect } from 'react'

/**
 * Names the browser tab after the page ("Audit log · dbairlock"), so screen
 * reader users hear where a navigation landed and tabs can be told apart
 * (WCAG 2.4.2).
 */
export function useDocumentTitle(title?: string) {
  useEffect(() => {
    document.title = title ? `${title} · dbairlock` : 'dbairlock'
  }, [title])
}
