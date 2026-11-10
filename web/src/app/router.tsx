import { createBrowserRouter, Navigate, type RouteObject } from 'react-router'
import { setSetupRequiredHandler, setUnauthorizedHandler, SIGN_IN_PATH } from '@/api'
import { AuditPage } from '@/features/audit/AuditPage'
import { SignInPage } from '@/features/auth/SignInPage'
import { ChangeDetailPage } from '@/features/changes/ChangeDetailPage'
import { ChangesPage } from '@/features/changes/ChangesPage'
import { NewChangePage } from '@/features/changes/NewChangePage'
import { ConnectionsPage } from '@/features/connections/ConnectionsPage'
import { SettingsPage } from '@/features/settings/SettingsPage'
import { RecoverPage } from '@/features/setup/RecoverPage'
import { SetupPage } from '@/features/setup/SetupPage'
import { NotFoundPage, RouteError } from './RouteError'
import { AppShell } from './shell/AppShell'

export const routes: RouteObject[] = [
  { path: '/setup', Component: SetupPage, errorElement: <RouteError /> },
  { path: '/recover', Component: RecoverPage, errorElement: <RouteError /> },
  { path: SIGN_IN_PATH, Component: SignInPage, errorElement: <RouteError /> },
  {
    path: '/',
    Component: AppShell,
    errorElement: <RouteError />,
    children: [
      { index: true, element: <Navigate to="/changes" replace /> },
      { path: 'changes', Component: ChangesPage },
      { path: 'changes/new', Component: NewChangePage },
      { path: 'changes/:id', Component: ChangeDetailPage },
      { path: 'audit', Component: AuditPage },
      { path: 'connections', Component: ConnectionsPage },
      { path: 'settings', Component: SettingsPage },
      { path: 'settings/:section', Component: SettingsPage },
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
