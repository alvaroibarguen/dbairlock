import { Field } from '@base-ui/react/field'
import { X } from 'lucide-react'
import { type ClipboardEvent, type KeyboardEvent, useState } from 'react'
import { cn } from './cn'

export interface TagInputProps {
  value: readonly string[]
  onChange: (value: string[]) => void
  /** Normalizes a typed entry (for example lower-casing). Defaults to trimming. */
  normalize?: (entry: string) => string
  placeholder?: string
  name?: string
  disabled?: boolean
  invalid?: boolean
  mono?: boolean
  onBlur?: () => void
  className?: string
}

const separators = /[\s,;]+/

/**
 * A list of short values as removable tags with a text input after them.
 * Enter, comma, space or leaving the field adds what was typed; Backspace in
 * an empty input removes the last tag; pasting a list adds every entry.
 * Rendered through Field.Control so it works inside a FormField.
 */
export function TagInput({
  value,
  onChange,
  normalize = (s) => s.trim(),
  placeholder,
  name,
  disabled,
  invalid,
  mono,
  onBlur,
  className,
}: TagInputProps) {
  const [draft, setDraft] = useState('')

  const add = (text: string) => {
    const entries = text
      .split(separators)
      .map(normalize)
      .filter((s) => s !== '')
    if (entries.length === 0) return
    const next = [...value]
    for (const e of entries) if (!next.includes(e)) next.push(e)
    onChange(next)
    setDraft('')
  }

  const remove = (entry: string) => onChange(value.filter((v) => v !== entry))

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if ((e.key === 'Enter' || e.key === ',' || e.key === ' ') && draft.trim() !== '') {
      e.preventDefault()
      add(draft)
    } else if (e.key === 'Enter') {
      // An empty Enter must not submit a half-finished list by accident.
      e.preventDefault()
    } else if (e.key === 'Backspace' && draft === '') {
      const last = value.at(-1)
      if (last === undefined) return
      e.preventDefault()
      remove(last)
    }
  }

  const onPaste = (e: ClipboardEvent<HTMLInputElement>) => {
    const text = e.clipboardData.getData('text')
    if (separators.test(text)) {
      e.preventDefault()
      add(draft + text)
    }
  }

  return (
    <div
      aria-invalid={invalid || undefined}
      className={cn(
        'flex min-h-8 w-full flex-wrap items-center gap-1 rounded-md border border-seam bg-bg px-1 py-[3px]',
        'transition-[border-color] duration-120 ease-out hover:border-mute/60',
        'focus-within:outline-2 focus-within:outline-offset-0 focus-within:outline-green',
        'has-[[data-invalid]]:border-red aria-invalid:border-red',
        disabled && 'cursor-not-allowed opacity-50',
        className,
      )}
    >
      {value.map((entry) => (
        <span
          key={entry}
          className={cn(
            'inline-flex h-[22px] items-center gap-1 rounded-sm border border-seam bg-raise pr-0.5 pl-2 text-xs text-text',
            mono && 'font-mono',
          )}
        >
          {entry}
          <button
            type="button"
            aria-label={`Remove ${entry}`}
            disabled={disabled}
            onClick={() => remove(entry)}
            className="grid size-4 cursor-pointer place-items-center rounded-[3px] text-mute hover:bg-seam hover:text-text focus-visible:outline-2 focus-visible:outline-green"
          >
            <X aria-hidden="true" className="size-3" />
          </button>
        </span>
      ))}
      <Field.Control
        render={
          <input
            name={name}
            value={draft}
            disabled={disabled}
            placeholder={value.length === 0 ? placeholder : undefined}
            spellCheck={false}
            autoCapitalize="none"
            onChange={(e) => setDraft(e.currentTarget.value)}
            onKeyDown={onKeyDown}
            onPaste={onPaste}
            onBlur={() => {
              add(draft)
              onBlur?.()
            }}
          />
        }
        className={cn(
          'h-6 min-w-24 flex-1 bg-transparent px-1.5 text-base text-text outline-none placeholder:text-mute',
          mono && 'font-mono',
        )}
      />
    </div>
  )
}
