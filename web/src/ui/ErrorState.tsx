import { CircleAlert } from 'lucide-react'
import type { ReactNode } from 'react'
import { cn } from './cn'

export interface ErrorStateProps {
  /** What failed, in plain words. */
  title: ReactNode
  /** The reason, for example the server's error message. */
  message?: ReactNode
  /** Stable error code shown in mono, for example "not_found". */
  code?: string
  /** One action, usually Retry. */
  action?: ReactNode
  className?: string
}

/** An error in place of content: names the failure and the reason, plus one action. */
export function ErrorState({ title, message, code, action, className }: ErrorStateProps) {
  return (
    <div
      role="alert"
      className={cn(
        'flex flex-col items-start gap-3 px-6 py-10 text-left sm:items-center sm:text-center',
        className,
      )}
    >
      <CircleAlert aria-hidden="true" className="size-5 text-red" />
      <div className="flex max-w-sm flex-col gap-1">
        <p className="text-base text-text">{title}</p>
        {message && <p className="text-base text-dim">{message}</p>}
        {code && <p className="font-mono text-xs text-mute">{code}</p>}
      </div>
      {action}
    </div>
  )
}
