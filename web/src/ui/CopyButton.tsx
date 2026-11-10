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
  /** Accessible name of the copy button, for example "Copy token". Shown as "Copy". */
  copyLabel?: string
  /** What the text is, shown in the header strip: "Terminal", ".mcp.json", "SQL". */
  caption?: string
  className?: string
}

/**
 * Mono text on the recessed ground. With a caption or copy label it gets a
 * 32px header strip, seamed off from the text, holding the caption on the
 * left and a compact copy button on the right. Content is always plain text.
 */
export function CodeBlock({ children, copyLabel, caption, className }: CodeBlockProps) {
  const header = Boolean(caption) || Boolean(copyLabel)
  return (
    <div className={cn('overflow-hidden rounded-md border border-seam bg-bg', className)}>
      {header && (
        <div className="flex h-8 items-center justify-between gap-2 border-b border-seam bg-panel/40 pr-1 pl-3">
          <span className="truncate font-mono text-xs text-mute">{caption}</span>
          {copyLabel && <CodeCopyButton text={children} label={copyLabel} />}
        </div>
      )}
      <pre className="px-3 py-2.5 font-mono text-sm leading-[1.7] break-all whitespace-pre-wrap text-text tabular">
        {children}
      </pre>
    </div>
  )
}

/** The header-strip copy button: ghost, 24px, visible text "Copy". */
function CodeCopyButton({ text, label }: { text: string; label: string }) {
  const [copied, setCopied] = useState(false)
  useEffect(() => {
    if (!copied) return
    const t = setTimeout(() => setCopied(false), 2000)
    return () => clearTimeout(t)
  }, [copied])
  return (
    <Button
      variant="ghost"
      size="sm"
      aria-label={label}
      className={cn('h-6 gap-1.5 px-2', copied && 'text-green')}
      icon={
        copied ? (
          <Check aria-hidden="true" className="size-3.5" />
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
      <span aria-live="polite">{copied ? 'Copied' : 'Copy'}</span>
    </Button>
  )
}
