import { KeyRound } from 'lucide-react'
import { type ReactNode, useEffect, useId, useRef } from 'react'
import { Button } from './Button'
import { cn } from './cn'
import { Input } from './Input'

/** What the server says about a stored secret. Never the value. */
export interface SecretStatusView {
  isSet: boolean
  setBy?: string
  /** ISO 8601 timestamp. */
  setAt?: string
}

/**
 * A pending change to a secret: `undefined` leaves it unchanged, a string
 * replaces it (the new value the person is typing) and `null` clears it.
 * This matches the API's update semantics, so it can be sent as is.
 */
export type SecretChange = string | null | undefined

export interface SecretFieldProps {
  label: ReactNode
  status: SecretStatusView
  value: SecretChange
  onChange: (value: SecretChange) => void
  help?: ReactNode
  error?: ReactNode
  /** Name of the password input, for forms and password managers. */
  name?: string
  disabled?: boolean
  className?: string
}

const dateFormat = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' })

function formatDate(iso: string | undefined): string | undefined {
  if (!iso) return undefined
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? undefined : dateFormat.format(d)
}

/**
 * A write-only secret. It shows whether the secret is set, by whom and when,
 * with Replace and Clear actions; Replace reveals an empty password input
 * (`autocomplete="new-password"`). It never receives a stored value, so it
 * cannot show one, and it has no show-password toggle.
 */
export function SecretField({
  label,
  status,
  value,
  onChange,
  help,
  error,
  name,
  disabled,
  className,
}: SecretFieldProps) {
  const id = useId()
  const labelId = `${id}-label`
  const helpId = `${id}-help`
  const errorId = `${id}-error`
  const replacing = typeof value === 'string'
  const clearing = value === null
  const invalid = error != null && error !== false && error !== ''

  // Return focus to the Replace/Set button after Cancel or Undo.
  const actionRef = useRef<HTMLButtonElement>(null)
  const wasEditing = useRef(false)
  useEffect(() => {
    const editing = replacing || clearing
    if (wasEditing.current && !editing) actionRef.current?.focus()
    wasEditing.current = editing
  }, [replacing, clearing])

  const setAt = formatDate(status.setAt)
  const describedBy = [help ? helpId : null, invalid ? errorId : null].filter(Boolean).join(' ')

  return (
    <div
      role="group"
      aria-labelledby={labelId}
      aria-describedby={describedBy || undefined}
      className={cn('flex flex-col gap-1.5', className)}
    >
      <span id={labelId} className="text-base font-medium text-text">
        {label}
      </span>

      {replacing ? (
        <div className="flex flex-wrap items-center gap-2">
          <Input
            type="password"
            name={name}
            autoComplete="new-password"
            autoFocus
            spellCheck={false}
            placeholder={status.isSet ? 'New value' : 'Value'}
            aria-labelledby={labelId}
            aria-describedby={describedBy || undefined}
            invalid={invalid}
            disabled={disabled}
            value={value}
            onChange={(e) => onChange(e.currentTarget.value)}
            className="min-w-48 flex-1 font-mono"
          />
          <Button variant="ghost" disabled={disabled} onClick={() => onChange(undefined)}>
            Cancel
          </Button>
        </div>
      ) : (
        <div className="flex flex-wrap items-center gap-2">
          <div
            className={cn(
              'flex min-h-8 min-w-48 flex-1 items-center gap-2 rounded-md border border-seam bg-bg px-2.5 py-1',
              clearing && 'border-amber/50',
            )}
          >
            <KeyRound aria-hidden="true" className="size-3.5 shrink-0 text-mute" />
            <span
              aria-hidden="true"
              className={cn(
                'inline-block size-[7px] shrink-0 rounded-full',
                clearing ? 'bg-amber' : status.isSet ? 'bg-green' : 'bg-mute',
              )}
            />
            <span className="min-w-0 text-base break-words" aria-live="polite">
              {clearing ? (
                <span className="text-amber">Will be cleared when you save.</span>
              ) : status.isSet ? (
                <span className="text-dim">
                  Set
                  {status.setBy && (
                    <>
                      {' by '}
                      <span className="text-text">{status.setBy}</span>
                    </>
                  )}
                  {setAt && (
                    <>
                      {' on '}
                      <time
                        dateTime={status.setAt}
                        className="font-mono text-xs whitespace-nowrap tabular"
                      >
                        {setAt}
                      </time>
                    </>
                  )}
                </span>
              ) : (
                <span className="text-mute">Not set</span>
              )}
            </span>
          </div>
          {clearing ? (
            <Button
              ref={actionRef}
              variant="ghost"
              disabled={disabled}
              onClick={() => onChange(undefined)}
            >
              Undo
            </Button>
          ) : (
            <>
              <Button
                ref={actionRef}
                disabled={disabled}
                onClick={() => onChange('')}
                aria-describedby={labelId}
              >
                {status.isSet ? 'Replace' : 'Set'}
              </Button>
              {status.isSet && (
                <Button
                  variant="destructive"
                  disabled={disabled}
                  onClick={() => onChange(null)}
                  aria-describedby={labelId}
                  className="ml-2"
                >
                  Clear
                </Button>
              )}
            </>
          )}
        </div>
      )}

      {help && (
        <p id={helpId} className="text-xs text-mute">
          {help}
        </p>
      )}
      {invalid && (
        <p id={errorId} className="text-xs text-red">
          {error}
        </p>
      )}
    </div>
  )
}
