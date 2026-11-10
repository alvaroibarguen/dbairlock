import type { ReactNode } from 'react'
import { cn } from './cn'

export interface EmptyStateProps {
  /** One sentence: what is true, and what to do next. */
  title: ReactNode
  description?: ReactNode
  /** At most one action. */
  action?: ReactNode
  icon?: ReactNode
  className?: string
}

/** Empty state: one sentence plus one action. No illustrations. */
export function EmptyState({ title, description, action, icon, className }: EmptyStateProps) {
  return (
    <div
      className={cn(
        'flex flex-col items-start gap-3 px-6 py-10 text-left sm:items-center sm:text-center',
        className,
      )}
    >
      {icon && <span className="flex text-mute">{icon}</span>}
      <div className="flex max-w-sm flex-col gap-1">
        <p className="text-base text-text">{title}</p>
        {description && <p className="text-base text-dim">{description}</p>}
      </div>
      {action}
    </div>
  )
}
