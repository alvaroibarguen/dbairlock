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

/** The dbairlock mark: a green outlined square with "ab" in mono. */
export function Logo({ className }: { className?: string }) {
  return (
    <span
      aria-hidden="true"
      className={cn(
        'grid size-7 shrink-0 place-items-center rounded-[7px] border-[1.5px] border-green font-mono text-xs font-semibold text-green',
        className,
      )}
    >
      ab
    </span>
  )
}
