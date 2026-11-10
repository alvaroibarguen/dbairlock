import { Checkbox as BaseCheckbox } from '@base-ui/react/checkbox'
import { Check, Minus } from 'lucide-react'
import type { ComponentProps, ReactNode } from 'react'
import { cn } from './cn'

export interface CheckboxProps extends Omit<ComponentProps<typeof BaseCheckbox.Root>, 'className'> {
  label?: ReactNode
  invalid?: boolean
  className?: string
}

export function Checkbox({ label, invalid, className, ...props }: CheckboxProps) {
  const box = (
    <BaseCheckbox.Root
      {...props}
      aria-invalid={invalid || undefined}
      className={cn(
        'flex size-4 shrink-0 cursor-pointer items-center justify-center rounded-[4px] border border-seam bg-bg text-on-green',
        'transition-[background-color,border-color] duration-120 ease-out hover:not-data-disabled:border-mute',
        'focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green',
        'data-checked:border-green data-checked:bg-green data-indeterminate:border-green data-indeterminate:bg-green',
        'aria-invalid:border-red data-invalid:border-red',
        'data-disabled:cursor-not-allowed data-disabled:opacity-50',
        !label && className,
      )}
    >
      <BaseCheckbox.Indicator className="flex data-unchecked:hidden">
        {props.indeterminate ? (
          <Minus aria-hidden="true" className="size-3" strokeWidth={3} />
        ) : (
          <Check aria-hidden="true" className="size-3" strokeWidth={3} />
        )}
      </BaseCheckbox.Indicator>
    </BaseCheckbox.Root>
  )
  if (!label) return box
  return (
    <label
      className={cn(
        'inline-flex cursor-pointer items-center gap-2 text-base text-text has-data-disabled:cursor-not-allowed has-data-disabled:opacity-70',
        className,
      )}
    >
      {box}
      {label}
    </label>
  )
}
