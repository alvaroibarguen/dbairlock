import { cn } from './cn'

/** A placeholder block while content loads. Size it with className. */
export function Skeleton({ className }: { className?: string }) {
  return (
    <span
      aria-hidden="true"
      className={cn(
        'block h-3.5 animate-pulse rounded-[4px] bg-raise motion-reduce:animate-none',
        className,
      )}
    />
  )
}
