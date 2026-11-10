import type { ReactNode } from 'react'
import { cn } from './cn'
import { PageHeader } from './PageHeader'

/** A single centred pane on the bare background, for pages outside the app shell. */
export function CenteredPanel({
  title,
  children,
  wide = false,
}: {
  title: string
  children: ReactNode
  /** A wider panel, for forms such as the setup wizard. */
  wide?: boolean
}) {
  return (
    <main className="flex min-h-dvh items-center justify-center bg-bg p-4">
      <section
        className={cn(
          'w-full overflow-hidden rounded-md border border-seam bg-panel',
          wide ? 'max-w-xl' : 'max-w-md',
        )}
      >
        <div className="flex items-center gap-2.5 border-b border-seam px-4 py-3">
          <Logo />
          <span className="font-mono text-base font-medium text-text">dbairlock</span>
        </div>
        <PageHeader as="h1" title={title} />
        {children}
      </section>
    </main>
  )
}

/**
 * The dbairlock mark, the same drawing as the app icon (web/public/favicon.svg):
 * two airlock doors holding one row between them, on a graphite tile. The
 * tile keeps its dark colours in both themes, like an app icon. Hidden from
 * assistive technology; the link that holds it carries the name.
 */
export function Logo({ className }: { className?: string }) {
  return (
    <svg
      aria-hidden="true"
      focusable="false"
      viewBox="0 0 32 32"
      className={cn('size-7 shrink-0', className)}
    >
      <rect width="32" height="32" rx="7" fill="#16181c" />
      <rect x="7" y="7" width="3" height="18" rx="1.5" fill="#7ee0a1" />
      <rect x="22" y="7" width="3" height="18" rx="1.5" fill="#7ee0a1" />
      <rect x="12.5" y="14.5" width="7" height="3" rx="1.5" fill="#e6e8eb" />
    </svg>
  )
}
