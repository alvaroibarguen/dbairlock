import type { ReactNode } from 'react'
import { cn } from './cn'

export interface PageHeaderProps {
  /** Pane title, rendered in small caps. */
  title: ReactNode
  /** Right-aligned content: a count, filters or actions. */
  actions?: ReactNode
  /** Heading level for the title; pane headers are h2 unless the pane is the page. */
  as?: 'h1' | 'h2' | 'h3'
  className?: string
}

/** The 44px pane header: small caps label, seam below, optional trailing content. */
export function PageHeader({ title, actions, as: Heading = 'h2', className }: PageHeaderProps) {
  return (
    <div
      className={cn(
        'flex h-11 shrink-0 items-center justify-between gap-3 border-b border-seam px-4',
        className,
      )}
    >
      <Heading className="truncate text-base font-medium small-caps text-dim">{title}</Heading>
      {actions && <div className="flex shrink-0 items-center gap-2 text-dim">{actions}</div>}
    </div>
  )
}
