import { useQuery, useQueryClient } from '@tanstack/react-query'
import { CircleCheck, TriangleAlert } from 'lucide-react'
import { type SyntheticEvent, useState } from 'react'
import { Navigate, useNavigate, useSearchParams } from 'react-router'
import { devLogin, googleSignInUrl, metaQuery, type Role, signInErrorMessage } from '@/api'
import { Button, CenteredPanel, FormField, Input, Select, Skeleton } from '@/ui'

/** A relative path on this site to return to after signing in, or "/". */
function safeNext(next: string | null): string {
  if (!next || !next.startsWith('/') || next.startsWith('//') || next.includes('\\')) return '/'
  return next
}

/** /signin: one button that signs in with Google. */
export function SignInPage() {
  const [params] = useSearchParams()
  const meta = useQuery(metaQuery())
  const next = safeNext(params.get('next'))
  const error = params.get('error')
  const recovered = params.get('recovered') === '1'

  if (meta.data?.setupState === 'setup_required') return <Navigate to="/setup" replace />

  return (
    <CenteredPanel title="Sign in">
      <div className="flex flex-col gap-5 px-4 py-6">
        {meta.isPending ? (
          <Skeleton className="w-3/4" />
        ) : (
          <p className="text-base text-dim">
            Sign in to <span className="font-medium text-text">{meta.data?.instanceName}</span> with
            your company Google account.
          </p>
        )}
        {error && (
          <div
            role="alert"
            className="flex items-start gap-2 rounded-md border border-red/35 bg-red/8 px-3 py-2.5 text-base text-red"
          >
            <TriangleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
            <div className="flex flex-col gap-1">
              <span>{signInErrorMessage(error)}</span>
              <span className="font-mono text-xs opacity-80">{error}</span>
            </div>
          </div>
        )}
        {recovered && (
          <div
            role="status"
            className="flex items-start gap-2 rounded-md border border-green/35 bg-green/8 px-3 py-2.5 text-base text-green"
          >
            <CircleCheck aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
            <span>Sign-in settings confirmed. Everyone can sign in again.</span>
          </div>
        )}
        <div>
          <Button
            variant="primary"
            nativeButton={false}
            render={<a href={googleSignInUrl(next === '/' ? undefined : next)} />}
          >
            Sign in with Google
          </Button>
        </div>
        {meta.data?.devLogin && <DevLoginForm next={next} />}
      </div>
    </CenteredPanel>
  )
}

const roleOptions = [
  { value: 'admin', label: 'Admin' },
  { value: 'member', label: 'Member' },
] as const satisfies readonly { value: Role; label: string }[]

/** Development only (--dev on loopback): sign in as anyone. */
function DevLoginForm({ next }: { next: string }) {
  const [email, setEmail] = useState('admin@dev.localhost')
  const [role, setRole] = useState<Role>('admin')
  const [error, setError] = useState<string>()
  const [pending, setPending] = useState(false)
  const navigate = useNavigate()
  const queryClient = useQueryClient()

  const submit = async (e: SyntheticEvent) => {
    e.preventDefault()
    setPending(true)
    try {
      await devLogin(email, role)
      queryClient.clear()
      void navigate(next, { replace: true })
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Dev sign-in failed.')
    } finally {
      setPending(false)
    }
  }

  return (
    <form
      onSubmit={(e) => void submit(e)}
      className="flex flex-col gap-3 border-t border-seam pt-5"
      aria-label="Development sign-in"
    >
      <p className="text-xs font-medium small-caps text-amber">Development mode</p>
      <div className="grid grid-cols-[1fr_auto] items-end gap-2">
        <FormField label="Email" error={error} name="email">
          <Input
            mono
            name="email"
            value={email}
            onChange={(e) => setEmail(e.currentTarget.value)}
          />
        </FormField>
        <FormField label="Role">
          <Select
            className="w-32 min-w-0"
            options={roleOptions}
            value={role}
            onValueChange={setRole}
          />
        </FormField>
      </div>
      <div>
        <Button type="submit" loading={pending}>
          Sign in as {role}
        </Button>
      </div>
    </form>
  )
}
