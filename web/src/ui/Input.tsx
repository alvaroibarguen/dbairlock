import { Input as BaseInput } from '@base-ui/react/input'
import type { ComponentProps } from 'react'
import { cn } from './cn'
import { controlClass } from './field-styles'

export interface InputProps extends Omit<ComponentProps<typeof BaseInput>, 'className'> {
  /** Marks the control invalid outside a FormField (inside one, the field decides). */
  invalid?: boolean
  mono?: boolean
  className?: string
}

export function Input({ invalid, mono, className, ...props }: InputProps) {
  return (
    <BaseInput
      {...props}
      aria-invalid={invalid ? true : props['aria-invalid']}
      className={cn(controlClass, 'h-8', mono && 'font-mono tabular', className)}
    />
  )
}
