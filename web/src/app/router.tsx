import { createBrowserRouter, Navigate, type RouteObject } from 'react-router'
import { setSetupRequiredHandler, setUnauthorizedHandler, SIGN_IN_PATH } from '@/api'
import { AuditPage } from '@/features/audit/AuditPage'
import { ConsentPage } from '@/features/auth/ConsentPage'
import { SignInPage } from '@/features/auth/SignInPage'
import { ChangeDetailPage } from '@/features/changes/ChangeDetailPage'
import { ChangesPage } from '@/features/changes/ChangesPage'
import { SequenceDetailPage } from '@/features/changes/SequenceDetailPage'
import { RecoverPage } from '@/features/setup/RecoverPage'
import { SetupPage } from '@/features/setup/SetupPage'
import { NotFoundPage, RouteError } from './RouteError'
import { AppShell } from './shell/AppShell'

// The editor and the admin pages load on first visit, keeping the initial
// bundle to what reviewing a change needs.
const newChange = () =>
  import('@/features/changes/NewChangePage').then((m) => ({ Component: m.NewChangePage }))
const connections = () =>
  import('@/features/connections/ConnectionsPage').then((m) => ({ Component: m.ConnectionsPage }))
const settings = () =>
  import('@/features/settings/SettingsPage').then((m) => ({ Component: m.SettingsPage }))
const agents = () =>
  import('@/features/agents/ConnectAgentPage').then((m) => ({ Component: m.ConnectAgentPage }))

export const routes: RouteObject[] = [
  { path: '/setup', Component: SetupPage, errorElement: <RouteError /> },
  { path: '/recover', Component: RecoverPage, errorElement: <RouteError /> },
  { path: SIGN_IN_PATH, Component: SignInPage, errorElement: <RouteError /> },
  // The OAuth consent page for MCP clients: outside the app shell, like sign-in.
  { path: '/oauth/consent', Component: ConsentPage, errorElement: <RouteError /> },
  {
    path: '/',
    Component: AppShell,
    errorElement: <RouteError />,
    children: [
      { index: true, element: <Navigate to="/changes" replace /> },
      { path: 'changes', Component: ChangesPage },
      { path: 'changes/new', lazy: newChange },
      { path: 'changes/:id', Component: ChangeDetailPage },
      { path: 'changes/:id/edit', lazy: newChange },
      { path: 'sequences/:id', Component: SequenceDetailPage },
      { path: 'audit', Component: AuditPage },
      { path: 'connections', lazy: connections },
      { path: 'connections/new', lazy: connections },
      { path: 'connections/:id', lazy: connections },
      { path: 'agents', lazy: agents },
      { path: 'settings', lazy: settings },
      { path: 'settings/:section', lazy: settings },
      { path: '*', Component: NotFoundPage },
    ],
  },
]

export function createAppRouter() {
  const router = createBrowserRouter(routes)
  // Route 401s and "setup required" through the router so the app is not reloaded.
  setUnauthorizedHandler((returnTo) => {
    void router.navigate(`${SIGN_IN_PATH}?next=${encodeURIComponent(returnTo)}`, {
      replace: true,
    })
  })
  setSetupRequiredHandler(() => {
    void router.navigate('/setup', { replace: true })
  })
  return router
}
