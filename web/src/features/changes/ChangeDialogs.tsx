import { type SyntheticEvent, useState } from 'react'
import { Button, Dialog, DialogClose, FormField, Textarea } from '@/ui'

/** Asks why before rejecting; the comment is shown to the author. */
export function RejectDialog({
  open,
  onOpenChange,
  pending,
  error,
  onReject,
  step,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  pending: boolean
  error?: string
  onReject: (comment: string) => void
  /** Set when the change is a step of a sequence (from 1). */
  step?: number
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
      description={
        step === undefined
          ? 'The change cannot run after it is rejected. The author sees your comment and can submit a new change.'
          : `This step cannot run after it is rejected, and the sequence stops: the steps after it are cancelled.${step > 1 ? ` Steps already run stay applied.` : ''} The author sees your comment.`
      }
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
  sequence,
  what = 'change',
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  pending: boolean
  onCancel: (reason: string) => void
  number: number
  /** Set when the change is a step of sequence #sequence. */
  sequence?: number
  /** What is cancelled: a change, or a whole sequence. */
  what?: 'change' | 'sequence'
}) {
  const [reason, setReason] = useState('')
  const description =
    what === 'sequence'
      ? 'The open step and every later step are cancelled and never run. Steps already run stay applied: nothing is rolled back.'
      : sequence !== undefined
        ? `A cancelled change never runs. It is a step of sequence #${sequence}, so the sequence stops too: its later steps are cancelled, and steps already run stay applied.`
        : 'A cancelled change never runs and cannot be reopened. Submit a new change to try again.'
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={`Cancel ${what} #${number}?`}
      description={description}
      footer={
        <>
          <DialogClose render={<Button>Keep it</Button>} />
          <Button variant="destructive" loading={pending} onClick={() => onCancel(reason.trim())}>
            Cancel {what}
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
