import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { type ReactNode, useState } from 'react'
import {
  isApiError,
  meQuery,
  queryKeys,
  removeUser,
  restoreUser,
  type Role,
  setUserRole,
  type User,
  usersQuery,
} from '@/api'
import {
  Badge,
  Button,
  cn,
  Dialog,
  DialogClose,
  EmptyState,
  ErrorState,
  PageHeader,
  Select,
  Skeleton,
  Table,
  TBody,
  Td,
  Th,
  THead,
  toast,
  Tr,
} from '@/ui'
import { formatTime } from './format'

type Filter = 'active' | 'removed'

const roleOptions = [
  { value: 'member', label: 'Member' },
  { value: 'admin', label: 'Admin' },
] as const satisfies readonly { value: Role; label: string }[]

function errorMessage(error: unknown, fallback: string): string {
  return isApiError(error) || error instanceof Error ? error.message : fallback
}

/** Settings → Users: roles, removal (offboarding) and restore. Admin only. */
export function UsersSection({ title }: { title: ReactNode }) {
  const [filter, setFilter] = useState<Filter>('active')
  const query = useQuery(usersQuery())
  const me = useQuery(meQuery())
  const users = query.data ?? []
  const shown = users.filter((u) => (filter === 'removed') === (u.removed_at !== undefined))
  const removedCount = users.filter((u) => u.removed_at !== undefined).length

  return (
    <>
      <PageHeader
        title={title}
        actions={
          <div role="group" aria-label="Show" className="flex rounded-md border border-seam p-0.5">
            {(
              [
                ['active', `Active ${users.length - removedCount}`],
                ['removed', `Removed ${removedCount}`],
              ] as const
            ).map(([value, label]) => (
              <button
                key={value}
                type="button"
                aria-pressed={filter === value}
                onClick={() => setFilter(value)}
                className={cn(
                  'h-6 cursor-pointer rounded-[4px] px-2 font-sans text-xs text-dim transition-colors duration-120',
                  'hover:text-text focus-visible:outline-2 focus-visible:outline-green',
                  filter === value && 'bg-raise text-text',
                )}
              >
                <span className="tabular">{label}</span>
              </button>
            ))}
          </div>
        }
      />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <p className="px-6 pt-5 pb-3 text-base text-dim">
          Everyone who has signed in. Removing someone is how offboarding works: it signs them out,
          revokes their API tokens and connected apps and cancels their open changes.
        </p>
        {query.isPending ? (
          <div className="flex flex-col gap-2 px-6 py-2" aria-busy="true" aria-label="Loading">
            <Skeleton className="h-8 w-full" />
            <Skeleton className="h-8 w-full" />
          </div>
        ) : query.isError ? (
          <ErrorState
            title="Users could not be loaded."
            message={query.error.message}
            code={isApiError(query.error) ? query.error.code : undefined}
            action={<Button onClick={() => void query.refetch()}>Retry</Button>}
          />
        ) : shown.length === 0 ? (
          <EmptyState
            title={filter === 'removed' ? 'Nobody has been removed.' : 'Nobody has signed in yet.'}
          />
        ) : (
          <Table aria-label={filter === 'removed' ? 'Removed users' : 'Users'}>
            <THead>
              <Tr>
                <Th className="pl-6">User</Th>
                <Th>Role</Th>
                <Th>{filter === 'removed' ? 'Removed' : 'Last sign-in'}</Th>
                <Th className="pr-6 text-right">
                  <span className="sr-only">Actions</span>
                </Th>
              </Tr>
            </THead>
            <TBody>
              {shown.map((u) => (
                <UserRow key={u.id} user={u} isSelf={u.id === me.data?.user.id} />
              ))}
            </TBody>
          </Table>
        )}
      </div>
    </>
  )
}

function UserRow({ user, isSelf }: { user: User; isSelf: boolean }) {
  const queryClient = useQueryClient()
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [demoteOpen, setDemoteOpen] = useState(false)
  const done = async () => {
    await queryClient.invalidateQueries({ queryKey: queryKeys.users() })
    if (isSelf) await queryClient.invalidateQueries({ queryKey: queryKeys.me() })
  }
  const role = useMutation({
    mutationFn: (next: Role) => setUserRole(user.id, next),
    onSuccess: async (u) => {
      setDemoteOpen(false)
      await done()
      toast.success(`${u.email} is now ${u.role === 'admin' ? 'an admin' : 'a member'}.`)
    },
    onError: (e) => toast.error(errorMessage(e, 'The role could not be changed.')),
  })
  const remove = useMutation({
    mutationFn: () => removeUser(user.id),
    onSuccess: async (u) => {
      setConfirmOpen(false)
      await done()
      toast.success(`${u.email} was removed.`)
    },
    onError: (e) => toast.error(errorMessage(e, 'The user could not be removed.')),
  })
  const restore = useMutation({
    mutationFn: () => restoreUser(user.id),
    onSuccess: async (u) => {
      await done()
      toast.success(`${u.email} was restored as a member.`)
    },
    onError: (e) => toast.error(errorMessage(e, 'The user could not be restored.')),
  })
  const removed = user.removed_at !== undefined

  return (
    <Tr>
      <Td className="pl-6">
        <div className="flex flex-col py-1.5">
          <span className="font-sans text-base text-text">
            {user.name || user.email}
            {isSelf && <span className="ml-2 font-sans text-xs text-mute">you</span>}
          </span>
          <span className="text-xs text-mute">{user.email}</span>
        </div>
      </Td>
      <Td>
        {removed ? (
          <Badge>Removed</Badge>
        ) : (
          <>
            <Select
              aria-label={`Role of ${user.email}`}
              className="h-7 w-32 min-w-0"
              options={roleOptions}
              value={user.role}
              disabled={role.isPending}
              onValueChange={(next) => {
                if (next === user.role) return
                // Taking admin rights away is confirmed first; granting them is not.
                if (next === 'member') setDemoteOpen(true)
                else role.mutate(next)
              }}
            />
            <Dialog
              open={demoteOpen}
              onOpenChange={setDemoteOpen}
              title={isSelf ? 'Make yourself a member?' : `Make ${user.email} a member?`}
              description={
                isSelf
                  ? 'From now on you cannot change settings, users or connections, or act on other people’s changes. Only another admin can undo this.'
                  : 'From now on they cannot change settings, users or connections, or act on other people’s changes.'
              }
              footer={
                <>
                  <DialogClose render={<Button>Keep as admin</Button>} />
                  <Button
                    variant="destructive"
                    className="ml-auto"
                    loading={role.isPending}
                    onClick={() => role.mutate('member')}
                  >
                    Make member
                  </Button>
                </>
              }
            />
          </>
        )}
      </Td>
      <Td>
        {removed
          ? `${formatTime(user.removed_at)}${user.removed_by ? ` by ${user.removed_by}` : ''}`
          : formatTime(user.last_sign_in_at, 'Never')}
      </Td>
      <Td className="pr-6 text-right">
        {removed ? (
          <Button size="sm" loading={restore.isPending} onClick={() => restore.mutate()}>
            Restore
          </Button>
        ) : (
          <Dialog
            open={confirmOpen}
            onOpenChange={setConfirmOpen}
            trigger={
              <Button size="sm" variant="destructive">
                Remove
              </Button>
            }
            title={`Remove ${user.email}?`}
            description="They lose access at once. Their record and history stay."
            footer={
              <>
                <DialogClose render={<Button>Cancel</Button>} />
                <Button
                  variant="destructive"
                  className="ml-auto"
                  loading={remove.isPending}
                  onClick={() => remove.mutate()}
                >
                  Remove user
                </Button>
              </>
            }
          >
            <ul className="flex list-disc flex-col gap-1.5 pl-5 font-sans text-base text-dim">
              <li>Signs them out of every browser.</li>
              <li>
                Revokes all their API tokens and connected apps permanently. Restoring them does not
                bring them back.
              </li>
              <li>Cancels their open changes, and their approvals stop counting.</li>
              <li>Their Google account can no longer sign in until restored.</li>
            </ul>
          </Dialog>
        )}
      </Td>
    </Tr>
  )
}
