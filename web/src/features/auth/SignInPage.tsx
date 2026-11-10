import { Button, CenteredPanel } from '@/ui'

/** /signin. Google sign-in arrives in phase 4. */
export function SignInPage() {
  return (
    <CenteredPanel title="Sign in">
      <div className="flex flex-col gap-4 px-4 py-6">
        <p className="text-base text-dim">
          dbairlock uses your company Google account. Sign-in becomes available once an
          administrator finishes setup.
        </p>
        <div className="flex items-center gap-3">
          <Button variant="primary" disabled>
            Sign in with Google
          </Button>
          <span className="text-xs text-mute">Not configured yet</span>
        </div>
      </div>
    </CenteredPanel>
  )
}
