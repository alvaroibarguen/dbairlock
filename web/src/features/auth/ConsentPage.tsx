import { useMutation, useQuery } from '@tanstack/react-query'
import { Check, Laptop, TriangleAlert, X } from 'lucide-react'
import { type ReactNode, useState } from 'react'
import { useSearchParams } from 'react-router'
import { decideOAuthRequest, isApiError, meQuery, oauthRequestQuery } from '@/api'
import { Badge, Button, CenteredPanel, cn, Skeleton, useDocumentTitle } from '@/ui'

/** Messages for the errors /oauth/authorize sends here instead of to the client. */
const requestErrors: Record<string, string> = {
  unavailable:
    'Agent sign-in with OAuth is turned off on this instance, or its public URL is not set. Use an API token instead, or ask an admin.',
  invalid_client:
    'This app is not registered with dbairlock, or its registration expired. Start the sign-in again from your agent.',
  invalid_redirect_uri:
    'The app asked to send the sign-in to an address it did not register. Nothing was sent.',
  invalid_request: 'The sign-in request was malformed. Start it again from your agent.',
  rate_limited: 'Too many sign-in attempts. Wait a minute, then start again from your agent.',
}

/**
 * /oauth/consent: a person allows or denies an MCP client (an agent) that
 * wants to act as them. The authorization endpoint sends the browser here
 * with ?request=…, or with ?error=… when the request could not be trusted.
 * Signing in first is handled by the API client: a 401 goes to /signin and
 * comes back here.
 */
export function ConsentPage() {
  useDocumentTitle('Connect an agent')
  const [params] = useSearchParams()
  const id = params.get('request')
  const error = params.get('error')

  if (error || !id) {
    return (
      <CenteredPanel title="Connect an agent" wide>
        <div className="flex flex-col gap-4 px-4 py-6">
          <Alert>
            {(error && requestErrors[error]) ??
              'This sign-in request could not be processed. Start it again from your agent.'}
            {error && <span className="block font-mono text-xs">{error}</span>}
          </Alert>
        </div>
      </CenteredPanel>
    )
  }
  return <ConsentForm id={id} />
}

function ConsentForm({ id }: { id: string }) {
  const request = useQuery(oauthRequestQuery(id))
  const me = useQuery(meQuery())
  const [sentTo, setSentTo] = useState<{ approved: boolean; host: string }>()
  const decide = useMutation({
    mutationFn: (approve: boolean) => decideOAuthRequest(id, approve),
    onSuccess: (url, approve) => {
      setSentTo({ approved: approve, host: request.data?.redirect_host ?? '' })
      // The client's loopback address on this computer. It is the address
      // the client registered; the server checked it.
      window.location.assign(url)
    },
  })

  if (request.isPending) {
    return (
      <CenteredPanel title="Connect an agent" wide>
        <div className="flex flex-col gap-3 px-4 py-6" aria-busy="true" aria-label="Loading">
          <Skeleton className="w-3/4" />
          <Skeleton className="w-1/2" />
        </div>
      </CenteredPanel>
    )
  }
  if (request.isError) {
    const e = request.error
    const gone = isApiError(e) && e.status === 404
    return (
      <CenteredPanel title="Connect an agent" wide>
        <div className="flex flex-col gap-4 px-4 py-6">
          <Alert>
            {gone
              ? 'This sign-in request has expired or was already answered. Start it again from your agent, for example with /mcp in Claude Code.'
              : e.message}
            {isApiError(e) && <span className="block font-mono text-xs">{e.code}</span>}
          </Alert>
        </div>
      </CenteredPanel>
    )
  }
  const r = request.data

  if (sentTo) {
    return (
      <CenteredPanel title={sentTo.approved ? 'Agent connected' : 'Request denied'} wide>
        <div className="flex flex-col gap-2 px-4 py-6 text-base text-dim" role="status">
          <p>
            {sentTo.approved
              ? `Returning to ${r.client_name} at ${sentTo.host}. You can close this tab once it says it is signed in.`
              : `${r.client_name} was told you denied the request. You can close this tab.`}
          </p>
        </div>
      </CenteredPanel>
    )
  }

  return (
    <CenteredPanel title="Connect an agent" wide>
      <div className="flex flex-col gap-5 px-4 py-6">
        <div className="flex flex-col gap-2">
          <p className="flex flex-wrap items-center gap-2 text-lg text-text">
            <span className="font-medium break-all">{r.client_name}</span>
            <Badge tone="amber">Unverified name</Badge>
          </p>
          <p className="text-base text-dim">
            wants to use dbairlock as{' '}
            <span className="font-medium text-text">{me.data?.user.email ?? 'you'}</span>. The app
            chose its name itself; dbairlock cannot check it.
          </p>
        </div>

        <dl className="grid grid-cols-[120px_minmax(0,1fr)] gap-x-3 gap-y-2 rounded-md border border-seam bg-bg px-3 py-2.5 text-base">
          <dt className="text-mute">Sends sign-in to</dt>
          <dd className="flex items-center gap-1.5 font-mono text-text">
            <Laptop aria-hidden="true" className="size-3.5 shrink-0 text-mute" />
            <span className="break-all">{r.redirect_host}</span>
          </dd>
          <dt className="text-mute">Registered</dt>
          <dd className="font-mono text-dim tabular-nums">
            {new Date(r.client_registered_at).toLocaleString()}
          </dd>
        </dl>
        <p className="-mt-3 text-xs text-mute">
          {r.redirect_host} is an address on this computer: only an app running here receives the
          sign-in.
        </p>

        <div className="grid gap-4 sm:grid-cols-2">
          <Capabilities title="It will be able to" allowed items={can} />
          <Capabilities title="It will not be able to" allowed={false} items={cannot} />
        </div>

        <div
          role="note"
          className="flex items-start gap-2 rounded-md border border-amber/35 bg-amber/8 px-3 py-2.5 text-base text-amber"
        >
          <TriangleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
          <span>
            Allow only if you just started this sign-in from your agent, for example with /mcp in
            Claude Code. You can disconnect it any time in Settings → API tokens.
          </span>
        </div>

        {decide.isError && (
          <Alert>
            {decide.error.message}
            {isApiError(decide.error) && (
              <span className="block font-mono text-xs">{decide.error.code}</span>
            )}
          </Alert>
        )}

        <div className="flex flex-wrap items-center gap-2">
          <Button
            variant="primary"
            loading={decide.isPending && decide.variables}
            disabled={decide.isPending}
            onClick={() => decide.mutate(true)}
          >
            Allow
          </Button>
          <Button
            loading={decide.isPending && !decide.variables}
            disabled={decide.isPending}
            onClick={() => decide.mutate(false)}
          >
            Deny
          </Button>
        </div>
      </div>
    </CenteredPanel>
  )
}

/** Exactly what the MCP tools allow (docs/agents.md). */
const can = [
  'List connections and read their schemas: tables, columns, types and keys',
  'Preview and submit INSERT, UPDATE and DELETE changes for review, authored by you',
  'Follow and cancel the changes it submitted',
]
const cannot = [
  'Approve or run any change',
  'See row data: it gets row counts only',
  "See other people's changes",
]

function Capabilities({
  title,
  allowed,
  items,
}: {
  title: string
  allowed: boolean
  items: string[]
}) {
  const Icon = allowed ? Check : X
  return (
    <section className="flex flex-col gap-1.5">
      <h2 className="text-base font-medium small-caps text-dim">{title}</h2>
      <ul className="flex flex-col gap-1.5 text-base text-dim">
        {items.map((item) => (
          <li key={item} className="flex items-start gap-2">
            <Icon
              aria-hidden="true"
              className={cn('mt-0.5 size-3.5 shrink-0', allowed ? 'text-green' : 'text-mute')}
            />
            <span>{item}</span>
          </li>
        ))}
      </ul>
    </section>
  )
}

function Alert({ children }: { children: ReactNode }) {
  return (
    <div
      role="alert"
      className="flex items-start gap-2 rounded-md border border-red/35 bg-red/8 px-3 py-2.5 text-base text-red"
    >
      <TriangleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
      <div className="flex flex-col gap-1">{children}</div>
    </div>
  )
}
