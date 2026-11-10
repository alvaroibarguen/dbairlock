import { useQuery } from '@tanstack/react-query'
import { Navigate, NavLink, useParams } from 'react-router'
import { isApiError, settingsQuery } from '@/api'
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
  useMediaQuery,
  useTheme,
} from '@/ui'
import { settingsGroups } from './settingsForm'
import { SettingsGroupForm } from './SettingsGroupForm'

const APPEARANCE = 'appearance'

const sections = [
  ...settingsGroups.map((g) => ({ id: g.id, title: g.title })),
  { id: APPEARANCE, title: 'Appearance' },
]

/**
 * /settings/:section. A list of sections beside the selected section's form.
 * Below 768px the list becomes a row of links above the form.
 */
export function SettingsPage() {
  const { section = settingsGroups[0]?.id } = useParams()
  const medium = useMediaQuery(MEDIUM, true)
  const current = sections.find((s) => s.id === section)
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
        <PageHeader title={<span id="section-title">{current.title}</span>} />
        <div className="min-h-0 flex-1 overflow-y-auto">
          {current.id === APPEARANCE ? <AppearanceSection /> : <GroupSection id={current.id} />}
        </div>
      </Pane>
    </PaneGrid>
  )
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
  return <SettingsGroupForm key={group.id} group={group} settings={query.data} />
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
