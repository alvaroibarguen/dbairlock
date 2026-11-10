import { useQuery } from '@tanstack/react-query'
import { Navigate, NavLink, useParams } from 'react-router'
import { can, isApiError, meQuery, type Permission, settingsQuery } from '@/api'
import {
  Button,
  cn,
  ErrorState,
  FormField,
  MEDIUM,
  PageHeader,
  Pane,
  PaneGrid,
  Select,
  Skeleton,
  type ThemePreference,
  useDocumentTitle,
  useMediaQuery,
  useTheme,
} from '@/ui'
import { NotificationTools } from './NotificationTools'
import { settingsGroups } from './settingsForm'
import { SettingsGroupForm } from './SettingsGroupForm'
import { SystemSection } from './SystemSection'
import { TokensSection } from './TokensSection'
import { UsersSection } from './UsersSection'

const APPEARANCE = 'appearance'
const USERS = 'users'
const TOKENS = 'api-tokens'
const SYSTEM = 'system'
const NOTIFICATIONS = 'notifications'

interface Section {
  id: string
  title: string
  /** Needed to see the section; the server enforces it on every request. */
  permission?: Permission
}

const groupSection = (id: string): Section => {
  const g = settingsGroups.find((s) => s.id === id)
  if (!g) throw new Error(`no settings group ${id}`)
  return { id: g.id, title: g.title, permission: 'settings.manage' }
}

const allSections: Section[] = [
  groupSection('general'),
  groupSection('sign-in'),
  { id: USERS, title: 'Users', permission: 'user.manage' },
  { id: TOKENS, title: 'API tokens' },
  groupSection('review-policy'),
  groupSection('limits'),
  groupSection(NOTIFICATIONS),
  { id: SYSTEM, title: 'System', permission: 'settings.manage' },
  { id: APPEARANCE, title: 'Appearance' },
]

/**
 * /settings/:section. A list of sections beside the selected section.
 * Members see only the sections they may use. Below 768px the list becomes a
 * row of links above the section.
 */
export function SettingsPage() {
  const { section } = useParams()
  const medium = useMediaQuery(MEDIUM, true)
  const me = useQuery(meQuery())
  const sectionTitle = allSections.find((s) => s.id === section)?.title
  useDocumentTitle(sectionTitle ? `${sectionTitle} · Settings` : 'Settings')

  if (me.isPending) {
    return (
      <Pane className="h-full" aria-busy="true" aria-label="Loading">
        <PageHeader as="h1" title="Settings" />
        <div className="flex flex-col gap-3 p-4">
          <Skeleton className="w-40" />
          <Skeleton className="w-32" />
        </div>
      </Pane>
    )
  }
  const sections = allSections.filter((s) => !s.permission || can(me.data, s.permission))
  const current = section === undefined ? sections[0] : sections.find((s) => s.id === section)
  if (!current) return <Navigate to="/settings" replace />

  const nav = (
    <nav aria-label="Settings sections">
      <ul className={cn(medium ? 'flex flex-col gap-0.5 p-2' : 'flex gap-1 overflow-x-auto p-2')}>
        {sections.map((s) => (
          <li key={s.id}>
            <NavLink
              to={`/settings/${s.id}`}
              aria-current={s.id === current.id ? 'page' : undefined}
              className={cn(
                'flex h-8 items-center rounded-md px-3 text-base whitespace-nowrap text-dim transition-colors duration-120 ease-out',
                'hover:bg-raise hover:text-text focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-green',
                s.id === current.id && 'bg-raise text-text',
              )}
            >
              {s.title}
            </NavLink>
          </li>
        ))}
      </ul>
    </nav>
  )

  return (
    <PaneGrid
      className={cn(
        'min-h-0',
        medium
          ? 'h-full grid-cols-[240px_minmax(0,1fr)]'
          : 'flex-1 grid-cols-1 grid-rows-[auto_1fr]',
      )}
    >
      <Pane aria-labelledby="settings-title" className="min-h-0">
        <PageHeader as="h1" title={<span id="settings-title">Settings</span>} />
        <div className="min-h-0 flex-1 overflow-y-auto">{nav}</div>
      </Pane>
      <Pane aria-labelledby="section-title" className="min-h-0">
        <SectionBody id={current.id} title={current.title} isAdmin={can(me.data, 'user.manage')} />
      </Pane>
    </PaneGrid>
  )
}

function SectionBody({ id, title, isAdmin }: { id: string; title: string; isAdmin: boolean }) {
  const header = <span id="section-title">{title}</span>
  switch (id) {
    case USERS:
      return <UsersSection title={header} />
    case TOKENS:
      return <TokensSection title={header} canSeeAll={isAdmin} />
    case SYSTEM:
      return (
        <>
          <PageHeader title={header} />
          <div className="min-h-0 flex-1 overflow-y-auto">
            <SystemSection />
          </div>
        </>
      )
    case APPEARANCE:
      return (
        <>
          <PageHeader title={header} />
          <div className="min-h-0 flex-1 overflow-y-auto">
            <AppearanceSection />
          </div>
        </>
      )
    default:
      return (
        <>
          <PageHeader title={header} />
          <div className="min-h-0 flex-1 overflow-y-auto">
            <GroupSection id={id} />
          </div>
        </>
      )
  }
}

function GroupSection({ id }: { id: string }) {
  const query = useQuery(settingsQuery())
  const group = settingsGroups.find((g) => g.id === id)
  if (!group) return null

  if (query.isPending) {
    return (
      <div className="flex max-w-xl flex-col gap-6 px-6 py-6" aria-busy="true" aria-label="Loading">
        <Skeleton className="w-3/4" />
        {group.fields.map((f) => (
          <div key={f.key} className="flex flex-col gap-2">
            <Skeleton className="w-32" />
            <Skeleton className="h-8 w-full" />
          </div>
        ))}
      </div>
    )
  }
  if (query.isError) {
    const err = query.error
    return (
      <ErrorState
        title="Settings could not be loaded."
        message={err.message}
        code={isApiError(err) ? err.code : undefined}
        action={<Button onClick={() => void query.refetch()}>Retry</Button>}
      />
    )
  }
  // Keyed by group so switching sections starts a fresh form.
  return (
    <>
      <SettingsGroupForm key={group.id} group={group} settings={query.data} />
      {id === NOTIFICATIONS && <NotificationTools settings={query.data} />}
    </>
  )
}

const themeOptions = [
  { value: 'system', label: 'System' },
  { value: 'dark', label: 'Dark' },
  { value: 'light', label: 'Light' },
] as const satisfies readonly { value: ThemePreference; label: string }[]

function AppearanceSection() {
  const { preference, setPreference } = useTheme()
  return (
    <div className="flex max-w-xl flex-col gap-5 px-6 py-6">
      <p className="text-base text-dim">Applies to this browser only.</p>
      <FormField label="Theme" help="System follows your operating system.">
        <Select
          className="w-60"
          options={themeOptions}
          value={preference}
          onValueChange={setPreference}
        />
      </FormField>
    </div>
  )
}
