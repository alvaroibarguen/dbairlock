import { describe, expect, it } from 'vitest'
import type { ChangeSummary, Sequence } from '@/api'
import {
  appliedSteps,
  moveItem,
  sequenceOutcome,
  splitStepField,
  stepLabel,
  stepState,
  stepTone,
} from './sequence'

const step = (number: number, status: ChangeSummary['status'], over: Partial<ChangeSummary> = {}) =>
  ({
    number,
    status,
    connection: { id: `c${number}`, name: number % 2 ? 'app' : 'billing', connector: 'postgres' },
    ...over,
  }) as ChangeSummary

const seq = (over: Partial<Sequence>) =>
  ({ number: 12, status: 'running', current_step: 1, steps: [], ...over }) as Sequence

describe('stepper', () => {
  it('colours executed green, the open step amber, failed and rejected red, the rest mute', () => {
    expect(stepTone('executed', false)).toBe('green')
    expect(stepTone('pending_review', true)).toBe('amber')
    expect(stepTone('approved', true)).toBe('amber')
    expect(stepTone('executing', true)).toBe('amber')
    expect(stepTone('failed', true)).toBe('red')
    expect(stepTone('rejected', true)).toBe('red')
    expect(stepTone('draft', false)).toBe('mute')
    expect(stepTone('cancelled', false)).toBe('mute')
    expect(stepTone('expired', true)).toBe('mute')
  })

  it('says what a step waits for', () => {
    const running = seq({})
    expect(stepState(running, step(14, 'draft'), 1)).toBe('Waiting for step 1')
    expect(stepState(seq({ status: 'stopped' }), step(14, 'draft'), 1)).toBe('Not run')
    expect(stepState(running, step(14, 'cancelled', { status_code: 'sequence_stopped' }), 2)).toBe(
      'Not run: the sequence stopped',
    )
    expect(stepState(running, step(14, 'pending_review'), 0)).toBe('In review')
  })

  it('names a step in lists', () => {
    expect(stepLabel({ step: 2, steps: 3 })).toBe('step 2/3')
  })
})

describe('sequenceOutcome', () => {
  it('is null while the sequence runs', () => {
    expect(sequenceOutcome(seq({}))).toBeNull()
  })

  it('names the applied steps of a stopped sequence and says they are not rolled back', () => {
    const stopped = seq({
      status: 'stopped',
      status_message: 'Stopped: step 2 failed.',
      steps: [
        step(13, 'executed'),
        step(14, 'failed'),
        step(15, 'cancelled', { status_code: 'sequence_stopped' }),
      ],
    })
    expect(appliedSteps(stopped)).toEqual([1])
    expect(sequenceOutcome(stopped)).toEqual({
      tone: 'red',
      title: 'Stopped: step 2 failed.',
      body: 'Step 1 (#13 on app) was applied and is not rolled back. Step 3 did not run.',
    })
  })

  it('lists several applied steps, and says so when none was applied', () => {
    const two = seq({
      status: 'stopped',
      status_message: 'Stopped: step 3 was rejected.',
      steps: [step(13, 'executed'), step(14, 'executed'), step(15, 'rejected')],
    })
    expect(sequenceOutcome(two)?.body).toBe(
      'Step 1 (#13 on app) and step 2 (#14 on billing) were applied and are not rolled back.',
    )
    const none = seq({
      status: 'stopped',
      status_message: 'Stopped: step 1 was cancelled.',
      steps: [
        step(13, 'cancelled'),
        step(14, 'cancelled', { status_code: 'sequence_stopped' }),
        step(15, 'cancelled', { status_code: 'sequence_stopped' }),
      ],
    })
    expect(sequenceOutcome(none)?.body).toBe(
      'No step was applied, so no database was changed. Steps 2, 3 did not run.',
    )
  })

  it('reports a completed sequence', () => {
    const done = seq({ status: 'completed', steps: [step(13, 'executed'), step(14, 'executed')] })
    expect(sequenceOutcome(done)?.title).toBe('Completed: all 2 steps executed.')
  })
})

describe('editor helpers', () => {
  it('moves an item and refuses to move past either end', () => {
    expect(moveItem(['a', 'b', 'c'], 0, 1)).toEqual(['b', 'a', 'c'])
    expect(moveItem(['a', 'b', 'c'], 2, -1)).toEqual(['a', 'c', 'b'])
    expect(moveItem(['a', 'b'], 0, -1)).toEqual(['a', 'b'])
    expect(moveItem(['a', 'b'], 1, 1)).toEqual(['a', 'b'])
  })

  it('splits a step field from the server', () => {
    expect(splitStepField('steps[1].statements[0]', 'Empty.')).toEqual({
      step: 1,
      field: 'statements[0]',
      message: 'Empty.',
    })
    expect(splitStepField('title', 'Enter a title.')).toEqual({
      field: 'title',
      message: 'Enter a title.',
    })
  })
})
