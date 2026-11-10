import { Bot, Database, History, type LucideIcon, Plus, Rows3, Settings } from 'lucide-react'

export interface NavItem {
  to: string
  label: string
  icon: LucideIcon
  /** Whether the item is the current location. */
  isActive: (pathname: string) => boolean
}

const under = (base: string) => (pathname: string) =>
  pathname === base || pathname.startsWith(`${base}/`)

export const primaryNav: NavItem[] = [
  {
    to: '/changes',
    label: 'Changes',
    icon: Rows3,
    isActive: (p) => under('/changes')(p) && !under('/changes/new')(p),
  },
  { to: '/audit', label: 'Audit log', icon: History, isActive: under('/audit') },
  { to: '/connections', label: 'Connections', icon: Database, isActive: under('/connections') },
  { to: '/agents', label: 'Connect an agent', icon: Bot, isActive: under('/agents') },
]

/**
 * The primary action, shown apart from the navigation: a green button under
 * the logo in the rail, a + in the phone top bar, and the N shortcut.
 */
export const newChangeNav: NavItem = {
  to: '/changes/new',
  label: 'New change',
  icon: Plus,
  isActive: under('/changes/new'),
}

/** The key that opens the editor from anywhere (outside text fields). */
export const NEW_CHANGE_KEY = 'n'

export const settingsNav: NavItem = {
  to: '/settings',
  label: 'Settings',
  icon: Settings,
  isActive: under('/settings'),
}
