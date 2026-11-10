import { useQuery } from '@tanstack/react-query'
import { Menu as MenuIcon } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { Link, Navigate, Outlet, useLocation, useNavigate } from 'react-router'
import { meQuery, metaQuery } from '@/api'
import { Button, Drawer, Logo, MEDIUM, typing, useMediaQuery } from '@/ui'
import { NEW_CHANGE_KEY, newChangeNav } from './nav'
import { NavList, NewChangeLink, Rail } from './Rail'
import { UserMenu } from './UserMenu'

/**
 * After a navigation, if the focused element went away with the old screen
 * (a queue row is re-rendered when a change opens; on a phone the detail
 * replaces the queue), put focus back on the selected list row when the new
 * screen shows one, or else on the content, instead of leaving keyboard
 * users at the top of the document. Runs after the new screen has rendered.
 */
function useKeepFocus() {
  const { pathname } = useLocation()
  const first = useRef(true)
  useEffect(() => {
    if (first.current) {
      first.current = false
      return
    }
    const active = document.activeElement
    if (active !== null && active !== document.body) return
    const main = document.getElementById('main')
    const row = main?.querySelector<HTMLElement>('[data-row][aria-current="page"]')
    if (row) row.focus()
    else main?.focus({ preventScroll: true })
  }, [pathname])
}

/** N opens the change editor from any screen, unless a text field, editor or dialog has the key. */
function useNewChangeShortcut() {
  const navigate = useNavigate()
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key.toLowerCase() !== NEW_CHANGE_KEY || e.metaKey || e.ctrlKey || e.altKey) return
      if (e.repeat || typing() || e.defaultPrevented) return
      if (newChangeNav.isActive(window.location.pathname)) return
      e.preventDefault()
      void navigate(newChangeNav.to)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [navigate])
}

const skipLink =
  'sr-only z-50 rounded-md bg-raise px-3 py-2 text-text focus:not-sr-only focus:fixed focus:top-2 focus:left-2'

/**
 * Rail + pane area. From 768px the rail sits on the left; below that it
 * becomes a top bar whose menu button opens the navigation in a drawer.
 */
export function AppShell() {
  const medium = useMediaQuery(MEDIUM, true)
  const [navOpen, setNavOpen] = useState(false)
  useKeepFocus()
  useNewChangeShortcut()
  const meta = useQuery(metaQuery())
  // Loading the signed-in user is the route guard: a 401 sends the browser
  // to sign in (see the API client), remembering where it was.
  useQuery(meQuery())

  if (meta.data?.setupState === 'setup_required') return <Navigate to="/setup" replace />

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
        <NewChangeLink side="bottom" className="size-8 rounded-md" />
        <UserMenu side="bottom" />
      </header>
      <main id="main" tabIndex={-1} className="flex min-w-0 flex-1 flex-col bg-bg outline-none">
        <Outlet />
      </main>
    </div>
  )
}
