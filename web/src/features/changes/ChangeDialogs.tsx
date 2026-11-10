import { type SyntheticEvent, useState } from 'react'
import { Button, Dialog, DialogClose, FormField, Textarea } from '@/ui'

/** Asks why before rejecting; the comment is shown to the author. */
export function RejectDialog({
  open,
  onOpenChange,
  pending,
  error,
  onReject,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  pending: boolean
  error?: string
  onReject: (comment: string) => void
}) {
  const [comment, setComment] = useState('')
  const [touched, setTouched] = useState(false)
  const missing = comment.trim() === ''
  const submit = (e: SyntheticEvent) => {
    e.preventDefault()
    setTouched(true)
    if (!missing) onReject(comment.trim())
  }
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title="Reject this change?"
      description="The change cannot run after it is rejected. The author sees your comment and can submit a new change."
      footer={
        <>
          <DialogClose render={<Button>Keep in review</Button>} />
          <Button variant="destructive" type="submit" form="reject-form" loading={pending}>
            Reject change
          </Button>
        </>
      }
    >
      <form id="reject-form" onSubmit={submit} className="flex flex-col gap-3">
        <FormField
          label="Why are you rejecting it?"
          error={(touched && missing ? 'Say why, so the author can fix it.' : undefined) ?? error}
        >
          <Textarea
            value={comment}
            onChange={(e) => setComment(e.target.value)}
            rows={4}
            maxLength={4000}
            autoFocus
          />
        </FormField>
      </form>
    </Dialog>
  )
}

/** Confirms cancelling; a cancelled change never runs. */
export function CancelDialog({
  open,
  onOpenChange,
  pending,
  onCancel,
  number,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  pending: boolean
  onCancel: (reason: string) => void
  number: number
}) {
  const [reason, setReason] = useState('')
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={`Cancel change #${number}?`}
      description="A cancelled change never runs and cannot be reopened. Submit a new change to try again."
      footer={
        <>
          <DialogClose render={<Button>Keep it</Button>} />
          <Button variant="destructive" loading={pending} onClick={() => onCancel(reason.trim())}>
            Cancel change
          </Button>
        </>
      }
    >
      <FormField label="Reason (optional)">
        <Textarea
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          rows={3}
          maxLength={4000}
        />
      </FormField>
    </Dialog>
  )
}
