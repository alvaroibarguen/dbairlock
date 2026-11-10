import { NavLink, useLocation } from 'react-router'
import { cn, Logo, Tooltip } from '@/ui'
import { type NavItem, primaryNav, settingsNav } from './nav'
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

/** The 52px icon rail on the far left (768px and wider). */
export function Rail() {
  return (
    <nav aria-label="Main" className="flex h-full flex-col items-center gap-1.5 bg-bg py-3">
      <NavLink to="/changes" aria-label="dbairlock home" className="mb-3 rounded-[7px]">
        <Logo />
      </NavLink>
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
      {[...primaryNav, settingsNav].map((item) => {
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
