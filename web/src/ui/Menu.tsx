import { Menu as BaseMenu } from '@base-ui/react/menu'
import { Check } from 'lucide-react'
import type { ReactElement, ReactNode } from 'react'
import { cn } from './cn'
import { listItem, overlaySurface, popupMotion } from './overlay-styles'

export interface MenuProps {
  /** The element that opens the menu. */
  trigger: ReactElement
  children: ReactNode
  side?: 'top' | 'right' | 'bottom' | 'left'
  align?: 'start' | 'center' | 'end'
  className?: string
}

export function Menu({
  trigger,
  children,
  side = 'bottom',
  align = 'start',
  className,
}: MenuProps) {
  return (
    <BaseMenu.Root>
      <BaseMenu.Trigger render={trigger} />
      <BaseMenu.Portal>
        <BaseMenu.Positioner side={side} align={align} sideOffset={8} className="z-50 outline-none">
          <BaseMenu.Popup
            className={cn(overlaySurface, popupMotion, 'min-w-52 p-1 outline-none', className)}
          >
            {children}
          </BaseMenu.Popup>
        </BaseMenu.Positioner>
      </BaseMenu.Portal>
    </BaseMenu.Root>
  )
}

export function MenuItem({
  children,
  onClick,
  disabled,
  icon,
}: {
  children: ReactNode
  onClick?: () => void
  disabled?: boolean
  icon?: ReactNode
}) {
  return (
    <BaseMenu.Item onClick={onClick} disabled={disabled} className={listItem}>
      {icon && <span className="flex w-3.5 text-mute">{icon}</span>}
      {children}
    </BaseMenu.Item>
  )
}

export function MenuSeparator() {
  return <BaseMenu.Separator className="mx-1 my-1 h-px bg-seam" />
}

/** Non-interactive heading or note inside a menu. */
export function MenuLabel({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div className={cn('px-2 pt-1.5 pb-1 text-xs font-medium small-caps text-dim', className)}>
      {children}
    </div>
  )
}

export interface MenuRadioGroupProps<T extends string> {
  label: string
  value: T
  onValueChange: (value: T) => void
  options: readonly { value: T; label: ReactNode; icon?: ReactNode }[]
}

export function MenuRadioGroup<T extends string>({
  label,
  value,
  onValueChange,
  options,
}: MenuRadioGroupProps<T>) {
  return (
    <BaseMenu.RadioGroup value={value} onValueChange={(next) => onValueChange(next as T)}>
      <BaseMenu.GroupLabel className="px-2 pt-1.5 pb-1 text-xs font-medium small-caps text-dim">
        {label}
      </BaseMenu.GroupLabel>
      {options.map((option) => (
        <BaseMenu.RadioItem
          key={option.value}
          value={option.value}
          className={cn(listItem, 'grid grid-cols-[14px_14px_1fr] pr-3')}
        >
          <BaseMenu.RadioItemIndicator className="col-start-1 flex text-green">
            <Check aria-hidden="true" className="size-3.5" />
          </BaseMenu.RadioItemIndicator>
          <span className="col-start-2 flex text-mute">{option.icon}</span>
          <span className="col-start-3">{option.label}</span>
        </BaseMenu.RadioItem>
      ))}
    </BaseMenu.RadioGroup>
  )
}
