import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ExternalLink } from 'lucide-react'
import { type SyntheticEvent, useState } from 'react'
import {
  isApiError,
  queryKeys,
  saveSetupSignIn,
  type SetupSignIn,
  type SetupSignInUpdate,
  setupSignInQuery,
} from '@/api'
import {
  Button,
  CopyButton,
  ErrorState,
  FormField,
  Input,
  SecretField,
  type SecretChange,
  Skeleton,
  TagInput,
} from '@/ui'

const CALLBACK_PATH = '/api/auth/google/callback'
const GOOGLE_HELP =
  'https://developers.google.com/identity/protocols/oauth2/web-server#creatingcred'

type Field = 'public_url' | 'client_id' | 'client_secret' | 'allowed_domains' | 'form'

/** Server field names (settings keys) to this form's fields. */
const serverFields: Record<string, Field> = {
  'instance.public_url': 'public_url',
  'auth.google.client_id': 'client_id',
  'auth.google.client_secret': 'client_secret',
  'auth.allowed_domains': 'allowed_domains',
}

function redirectUri(publicUrl: string): string {
  const base = publicUrl.trim().replace(/\/+$/, '')
  return base === '' ? '' : base + CALLBACK_PATH
}

/**
 * The Google sign-in settings, used by the setup wizard and by recovery.
 * Shows the redirect URI to register with Google as it will be, and saves
 * all fields in one atomic update.
 */
export function SignInSettingsForm({
  onSaved,
  submitLabel,
}: {
  onSaved: (saved: SetupSignIn) => void
  submitLabel: string
}) {
  const query = useQuery(setupSignInQuery())
  if (query.isPending) {
    return (
      <div className="flex flex-col gap-4 px-4 py-6" aria-busy="true" aria-label="Loading">
        <Skeleton className="h-8 w-full" />
        <Skeleton className="h-8 w-full" />
        <Skeleton className="h-8 w-full" />
      </div>
    )
  }
  if (query.isError) {
    return (
      <ErrorState
        title="The sign-in settings could not be loaded."
        message={query.error.message}
        code={isApiError(query.error) ? query.error.code : undefined}
        action={<Button onClick={() => void query.refetch()}>Retry</Button>}
      />
    )
  }
  return <Form initial={query.data} onSaved={onSaved} submitLabel={submitLabel} />
}

function Form({
  initial,
  onSaved,
  submitLabel,
}: {
  initial: SetupSignIn
  onSaved: (saved: SetupSignIn) => void
  submitLabel: string
}) {
  const queryClient = useQueryClient()
  const [publicUrl, setPublicUrl] = useState(initial.public_url || initial.suggested_public_url)
  const [clientId, setClientId] = useState(initial.client_id)
  const [secret, setSecret] = useState<SecretChange>(initial.client_secret.is_set ? undefined : '')
  const [domains, setDomains] = useState<string[]>(initial.allowed_domains)
  const [errors, setErrors] = useState<Partial<Record<Field, string>>>({})

  const save = useMutation({
    mutationFn: (body: SetupSignInUpdate) => saveSetupSignIn(body),
    onSuccess: (saved) => {
      queryClient.setQueryData(queryKeys.setup.signIn(), saved)
      onSaved(saved)
    },
    onError: (error) => {
      if (isApiError(error) && error.fields.length > 0) {
        const next: Partial<Record<Field, string>> = {}
        for (const f of error.fields) next[serverFields[f.field] ?? 'form'] = f.message
        setErrors(next)
        return
      }
      setErrors({ form: error instanceof Error ? error.message : 'The settings were not saved.' })
    },
  })

  const submit = (e: SyntheticEvent) => {
    e.preventDefault()
    const next: Partial<Record<Field, string>> = {}
    if (publicUrl.trim() === '') next.public_url = 'Enter the URL people use to open dbairlock.'
    if (clientId.trim() === '')
      next.client_id = 'Enter the client ID from the Google Cloud console.'
    if (secret === '' || (secret === undefined && !initial.client_secret.is_set)) {
      next.client_secret = 'Enter the client secret.'
    }
    if (secret === null) {
      next.client_secret = 'Sign-in needs a client secret. Replace it instead of clearing it.'
    }
    if (domains.length === 0) next.allowed_domains = 'Enter at least one domain.'
    setErrors(next)
    if (Object.keys(next).length > 0) return
    const body: SetupSignInUpdate = {
      public_url: publicUrl.trim(),
      client_id: clientId.trim(),
      allowed_domains: domains,
    }
    if (typeof secret === 'string') body.client_secret = secret
    save.mutate(body)
  }

  const uri = redirectUri(publicUrl)

  return (
    <form noValidate onSubmit={submit} className="flex flex-col">
      <div className="flex flex-col gap-5 px-4 py-5">
        <FormField
          label="Public URL"
          name="public_url"
          error={errors.public_url}
          help="The address people use to open dbairlock. Filled in from this page's address; check it before saving."
        >
          <Input
            mono
            type="url"
            name="public_url"
            value={publicUrl}
            spellCheck={false}
            placeholder="https://dbairlock.example.com"
            onChange={(e) => setPublicUrl(e.currentTarget.value)}
          />
        </FormField>

        <div className="flex flex-col gap-1.5">
          <span className="text-base font-medium text-text">Redirect URI</span>
          <div className="flex items-center gap-2 rounded-md border border-seam bg-bg py-1 pr-1 pl-2.5">
            <code className="min-w-0 flex-1 truncate font-mono text-sm text-text" title={uri}>
              {uri || 'Enter the public URL first.'}
            </code>
            {uri && <CopyButton text={uri} label="Copy URI" />}
          </div>
          <p className="text-xs text-mute">
            In the Google Cloud console, create an OAuth client of type &quot;Web application&quot;
            and add this as an authorized redirect URI.{' '}
            <a
              href={GOOGLE_HELP}
              target="_blank"
              rel="noreferrer noopener"
              className="inline-flex items-center gap-1 text-blue underline-offset-2 hover:underline"
            >
              Google&apos;s instructions
              <ExternalLink aria-hidden="true" className="size-3" />
            </a>
          </p>
        </div>

        <FormField label="Google client ID" name="client_id" error={errors.client_id}>
          <Input
            mono
            name="client_id"
            value={clientId}
            spellCheck={false}
            autoComplete="off"
            placeholder="1234567890-abc.apps.googleusercontent.com"
            onChange={(e) => setClientId(e.currentTarget.value)}
          />
        </FormField>

        <SecretField
          label="Google client secret"
          name="client_secret"
          status={{
            isSet: initial.client_secret.is_set,
            setBy: initial.client_secret.set_by,
            setAt: initial.client_secret.set_at,
          }}
          value={secret}
          onChange={setSecret}
          error={errors.client_secret}
          help="Write-only. Once saved it cannot be viewed again, only replaced."
        />

        <FormField
          label="Allowed domains"
          name="allowed_domains"
          error={errors.allowed_domains}
          help="Google Workspace domains whose accounts may sign in. Press Enter after each."
        >
          <TagInput
            mono
            name="allowed_domains"
            value={domains}
            normalize={(s) => s.trim().toLowerCase()}
            placeholder="example.com"
            onChange={setDomains}
          />
        </FormField>
      </div>
      <div className="flex flex-wrap items-center gap-3 border-t border-seam px-4 py-3">
        <Button type="submit" variant="primary" loading={save.isPending}>
          {submitLabel}
        </Button>
        {errors.form && (
          <p role="alert" className="text-base text-red">
            {errors.form}
          </p>
        )}
      </div>
    </form>
  )
}
