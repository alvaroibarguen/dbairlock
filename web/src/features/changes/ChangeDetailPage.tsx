import { Link, useParams } from 'react-router'
import { Button, EmptyState } from '@/ui'
import { ChangesLayout } from './ChangesLayout'

/** /changes/:id. Placeholder until the changes API exists (phase 7). */
export function ChangeDetailPage() {
  const { id = '' } = useParams()
  return (
    <ChangesLayout
      queue={<EmptyState title="No changes are waiting for review." />}
      detail={
        <div className="flex h-full flex-col">
          <div className="border-b border-seam px-6 pt-4.5 pb-4">
            <p className="font-mono text-xs text-mute">{id}</p>
          </div>
          <EmptyState
            className="flex-1 justify-center"
            title="This change cannot be shown yet."
            description="Change review arrives with the changes API."
            action={
              <Button render={<Link to="/changes" />} nativeButton={false}>
                Back to the queue
              </Button>
            }
          />
        </div>
      }
      rows={<EmptyState title="No affected rows to show." />}
    />
  )
}
