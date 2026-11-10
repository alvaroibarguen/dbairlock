import { Dialog as BaseDialog } from '@base-ui/react/dialog'
import { X } from 'lucide-react'
import type { ReactElement, ReactNode } from 'react'
import { cn } from './cn'
import { overlaySurface } from './overlay-styles'

export interface DialogProps {
  open?: boolean
  defaultOpen?: boolean
  onOpenChange?: (open: boolean) => void
  /** Element that opens the dialog, for example a <Button />. */
  trigger?: ReactElement
  title: ReactNode
  description?: ReactNode
  /** Action bar; put the primary action last and keep destructive actions apart. */
  footer?: ReactNode
  children?: ReactNode
  className?: string
}

export const backdropClass =
  'fixed inset-0 min-h-dvh bg-black/50 transition-opacity duration-160 ease-out data-starting-style:opacity-0 data-ending-style:opacity-0 supports-[-webkit-touch-callout:none]:absolute'

/** A modal dialog: focus is trapped, Escape and the close button dismiss it. */
export function Dialog({
  open,
  defaultOpen,
  onOpenChange,
  trigger,
  title,
  description,
  footer,
  children,
  className,
}: DialogProps) {
  return (
    <BaseDialog.Root
      open={open}
      defaultOpen={defaultOpen}
      onOpenChange={onOpenChange ? (next) => onOpenChange(next) : undefined}
    >
      {trigger && <BaseDialog.Trigger render={trigger} />}
      <BaseDialog.Portal>
        <BaseDialog.Backdrop className={backdropClass} />
        <BaseDialog.Popup
          className={cn(
            overlaySurface,
            'fixed top-1/2 left-1/2 z-50 flex max-h-[calc(100dvh-2rem)] w-[480px] max-w-[calc(100vw-2rem)] -translate-x-1/2 -translate-y-1/2 flex-col overflow-hidden outline-none',
            'transition-[scale,opacity] duration-120 ease-out data-ending-style:scale-[0.98] data-ending-style:opacity-0 data-starting-style:scale-[0.98] data-starting-style:opacity-0',
            className,
          )}
        >
          <div className="flex items-start justify-between gap-4 border-b border-seam px-5 pt-4 pb-3">
            <div className="flex min-w-0 flex-col gap-1">
              <BaseDialog.Title className="text-lg font-semibold tracking-[-0.01em]">
                {title}
              </BaseDialog.Title>
              {description && (
                <BaseDialog.Description className="text-base text-dim">
                  {description}
                </BaseDialog.Description>
              )}
            </div>
            <BaseDialog.Close
              aria-label="Close"
              className="-mr-1.5 flex size-7 shrink-0 cursor-pointer items-center justify-center rounded-lg text-mute transition-colors duration-120 hover:bg-seam hover:text-text"
            >
              <X aria-hidden="true" className="size-4" />
            </BaseDialog.Close>
          </div>
          {children && <div className="overflow-y-auto px-5 py-4">{children}</div>}
          {footer && (
            <div className="flex items-center gap-2 border-t border-seam px-5 py-3">{footer}</div>
          )}
        </BaseDialog.Popup>
      </BaseDialog.Portal>
    </BaseDialog.Root>
  )
}

/** Closes the enclosing Dialog when its rendered element is activated. */
export function DialogClose({ render }: { render: ReactElement }) {
  return <BaseDialog.Close render={render} />
}
