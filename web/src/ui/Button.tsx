import { Button as BaseButton } from '@base-ui/react/button'
import { type ComponentProps, type ReactNode, useId } from 'react'
import { cn } from './cn'
import { Spinner } from './Spinner'

export type ButtonVariant = 'primary' | 'secondary' | 'ghost' | 'destructive'
export type ButtonSize = 'md' | 'sm' | 'icon'

const base =
  'inline-flex shrink-0 cursor-pointer items-center justify-center gap-2 font-sans text-base font-medium whitespace-nowrap select-none ' +
  'border transition-[background-color,border-color,color,filter] duration-120 ease-out ' +
  'focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ' +
  'data-disabled:cursor-not-allowed data-disabled:opacity-50'

const variants: Record<ButtonVariant, string> = {
  primary:
    'border-green bg-green text-on-green hover:not-data-disabled:brightness-110 active:not-data-disabled:brightness-95',
  secondary:
    'border-seam bg-raise text-text hover:not-data-disabled:border-mute/60 active:not-data-disabled:bg-seam',
  ghost:
    'border-transparent bg-transparent text-dim hover:not-data-disabled:bg-raise hover:not-data-disabled:text-text',
  destructive:
    'border-red/50 bg-transparent text-red hover:not-data-disabled:border-red hover:not-data-disabled:bg-red/8 focus-visible:outline-red',
}

const sizes: Record<ButtonSize, string> = {
  md: 'h-8 rounded-md px-3.5',
  sm: 'h-7 rounded-md px-2.5 text-xs',
  icon: 'size-9 rounded-lg p-0',
}

export interface ButtonProps extends Omit<ComponentProps<typeof BaseButton>, 'className'> {
  variant?: ButtonVariant
  size?: ButtonSize
  /** Shows a spinner, disables the button and keeps it focusable. */
  loading?: boolean
  /** Keyboard hint shown in mono at 60% opacity, for example "⌘↵". */
  shortcut?: string
  icon?: ReactNode
  className?: string
}

/**
 * The one button. 32px high, 6px radius. Destructive buttons are transparent
 * with red text; place them apart from the primary action.
 */
export function Button({
  variant = 'secondary',
  size = 'md',
  loading = false,
  disabled,
  shortcut,
  icon,
  className,
  children,
  ...props
}: ButtonProps) {
  const labelId = useId()
  return (
    <BaseButton
      {...props}
      disabled={disabled || loading}
      focusableWhenDisabled={loading || props.focusableWhenDisabled}
      aria-busy={loading || undefined}
      aria-labelledby={loading && children ? labelId : props['aria-labelledby']}
      data-variant={variant}
      className={cn(base, variants[variant], sizes[size], className)}
    >
      {loading ? <Spinner /> : icon}
      {children != null && <span id={labelId}>{children}</span>}
      {shortcut && (
        <kbd className="font-mono text-2xs font-normal opacity-60" aria-hidden="true">
          {shortcut}
        </kbd>
      )}
    </BaseButton>
  )
}
