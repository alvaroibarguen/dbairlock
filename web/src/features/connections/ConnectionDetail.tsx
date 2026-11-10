import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { type ReactNode, useState } from 'react'
import { useNavigate } from 'react-router'
import {
  archiveConnection,
  type Connection,
  connectionQuery,
  type Connector,
  isApiError,
  queryKeys,
} from '@/api'
import {
  Badge,
  Button,
  Dialog,
  DialogClose,
  ErrorState,
  PageHeader,
  Skeleton,
  Tabs,
  toast,
} from '@/ui'
import { ConnectionForm } from './ConnectionForm'
import { configFields, type PolicyDefaults, policySummary } from './connectionForm'
import { SchemaBrowser } from './SchemaBrowser'
import { dotClass, testStatus } from './status'
import { TestResultView } from './TestResultView'

export interface ConnectionDetailProps {
  id: string
  connectors: Connector[]
  defaults: PolicyDefaults
  canManage: boolean
  /** Shown before the title on narrow screens, to go back to the list. */
  back?: ReactNode
}

/** One connection: its settings (editable by admins) and its schema. */
export function ConnectionDetail({
  id,
  connectors,
  defaults,
  canManage,
  back,
}: ConnectionDetailProps) {
  const query = useQuery(connectionQuery(id))

  if (query.isPending) {
    return (
      <div className="flex flex-col gap-3 p-6" aria-busy="true" aria-label="Loading">
        <Skeleton className="h-6 w-48" />
        <Skeleton className="h-8 w-full" />
        <Skeleton className="h-8 w-full" />
      </div>
    )
  }
  if (query.isError) {
    return (
      <>
        <PageHeader
          title={
            <span className="flex items-center gap-2">
              {back}
              <span>Connection</span>
            </span>
          }
        />
        <ErrorState
          title="The connection could not be loaded."
          message={query.error.message}
          code={isApiError(query.error) ? query.error.code : undefined}
          action={<Button onClick={() => void query.refetch()}>Retry</Button>}
        />
      </>
    )
  }

  const c = query.data
  const connector = connectors.find((x) => x.id === c.connector)
  const status = testStatus(c.last_test)
  const archived = c.archived_at !== undefined

  return (
    <>
      <PageHeader
        as="h2"
        title={
          <span className="flex min-w-0 items-center gap-2">
            {back}
            <span
              id="detail-title"
              className="truncate font-mono tracking-normal text-text [font-variant:normal]"
            >
              {c.name}
            </span>
          </span>
        }
        actions={
          <>
            <span className="hidden items-center gap-1.5 text-xs text-dim sm:flex">
              <span
                aria-hidden="true"
                className={`inline-block size-[7px] rounded-full ${dotClass[status.tone]}`}
              />
              {status.label}
            </span>
            <Badge>{connector?.display_name ?? c.connector}</Badge>
            {archived && <Badge tone="neutral">Archived</Badge>}
          </>
        }
      />
      <Tabs
        aria-label="Connection"
        className="min-h-0 flex-1"
        items={[
          {
            value: 'settings',
            label: 'Settings',
            content:
              canManage && !archived ? (
                <ConnectionForm
                  key={c.id}
                  connectors={connectors}
                  defaults={defaults}
                  connection={c}
                  onSaved={() => undefined}
                  dangerZone={<ArchiveButton connection={c} />}
                />
              ) : (
                <Summary connection={c} connector={connector} />
              ),
          },
          {
            value: 'schema',
            label: 'Schema',
            content: archived ? (
              <p className="px-6 py-5 text-base text-dim">
                This connection is archived; its schema is no longer read.
              </p>
            ) : (
              <SchemaBrowser connectionId={c.id} />
            ),
          },
        ]}
      />
    </>
  )
}

/** Read-only details for people who may not change connections. */
function Summary({ connection: c, connector }: { connection: Connection; connector?: Connector }) {
  const rows: [string, string][] = [
    ['Database type', connector?.display_name ?? c.connector],
    ...configFields(connector).map((f): [string, string] => {
      const v = c.config[f.key]
      return [
        f.label,
        typeof v === 'string' || typeof v === 'number' || typeof v === 'boolean' ? String(v) : '—',
      ]
    }),
    ['Review policy', policySummary(c.policy)],
    ['Row images', c.policy.store_row_images ? 'Stored' : 'Not stored'],
    ['Execution', c.policy.auto_execute ? 'Automatic after the last approval' : 'Manual'],
    ['Allowed functions', c.policy.allowed_functions.join(', ') || 'None'],
  ]
  return (
    <div className="flex max-w-2xl flex-col gap-6 px-6 py-6">
      {c.description && <p className="text-base whitespace-pre-wrap text-text">{c.description}</p>}
      <dl className="grid grid-cols-1 gap-x-6 gap-y-2 sm:grid-cols-[180px_minmax(0,1fr)]">
        {rows.map(([k, v]) => (
          <div key={k} className="contents">
            <dt className="text-base text-dim">{k}</dt>
            <dd className="mb-2 font-mono text-sm break-words text-text sm:mb-0">{v}</dd>
          </div>
        ))}
      </dl>
      {c.archived_at && (
        <p className="text-base text-dim">
          Archived{c.archived_by ? ` by ${c.archived_by}` : ''}. Its history remains.
        </p>
      )}
      {c.last_test && <TestResultView result={c.last_test} />}
    </div>
  )
}

function ArchiveButton({ connection }: { connection: Connection }) {
  const [open, setOpen] = useState(false)
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const archive = useMutation({
    mutationFn: () => archiveConnection(connection.id),
    onSuccess: async (c) => {
      setOpen(false)
      queryClient.setQueryData(queryKeys.connections.detail(c.id), c)
      await queryClient.invalidateQueries({ queryKey: queryKeys.connections.list() })
      toast.success(`${c.name} was archived.`)
      void navigate('/connections')
    },
    onError: (e) =>
      toast.error(e instanceof Error ? e.message : 'The connection could not be archived.'),
  })
  return (
    <>
      <span className="hidden text-xs text-mute sm:inline">Hides it; history stays.</span>
      <Dialog
        open={open}
        onOpenChange={setOpen}
        trigger={<Button variant="destructive">Archive</Button>}
        title={`Archive ${connection.name}?`}
        description="Nobody can submit changes to it any more. Its changes and audit history remain."
        footer={
          <>
            <DialogClose render={<Button>Cancel</Button>} />
            <Button
              variant="destructive"
              className="ml-auto"
              loading={archive.isPending}
              onClick={() => archive.mutate()}
            >
              Archive connection
            </Button>
          </>
        }
      >
        <ul className="flex list-disc flex-col gap-1.5 pl-5 font-sans text-base text-dim">
          <li>It disappears from the list and from agents.</li>
          <li>Its password is deleted from the secret store.</li>
          <li>Its name becomes free for a new connection.</li>
        </ul>
      </Dialog>
    </>
  )
}
