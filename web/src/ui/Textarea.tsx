import { Field } from '@base-ui/react/field'
import type { ComponentProps } from 'react'
import { cn } from './cn'
import { controlClass } from './field-styles'

export interface TextareaProps extends Omit<ComponentProps<'textarea'>, 'className'> {
  invalid?: boolean
  mono?: boolean
  className?: string
}

/**
 * A multi-line text control. Rendered through Field.Control so it registers
 * with an enclosing FormField (label, description and error wiring).
 */
export function Textarea({ invalid, mono, className, rows = 4, ...props }: TextareaProps) {
  return (
    <Field.Control
      render={<textarea rows={rows} {...props} />}
      aria-invalid={invalid ? true : props['aria-invalid']}
      className={cn(
        controlClass,
        'min-h-16 resize-y py-1.5 leading-[1.45]',
        mono && 'font-mono tabular',
        className,
      )}
    />
  )
}
