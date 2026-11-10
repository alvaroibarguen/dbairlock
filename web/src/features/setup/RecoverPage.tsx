import { useQueryClient } from '@tanstack/react-query'
import { TriangleAlert } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { isApiError, queryKeys, verifyRecoveryCode } from '@/api'
import { Button, CenteredPanel, Skeleton } from '@/ui'

/** Reads the one-time code from the link's fragment, which never reaches a server log. */
function codeFromHash(): string {
  return new URLSearchParams(window.location.hash.slice(1)).get('code') ?? ''
}

/**
 * /recover#code=...: the link printed by `dbairlock recover`. It trades the
 * code for a recovery session, then continues in the setup page, which
 * shows only the sign-in settings.
 */
export function RecoverPage() {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [code] = useState(codeFromHash)
  const [error, setError] = useState<string | undefined>(() =>
    code === '' ? 'This link has no recovery code.' : undefined,
  )
  const started = useRef(false)

  useEffect(() => {
    // Remove the code from the address bar and history straight away.
    window.history.replaceState(null, '', window.location.pathname)
    if (started.current || code === '') return
    started.current = true
    verifyRecoveryCode(code).then(
      async () => {
        await queryClient.invalidateQueries({ queryKey: queryKeys.setup.session() })
        void navigate('/setup', { replace: true })
      },
      (e: unknown) => {
        setError(
          isApiError(e) && e.code === 'invalid_code'
            ? 'This recovery link is invalid, was already used, or has expired.'
            : e instanceof Error
              ? e.message
              : 'The recovery link could not be used.',
        )
      },
    )
  }, [code, navigate, queryClient])

  return (
    <CenteredPanel title="Recover sign-in">
      {error ? (
        <div className="flex flex-col gap-4 px-4 py-6">
          <div
            role="alert"
            className="flex items-start gap-2 rounded-md border border-red/35 bg-red/8 px-3 py-2.5 text-base text-red"
          >
            <TriangleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
            <span>{error}</span>
          </div>
          <p className="text-base text-dim">
            Run <span className="font-mono text-text">dbairlock recover</span> on the server for a
            new link. Each link works once, within 15 minutes.
          </p>
          <div>
            <Button nativeButton={false} render={<Link to="/signin" />}>
              Go to sign-in
            </Button>
          </div>
        </div>
      ) : (
        <div
          className="flex flex-col gap-3 px-4 py-6"
          aria-busy="true"
          aria-label="Checking the link"
        >
          <Skeleton className="w-3/4" />
        </div>
      )}
    </CenteredPanel>
  )
}
