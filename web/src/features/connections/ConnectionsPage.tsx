import { useQuery } from '@tanstack/react-query'
import { ArrowLeft, Plus } from 'lucide-react'
import { Link, useMatch, useNavigate, useParams } from 'react-router'
import { can, connectionsQuery, connectorsQuery, isApiError, meQuery, settingsQuery } from '@/api'
import {
  Button,
  cn,
  EmptyState,
  ErrorState,
  MEDIUM,
  PageHeader,
  Pane,
  PaneGrid,
  Skeleton,
  useMediaQuery,
} from '@/ui'
import { ConnectionDetail } from './ConnectionDetail'
import { ConnectionForm } from './ConnectionForm'
import type { PolicyDefaults } from './connectionForm'
import { ConnectionList } from './ConnectionList'

/**
 * /connections, /connections/new and /connections/:id. A list of
 * connections grouped by connector beside the selected one. Below 768px the
 * list and the detail are separate screens.
 */
export function ConnectionsPage() {
  const { id } = useParams()
  const isNew = useMatch('/connections/new') !== null
  const selected = isNew ? undefined : id
  const medium = useMediaQuery(MEDIUM, true)
  const navigate = useNavigate()

  const me = useQuery(meQuery())
  const canManage = can(me.data, 'database.manage')
  const connectors = useQuery(connectorsQuery())
  const list = useQuery(connectionsQuery())
  // Admins see the instance defaults a new connection starts from.
  const settings = useQuery({ ...settingsQuery(), enabled: canManage })

  const defaults: PolicyDefaults = {
    allowed_functions: connectors.data?.default_allowed_functions ?? [],
    required_approvals: settings.data?.values['policy.default_required_approvals'],
    max_rows: settings.data?.values['policy.default_max_rows'],
    approval_ttl_hours: settings.data?.values['policy.default_approval_ttl_hours'],
    store_row_images: settings.data?.values['policy.default_store_row_images'],
    auto_execute: settings.data?.values['policy.default_auto_execute'],
  }
  const allConnectors = connectors.data?.connectors ?? []

  const addButton = canManage && (
    <Button
      size="sm"
      variant="primary"
      icon={<Plus aria-hidden="true" className="size-3.5" />}
      render={<Link to="/connections/new" />}
      nativeButton={false}
    >
      Add connection
    </Button>
  )

  const listBody =
    list.isPending || connectors.isPending ? (
      <div className="flex flex-col gap-2 p-4" aria-busy="true" aria-label="Loading">
        <Skeleton className="h-12 w-full" />
        <Skeleton className="h-12 w-full" />
      </div>
    ) : list.isError || connectors.isError ? (
      <ErrorState
        title="Connections could not be loaded."
        message={(list.error ?? connectors.error)?.message}
        code={
          isApiError(list.error)
            ? list.error.code
            : isApiError(connectors.error)
              ? connectors.error.code
              : undefined
        }
        action={
          <Button
            onClick={() => {
              void list.refetch()
              void connectors.refetch()
            }}
          >
            Retry
          </Button>
        }
      />
    ) : list.data.length === 0 ? (
      <EmptyState
        title="No databases are connected yet."
        description={
          canManage
            ? 'Add a connection so people and agents can submit changes to it.'
            : 'An admin has to add a connection before changes can be submitted.'
        }
        action={addButton || undefined}
      />
    ) : (
      <ConnectionList connections={list.data} connectors={allConnectors} selectedId={selected} />
    )

  const listPane = (
    <Pane aria-labelledby="connections-title" className="min-h-0">
      <PageHeader
        as="h1"
        title={<span id="connections-title">Connections</span>}
        actions={addButton}
      />
      <div className="min-h-0 flex-1 overflow-y-auto">{listBody}</div>
    </Pane>
  )

  const back = !medium && (
    <Button
      variant="ghost"
      size="icon"
      className="-ml-2 size-8 shrink-0"
      aria-label="Back to connections"
      render={<Link to="/connections" />}
      nativeButton={false}
      icon={<ArrowLeft aria-hidden="true" className="size-4" />}
    />
  )

  let detail = null
  if (isNew) {
    detail = !canManage ? (
      <EmptyState title="Only admins can add connections." />
    ) : connectors.isPending ? null : (
      <>
        <PageHeader
          title={
            <span className="flex items-center gap-2">
              {back}
              <span id="detail-title">Add a connection</span>
            </span>
          }
        />
        <div className="min-h-0 flex-1 overflow-y-auto">
          <ConnectionForm
            // Remount once the defaults arrive so the form starts from them.
            key={settings.isSuccess ? 'with-defaults' : 'loading-defaults'}
            connectors={allConnectors}
            defaults={defaults}
            onSaved={(c) => void navigate(`/connections/${c.id}`)}
            onCancel={() => void navigate('/connections')}
          />
        </div>
      </>
    )
  } else if (selected) {
    detail = (
      <ConnectionDetail
        key={selected}
        id={selected}
        connectors={allConnectors}
        defaults={defaults}
        canManage={canManage}
        back={back}
      />
    )
  }

  const detailPane = (
    <Pane
      aria-labelledby={detail ? 'detail-title' : undefined}
      aria-label={detail ? undefined : 'Connection'}
      className="min-h-0"
    >
      {detail ?? (
        <EmptyState
          className="flex-1 justify-center"
          title={
            list.data && list.data.length > 0
              ? 'Select a connection to see its settings and schema.'
              : 'Connections you add appear here.'
          }
        />
      )}
    </Pane>
  )

  if (medium) {
    return (
      <PaneGrid className="h-full min-h-0 grid-cols-[320px_minmax(0,1fr)]">
        {listPane}
        {detailPane}
      </PaneGrid>
    )
  }
  return (
    <PaneGrid className={cn('min-h-0 flex-1 grid-cols-1')}>
      {detail ? detailPane : listPane}
    </PaneGrid>
  )
}
