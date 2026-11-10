import { Field } from '@base-ui/react/field'
import type { ReactNode } from 'react'
import { cn } from './cn'

export interface FormFieldProps {
  label: ReactNode
  /** Help text shown under the control. */
  help?: ReactNode
  /** Error message; when set the field is invalid and the message replaces nothing. */
  error?: ReactNode
  name?: string
  disabled?: boolean
  touched?: boolean
  dirty?: boolean
  className?: string
  children: ReactNode
}

/**
 * Label, control, help text and error message, wired together for assistive
 * tech by Base UI's Field. Pass the control (Input, Textarea, Select, ...) as
 * children. Works with react-hook-form via `error={fieldState.error?.message}`.
 */
export function FormField({
  label,
  help,
  error,
  name,
  disabled,
  touched,
  dirty,
  className,
  children,
}: FormFieldProps) {
  const invalid = error != null && error !== false && error !== ''
  return (
    <Field.Root
      name={name}
      disabled={disabled}
      invalid={invalid}
      touched={touched}
      dirty={dirty}
      className={cn('flex flex-col gap-1.5', className)}
    >
      <Field.Label className="text-base font-medium text-text data-disabled:opacity-50">
        {label}
      </Field.Label>
      {children}
      {help && <Field.Description className="text-xs text-mute">{help}</Field.Description>}
      <Field.Error match={invalid} className="text-xs text-red">
        {error}
      </Field.Error>
    </Field.Root>
  )
}
