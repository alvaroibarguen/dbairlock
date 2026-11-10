import { Drawer as BaseDrawer } from '@base-ui/react/drawer'
import { X } from 'lucide-react'
import type { ReactElement, ReactNode } from 'react'
import { cn } from './cn'
import { backdropClass } from './Dialog'

export interface DrawerProps {
  open?: boolean
  defaultOpen?: boolean
  onOpenChange?: (open: boolean) => void
  trigger?: ReactElement
  /** Edge the drawer slides in from. */
  side?: 'left' | 'right'
  title: ReactNode
  description?: ReactNode
  children?: ReactNode
  className?: string
}

const sideClass = {
  left: 'left-0 border-r data-starting-style:-translate-x-full data-ending-style:-translate-x-full',
  right: 'right-0 border-l data-starting-style:translate-x-full data-ending-style:translate-x-full',
} as const

/** A modal panel that slides in from the edge; swipe or Escape dismisses it. */
export function Drawer({
  open,
  defaultOpen,
  onOpenChange,
  trigger,
  side = 'right',
  title,
  description,
  children,
  className,
}: DrawerProps) {
  return (
    <BaseDrawer.Root
      open={open}
      defaultOpen={defaultOpen}
      onOpenChange={onOpenChange ? (next) => onOpenChange(next) : undefined}
      swipeDirection={side}
    >
      {trigger && <BaseDrawer.Trigger render={trigger} />}
      <BaseDrawer.Portal>
        <BaseDrawer.Backdrop className={backdropClass} />
        <BaseDrawer.Viewport className="fixed inset-0 z-50">
          <BaseDrawer.Popup
            className={cn(
              'fixed top-0 bottom-0 flex w-[320px] max-w-[calc(100vw-3rem)] flex-col border-seam bg-raise text-text shadow-overlay outline-none',
              '[translate:var(--drawer-swipe-movement-x)_0] transition-[translate] duration-160 ease-out data-swiping:duration-0',
              sideClass[side],
              className,
            )}
          >
            <BaseDrawer.Content className="flex min-h-0 flex-1 flex-col">
              <div className="flex h-11 shrink-0 items-center justify-between gap-4 border-b border-seam pr-2 pl-4">
                <BaseDrawer.Title className="text-base font-medium small-caps text-dim">
                  {title}
                </BaseDrawer.Title>
                <BaseDrawer.Close
                  aria-label="Close"
                  className="flex size-8 cursor-pointer items-center justify-center rounded-lg text-mute transition-colors duration-120 hover:bg-seam hover:text-text"
                >
                  <X aria-hidden="true" className="size-4" />
                </BaseDrawer.Close>
              </div>
              {description && (
                <BaseDrawer.Description className="px-4 pt-3 text-base text-dim">
                  {description}
                </BaseDrawer.Description>
              )}
              <div className="min-h-0 flex-1 overflow-y-auto">{children}</div>
            </BaseDrawer.Content>
          </BaseDrawer.Popup>
        </BaseDrawer.Viewport>
      </BaseDrawer.Portal>
    </BaseDrawer.Root>
  )
}

/** Closes the enclosing Drawer when its rendered element is activated. */
export function DrawerClose({ render }: { render: ReactElement }) {
  return <BaseDrawer.Close render={render} />
}
