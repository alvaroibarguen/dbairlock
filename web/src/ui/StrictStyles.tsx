import { CSPProvider } from '@base-ui/react/csp-provider'
import type { ReactNode } from 'react'

/**
 * The server's Content-Security-Policy blocks inline <style> elements. Base UI
 * inserts one to hide the scrollbar of Select and ScrollArea; this turns it
 * off, and index.css carries the same rule for the class Base UI still sets.
 * Mount once, around the app.
 */
export function StrictStyles({ children }: { children: ReactNode }) {
  return <CSPProvider disableStyleElements>{children}</CSPProvider>
}
