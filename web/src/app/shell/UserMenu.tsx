import { useQuery, useQueryClient } from '@tanstack/react-query'
import { LogOut, MonitorX, Monitor, Moon, Sun, UserRound } from 'lucide-react'
import type { ComponentProps } from 'react'
import { useNavigate } from 'react-router'
import { meQuery, metaQuery, signOut, signOutEverywhere, SIGN_IN_PATH } from '@/api'
import {
  cn,
  Menu,
  MenuItem,
  MenuRadioGroup,
  MenuSeparator,
  type ThemePreference,
  toast,
  useTheme,
} from '@/ui'

const themeOptions = [
  { value: 'system', label: 'System', icon: <Monitor aria-hidden="true" className="size-3.5" /> },
  { value: 'dark', label: 'Dark', icon: <Moon aria-hidden="true" className="size-3.5" /> },
  { value: 'light', label: 'Light', icon: <Sun aria-hidden="true" className="size-3.5" /> },
] as const satisfies readonly { value: ThemePreference; label: string; icon: unknown }[]

/** Two initials from a name or email, for the avatar. */
function initials(nameOrEmail: string): string {
  const base = nameOrEmail.split('@')[0] ?? nameOrEmail
  const [first = '', second = ''] = base.split(/[\s._-]+/).filter(Boolean)
  const letters = second ? `${first.charAt(0)}${second.charAt(0)}` : base.slice(0, 2)
  return letters.toUpperCase()
}

function AvatarButton({
  label,
  className,
  ...props
}: ComponentProps<'button'> & { label?: string }) {
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
      {/* The initials are an avatar picture, drawn by CSS so they stay out of the button's name. */}
      <span
        aria-hidden="true"
        data-initials={label}
        className="grid size-7 place-items-center rounded-full bg-raise font-mono text-2xs font-medium text-dim before:content-[attr(data-initials)]"
      >
        {label === undefined && <UserRound className="size-4" />}
      </span>
    </button>
  )
}

/** The avatar menu: who is signed in, sign out, theme and the running version. */
export function UserMenu({ side = 'right' }: { side?: 'right' | 'bottom' }) {
  const { preference, setPreference } = useTheme()
  const meta = useQuery(metaQuery())
  const me = useQuery(meQuery())
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const user = me.data?.user

  const leave = async (everywhere: boolean) => {
    try {
      await (everywhere ? signOutEverywhere() : signOut())
    } catch (error) {
      toast.error(
        error instanceof Error ? error.message : 'You could not be signed out. Try again.',
      )
      return
    }
    queryClient.clear()
    void navigate(SIGN_IN_PATH, { replace: true })
  }

  return (
    <Menu
      trigger={<AvatarButton label={user ? initials(user.name || user.email) : undefined} />}
      side={side}
      align="end"
    >
      {user ? (
        <div className="flex flex-col gap-0.5 px-2 pt-1.5 pb-1.5">
          <span className="truncate text-base text-text">{user.name || user.email}</span>
          <span className="truncate font-mono text-xs text-mute">{user.email}</span>
          <span className="text-xs text-dim">{user.role === 'admin' ? 'Admin' : 'Member'}</span>
        </div>
      ) : (
        <p className="px-2 pt-1.5 pb-1 text-base text-dim">Not signed in</p>
      )}
      {user && (
        <>
          <MenuSeparator />
          <MenuItem
            icon={<LogOut aria-hidden="true" className="size-3.5" />}
            onClick={() => void leave(false)}
          >
            Sign out
          </MenuItem>
          <MenuItem
            icon={<MonitorX aria-hidden="true" className="size-3.5" />}
            onClick={() => void leave(true)}
          >
            Sign out everywhere
          </MenuItem>
        </>
      )}
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
