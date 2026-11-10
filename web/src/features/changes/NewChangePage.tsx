import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  ArrowDown,
  ArrowLeft,
  ArrowUp,
  Eye,
  ListPlus,
  Plus,
  Save,
  Send,
  Trash2,
} from 'lucide-react'
import { type SyntheticEvent, useId, useMemo, useState } from 'react'
import { Link, useLocation, useNavigate, useParams } from 'react-router'
import {
  type Change,
  type ChangePreview,
  changeQuery,
  connectionSchemaQuery,
  connectionsQuery,
  createChange,
  createSequence,
  isApiError,
  previewChange,
  queryKeys,
  type Sequence,
  sequenceQuery,
  submitChange,
  updateChange,
} from '@/api'
import {
  Button,
  cn,
  EmptyState,
  ErrorState,
  FormField,
  Input,
  PageHeader,
  Pane,
  PaneGrid,
  RowDiff,
  Select,
  type SelectOption,
  Skeleton,
  type SqlDiagnostic,
  SqlEditor,
  Textarea,
  toast,
  useDocumentTitle,
  useMediaQuery,
  WIDE,
} from '@/ui'
import { changeRef, rows } from './format'
import { useConnectorInfo } from './hooks'
import { moveItem, SEQUENCE_RULE } from './sequence'

/** The most steps a sequence can have (the server checks too). */
const MAX_STEPS = 10

interface StmtDraft {
  key: number
  sql: string
}

interface StepDraft {
  key: number
  connectionId: string
  /** Optional in a sequence; unused for a single change. */
  title: string
  statements: StmtDraft[]
}

/** One change is one step; two or more steps make a sequence. */
interface Draft {
  title: string
  reason: string
  steps: StepDraft[]
}

let nextKey = 1
const stmt = (sql = ''): StmtDraft => ({ key: nextKey++, sql })
const newStep = (connectionId = '', statements: StmtDraft[] = [stmt()]): StepDraft => ({
  key: nextKey++,
  connectionId,
  title: '',
  statements,
})

function fromChange(c: Change): Draft {
  return {
    title: c.title,
    reason: c.reason,
    steps: [
      newStep(
        c.connection.id,
        c.statements.map((s) => stmt(s.sql)),
      ),
    ],
  }
}

/** /changes/new, and /changes/:id/edit for the author of a draft or a change in review. */
export function NewChangePage() {
  const { id } = useParams()
  const location = useLocation()
  const copyOf = (location.state as { from?: Change } | null)?.from
  useDocumentTitle(id === undefined ? 'New change' : `Edit change #${id}`)
  const existing = useQuery({
    ...changeQuery(id ?? ''),
    enabled: id !== undefined,
    refetchInterval: false,
  })

  if (id !== undefined) {
    if (existing.isPending) {
      return (
        <Pane className="h-full">
          <PageHeader as="h1" title="Edit change" />
          <Skeleton className="m-6 h-40" />
        </Pane>
      )
    }
    if (existing.isError) {
      return (
        <Pane className="h-full">
          <PageHeader as="h1" title="Edit change" />
          <ErrorState
            title="The change could not be loaded."
            message={existing.error.message}
            code={isApiError(existing.error) ? existing.error.code : undefined}
            action={<Button onClick={() => void existing.refetch()}>Retry</Button>}
          />
        </Pane>
      )
    }
    if (!existing.data.actions.edit) {
      return (
        <Pane className="h-full">
          <PageHeader as="h1" title="Edit change" />
          <EmptyState
            className="flex-1 justify-center"
            title="Only the author can edit a draft or a change in review."
            action={
              <Button
                render={<Link to={`/changes/${existing.data.number}`} />}
                nativeButton={false}
              >
                Back to the change
              </Button>
            }
          />
        </Pane>
      )
    }
    return (
      <Editor key={existing.data.id} editing={existing.data} initial={fromChange(existing.data)} />
    )
  }
  const initial: Draft = copyOf
    ? { ...fromChange(copyOf), title: copyOf.title }
    : { title: '', reason: '', steps: [newStep()] }
  return <Editor initial={initial} copied={copyOf} />
}

type Results = Record<number, ChangePreview['statements'][number]>

function Editor({
  initial,
  editing,
  copied,
}: {
  initial: Draft
  editing?: Change
  copied?: Change
}) {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const wide = useMediaQuery(WIDE, true)
  const connectorInfo = useConnectorInfo()
  const formId = useId()
  const [draft, setDraft] = useState<Draft>(initial)
  const [results, setResults] = useState<{ byKey: Results; problem?: string; ok: boolean } | null>(
    null,
  )
  const [diags, setDiags] = useState<Record<number, SqlDiagnostic[]>>({})
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({})
  const [duplicate, setDuplicate] = useState<number | null>(null)

  const connections = useQuery(connectionsQuery())
  const isSequence = draft.steps.length > 1
  const first = draft.steps[0] ?? newStep()
  // A step of a sequence waiting for the step before it: validated on
  // save, never dry-run (its rows may depend on that step).
  const waitingSeq = editing?.status === 'draft' ? editing.sequence : undefined
  const waitingStep = waitingSeq !== undefined

  const set = (patch: Partial<Draft>) => setDraft((d) => ({ ...d, ...patch }))
  const setStep = (key: number, patch: Partial<StepDraft>) =>
    setDraft((d) => ({ ...d, steps: d.steps.map((s) => (s.key === key ? { ...s, ...patch } : s)) }))

  // Statement problems from the server, keyed by statement key. Issues
  // index the statements of step `stepIndex`.
  const applyIssues = (
    issues: { statement: number; message: string; position: number }[],
    stepIndex = 0,
  ) => {
    const next: Record<number, SqlDiagnostic[]> = {}
    const stmts = draft.steps[stepIndex]?.statements ?? []
    for (const i of issues) {
      const s = stmts[i.statement]
      if (s) next[s.key] = [...(next[s.key] ?? []), { position: i.position, message: i.message }]
    }
    setDiags(next)
  }

  const handleError = (err: unknown) => {
    if (!isApiError(err)) {
      toast.error(err instanceof Error ? err.message : 'The request did not complete. Try again.')
      return
    }
    const fields: Record<string, string> = {}
    for (const f of err.fields) fields[f.field] = f.message
    setFieldErrors(fields)
    const stepIndex = err.details?.step !== undefined ? err.details.step - 1 : 0
    if (err.details?.statements) applyIssues(err.details.statements, stepIndex)
    if (err.code === 'duplicate' && err.details?.change_number)
      setDuplicate(err.details.change_number)
    else toast.error(err.message)
  }

  const preview = useMutation({
    mutationFn: () =>
      previewChange(
        first.connectionId,
        first.statements.map((s) => s.sql),
      ),
    onSuccess: (r) => {
      setFieldErrors({})
      const byKey: Results = {}
      r.statements.forEach((s, i) => {
        const key = first.statements[i]?.key
        if (key !== undefined) byKey[key] = s
      })
      setResults({ byKey, problem: r.problem?.message, ok: r.ok })
      applyIssues(
        r.statements.flatMap((s) =>
          s.issue && s.issue.code !== 'not_run'
            ? [{ statement: s.position, message: s.issue.message, position: s.issue.position }]
            : [],
        ),
      )
      setDuplicate(isSequence ? null : (r.duplicate?.number ?? null))
    },
    onError: handleError,
  })

  const save = useMutation({
    mutationFn: async (submit: boolean): Promise<Change | Sequence> => {
      const sqls = first.statements.map((s) => s.sql)
      if (editing) {
        // Unchanged SQL is not sent, so a title fix keeps the approvals.
        const same =
          JSON.stringify(sqls.map((s) => s.trim())) ===
          JSON.stringify(editing.statements.map((s) => s.sql))
        let c = await updateChange(editing.id, {
          revision: editing.revision,
          title: draft.title,
          reason: draft.reason,
          ...(same ? {} : { statements: sqls }),
        })
        // A step of a sequence is submitted by the sequence, never by hand.
        if (submit && c.status === 'draft' && !c.sequence) c = await submitChange(c.id, c.revision)
        return c
      }
      if (isSequence) {
        return createSequence({
          title: draft.title,
          reason: draft.reason,
          steps: draft.steps.map((s) => ({
            connection_id: s.connectionId,
            ...(s.title.trim() ? { title: s.title } : {}),
            statements: s.statements.map((x) => x.sql),
          })),
        })
      }
      return createChange({
        connection_id: first.connectionId,
        title: draft.title,
        reason: draft.reason,
        statements: sqls,
        submit,
      })
    },
    onSuccess: (res) => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.changes.all })
      if ('current_step' in res) {
        queryClient.setQueryData(sequenceQuery(String(res.number)).queryKey, res)
        if (res.already_existed) {
          toast.info(
            `You already have a running sequence with these steps: ${changeRef(res.number)}.`,
          )
        } else {
          toast.success(`Sequence ${changeRef(res.number)} submitted. Step 1 is in review.`)
        }
        void navigate(`/sequences/${res.number}`)
        return
      }
      const c = res
      if (c.already_existed) {
        setDuplicate(c.number)
        return
      }
      queryClient.setQueryData(changeQuery(String(c.number)).queryKey, c)
      toast.success(
        c.status === 'draft' && !c.sequence
          ? `${changeRef(c.number)} saved as a draft.`
          : editing
            ? `${changeRef(c.number)} updated.`
            : `${changeRef(c.number)} submitted for review.`,
      )
      void navigate(`/changes/${c.number}`)
    },
    onError: handleError,
  })

  const busy = preview.isPending || save.isPending
  const filled = (s: StepDraft) =>
    s.connectionId !== '' && s.statements.some((x) => x.sql.trim() !== '')
  const canPreview = filled(first) && !waitingStep
  const canSubmit = draft.steps.every(filled)
  const runPreview = () => {
    if (canPreview && !busy) preview.mutate()
  }
  const onSubmit = (e: SyntheticEvent) => {
    e.preventDefault()
    save.mutate(true)
  }

  const connectionOptions = useMemo(
    () =>
      (connections.data ?? []).map((c) => ({
        value: c.id,
        label: `${c.name} · ${connectorInfo(c.connector).name}`,
      })),
    [connections.data, connectorInfo],
  )

  const addStep = () => {
    if (draft.steps.length >= MAX_STEPS) return
    set({ steps: [...draft.steps, newStep()] })
  }
  const moveStep = (i: number, by: number) => {
    set({ steps: moveItem(draft.steps, i, by) })
    // Step 1 is the one previewed: a new step 1 needs a new preview.
    if (i === 0 || i + by === 0) setResults(null)
  }
  const removeStep = (key: number) => {
    const index = draft.steps.findIndex((s) => s.key === key)
    set({ steps: draft.steps.filter((s) => s.key !== key) })
    if (index === 0) setResults(null)
  }

  const title = editing
    ? editing.sequence
      ? `Edit step ${editing.sequence.step} of ${changeRef(editing.sequence.number)}`
      : `Edit ${changeRef(editing.number)}`
    : isSequence
      ? 'New sequence'
      : 'New change'
  const inReview = editing?.status === 'pending_review'
  // Field names from the server: "statements[0]" for a change,
  // "steps[1].statements[0]" for a sequence.
  const errorOf = (stepIndex: number, field: string) =>
    isSequence ? fieldErrors[`steps[${stepIndex}].${field}`] : fieldErrors[field]

  const previewPane = (
    <PreviewResults
      step={first}
      results={results}
      running={preview.isPending}
      sequence={isSequence}
      waiting={waitingStep}
    />
  )

  const connectionField = (step: StepDraft, index: number) => (
    <FormField
      label={
        isSequence ? (
          <>
            Connection<span className="sr-only"> of step {index + 1}</span>
          </>
        ) : (
          'Connection'
        )
      }
      error={
        errorOf(index, 'connection_id') ??
        (connections.isError
          ? `Connections could not be loaded: ${connections.error.message}`
          : undefined)
      }
      help={
        editing ? (
          'The connection of a saved change cannot change.'
        ) : connections.data?.length === 0 ? (
          <>
            No databases are connected yet. An admin adds them in{' '}
            <Link className="text-blue underline" to="/connections">
              Connections
            </Link>
            .
          </>
        ) : undefined
      }
    >
      <ConnectionSelect
        label={isSequence ? `Connection of step ${index + 1}` : 'Connection'}
        options={connectionOptions}
        value={step.connectionId}
        loading={connections.isPending}
        disabled={editing !== undefined}
        invalid={errorOf(index, 'connection_id') !== undefined}
        onChange={(v) => {
          setStep(step.key, { connectionId: v })
          if (index === 0) setResults(null)
          const keys = new Set(step.statements.map((s) => s.key))
          setDiags((all) =>
            Object.fromEntries(Object.entries(all).filter(([k]) => !keys.has(Number(k)))),
          )
        }}
      />
    </FormField>
  )

  const statements = (step: StepDraft, index: number) => (
    <StepStatements
      formId={formId}
      step={step}
      stepIndex={index}
      sequence={isSequence}
      dialect={
        connectorInfo(
          connections.data?.find((c) => c.id === step.connectionId)?.connector ??
            editing?.connection.connector ??
            '',
        ).dialect
      }
      defaultNamespace={(() => {
        const v = connections.data?.find((c) => c.id === step.connectionId)?.config.default_schema
        return typeof v === 'string' ? v : undefined
      })()}
      results={index === 0 ? results?.byKey : undefined}
      diags={diags}
      errorOf={(field) => errorOf(index, field)}
      onRun={runPreview}
      onChange={(list) => setStep(step.key, { statements: list })}
      onClearDiag={(key) => {
        if (diags[key]) setDiags((all) => ({ ...all, [key]: [] }))
      }}
    />
  )

  const form = (
    <Pane aria-labelledby={`${formId}-title`} className="min-h-0">
      <PageHeader
        as="h1"
        title={
          <span className="flex items-center gap-2">
            <Button
              variant="ghost"
              size="icon"
              className="-ml-2 size-8"
              aria-label="Back"
              render={<Link to={editing ? `/changes/${editing.number}` : '/changes'} />}
              nativeButton={false}
              icon={<ArrowLeft aria-hidden="true" className="size-4" />}
            />
            <span id={`${formId}-title`}>{title}</span>
          </span>
        }
      />
      <form
        id={formId}
        onSubmit={onSubmit}
        className="flex min-h-0 flex-1 flex-col overflow-y-auto"
        noValidate
      >
        <div className="flex flex-col gap-4 px-4 py-5 sm:px-6">
          {copied && (
            <p
              role="status"
              className="rounded-md border border-seam bg-raise px-3 py-2 text-base text-dim"
            >
              Copied from {changeRef(copied.number)}. Preview it again: the data may have changed.
            </p>
          )}
          {inReview && (
            <p
              role="note"
              className="rounded-md border border-amber/35 bg-amber/8 px-3 py-2 text-base text-amber"
            >
              Saving new SQL resets the review: approvals given so far stop counting.
            </p>
          )}
          {waitingSeq && (
            <p
              role="note"
              className="rounded-md border border-seam bg-raise px-3 py-2 text-base text-dim"
            >
              This step waits for step {waitingSeq.step - 1}. Saving checks the SQL without running
              it; the step is previewed and goes to review after step {waitingSeq.step - 1} has run.
            </p>
          )}
          {isSequence ? (
            <FormField label="Title" error={fieldErrors.title}>
              <Input
                value={draft.title}
                maxLength={200}
                placeholder="Move ACME's invoices to the billing database"
                onChange={(e) => set({ title: e.target.value })}
              />
            </FormField>
          ) : (
            <div className="grid grid-cols-1 gap-4 md:grid-cols-[minmax(0,240px)_minmax(0,1fr)]">
              {connectionField(first, 0)}
              <FormField label="Title" error={fieldErrors.title}>
                <Input
                  value={draft.title}
                  maxLength={200}
                  placeholder="Fix the duplicate invoice for ACME"
                  onChange={(e) => set({ title: e.target.value })}
                />
              </FormField>
            </div>
          )}
          <FormField
            label="Reason"
            error={fieldErrors.reason}
            help="Why this change is needed: the ticket, the incident, what the reviewer should check."
          >
            <Textarea
              value={draft.reason}
              rows={3}
              maxLength={4000}
              onChange={(e) => set({ reason: e.target.value })}
            />
          </FormField>

          {isSequence ? (
            <div className="flex flex-col gap-3">
              <p className="rounded-md border border-seam bg-raise px-3 py-2 text-base text-dim">
                {SEQUENCE_RULE}
              </p>
              <ol aria-label="Steps" className="flex flex-col gap-3">
                {draft.steps.map((step, i) => (
                  <li key={step.key}>
                    <section
                      aria-labelledby={`${formId}-step-${step.key}`}
                      className="flex flex-col gap-3 rounded-md border border-seam p-3 sm:p-4"
                    >
                      <div className="flex items-center justify-between gap-2">
                        <h2
                          id={`${formId}-step-${step.key}`}
                          className="text-base font-medium text-text"
                        >
                          Step {i + 1}
                          <span className="ml-2 font-mono text-xs font-normal text-mute">
                            {i === 0 ? 'reviewed first' : `runs after step ${i}`}
                          </span>
                        </h2>
                        <span className="flex gap-1">
                          <Button
                            variant="ghost"
                            size="icon"
                            className="size-7"
                            aria-label={`Move step ${i + 1} up`}
                            disabled={i === 0}
                            onClick={() => moveStep(i, -1)}
                            icon={<ArrowUp aria-hidden="true" className="size-3.5" />}
                          />
                          <Button
                            variant="ghost"
                            size="icon"
                            className="size-7"
                            aria-label={`Move step ${i + 1} down`}
                            disabled={i === draft.steps.length - 1}
                            onClick={() => moveStep(i, 1)}
                            icon={<ArrowDown aria-hidden="true" className="size-3.5" />}
                          />
                          <Button
                            variant="ghost"
                            size="icon"
                            className="size-7"
                            aria-label={`Remove step ${i + 1}`}
                            onClick={() => removeStep(step.key)}
                            icon={<Trash2 aria-hidden="true" className="size-3.5" />}
                          />
                        </span>
                      </div>
                      <div className="grid grid-cols-1 gap-4 md:grid-cols-[minmax(0,240px)_minmax(0,1fr)]">
                        {connectionField(step, i)}
                        <FormField
                          label={
                            <>
                              Title<span className="sr-only"> of step {i + 1}</span>{' '}
                              <span className="font-normal text-mute">(optional)</span>
                            </>
                          }
                          error={errorOf(i, 'title')}
                        >
                          <Input
                            value={step.title}
                            maxLength={200}
                            placeholder={
                              draft.title
                                ? `${draft.title} · step ${i + 1} of ${draft.steps.length}`
                                : undefined
                            }
                            onChange={(e) => setStep(step.key, { title: e.target.value })}
                          />
                        </FormField>
                      </div>
                      {statements(step, i)}
                    </section>
                  </li>
                ))}
              </ol>
            </div>
          ) : (
            statements(first, 0)
          )}

          {!editing && (
            <div className="flex flex-col gap-1.5">
              <div>
                <Button
                  size="sm"
                  disabled={draft.steps.length >= MAX_STEPS}
                  icon={<ListPlus aria-hidden="true" className="size-3.5" />}
                  onClick={addStep}
                >
                  Add step
                </Button>
              </div>
              <p className="text-xs text-mute">
                {isSequence
                  ? `Only step 1 is previewed now. Each later step is previewed and reviewed after the step before it has run, because its rows may depend on it. Up to ${MAX_STEPS} steps.`
                  : 'Add a step when another change, on this or another database, must run only after this one has run.'}
              </p>
            </div>
          )}
          {fieldErrors.steps && <p className="text-xs text-red">{fieldErrors.steps}</p>}

          {duplicate !== null && (
            <p
              role="alert"
              className="rounded-md border border-amber/35 bg-amber/8 px-3 py-2 text-base text-amber"
            >
              You already have an open change with this SQL:{' '}
              <Link className="font-mono text-blue underline" to={`/changes/${duplicate}`}>
                {changeRef(duplicate)}
              </Link>
              .
            </p>
          )}
          {results?.problem && (
            <p role="alert" className="text-base text-red">
              {results.problem}
            </p>
          )}
        </div>

        {!wide && (
          <section aria-labelledby={`${formId}-preview`} className="border-t border-seam">
            <h2
              id={`${formId}-preview`}
              className="border-b border-seam px-4 py-2.5 text-base font-medium small-caps text-dim sm:px-6"
            >
              {isSequence ? 'Preview of step 1 · before / after' : 'Preview · before / after'}
            </h2>
            {previewPane}
          </section>
        )}

        <div className="sticky bottom-0 mt-auto flex flex-wrap items-center gap-2 border-t border-seam bg-panel px-4 py-3 sm:px-6">
          <Button
            shortcut="⌘↵"
            icon={<Eye aria-hidden="true" className="size-3.5" />}
            loading={preview.isPending}
            disabled={!canPreview || save.isPending}
            onClick={runPreview}
          >
            {isSequence ? 'Preview step 1' : 'Preview'}
          </Button>
          {!isSequence && !editing?.sequence && (!editing || editing.status === 'draft') && (
            <Button
              icon={<Save aria-hidden="true" className="size-3.5" />}
              loading={save.isPending && !save.variables}
              disabled={busy || first.connectionId === ''}
              onClick={() => save.mutate(false)}
            >
              {editing ? 'Save draft' : 'Save as draft'}
            </Button>
          )}
          <span className="flex-1" />
          <Button
            variant="primary"
            type="submit"
            icon={<Send aria-hidden="true" className="size-3.5" />}
            loading={save.isPending && save.variables}
            disabled={busy || !canSubmit}
          >
            {waitingStep
              ? 'Save step'
              : inReview
                ? 'Save changes'
                : isSequence
                  ? 'Submit sequence'
                  : 'Submit for review'}
          </Button>
        </div>
      </form>
    </Pane>
  )

  if (wide) {
    return (
      <PaneGrid className="h-full min-h-0 grid-cols-[minmax(0,1fr)_548px]">
        {form}
        <Pane aria-labelledby={`${formId}-preview`} className="min-h-0">
          <PageHeader
            title={
              <span id={`${formId}-preview`}>
                {isSequence ? 'Preview of step 1 · before / after' : 'Preview · before / after'}
              </span>
            }
          />
          <div className="min-h-0 flex-1 overflow-auto">{previewPane}</div>
        </Pane>
      </PaneGrid>
    )
  }
  return <PaneGrid className="min-h-0 flex-1 grid-cols-1">{form}</PaneGrid>
}

function ConnectionSelect({
  label,
  options,
  value,
  loading,
  disabled,
  invalid,
  onChange,
}: {
  label: string
  options: SelectOption<string>[]
  value: string
  loading: boolean
  disabled: boolean
  invalid: boolean
  onChange: (v: string) => void
}) {
  return (
    <Select
      aria-label={label}
      options={options}
      value={value || null}
      placeholder={loading ? 'Loading…' : 'Choose a database'}
      disabled={disabled}
      invalid={invalid}
      onValueChange={onChange}
    />
  )
}

/** The statements of one step (or of a single change), in run order. */
function StepStatements({
  formId,
  step,
  stepIndex,
  sequence,
  dialect,
  defaultNamespace,
  results,
  diags,
  errorOf,
  onRun,
  onChange,
  onClearDiag,
}: {
  formId: string
  step: StepDraft
  stepIndex: number
  sequence: boolean
  dialect: string
  defaultNamespace?: string
  results?: Results
  diags: Record<number, SqlDiagnostic[]>
  errorOf: (field: string) => string | undefined
  onRun: () => void
  onChange: (list: StmtDraft[]) => void
  onClearDiag: (key: number) => void
}) {
  const schema = useQuery({
    ...connectionSchemaQuery(step.connectionId),
    enabled: step.connectionId !== '',
  })
  // "statement 1" in a change; "step 2, statement 1" in a sequence.
  const name = (i: number) =>
    sequence ? `step ${stepIndex + 1}, statement ${i + 1}` : `statement ${i + 1}`
  const list = step.statements
  return (
    <div className="flex flex-col gap-3">
      {list.map((s, i) => {
        const errId = `${formId}-stmt-${s.key}-error`
        const d = diags[s.key] ?? []
        const fieldErr = errorOf(`statements[${i}]`)
        const r = results?.[s.key]
        return (
          <div
            key={s.key}
            className="flex flex-col gap-1.5"
            role="group"
            aria-label={capitalize(name(i))}
          >
            <div className="flex items-center justify-between gap-2">
              <span className="text-base font-medium text-text">
                Statement {i + 1}
                {r?.valid && (
                  <span className="ml-2 font-mono text-xs font-normal text-mute">
                    {r.kind?.toUpperCase()} {r.schema}.{r.table}
                  </span>
                )}
              </span>
              <span className="flex gap-1">
                <Button
                  variant="ghost"
                  size="icon"
                  className="size-7"
                  aria-label={`Move ${name(i)} up`}
                  disabled={i === 0}
                  onClick={() => onChange(moveItem(list, i, -1))}
                  icon={<ArrowUp aria-hidden="true" className="size-3.5" />}
                />
                <Button
                  variant="ghost"
                  size="icon"
                  className="size-7"
                  aria-label={`Move ${name(i)} down`}
                  disabled={i === list.length - 1}
                  onClick={() => onChange(moveItem(list, i, 1))}
                  icon={<ArrowDown aria-hidden="true" className="size-3.5" />}
                />
                <Button
                  variant="ghost"
                  size="icon"
                  className="size-7"
                  aria-label={`Remove ${name(i)}`}
                  disabled={list.length === 1}
                  onClick={() => onChange(list.filter((x) => x.key !== s.key))}
                  icon={<Trash2 aria-hidden="true" className="size-3.5" />}
                />
              </span>
            </div>
            <SqlEditor
              aria-label={`SQL of ${name(i)}`}
              aria-describedby={d.length > 0 || fieldErr ? errId : undefined}
              value={s.sql}
              dialect={dialect}
              schema={schema.data}
              defaultNamespace={defaultNamespace}
              diagnostics={d}
              invalid={d.length > 0 || fieldErr !== undefined}
              placeholder="UPDATE users SET name = 'Ann' WHERE id = 1"
              onRun={onRun}
              onChange={(sql) => {
                onChange(list.map((x) => (x.key === s.key ? { ...x, sql } : x)))
                onClearDiag(s.key)
              }}
            />
            {(d.length > 0 || fieldErr) && (
              <p id={errId} className="text-xs text-red">
                {[fieldErr, ...d.map((x) => x.message)].filter(Boolean).join(' ')}
              </p>
            )}
          </div>
        )
      })}
      <div>
        <Button
          size="sm"
          disabled={list.length >= 20}
          icon={<Plus aria-hidden="true" className="size-3.5" />}
          onClick={() => onChange([...list, stmt()])}
        >
          {sequence ? `Add statement to step ${stepIndex + 1}` : 'Add statement'}
        </Button>
      </div>
      {list.length > 1 && (
        <p className="text-xs text-mute">
          The statements {sequence ? 'of a step ' : ''}run top to bottom in one transaction, so a
          later statement sees the earlier ones. Use the arrows to reorder.
        </p>
      )}
      {errorOf('statements') && <p className="text-xs text-red">{errorOf('statements')}</p>}
    </div>
  )
}

function capitalize(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1)
}

/** The dry-run result of step 1's statements: counts and rows. Nothing is saved. */
function PreviewResults({
  step,
  results,
  running,
  sequence,
  waiting,
}: {
  step: StepDraft
  results: { byKey: Results; problem?: string; ok: boolean } | null
  running: boolean
  sequence: boolean
  waiting: boolean
}) {
  if (waiting) {
    return (
      <EmptyState
        title="This step is not previewed yet."
        description="It waits for the step before it: its rows may depend on that step, so it is previewed after that step has run."
      />
    )
  }
  if (!results) {
    return (
      <EmptyState
        title={running ? 'Running the dry run…' : 'Preview to see exactly which rows change.'}
        description={
          sequence
            ? 'Only step 1 can be dry-run now: each later step is previewed after the step before it has run. The dry run is always rolled back.'
            : 'The statements run in a transaction that is always rolled back. Ctrl+Enter or ⌘↵ in the editor.'
        }
      />
    )
  }
  return (
    <div className={cn('flex flex-col', running && 'opacity-60')} aria-busy={running}>
      <p
        role="status"
        className={cn(
          'border-b border-seam px-4 py-2.5 text-base',
          results.ok ? 'text-green' : 'text-amber',
        )}
      >
        {results.ok
          ? sequence
            ? 'Step 1 is valid and its dry run succeeded. Nothing was changed. The later steps are checked when you submit.'
            : 'Every statement is valid and the dry run succeeded. Nothing was changed.'
          : (results.problem ?? 'Some statements need fixing before you can submit.')}
      </p>
      {step.statements.map((s, i) => {
        const r = results.byKey[s.key]
        if (!r) return null
        return (
          <section
            key={s.key}
            aria-label={`Statement ${i + 1} preview`}
            className="border-b border-seam"
          >
            <h3 className="flex items-center justify-between gap-3 bg-bg/40 px-4 py-2 font-mono text-xs text-dim">
              <span>
                {i + 1}. {r.kind?.toUpperCase() ?? 'invalid'} {r.schema && `${r.schema}.${r.table}`}
              </span>
              {r.preview && <span className="text-mute">{rows(r.preview.rows_affected)}</span>}
            </h3>
            {r.issue && <p className="px-4 py-2.5 text-base text-red">{r.issue.message}</p>}
            {r.preview && r.kind && (
              <RowDiff
                kind={r.kind}
                image={r.preview}
                caption={`Statement ${i + 1}: ${r.kind.toUpperCase()} ${r.schema}.${r.table}`}
              />
            )}
          </section>
        )
      })}
    </div>
  )
}
