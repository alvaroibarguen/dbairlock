import { Tabs as BaseTabs } from '@base-ui/react/tabs'
import type { ReactNode } from 'react'
import { cn } from './cn'

export interface TabItem<T extends string> {
  value: T
  label: ReactNode
  content: ReactNode
  disabled?: boolean
}

export interface TabsProps<T extends string> {
  items: readonly TabItem<T>[]
  value?: T
  defaultValue?: T
  onValueChange?: (value: T) => void
  'aria-label'?: string
  className?: string
  listClassName?: string
  panelClassName?: string
}

/** Underlined tabs in a 44px strip (aligned with pane headers) with a seam below; arrow keys move between tabs. */
export function Tabs<T extends string>({
  items,
  value,
  defaultValue,
  onValueChange,
  className,
  listClassName,
  panelClassName,
  ...aria
}: TabsProps<T>) {
  return (
    <BaseTabs.Root
      value={value}
      defaultValue={defaultValue ?? items[0]?.value}
      onValueChange={onValueChange ? (next) => onValueChange(next as T) : undefined}
      className={cn('flex min-h-0 flex-col', className)}
    >
      <BaseTabs.List
        aria-label={aria['aria-label']}
        className={cn(
          'relative flex h-11 shrink-0 items-stretch gap-4 border-b border-seam px-4',
          listClassName,
        )}
      >
        {items.map((item) => (
          <BaseTabs.Tab
            key={item.value}
            value={item.value}
            disabled={item.disabled}
            className={cn(
              'flex cursor-pointer items-center text-base text-mute transition-colors duration-120 ease-out select-none',
              'hover:not-data-disabled:text-text data-active:text-text',
              'focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-green',
              'data-disabled:cursor-not-allowed data-disabled:opacity-50',
            )}
          >
            {item.label}
          </BaseTabs.Tab>
        ))}
        <BaseTabs.Indicator className="absolute bottom-[-1px] left-0 h-0.5 w-(--active-tab-width) translate-x-(--active-tab-left) bg-green transition-[translate,width] duration-160 ease-out" />
      </BaseTabs.List>
      {items.map((item) => (
        <BaseTabs.Panel
          key={item.value}
          value={item.value}
          className={cn(
            'min-h-0 flex-1 overflow-auto focus-visible:outline-2 focus-visible:-outline-offset-2',
            panelClassName,
          )}
        >
          {item.content}
        </BaseTabs.Panel>
      ))}
    </BaseTabs.Root>
  )
}
