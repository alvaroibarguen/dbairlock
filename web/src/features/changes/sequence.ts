import type { ChangeSummary, Sequence, SequenceRef } from '@/api'
import { changeRef } from './format'

/** The honest summary every sequence screen repeats. */
export const SEQUENCE_RULE =
  'Steps run in order. Each step is reviewed and runs on its own database; a later step starts only after the earlier one has run. Steps already run are not undone if a later one fails.'

/** How a step's position in its sequence is named in lists: "step 2/3". */
export function stepLabel(ref: Pick<SequenceRef, 'step' | 'steps'>): string {
  return `step ${ref.step}/${ref.steps}`
}

export type StepTone = 'green' | 'amber' | 'red' | 'mute'

/**
 * The stepper's dot: executed green, the open step amber, failed or
 * rejected red; waiting, cancelled and expired steps mute.
 */
export function stepTone(status: ChangeSummary['status'], current: boolean): StepTone {
  switch (status) {
    case 'executed':
      return 'green'
    case 'failed':
    case 'rejected':
      return 'red'
    case 'pending_review':
    case 'approved':
    case 'executing':
      return current ? 'amber' : 'mute'
    default:
      return 'mute'
  }
}

/** Where one step stands, in words. `index` is 0-based. */
export function stepState(
  seq: Pick<Sequence, 'status'>,
  step: ChangeSummary,
  index: number,
): string {
  switch (step.status) {
    case 'draft':
      return seq.status === 'running' ? `Waiting for step ${index}` : 'Not run'
    case 'pending_review':
      return 'In review'
    case 'approved':
      return 'Approved, about to run'
    case 'executing':
      return 'Running now'
    case 'executed':
      return 'Executed'
    case 'failed':
      return 'Failed'
    case 'rejected':
      return 'Rejected'
    case 'expired':
      return 'Expired'
    case 'cancelled':
      return step.status_code === 'sequence_stopped' ? 'Not run: the sequence stopped' : 'Cancelled'
  }
}

/** The steps (1-based) that executed. They are never rolled back. */
export function appliedSteps(seq: Pick<Sequence, 'steps'>): number[] {
  return seq.steps.flatMap((s, i) => (s.status === 'executed' ? [i + 1] : []))
}

function stepList(seq: Pick<Sequence, 'steps'>, numbers: number[]): string {
  const parts = numbers.map((n) => {
    const s = seq.steps[n - 1]
    return s ? `step ${n} (${changeRef(s.number)} on ${s.connection.name})` : `step ${n}`
  })
  if (parts.length <= 1) return parts.join('')
  return `${parts.slice(0, -1).join(', ')} and ${parts[parts.length - 1]}`
}

export interface SequenceOutcome {
  tone: 'green' | 'red'
  title: string
  body: string
}

/**
 * How a finished sequence ended, in plain words, naming the steps that were
 * applied and saying that they were not rolled back. Null while it runs.
 */
export function sequenceOutcome(seq: Sequence): SequenceOutcome | null {
  if (seq.status === 'running') return null
  if (seq.status === 'completed') {
    return {
      tone: 'green',
      title: `Completed: all ${seq.steps.length} steps executed.`,
      body: 'Each step ran on its own database after the one before it.',
    }
  }
  const applied = appliedSteps(seq)
  const notRun = seq.steps.flatMap((s, i) =>
    s.status === 'cancelled' && s.status_code === 'sequence_stopped' ? [i + 1] : [],
  )
  const parts: string[] = []
  if (applied.length === 0) {
    parts.push('No step was applied, so no database was changed.')
  } else {
    parts.push(
      `${capitalize(stepList(seq, applied))} ${applied.length === 1 ? 'was' : 'were'} applied and ${applied.length === 1 ? 'is' : 'are'} not rolled back.`,
    )
  }
  if (notRun.length > 0) {
    parts.push(
      `${capitalize(notRun.length === 1 ? `step ${notRun[0]}` : `steps ${notRun.join(', ')}`)} did not run.`,
    )
  }
  return {
    tone: 'red',
    title: seq.status_message ?? 'Stopped.',
    body: parts.join(' '),
  }
}

function capitalize(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1)
}

/** Moves the item at `i` by `by` places (up is -1), returning a new list. */
export function moveItem<T>(list: readonly T[], i: number, by: number): T[] {
  const j = i + by
  if (j < 0 || j >= list.length || i < 0 || i >= list.length) return [...list]
  const out = [...list]
  const [item] = out.splice(i, 1)
  if (item !== undefined) out.splice(j, 0, item)
  return out
}

export interface StepError {
  /** 0-based step the error is in, or undefined for the whole request. */
  step?: number
  /** The field inside the step: "connection_id", "title", "statements[0]". */
  field: string
  message: string
}

/** Splits "steps[1].statements[0]" into its step and the field inside it. */
export function splitStepField(field: string, message: string): StepError {
  const m = /^steps\[(\d+)\]\.(.+)$/.exec(field)
  if (!m?.[1] || !m[2]) return { field, message }
  return { step: Number(m[1]), field: m[2], message }
}
