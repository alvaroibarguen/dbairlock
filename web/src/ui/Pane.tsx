import type { ComponentProps } from 'react'
import { cn } from './cn'

/** A pane surface. Panes sit in a grid with 1px gaps that show the seam colour. */
export function Pane({ className, ...props }: ComponentProps<'section'>) {
  return <section {...props} className={cn('flex min-w-0 flex-col bg-panel', className)} />
}

/** A grid of panes separated by 1px seams (the gap shows --seam). */
export function PaneGrid({ className, ...props }: ComponentProps<'div'>) {
  return <div {...props} className={cn('grid gap-px bg-seam', className)} />
}
