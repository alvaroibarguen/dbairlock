import { Select as BaseSelect } from '@base-ui/react/select'
import { Check, ChevronsUpDown } from 'lucide-react'
import type { ReactNode } from 'react'
import { cn } from './cn'
import { listItem, overlaySurface, popupMotion } from './overlay-styles'

export interface SelectOption<T extends string> {
  value: T
  label: ReactNode
  disabled?: boolean
}

export interface SelectProps<T extends string> {
  options: readonly SelectOption<T>[]
  value?: T | null
  defaultValue?: T | null
  onValueChange?: (value: T) => void
  placeholder?: ReactNode
  name?: string
  disabled?: boolean
  required?: boolean
  /** Marks the trigger invalid outside a FormField. */
  invalid?: boolean
  id?: string
  'aria-label'?: string
  className?: string
}

/** A single-choice select with a 32px trigger styled like an input. */
export function Select<T extends string>({
  options,
  value,
  defaultValue,
  onValueChange,
  placeholder = 'Select…',
  name,
  disabled,
  required,
  invalid,
  id,
  className,
  ...aria
}: SelectProps<T>) {
  return (
    <BaseSelect.Root
      items={options.map((o) => ({ value: o.value, label: o.label }))}
      value={value}
      defaultValue={defaultValue}
      onValueChange={(next) => {
        if (next != null) onValueChange?.(next)
      }}
      name={name}
      disabled={disabled}
      required={required}
      id={id}
    >
      <BaseSelect.Trigger
        aria-label={aria['aria-label']}
        aria-invalid={invalid || undefined}
        className={cn(
          'flex h-8 w-full min-w-40 cursor-pointer items-center justify-between gap-3 rounded-md border border-seam bg-bg pr-2 pl-2.5 text-left text-base text-text select-none',
          'transition-[border-color] duration-120 ease-out hover:not-data-disabled:border-mute/60',
          'focus-visible:outline-2 focus-visible:outline-offset-0 focus-visible:outline-green data-popup-open:border-mute/60',
          'aria-invalid:border-red data-invalid:border-red',
          'data-disabled:cursor-not-allowed data-disabled:opacity-50',
          className,
        )}
      >
        <BaseSelect.Value
          className="truncate data-placeholder:text-mute"
          placeholder={placeholder}
        />
        <BaseSelect.Icon className="flex text-mute">
          <ChevronsUpDown aria-hidden="true" className="size-3.5" />
        </BaseSelect.Icon>
      </BaseSelect.Trigger>
      <BaseSelect.Portal>
        <BaseSelect.Positioner
          className="z-50 outline-none"
          sideOffset={4}
          alignItemWithTrigger={false}
        >
          <BaseSelect.Popup
            className={cn(overlaySurface, popupMotion, 'min-w-(--anchor-width) p-1 outline-none')}
          >
            <BaseSelect.List className="max-h-(--available-height) overflow-y-auto outline-none">
              {options.map((option) => (
                <BaseSelect.Item
                  key={option.value}
                  value={option.value}
                  disabled={option.disabled}
                  className={cn(listItem, 'grid grid-cols-[14px_1fr] pr-3')}
                >
                  <BaseSelect.ItemIndicator className="col-start-1 flex text-green">
                    <Check aria-hidden="true" className="size-3.5" />
                  </BaseSelect.ItemIndicator>
                  <BaseSelect.ItemText className="col-start-2">{option.label}</BaseSelect.ItemText>
                </BaseSelect.Item>
              ))}
            </BaseSelect.List>
          </BaseSelect.Popup>
        </BaseSelect.Positioner>
      </BaseSelect.Portal>
    </BaseSelect.Root>
  )
}
