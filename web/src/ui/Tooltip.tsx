import { Tooltip as BaseTooltip } from '@base-ui/react/tooltip'
import type { ReactElement, ReactNode } from 'react'
import { cn } from './cn'

/** Shares hover delay across tooltips; mount once near the app root. */
export function TooltipProvider({ children }: { children: ReactNode }) {
  return <BaseTooltip.Provider delay={400}>{children}</BaseTooltip.Provider>
}

export interface TooltipProps {
  content: ReactNode
  /** The element the tooltip describes. Give icon-only triggers an aria-label. */
  children: ReactElement
  side?: 'top' | 'right' | 'bottom' | 'left'
  /** Keyboard shortcut shown after the label. */
  shortcut?: string
}

export function Tooltip({ content, children, side = 'top', shortcut }: TooltipProps) {
  return (
    <BaseTooltip.Root>
      <BaseTooltip.Trigger render={children} />
      <BaseTooltip.Portal>
        <BaseTooltip.Positioner side={side} sideOffset={8} className="z-50">
          <BaseTooltip.Popup
            className={cn(
              'flex items-center gap-2 rounded-md border border-seam bg-raise px-2 py-1 text-xs text-text shadow-overlay',
              'origin-(--transform-origin) transition-[scale,opacity] duration-120 ease-out',
              'data-ending-style:opacity-0 data-instant:transition-none data-starting-style:scale-[0.98] data-starting-style:opacity-0',
            )}
          >
            {content}
            {shortcut && <kbd className="font-mono text-2xs opacity-60">{shortcut}</kbd>}
          </BaseTooltip.Popup>
        </BaseTooltip.Positioner>
      </BaseTooltip.Portal>
    </BaseTooltip.Root>
  )
}
