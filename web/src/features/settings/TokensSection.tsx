import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, TriangleAlert } from 'lucide-react'
import { type ReactNode, type SyntheticEvent, useState } from 'react'
import { Link } from 'react-router'
import {
  type ApiToken,
  createToken,
  isApiError,
  type OAuthGrant,
  oauthGrantsQuery,
  queryKeys,
  revokeOAuthGrant,
  revokeToken,
  type TokenCreated,
  type TokenExpiryDays,
  tokensQuery,
} from '@/api'
import { claudeCodeCommand, genericConfig } from '@/api/mcp'
import {
  Button,
  CodeBlock,
  Dialog,
  DialogClose,
  EmptyState,
  ErrorState,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  Switch,
  Table,
  TBody,
  Td,
  Th,
  THead,
  toast,
  Tr,
} from '@/ui'
import { formatDate, formatTime } from './format'

type Expiry = '30' | '90' | '365' | 'never'

const expiryOptions = [
  { value: '30', label: '30 days' },
  { value: '90', label: '90 days' },
  { value: '365', label: '1 year' },
  { value: 'never', label: 'No expiry' },
] as const satisfies readonly { value: Expiry; label: string }[]

/** Settings → API tokens: personal access tokens for agents. */
export function TokensSection({ title, canSeeAll }: { title: ReactNode; canSeeAll: boolean }) {
  const [all, setAll] = useState(false)
  const query = useQuery(tokensQuery(all && canSeeAll))
  const tokens = query.data ?? []

  return (
    <>
      <PageHeader title={title} actions={<CreateTokenDialog />} />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="flex flex-wrap items-center justify-between gap-3 px-6 pt-5 pb-3">
          <p className="max-w-xl text-base text-dim">
            Tokens let your agents use dbairlock over MCP as you. Changes they submit are authored
            by you and still need another person&apos;s review. Agents that support OAuth, such as
            Claude Code, can sign in with your browser instead; they are listed under Connected
            apps.
          </p>
          {canSeeAll && (
            <Switch label="Everyone's tokens and apps" checked={all} onCheckedChange={setAll} />
          )}
        </div>
        {query.isPending ? (
          <div className="flex flex-col gap-2 px-6 py-2" aria-busy="true" aria-label="Loading">
            <Skeleton className="h-8 w-full" />
          </div>
        ) : query.isError ? (
          <ErrorState
            title="Tokens could not be loaded."
            message={query.error.message}
            code={isApiError(query.error) ? query.error.code : undefined}
            action={<Button onClick={() => void query.refetch()}>Retry</Button>}
          />
        ) : tokens.length === 0 ? (
          <EmptyState
            title={all ? 'Nobody has an active token.' : 'You have no active tokens.'}
            description="Create one to connect an agent."
          />
        ) : (
          <Table aria-label="API tokens">
            <THead>
              <Tr>
                <Th className="pl-6">Name</Th>
                <Th>Token</Th>
                {all && <Th>Owner</Th>}
                <Th>Created</Th>
                <Th>Last used</Th>
                <Th>Expires</Th>
                <Th className="pr-6 text-right">
                  <span className="sr-only">Actions</span>
                </Th>
              </Tr>
            </THead>
            <TBody>
              {tokens.map((t) => (
                <TokenRow key={t.id} token={t} showOwner={all} />
              ))}
            </TBody>
          </Table>
        )}
        <ConnectedApps all={all && canSeeAll} />
      </div>
    </>
  )
}

/**
 * Agents signed in with OAuth instead of a token: one row per approval.
 * Disconnecting revokes the app's access and refresh tokens.
 */
function ConnectedApps({ all }: { all: boolean }) {
  const query = useQuery(oauthGrantsQuery(all))
  const grants = query.data ?? []
  return (
    <section aria-labelledby="connected-apps" className="mt-6 border-t border-seam">
      <div className="flex flex-col gap-1 px-6 pt-5 pb-3">
        <h3 id="connected-apps" className="text-base font-medium small-caps text-dim">
          Connected apps
        </h3>
        <p className="max-w-xl text-base text-dim">
          Agents you signed in with your browser, for example Claude Code through{' '}
          <code className="font-mono">/mcp</code>. They act as you, exactly like a token.
        </p>
      </div>
      {query.isPending ? (
        <div className="flex flex-col gap-2 px-6 py-2" aria-busy="true" aria-label="Loading">
          <Skeleton className="h-8 w-full" />
        </div>
      ) : query.isError ? (
        <ErrorState
          title="Connected apps could not be loaded."
          message={query.error.message}
          code={isApiError(query.error) ? query.error.code : undefined}
          action={<Button onClick={() => void query.refetch()}>Retry</Button>}
        />
      ) : grants.length === 0 ? (
        <EmptyState
          title={all ? 'Nobody has a connected app.' : 'You have no connected apps.'}
          description="Add dbairlock to your agent without a token and sign in when it asks."
        />
      ) : (
        <Table aria-label="Connected apps">
          <THead>
            <Tr>
              <Th className="pl-6">App</Th>
              <Th>Sends sign-in to</Th>
              {all && <Th>Owner</Th>}
              <Th>Connected</Th>
              <Th>Last used</Th>
              <Th className="pr-6 text-right">
                <span className="sr-only">Actions</span>
              </Th>
            </Tr>
          </THead>
          <TBody>
            {grants.map((g) => (
              <GrantRow key={g.id} grant={g} showOwner={all} />
            ))}
          </TBody>
        </Table>
      )}
    </section>
  )
}

function GrantRow({ grant, showOwner }: { grant: OAuthGrant; showOwner: boolean }) {
  const queryClient = useQueryClient()
  const [open, setOpen] = useState(false)
  const revoke = useMutation({
    mutationFn: () => revokeOAuthGrant(grant.id),
    onSuccess: async () => {
      setOpen(false)
      await queryClient.invalidateQueries({ queryKey: queryKeys.oauthGrantsAll })
      toast.success(`"${grant.client_name}" was disconnected.`)
    },
    onError: (e) =>
      toast.error(e instanceof Error ? e.message : 'The app could not be disconnected.'),
  })
  let host = grant.redirect_uri
  try {
    host = new URL(grant.redirect_uri).host
  } catch {
    // keep the URI as it is
  }
  return (
    <Tr>
      <Td className="pl-6 font-sans text-base text-text">{grant.client_name}</Td>
      <Td>{host}</Td>
      {showOwner && <Td>{grant.owner_email}</Td>}
      <Td>{formatDate(grant.created_at)}</Td>
      <Td>{formatTime(grant.last_used_at, 'Never')}</Td>
      <Td className="pr-6 text-right">
        <Dialog
          open={open}
          onOpenChange={setOpen}
          trigger={
            <Button size="sm" variant="destructive">
              Disconnect
            </Button>
          }
          title={`Disconnect "${grant.client_name}"?`}
          description="The app is refused from its next request. To use it again, sign in from the app again."
          footer={
            <>
              <DialogClose render={<Button>Cancel</Button>} />
              <Button
                variant="destructive"
                className="ml-auto"
                loading={revoke.isPending}
                onClick={() => revoke.mutate()}
              >
                Disconnect app
              </Button>
            </>
          }
        />
      </Td>
    </Tr>
  )
}

function TokenRow({ token, showOwner }: { token: ApiToken; showOwner: boolean }) {
  const queryClient = useQueryClient()
  const [open, setOpen] = useState(false)
  const revoke = useMutation({
    mutationFn: () => revokeToken(token.id),
    onSuccess: async () => {
      setOpen(false)
      await queryClient.invalidateQueries({ queryKey: queryKeys.tokensAll })
      toast.success(`Token "${token.name}" was revoked.`)
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : 'The token could not be revoked.'),
  })
  return (
    <Tr>
      <Td className="pl-6 font-sans text-base text-text">{token.name}</Td>
      <Td>{token.prefix}…</Td>
      {showOwner && <Td>{token.owner_email}</Td>}
      <Td>{formatDate(token.created_at)}</Td>
      <Td>{formatTime(token.last_used_at, 'Never')}</Td>
      <Td>{formatDate(token.expires_at, 'Never')}</Td>
      <Td className="pr-6 text-right">
        <Dialog
          open={open}
          onOpenChange={setOpen}
          trigger={
            <Button size="sm" variant="destructive">
              Revoke
            </Button>
          }
          title={`Revoke "${token.name}"?`}
          description="Agents using it are refused from their next request. This cannot be undone."
          footer={
            <>
              <DialogClose render={<Button>Cancel</Button>} />
              <Button
                variant="destructive"
                className="ml-auto"
                loading={revoke.isPending}
                onClick={() => revoke.mutate()}
              >
                Revoke token
              </Button>
            </>
          }
        />
      </Td>
    </Tr>
  )
}

function CreateTokenDialog() {
  const queryClient = useQueryClient()
  const [open, setOpen] = useState(false)
  const [name, setName] = useState('')
  const [expiry, setExpiry] = useState<Expiry>('90')
  const [error, setError] = useState<string>()
  const [created, setCreated] = useState<TokenCreated>()

  const create = useMutation({
    mutationFn: () =>
      createToken(name.trim(), expiry === 'never' ? null : (Number(expiry) as TokenExpiryDays)),
    onSuccess: async (result) => {
      setCreated(result)
      await queryClient.invalidateQueries({ queryKey: queryKeys.tokensAll })
    },
    onError: (e) => setError(e instanceof Error ? e.message : 'The token could not be created.'),
  })

  const reset = (next: boolean) => {
    setOpen(next)
    if (!next) {
      // Forget the token: it is shown once.
      setCreated(undefined)
      setName('')
      setExpiry('90')
      setError(undefined)
      create.reset()
    }
  }

  const submit = (e: SyntheticEvent) => {
    e.preventDefault()
    if (name.trim() === '') {
      setError('Enter a name, for example the agent or machine it is for.')
      return
    }
    setError(undefined)
    create.mutate()
  }

  return (
    <Dialog
      open={open}
      onOpenChange={reset}
      className="w-[560px]"
      trigger={
        <Button size="sm" variant="primary" icon={<Plus aria-hidden="true" className="size-3.5" />}>
          Create token
        </Button>
      }
      title={created ? `Token "${created.token.name}" created` : 'Create an API token'}
      description={
        created
          ? undefined
          : 'The token acts as you. Give it a name you will recognise when revoking it.'
      }
      footer={
        created ? (
          <Button variant="primary" className="ml-auto" onClick={() => reset(false)}>
            Done
          </Button>
        ) : (
          <>
            <DialogClose render={<Button>Cancel</Button>} />
            <Button
              variant="primary"
              className="ml-auto"
              type="submit"
              form="create-token"
              loading={create.isPending}
            >
              Create token
            </Button>
          </>
        )
      }
    >
      {created ? (
        <div className="flex flex-col gap-4">
          <div
            role="note"
            className="flex items-start gap-2 rounded-md border border-amber/35 bg-amber/8 px-3 py-2.5 text-base text-amber"
          >
            <TriangleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
            <span>Copy the token now. It is shown only once and cannot be retrieved later.</span>
          </div>
          <div className="flex flex-col gap-1.5">
            <span className="text-base font-medium text-text">Token</span>
            <CodeBlock caption="dbal_pat" copyLabel="Copy token">
              {created.secret}
            </CodeBlock>
          </div>
          <div className="flex flex-col gap-1.5">
            <span className="text-base font-medium text-text">Claude Code</span>
            <CodeBlock caption="Terminal" copyLabel="Copy command">
              {claudeCodeCommand(created.mcp_url, created.secret)}
            </CodeBlock>
          </div>
          <div className="flex flex-col gap-1.5">
            <span className="text-base font-medium text-text">Other MCP clients</span>
            <CodeBlock caption=".mcp.json" copyLabel="Copy configuration">
              {genericConfig(created.mcp_url, created.secret)}
            </CodeBlock>
            <span className="text-xs text-mute">
              Paste it into your agent&apos;s MCP settings, for example .mcp.json.{' '}
              <Link className="text-blue underline" to="/agents">
                More about connecting agents
              </Link>
            </span>
          </div>
        </div>
      ) : (
        <form id="create-token" noValidate onSubmit={submit} className="flex flex-col gap-4">
          <FormField label="Name" error={error} name="name">
            <Input
              name="name"
              value={name}
              maxLength={64}
              autoComplete="off"
              placeholder="claude-code on my laptop"
              onChange={(e) => setName(e.currentTarget.value)}
            />
          </FormField>
          <FormField label="Expires after">
            <Select options={expiryOptions} value={expiry} onValueChange={setExpiry} />
          </FormField>
        </form>
      )}
    </Dialog>
  )
}
