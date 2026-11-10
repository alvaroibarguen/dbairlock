import { NavLink, useLocation } from 'react-router'
import { cn, Logo, Tooltip } from '@/ui'
import { NEW_CHANGE_KEY, type NavItem, newChangeNav, primaryNav, settingsNav } from './nav'
import { UserMenu } from './UserMenu'

const railItem =
  'grid size-9 place-items-center rounded-lg text-mute transition-colors duration-120 ease-out hover:bg-raise hover:text-text aria-[current=page]:bg-raise aria-[current=page]:text-text'

function RailLink({ item }: { item: NavItem }) {
  const { pathname } = useLocation()
  const active = item.isActive(pathname)
  const Icon = item.icon
  return (
    <Tooltip content={item.label} side="right">
      <NavLink
        to={item.to}
        end
        aria-label={item.label}
        aria-current={active ? 'page' : undefined}
        className={railItem}
      >
        <Icon aria-hidden="true" className="size-[18px]" strokeWidth={1.6} />
      </NavLink>
    </Tooltip>
  )
}

/**
 * The primary action: a solid green button, set apart from the navigation.
 * `size` follows the surrounding control (rail item or top-bar icon button).
 */
export function NewChangeLink({
  className,
  side = 'right',
}: {
  className?: string
  side?: 'right' | 'bottom'
}) {
  const { pathname } = useLocation()
  const active = newChangeNav.isActive(pathname)
  const Icon = newChangeNav.icon
  return (
    <Tooltip content={newChangeNav.label} shortcut={NEW_CHANGE_KEY.toUpperCase()} side={side}>
      <NavLink
        to={newChangeNav.to}
        end
        aria-label={newChangeNav.label}
        aria-keyshortcuts={NEW_CHANGE_KEY.toUpperCase()}
        aria-current={active ? 'page' : undefined}
        className={cn(
          'grid size-9 place-items-center rounded-lg border border-green bg-green text-on-green transition-[filter] duration-120 ease-out hover:brightness-110 active:brightness-95 aria-[current=page]:outline-2 aria-[current=page]:outline-offset-2 aria-[current=page]:outline-green/50',
          className,
        )}
      >
        <Icon aria-hidden="true" className="size-[18px]" strokeWidth={2.2} />
      </NavLink>
    </Tooltip>
  )
}

/** The 52px icon rail on the far left (768px and wider). */
export function Rail() {
  return (
    <nav aria-label="Main" className="flex h-full flex-col items-center gap-1.5 bg-bg py-3">
      <NavLink to="/changes" aria-label="dbairlock home" className="mb-3 rounded-[7px]">
        <Logo />
      </NavLink>
      <NewChangeLink className="mb-3" />
      {primaryNav.map((item) => (
        <RailLink key={item.to} item={item} />
      ))}
      <div className="flex-1" />
      <RailLink item={settingsNav} />
      <UserMenu side="right" />
    </nav>
  )
}

/** Navigation list for the mobile drawer. */
export function NavList({ onNavigate }: { onNavigate: () => void }) {
  const { pathname } = useLocation()
  return (
    <ul className="flex flex-col gap-0.5 p-2">
      {[newChangeNav, ...primaryNav, settingsNav].map((item) => {
        const Icon = item.icon
        const active = item.isActive(pathname)
        return (
          <li key={item.to}>
            <NavLink
              to={item.to}
              end
              onClick={onNavigate}
              aria-current={active ? 'page' : undefined}
              className={cn(
                'flex h-10 items-center gap-3 rounded-md px-3 text-base text-dim transition-colors duration-120 hover:bg-seam hover:text-text',
                active && 'bg-seam text-text',
              )}
            >
              <Icon aria-hidden="true" className="size-[18px]" strokeWidth={1.6} />
              {item.label}
            </NavLink>
          </li>
        )
      })}
    </ul>
  )
}
