import { Check, Copy } from 'lucide-react'
import { useEffect, useState } from 'react'
import { Button, type ButtonSize, type ButtonVariant } from './Button'
import { cn } from './cn'

export interface CopyButtonProps {
  /** The text to put on the clipboard. */
  text: string
  /** Visible label; the accessible name is "Copy <label>". */
  label?: string
  variant?: ButtonVariant
  size?: ButtonSize
  className?: string
}

/** Copies `text` and confirms for two seconds. */
export function CopyButton({
  text,
  label = 'Copy',
  variant = 'secondary',
  size = 'sm',
  className,
}: CopyButtonProps) {
  const [copied, setCopied] = useState(false)
  useEffect(() => {
    if (!copied) return
    const t = setTimeout(() => setCopied(false), 2000)
    return () => clearTimeout(t)
  }, [copied])

  return (
    <Button
      variant={variant}
      size={size}
      className={className}
      icon={
        copied ? (
          <Check aria-hidden="true" className="size-3.5 text-green" />
        ) : (
          <Copy aria-hidden="true" className="size-3.5" />
        )
      }
      onClick={() => {
        void navigator.clipboard.writeText(text).then(
          () => setCopied(true),
          () => setCopied(false),
        )
      }}
    >
      <span aria-live="polite">{copied ? 'Copied' : label}</span>
    </Button>
  )
}

export interface CodeBlockProps {
  children: string
  /** Name used for the copy button, for example "token". */
  copyLabel?: string
  className?: string
}

/** Mono text on the recessed ground with a copy button. Content is always plain text. */
export function CodeBlock({ children, copyLabel, className }: CodeBlockProps) {
  return (
    <div className={cn('relative rounded-md border border-seam bg-bg', className)}>
      <pre
        className={cn(
          'py-2.5 pl-3 font-mono text-sm leading-[1.7] break-all whitespace-pre-wrap text-text tabular',
          copyLabel ? 'pt-10 pr-3' : 'pr-3',
        )}
      >
        {children}
      </pre>
      {copyLabel && (
        <CopyButton text={children} label={copyLabel} className="absolute top-1.5 right-1.5" />
      )}
    </div>
  )
}
