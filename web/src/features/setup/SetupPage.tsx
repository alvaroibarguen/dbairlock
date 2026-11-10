import { CenteredPanel, EmptyState } from '@/ui'

/** /setup. The first-run wizard arrives in phase 4. */
export function SetupPage() {
  return (
    <CenteredPanel title="Set up dbairlock">
      <EmptyState
        className="py-8"
        title="The setup wizard is not available in this build yet."
        description="It will ask for the one-time setup code printed in the server log."
      />
    </CenteredPanel>
  )
}
