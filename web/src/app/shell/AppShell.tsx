import { Menu as MenuIcon } from 'lucide-react'
import { useState } from 'react'
import { Link, Outlet } from 'react-router'
import { Button, Drawer, Logo, MEDIUM, useMediaQuery } from '@/ui'
import { NavList, Rail } from './Rail'
import { UserMenu } from './UserMenu'

const skipLink =
  'sr-only z-50 rounded-md bg-raise px-3 py-2 text-text focus:not-sr-only focus:fixed focus:top-2 focus:left-2'

/**
 * Rail + pane area. From 768px the rail sits on the left; below that it
 * becomes a top bar whose menu button opens the navigation in a drawer.
 */
export function AppShell() {
  const medium = useMediaQuery(MEDIUM, true)
  const [navOpen, setNavOpen] = useState(false)

  if (medium) {
    return (
      <div className="grid h-dvh grid-cols-[52px_minmax(0,1fr)] gap-px overflow-hidden bg-seam">
        <a href="#main" className={skipLink}>
          Skip to content
        </a>
        <Rail />
        <main
          id="main"
          tabIndex={-1}
          className="min-h-0 min-w-0 overflow-hidden bg-bg outline-none"
        >
          <Outlet />
        </main>
      </div>
    )
  }

  return (
    <div className="flex min-h-dvh flex-col bg-seam">
      <a href="#main" className={skipLink}>
        Skip to content
      </a>
      <header className="sticky top-0 z-40 flex h-12 shrink-0 items-center gap-2 border-b border-seam bg-bg px-2">
        <Drawer
          side="left"
          title="Navigation"
          open={navOpen}
          onOpenChange={setNavOpen}
          trigger={
            <Button
              variant="ghost"
              size="icon"
              aria-label="Open navigation"
              icon={<MenuIcon aria-hidden="true" className="size-[18px]" />}
            />
          }
        >
          <nav aria-label="Main">
            <NavList onNavigate={() => setNavOpen(false)} />
          </nav>
        </Drawer>
        <Link
          to="/changes"
          aria-label="dbairlock home"
          className="flex items-center gap-2 rounded-md"
        >
          <Logo />
          <span className="font-mono text-base font-medium text-text">dbairlock</span>
        </Link>
        <div className="flex-1" />
        <UserMenu side="bottom" />
      </header>
      <main id="main" tabIndex={-1} className="flex min-w-0 flex-1 flex-col bg-bg outline-none">
        <Outlet />
      </main>
    </div>
  )
}
