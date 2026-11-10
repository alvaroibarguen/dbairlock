import { Bot, Database, History, type LucideIcon, Rows3, Settings, SquarePen } from 'lucide-react'

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
  { to: '/changes/new', label: 'New change', icon: SquarePen, isActive: under('/changes/new') },
  { to: '/audit', label: 'Audit log', icon: History, isActive: under('/audit') },
  { to: '/connections', label: 'Connections', icon: Database, isActive: under('/connections') },
  { to: '/agents', label: 'Connect an agent', icon: Bot, isActive: under('/agents') },
]

export const settingsNav: NavItem = {
  to: '/settings',
  label: 'Settings',
  icon: Settings,
  isActive: under('/settings'),
}
