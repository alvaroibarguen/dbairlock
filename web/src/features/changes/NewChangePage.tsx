import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowDown, ArrowLeft, ArrowUp, Eye, Plus, Save, Send, Trash2 } from 'lucide-react'
import { type SyntheticEvent, useId, useMemo, useState } from 'react'
import { Link, useLocation, useNavigate, useParams } from 'react-router'
import {
  type Change,
  type ChangePreview,
  changeQuery,
  connectionSchemaQuery,
  connectionsQuery,
  createChange,
  isApiError,
  previewChange,
  queryKeys,
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

interface Draft {
  connectionId: string
  title: string
  reason: string
  statements: { key: number; sql: string }[]
}

let nextKey = 1
const stmt = (sql = '') => ({ key: nextKey++, sql })

function fromChange(c: Change): Draft {
  return {
    connectionId: c.connection.id,
    title: c.title,
    reason: c.reason,
    statements: c.statements.map((s) => stmt(s.sql)),
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
    : { connectionId: '', title: '', reason: '', statements: [stmt()] }
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
  const conn = connections.data?.find((c) => c.id === draft.connectionId)
  const info = connectorInfo(conn?.connector ?? editing?.connection.connector ?? '')
  const schema = useQuery({
    ...connectionSchemaQuery(draft.connectionId),
    enabled: draft.connectionId !== '',
  })
  const defaultNamespace =
    typeof conn?.config.default_schema === 'string' ? conn.config.default_schema : undefined

  const sqls = draft.statements.map((s) => s.sql)
  const set = (patch: Partial<Draft>) => setDraft((d) => ({ ...d, ...patch }))

  // Statement problems from the server, keyed by statement position.
  const applyIssues = (issues: { statement: number; message: string; position: number }[]) => {
    const next: Record<number, SqlDiagnostic[]> = {}
    for (const i of issues) {
      const s = draft.statements[i.statement]
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
    if (err.details?.statements) applyIssues(err.details.statements)
    if (err.code === 'duplicate' && err.details?.change_number)
      setDuplicate(err.details.change_number)
    else toast.error(err.message)
  }

  const preview = useMutation({
    mutationFn: () => previewChange(draft.connectionId, sqls),
    onSuccess: (r) => {
      setFieldErrors({})
      const byKey: Results = {}
      r.statements.forEach((s, i) => {
        const key = draft.statements[i]?.key
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
      setDuplicate(r.duplicate?.number ?? null)
    },
    onError: handleError,
  })

  const save = useMutation({
    mutationFn: async (submit: boolean): Promise<Change> => {
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
        if (submit && c.status === 'draft') c = await submitChange(c.id, c.revision)
        return c
      }
      return createChange({
        connection_id: draft.connectionId,
        title: draft.title,
        reason: draft.reason,
        statements: sqls,
        submit,
      })
    },
    onSuccess: (c) => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.changes.all })
      if (c.already_existed) {
        setDuplicate(c.number)
        return
      }
      queryClient.setQueryData(changeQuery(String(c.number)).queryKey, c)
      toast.success(
        c.status === 'draft'
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
  const canRun = draft.connectionId !== '' && sqls.some((s) => s.trim() !== '')
  const runPreview = () => {
    if (canRun && !busy) preview.mutate()
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

  const move = (i: number, by: number) => {
    const list = [...draft.statements]
    const [item] = list.splice(i, 1)
    if (item) list.splice(i + by, 0, item)
    set({ statements: list })
  }

  const title = editing ? `Edit ${changeRef(editing.number)}` : 'New change'
  const inReview = editing?.status === 'pending_review'

  const previewPane = <PreviewResults draft={draft} results={results} running={preview.isPending} />

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
          <div className="grid grid-cols-1 gap-4 md:grid-cols-[minmax(0,240px)_minmax(0,1fr)]">
            <FormField
              label="Connection"
              error={
                fieldErrors.connection_id ??
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
              <Select
                aria-label="Connection"
                options={connectionOptions}
                value={draft.connectionId || null}
                placeholder={connections.isPending ? 'Loading…' : 'Choose a database'}
                disabled={editing !== undefined}
                invalid={fieldErrors.connection_id !== undefined}
                onValueChange={(v) => {
                  set({ connectionId: v })
                  setResults(null)
                  setDiags({})
                }}
              />
            </FormField>
            <FormField label="Title" error={fieldErrors.title}>
              <Input
                value={draft.title}
                maxLength={200}
                placeholder="Fix the duplicate invoice for ACME"
                onChange={(e) => set({ title: e.target.value })}
              />
            </FormField>
          </div>
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

          <div className="flex flex-col gap-3">
            {draft.statements.map((s, i) => {
              const errId = `${formId}-stmt-${s.key}-error`
              const d = diags[s.key] ?? []
              const fieldErr = fieldErrors[`statements[${i}]`]
              return (
                <div
                  key={s.key}
                  className="flex flex-col gap-1.5"
                  role="group"
                  aria-label={`Statement ${i + 1}`}
                >
                  <div className="flex items-center justify-between gap-2">
                    <span className="text-base font-medium text-text">
                      Statement {i + 1}
                      {results?.byKey[s.key]?.valid && (
                        <span className="ml-2 font-mono text-xs font-normal text-mute">
                          {results.byKey[s.key]?.kind?.toUpperCase()} {results.byKey[s.key]?.schema}
                          .{results.byKey[s.key]?.table}
                        </span>
                      )}
                    </span>
                    <span className="flex gap-1">
                      <Button
                        variant="ghost"
                        size="icon"
                        className="size-7"
                        aria-label={`Move statement ${i + 1} up`}
                        disabled={i === 0}
                        onClick={() => move(i, -1)}
                        icon={<ArrowUp aria-hidden="true" className="size-3.5" />}
                      />
                      <Button
                        variant="ghost"
                        size="icon"
                        className="size-7"
                        aria-label={`Move statement ${i + 1} down`}
                        disabled={i === draft.statements.length - 1}
                        onClick={() => move(i, 1)}
                        icon={<ArrowDown aria-hidden="true" className="size-3.5" />}
                      />
                      <Button
                        variant="ghost"
                        size="icon"
                        className="size-7"
                        aria-label={`Remove statement ${i + 1}`}
                        disabled={draft.statements.length === 1}
                        onClick={() =>
                          set({ statements: draft.statements.filter((x) => x.key !== s.key) })
                        }
                        icon={<Trash2 aria-hidden="true" className="size-3.5" />}
                      />
                    </span>
                  </div>
                  <SqlEditor
                    aria-label={`SQL of statement ${i + 1}`}
                    aria-describedby={d.length > 0 || fieldErr ? errId : undefined}
                    value={s.sql}
                    dialect={info.dialect}
                    schema={schema.data}
                    defaultNamespace={defaultNamespace}
                    diagnostics={d}
                    invalid={d.length > 0 || fieldErr !== undefined}
                    placeholder="UPDATE users SET name = 'Ann' WHERE id = 1"
                    onRun={runPreview}
                    onChange={(sql) => {
                      set({
                        statements: draft.statements.map((x) =>
                          x.key === s.key ? { ...x, sql } : x,
                        ),
                      })
                      if (diags[s.key]) setDiags((all) => ({ ...all, [s.key]: [] }))
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
                disabled={draft.statements.length >= 20}
                icon={<Plus aria-hidden="true" className="size-3.5" />}
                onClick={() => set({ statements: [...draft.statements, stmt()] })}
              >
                Add statement
              </Button>
            </div>
            {fieldErrors.statements && <p className="text-xs text-red">{fieldErrors.statements}</p>}
          </div>

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
              Preview · before / after
            </h2>
            {previewPane}
          </section>
        )}

        <div className="sticky bottom-0 mt-auto flex flex-wrap items-center gap-2 border-t border-seam bg-panel px-4 py-3 sm:px-6">
          <Button
            shortcut="⌘↵"
            icon={<Eye aria-hidden="true" className="size-3.5" />}
            loading={preview.isPending}
            disabled={!canRun || save.isPending}
            onClick={runPreview}
          >
            Preview
          </Button>
          {(!editing || editing.status === 'draft') && (
            <Button
              icon={<Save aria-hidden="true" className="size-3.5" />}
              loading={save.isPending && !save.variables}
              disabled={busy || draft.connectionId === ''}
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
            disabled={busy || !canRun}
          >
            {inReview ? 'Save changes' : 'Submit for review'}
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
          <PageHeader title={<span id={`${formId}-preview`}>Preview · before / after</span>} />
          <div className="min-h-0 flex-1 overflow-auto">{previewPane}</div>
        </Pane>
      </PaneGrid>
    )
  }
  return <PaneGrid className="min-h-0 flex-1 grid-cols-1">{form}</PaneGrid>
}

/** The dry-run result of the editor's statements: counts and rows. Nothing is saved. */
function PreviewResults({
  draft,
  results,
  running,
}: {
  draft: Draft
  results: { byKey: Results; problem?: string; ok: boolean } | null
  running: boolean
}) {
  if (!results) {
    return (
      <EmptyState
        title={running ? 'Running the dry run…' : 'Preview to see exactly which rows change.'}
        description="The statements run in a transaction that is always rolled back. Ctrl+Enter or ⌘↵ in the editor."
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
          ? 'Every statement is valid and the dry run succeeded. Nothing was changed.'
          : (results.problem ?? 'Some statements need fixing before you can submit.')}
      </p>
      {draft.statements.map((s, i) => {
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
