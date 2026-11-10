import { Switch as BaseSwitch } from '@base-ui/react/switch'
import type { ComponentProps, ReactNode } from 'react'
import { cn } from './cn'

export interface SwitchProps extends Omit<ComponentProps<typeof BaseSwitch.Root>, 'className'> {
  label?: ReactNode
  className?: string
}

export function Switch({ label, className, ...props }: SwitchProps) {
  const control = (
    <BaseSwitch.Root
      {...props}
      className={cn(
        'relative flex h-[18px] w-8 shrink-0 cursor-pointer items-center rounded-full border border-seam bg-bg p-px',
        'transition-[background-color,border-color] duration-120 ease-out hover:not-data-disabled:border-mute',
        'focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green',
        'data-checked:border-green data-checked:bg-green',
        'data-disabled:cursor-not-allowed data-disabled:opacity-50',
        !label && className,
      )}
    >
      <BaseSwitch.Thumb className="size-3.5 rounded-full bg-mute transition-[translate,background-color] duration-120 ease-out data-checked:translate-x-3.5 data-checked:bg-on-green" />
    </BaseSwitch.Root>
  )
  if (!label) return control
  return (
    <label
      className={cn(
        'inline-flex cursor-pointer items-center gap-2 text-base text-text has-data-disabled:cursor-not-allowed has-data-disabled:opacity-70',
        className,
      )}
    >
      {control}
      {label}
    </label>
  )
}
