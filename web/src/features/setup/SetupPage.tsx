import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, TriangleAlert } from 'lucide-react'
import { type ReactNode, type SyntheticEvent, useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import {
  isApiError,
  metaQuery,
  queryKeys,
  SETUP_GOOGLE_URL,
  type SetupSessionKind,
  setupSessionQuery,
  signInErrorMessage,
  verifySetupCode,
} from '@/api'
import { Button, CenteredPanel, cn, FormField, Input, Skeleton } from '@/ui'
import { SignInSettingsForm } from './SignInSettingsForm'

type Step = 'code' | 'settings' | 'finish'

const setupSteps: { id: Step; label: string }[] = [
  { id: 'code', label: 'Setup code' },
  { id: 'settings', label: 'Google sign-in' },
  { id: 'finish', label: 'Sign in' },
]

/**
 * /setup: the first-run wizard (setup code, Google sign-in settings, a real
 * sign-in that makes the first admin), and the recovery flow that reopens
 * only the sign-in settings.
 */
export function SetupPage() {
  const [params] = useSearchParams()
  const error = params.get('error')
  const meta = useQuery(metaQuery())
  const session = useQuery(setupSessionQuery())
  const [step, setStep] = useState<Exclude<Step, 'code'>>('settings')

  if (session.isPending || meta.isPending) {
    return (
      <CenteredPanel title="Set up dbairlock" wide>
        <div className="flex flex-col gap-3 px-4 py-6" aria-busy="true" aria-label="Loading">
          <Skeleton className="w-3/4" />
          <Skeleton className="h-8 w-full" />
        </div>
      </CenteredPanel>
    )
  }

  const kind: SetupSessionKind | undefined = session.data
  const banner = error ? <ErrorBanner code={error} /> : null

  if (kind === 'recovery') {
    return (
      <CenteredPanel title="Recover sign-in" wide>
        {banner}
        <p className="px-4 pt-5 text-base text-dim">
          This recovery link opens only the Google sign-in settings. Fix them, then confirm with a
          sign-in. It does not sign you in or grant any other access.
        </p>
        {step === 'settings' ? (
          <SignInSettingsForm submitLabel="Save and continue" onSaved={() => setStep('finish')} />
        ) : (
          <FinishStep
            onBack={() => setStep('settings')}
            button="Confirm with Google"
            text="Sign in with any allowed account to prove the settings work. Afterwards everyone signs in as usual."
          />
        )}
      </CenteredPanel>
    )
  }

  if (meta.data?.setupState === 'ready') {
    return (
      <CenteredPanel title="Set up dbairlock">
        {banner}
        <div className="flex flex-col gap-4 px-4 py-6">
          <p className="text-base text-dim">This instance is already set up.</p>
          <div>
            <Button variant="primary" nativeButton={false} render={<Link to="/signin" />}>
              Go to sign-in
            </Button>
          </div>
        </div>
      </CenteredPanel>
    )
  }

  const current: Step = kind === 'setup' ? step : 'code'
  return (
    <CenteredPanel title="Set up dbairlock" wide>
      <Steps current={current} />
      {banner}
      {current === 'code' && <CodeStep />}
      {current === 'settings' && (
        <SignInSettingsForm submitLabel="Save and continue" onSaved={() => setStep('finish')} />
      )}
      {current === 'finish' && (
        <FinishStep
          onBack={() => setStep('settings')}
          button="Sign in with Google to finish"
          text="The account you sign in with becomes the first admin. Setup ends only once this sign-in works, so a mistake in the settings cannot lock you out."
        />
      )}
    </CenteredPanel>
  )
}

function Steps({ current }: { current: Step }) {
  const index = setupSteps.findIndex((s) => s.id === current)
  return (
    <ol aria-label="Setup steps" className="flex border-b border-seam px-4 py-2.5">
      {setupSteps.map((s, i) => (
        <li
          key={s.id}
          aria-current={i === index ? 'step' : undefined}
          className={cn(
            'flex flex-1 items-center gap-2 text-xs',
            i === index ? 'text-text' : i < index ? 'text-green' : 'text-mute',
          )}
        >
          <span
            aria-hidden="true"
            className={cn(
              'grid size-5 shrink-0 place-items-center rounded-full border font-mono text-2xs',
              i === index && 'border-green text-green',
              i < index && 'border-green/35 bg-green/8',
              i > index && 'border-seam',
            )}
          >
            {i < index ? <Check className="size-3" /> : i + 1}
          </span>
          <span>{s.label}</span>
          {i < index && <span className="sr-only">(done)</span>}
        </li>
      ))}
    </ol>
  )
}

function ErrorBanner({ code }: { code: string }) {
  return (
    <div
      role="alert"
      className="mx-4 mt-4 flex items-start gap-2 rounded-md border border-red/35 bg-red/8 px-3 py-2.5 text-base text-red"
    >
      <TriangleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
      <div className="flex flex-col gap-1">
        <span>{signInErrorMessage(code)}</span>
        <span className="font-mono text-xs opacity-80">{code}</span>
      </div>
    </div>
  )
}

function CodeStep() {
  const queryClient = useQueryClient()
  const [code, setCode] = useState('')
  const [error, setError] = useState<string>()
  const verify = useMutation({
    mutationFn: () => verifySetupCode(code),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: queryKeys.setup.session() }),
    onError: (e) =>
      setError(isApiError(e) || e instanceof Error ? e.message : 'The code was not accepted.'),
  })
  const submit = (e: SyntheticEvent) => {
    e.preventDefault()
    if (code.trim() === '') {
      setError('Enter the setup code from the server log.')
      return
    }
    setError(undefined)
    verify.mutate()
  }
  return (
    <form noValidate onSubmit={submit} className="flex flex-col">
      <div className="flex flex-col gap-4 px-4 py-5">
        <p className="text-base text-dim">
          dbairlock printed a one-time setup code in its log when it started, in a block marked{' '}
          <span className="font-mono text-text">dbairlock setup</span>. Only someone with access to
          the server can read it. A new code is printed on every restart.
        </p>
        <FormField label="Setup code" name="code" error={error}>
          <Input
            mono
            name="code"
            value={code}
            autoFocus
            autoComplete="one-time-code"
            autoCapitalize="characters"
            spellCheck={false}
            placeholder="XXXXX-XXXXX-XXXXX-XXXXX"
            onChange={(e) => setCode(e.currentTarget.value)}
            className="text-base tracking-[0.06em]"
          />
        </FormField>
      </div>
      <div className="flex items-center gap-3 border-t border-seam px-4 py-3">
        <Button type="submit" variant="primary" loading={verify.isPending}>
          Continue
        </Button>
      </div>
    </form>
  )
}

function FinishStep({
  onBack,
  button,
  text,
}: {
  onBack: () => void
  button: ReactNode
  text: ReactNode
}) {
  return (
    <div className="flex flex-col">
      <div className="flex flex-col gap-3 px-4 py-5">
        <p className="flex items-center gap-2 text-base text-green">
          <Check aria-hidden="true" className="size-4" />
          Sign-in settings saved.
        </p>
        <p className="text-base text-dim">{text}</p>
      </div>
      <div className="flex flex-wrap items-center gap-3 border-t border-seam px-4 py-3">
        <Button variant="primary" nativeButton={false} render={<a href={SETUP_GOOGLE_URL} />}>
          {button}
        </Button>
        <Button variant="ghost" onClick={onBack}>
          Back to settings
        </Button>
      </div>
    </div>
  )
}
