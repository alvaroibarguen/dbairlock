import { useQuery } from '@tanstack/react-query'
import { Monitor, Moon, Sun, UserRound } from 'lucide-react'
import type { ComponentProps } from 'react'
import { metaQuery } from '@/api'
import { cn, Menu, MenuRadioGroup, MenuSeparator, type ThemePreference, useTheme } from '@/ui'

const themeOptions = [
  { value: 'system', label: 'System', icon: <Monitor aria-hidden="true" className="size-3.5" /> },
  { value: 'dark', label: 'Dark', icon: <Moon aria-hidden="true" className="size-3.5" /> },
  { value: 'light', label: 'Light', icon: <Sun aria-hidden="true" className="size-3.5" /> },
] as const satisfies readonly { value: ThemePreference; label: string; icon: unknown }[]

function AvatarButton({ className, ...props }: ComponentProps<'button'>) {
  return (
    <button
      type="button"
      aria-label="User menu"
      {...props}
      className={cn(
        'grid size-9 cursor-pointer place-items-center rounded-lg text-mute transition-colors duration-120 ease-out hover:bg-raise hover:text-text data-popup-open:bg-raise data-popup-open:text-text',
        className,
      )}
    >
      <span className="grid size-7 place-items-center rounded-full bg-raise text-dim">
        <UserRound aria-hidden="true" className="size-4" />
      </span>
    </button>
  )
}

/** The avatar menu: identity, theme switch and the running version. */
export function UserMenu({ side = 'right' }: { side?: 'right' | 'bottom' }) {
  const { preference, setPreference } = useTheme()
  const meta = useQuery(metaQuery())
  return (
    <Menu trigger={<AvatarButton />} side={side} align="end">
      <p className="px-2 pt-1.5 pb-1 text-base text-dim">Not signed in</p>
      <MenuSeparator />
      <MenuRadioGroup
        label="Theme"
        value={preference}
        onValueChange={setPreference}
        options={themeOptions}
      />
      {meta.data && (
        <>
          <MenuSeparator />
          <p className="px-2 py-1.5 font-mono text-xs text-mute">dbairlock {meta.data.version}</p>
        </>
      )}
    </Menu>
  )
}
