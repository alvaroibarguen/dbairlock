import type { ComponentProps } from 'react'
import { cn } from './cn'

/**
 * Presentational table parts. Data cells are mono with tabular figures;
 * rows are 32px. Pair with TanStack Table in features for sorting and paging.
 */
export function Table({ className, ...props }: ComponentProps<'table'>) {
  return (
    <div className="w-full overflow-x-auto">
      <table
        {...props}
        className={cn('w-full border-collapse font-mono text-sm tabular', className)}
      />
    </div>
  )
}

export function THead({ className, ...props }: ComponentProps<'thead'>) {
  return <thead {...props} className={cn('', className)} />
}

export function TBody({ className, ...props }: ComponentProps<'tbody'>) {
  return <tbody {...props} className={cn('', className)} />
}

export function Tr({
  className,
  selected,
  ...props
}: ComponentProps<'tr'> & { selected?: boolean }) {
  return (
    <tr
      {...props}
      aria-selected={selected || undefined}
      className={cn(
        'border-b border-seam transition-colors duration-120 last:border-b-0',
        selected && 'bg-raise',
        className,
      )}
    />
  )
}

export function Th({ className, ...props }: ComponentProps<'th'>) {
  return (
    <th
      scope="col"
      {...props}
      className={cn(
        'h-8 border-b border-seam px-3 text-left font-sans text-xs font-medium small-caps whitespace-nowrap text-mute',
        className,
      )}
    />
  )
}

export function Td({ className, ...props }: ComponentProps<'td'>) {
  return <td {...props} className={cn('h-8 px-3 whitespace-nowrap text-dim', className)} />
}
