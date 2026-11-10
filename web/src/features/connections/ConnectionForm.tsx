import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Eye } from 'lucide-react'
import { type KeyboardEvent, type ReactNode, useEffect, useState } from 'react'
import { Controller, type FieldPath, useForm, useWatch } from 'react-hook-form'
import {
  type Connection,
  type ConnectionTestResult,
  type Connector,
  type ConnectorField,
  createConnection,
  isApiError,
  queryKeys,
  testConnection,
  testConnectionSettings,
  updateConnection,
} from '@/api'
import {
  Button,
  FormField,
  Input,
  SecretField,
  type SecretChange,
  Select,
  Switch,
  TagInput,
  Textarea,
  toast,
} from '@/ui'
import {
  configFields,
  DESCRIPTION_MAX,
  formFieldFor,
  type FormValues,
  FUNCTION_PATTERN,
  initialConfig,
  NAME_MESSAGE,
  NAME_PATTERN,
  type PolicyDefaults,
  policyBounds,
  secretField,
  toCreateBody,
  toFormValues,
  toTestBody,
  toUpdateBody,
} from './connectionForm'
import { TestResultView } from './TestResultView'

export interface ConnectionFormProps {
  connectors: Connector[]
  defaults: PolicyDefaults
  /** The saved connection when editing; absent when adding one. */
  connection?: Connection
  onSaved: (c: Connection) => void
  onCancel?: () => void
  /** Rendered at the far end of the action bar, apart from Save (archive). */
  dangerZone?: ReactNode
}

export const PREVIEW_NOTICE =
  'Everyone who can sign in will be able to see rows from any table this database user can read, through change previews. Grant this user access only to tables your whole team may see.'

const DESCRIPTION_HELP =
  "Say what this database holds and when to use it, for example 'Production app database: users, orders, billing'. Agents read this to decide where a change belongs."

function message(e: unknown, fallback: string): string {
  return e instanceof Error ? e.message : fallback
}

/** The add and edit form of a connection, rendered from the connector's declared fields. */
export function ConnectionForm({
  connectors,
  defaults,
  connection,
  onSaved,
  onCancel,
  dangerZone,
}: ConnectionFormProps) {
  const queryClient = useQueryClient()
  const editing = connection !== undefined
  const form = useForm<FormValues>({
    defaultValues: toFormValues(connectors, connection, defaults),
    mode: 'onTouched',
  })
  const { control, handleSubmit, reset, setError, setValue, getValues, formState, clearErrors } =
    form
  const connectorId = useWatch({ control, name: 'connector' })
  const connector = connectors.find((c) => c.id === connectorId)
  const [result, setResult] = useState<ConnectionTestResult | null>(connection?.last_test ?? null)

  // A saved connection that changed elsewhere (or was just saved) resets the
  // form, keeping what the person is typing.
  useEffect(() => {
    if (connection) reset(toFormValues(connectors, connection), { keepDirtyValues: true })
  }, [connection, connectors, reset])

  const save = useMutation({
    mutationFn: async (values: FormValues) => {
      if (connection) {
        return updateConnection(
          connection.id,
          toUpdateBody(connectors, values, formState.dirtyFields),
        )
      }
      return createConnection(toCreateBody(connectors, values))
    },
    onSuccess: async (saved) => {
      queryClient.setQueryData(queryKeys.connections.detail(saved.id), saved)
      await queryClient.invalidateQueries({ queryKey: queryKeys.connections.list() })
      reset(toFormValues(connectors, saved))
      toast.success(editing ? `${saved.name} saved.` : `${saved.name} added.`)
      onSaved(saved)
    },
    onError: (error) => {
      if (isApiError(error) && error.fields.length > 0) {
        for (const f of error.fields) {
          const name = formFieldFor(f.field)
          if (name) setError(name as FieldPath<FormValues>, { type: 'server', message: f.message })
          else setError('root.server', { type: 'server', message: f.message })
        }
        return
      }
      if (isApiError(error) && error.code === 'name_taken') {
        setError('name', { type: 'server', message: error.message }, { shouldFocus: true })
        return
      }
      setError('root.server', {
        type: 'server',
        message: message(error, 'The connection could not be saved.'),
      })
    },
  })

  const test = useMutation({
    mutationFn: async () => {
      const values = getValues()
      if (connection && !formState.isDirty) return testConnection(connection.id)
      return testConnectionSettings(toTestBody(connectors, values, connection?.id))
    },
    onSuccess: async (res) => {
      setResult(res)
      if (connection && !formState.isDirty) {
        await queryClient.invalidateQueries({ queryKey: queryKeys.connections.all })
      }
    },
    onError: (error) => {
      if (isApiError(error) && error.fields.length > 0) {
        for (const f of error.fields) {
          const name = formFieldFor(f.field)
          if (name) setError(name as FieldPath<FormValues>, { type: 'server', message: f.message })
          else setError('root.server', { type: 'server', message: f.message })
        }
        return
      }
      setError('root.server', {
        type: 'server',
        message: message(error, 'The connection could not be tested.'),
      })
    },
  })

  const onTest = () => {
    clearErrors('root.server')
    const pw = getValues('password')
    if (!connection && (typeof pw !== 'string' || pw === '')) {
      setError('password', {
        type: 'manual',
        message: 'Enter the password to test the connection.',
      })
      return
    }
    test.mutate()
  }

  const onSubmit = handleSubmit((values) => {
    clearErrors('root.server')
    save.mutate(values)
  })

  const onKeyDown = (e: KeyboardEvent<HTMLFormElement>) => {
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
      e.preventDefault()
      void onSubmit()
    }
  }

  const busy = save.isPending
  const rootError = formState.errors.root?.server?.message
  const secret = secretField(connector)

  return (
    <form
      noValidate
      onSubmit={(e) => void onSubmit(e)}
      onKeyDown={onKeyDown}
      className="flex flex-col"
      aria-label={editing ? `Edit ${connection.name}` : 'Add a connection'}
    >
      <div className="flex max-w-2xl flex-col gap-8 px-6 py-6">
        <div
          role="note"
          className="flex items-start gap-2.5 rounded-md border border-amber/35 bg-amber/8 px-3 py-2.5 text-base text-amber"
        >
          <Eye aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
          <span>{PREVIEW_NOTICE}</span>
        </div>

        <Section title="Connection">
          <Controller
            name="connector"
            control={control}
            render={({ field }) => (
              <FormField
                label="Database type"
                help={
                  editing ? 'The database type of a saved connection cannot change.' : undefined
                }
              >
                <Select
                  className="w-60"
                  aria-label="Database type"
                  options={connectors.map((c) => ({ value: c.id, label: c.display_name }))}
                  value={field.value}
                  disabled={editing || busy}
                  onValueChange={(next) => {
                    field.onChange(next)
                    const c = connectors.find((x) => x.id === next)
                    setValue('config', initialConfig(c), { shouldDirty: true })
                    setResult(null)
                  }}
                />
              </FormField>
            )}
          />
          <Controller
            name="name"
            control={control}
            rules={{
              required: 'A name is required.',
              pattern: { value: NAME_PATTERN, message: NAME_MESSAGE },
            }}
            render={({ field, fieldState }) => (
              <FormField
                label="Name"
                name={field.name}
                help="Unique across all databases. Agents refer to a database by this name."
                error={fieldState.error?.message}
              >
                <Input
                  mono
                  spellCheck={false}
                  autoComplete="off"
                  placeholder="prod-app"
                  disabled={busy}
                  {...field}
                />
              </FormField>
            )}
          />
          <Controller
            name="description"
            control={control}
            rules={{
              maxLength: {
                value: DESCRIPTION_MAX,
                message: `Use at most ${DESCRIPTION_MAX} characters.`,
              },
            }}
            render={({ field, fieldState }) => (
              <FormField
                label="Description"
                name={field.name}
                help={DESCRIPTION_HELP}
                error={fieldState.error?.message}
              >
                <Textarea rows={2} disabled={busy} {...field} />
              </FormField>
            )}
          />
          {connector?.fields.map((f) =>
            f.type === 'secret' ? (
              <Controller
                key={f.key}
                name="password"
                control={control}
                rules={{
                  validate: (v: SecretChange) =>
                    (!editing && (typeof v !== 'string' || v === '')) || (editing && v === '')
                      ? editing
                        ? 'Enter the new password, or cancel.'
                        : 'A password is required.'
                      : true,
                }}
                render={({ field, fieldState }) => (
                  <SecretField
                    label={f.label}
                    help={f.help}
                    clearable={false}
                    status={{
                      isSet: connection?.password.is_set ?? false,
                      setBy: connection?.password.set_by,
                      setAt: connection?.password.set_at,
                    }}
                    value={field.value}
                    onChange={field.onChange}
                    name="password"
                    error={fieldState.error?.message}
                    disabled={busy}
                  />
                )}
              />
            ) : (
              <ConfigField
                key={`${connectorId}.${f.key}`}
                field={f}
                control={control}
                busy={busy}
              />
            ),
          )}
          {configFields(connector).length === 0 && !secret && (
            <p className="text-base text-dim">This connector has no settings.</p>
          )}
          <div className="flex flex-col gap-3">
            <div className="flex flex-wrap items-center gap-3">
              <Button onClick={onTest} loading={test.isPending} disabled={busy}>
                Test connection
              </Button>
              <span className="text-xs text-mute">
                {connection && !formState.isDirty
                  ? 'Tests the saved settings and records the result.'
                  : 'Tests the settings in this form; nothing is saved.'}
              </span>
            </div>
            {result && <TestResultView result={result} />}
          </div>
        </Section>

        <Section
          title="Review policy"
          description={
            editing ? undefined : 'Starts from the instance defaults in Settings, Policy defaults.'
          }
        >
          <div className="grid gap-5 sm:grid-cols-3">
            <PolicyInt
              control={control}
              name="policy.required_approvals"
              label="Approvals required"
              required={editing}
              busy={busy}
            />
            <PolicyInt
              control={control}
              name="policy.max_rows"
              label="Max rows per statement"
              required={editing}
              busy={busy}
            />
            <PolicyInt
              control={control}
              name="policy.approval_ttl_hours"
              label="Approval lifetime"
              unit="hours"
              required={editing}
              busy={busy}
            />
          </div>
          <Controller
            name="policy.store_row_images"
            control={control}
            render={({ field }) => (
              <div className="flex flex-col gap-1">
                <Switch
                  label="Store row images"
                  checked={field.value}
                  onCheckedChange={(v) => field.onChange(v)}
                  onBlur={field.onBlur}
                  disabled={busy}
                />
                <p className="pl-10 text-xs text-mute">
                  Keep the before and after rows of each change. When off, only column names and
                  counts are kept.
                </p>
              </div>
            )}
          />
          <Controller
            name="policy.auto_execute"
            control={control}
            render={({ field }) => (
              <div className="flex flex-col gap-1">
                <Switch
                  label="Execute automatically"
                  checked={field.value}
                  onCheckedChange={(v) => field.onChange(v)}
                  onBlur={field.onBlur}
                  disabled={busy}
                />
                <p className="pl-10 text-xs text-mute">
                  Run a change as soon as its last required approval lands.
                </p>
              </div>
            )}
          />
          <Controller
            name="policy.allowed_functions"
            control={control}
            rules={{
              validate: (list: string[]) => {
                const bad = list.find((f) => !FUNCTION_PATTERN.test(f))
                if (bad !== undefined) return `"${bad}" is not a function name.`
                return list.length <= 100 || 'Allow at most 100 functions.'
              },
            }}
            render={({ field, fieldState }) => (
              <FormField
                label="Allowed functions"
                name={field.name}
                help="Statements may call only these functions. Keep the list small and free of side effects."
                error={fieldState.error?.message}
              >
                <TagInput
                  mono
                  name={field.name}
                  value={field.value}
                  normalize={(s) => s.trim().toLowerCase()}
                  placeholder="Add a function"
                  disabled={busy}
                  onChange={field.onChange}
                  onBlur={field.onBlur}
                />
              </FormField>
            )}
          />
        </Section>
      </div>

      <div className="flex flex-wrap items-center gap-3 border-t border-seam px-6 py-4">
        <Button
          type="submit"
          variant="primary"
          loading={busy}
          disabled={editing && !formState.isDirty}
          shortcut="⌘↵"
        >
          {editing ? 'Save changes' : 'Save connection'}
        </Button>
        {editing ? (
          <Button
            variant="ghost"
            disabled={!formState.isDirty || busy}
            onClick={() => reset(toFormValues(connectors, connection))}
          >
            Discard
          </Button>
        ) : (
          onCancel && (
            <Button variant="ghost" disabled={busy} onClick={onCancel}>
              Cancel
            </Button>
          )
        )}
        {rootError ? (
          <p role="alert" className="text-base text-red">
            {rootError}
          </p>
        ) : (
          editing && formState.isDirty && <p className="text-xs text-mute">Unsaved changes</p>
        )}
        {dangerZone && <div className="ml-auto flex items-center gap-3">{dangerZone}</div>}
      </div>
    </form>
  )
}

function Section({
  title,
  description,
  children,
}: {
  title: string
  description?: string
  children: ReactNode
}) {
  return (
    <fieldset className="flex min-w-0 flex-col gap-5">
      <legend className="mb-4 flex w-full flex-col gap-1 border-b border-seam pb-2">
        <span className="text-base font-medium small-caps text-dim">{title}</span>
        {description && <span className="text-xs text-mute">{description}</span>}
      </legend>
      {children}
    </fieldset>
  )
}

type FormControl = ReturnType<typeof useForm<FormValues>>['control']

function ConfigField({
  field: f,
  control,
  busy,
}: {
  field: ConnectorField
  control: FormControl
  busy: boolean
}) {
  const name = `config.${f.key}` as const
  return (
    <Controller
      name={name}
      control={control}
      rules={{
        validate: (v) => {
          if (f.type === 'bool') return true
          const s = typeof v === 'string' ? v.trim() : ''
          if (f.required && s === '') return `${f.label} is required.`
          if (f.type === 'int' && s !== '' && !/^\d+$/.test(s)) return 'Must be a whole number.'
          return true
        },
      }}
      render={({ field, fieldState }) => {
        const value = field.value
        switch (f.type) {
          case 'bool':
            return (
              <div className="flex flex-col gap-1">
                <Switch
                  label={f.label}
                  checked={value === true}
                  onCheckedChange={(v) => field.onChange(v)}
                  onBlur={field.onBlur}
                  disabled={busy}
                />
                {f.help && <p className="pl-10 text-xs text-mute">{f.help}</p>}
              </div>
            )
          case 'enum':
            return (
              <FormField label={f.label} help={f.help} error={fieldState.error?.message}>
                <Select
                  className="w-60"
                  aria-label={f.label}
                  options={(f.options ?? []).map((o) => ({ value: o, label: o }))}
                  value={typeof value === 'string' && value !== '' ? value : null}
                  onValueChange={(v) => field.onChange(v)}
                  disabled={busy}
                />
              </FormField>
            )
          case 'text':
            return (
              <FormField
                label={f.label}
                name={field.name}
                help={f.help}
                error={fieldState.error?.message}
              >
                <Textarea
                  mono
                  rows={3}
                  spellCheck={false}
                  disabled={busy}
                  name={field.name}
                  value={typeof value === 'string' ? value : ''}
                  onChange={(e) => field.onChange(e.currentTarget.value)}
                  onBlur={field.onBlur}
                />
              </FormField>
            )
          default:
            return (
              <FormField
                label={f.label}
                name={field.name}
                help={f.help}
                error={fieldState.error?.message}
              >
                <Input
                  mono
                  spellCheck={false}
                  autoComplete="off"
                  inputMode={f.type === 'int' ? 'numeric' : undefined}
                  className={f.type === 'int' ? 'w-32' : undefined}
                  disabled={busy}
                  name={field.name}
                  value={typeof value === 'string' ? value : ''}
                  onChange={(e) => field.onChange(e.currentTarget.value)}
                  onBlur={field.onBlur}
                />
              </FormField>
            )
        }
      }}
    />
  )
}

function PolicyInt({
  control,
  name,
  label,
  unit,
  required,
  busy,
}: {
  control: FormControl
  name: 'policy.required_approvals' | 'policy.max_rows' | 'policy.approval_ttl_hours'
  label: string
  unit?: string
  required: boolean
  busy: boolean
}) {
  const bounds = policyBounds[name.slice('policy.'.length) as keyof typeof policyBounds]
  return (
    <Controller
      name={name}
      control={control}
      rules={{
        validate: (v) => {
          const s = typeof v === 'string' ? v.trim() : ''
          if (s === '') return required ? 'A value is required.' : true
          const n = Number(s)
          if (!/^\d+$/.test(s) || n < bounds.min || n > bounds.max) {
            return `Must be between ${bounds.min} and ${bounds.max}.`
          }
          return true
        },
      }}
      render={({ field, fieldState }) => (
        <FormField
          label={label}
          name={field.name}
          help={`${bounds.min} to ${bounds.max.toLocaleString('en-US')}${unit ? ` ${unit}` : ''}.`}
          error={fieldState.error?.message}
        >
          <Input
            mono
            inputMode="numeric"
            className="w-28 text-right"
            disabled={busy}
            name={field.name}
            value={field.value}
            onChange={(e) => field.onChange(e.currentTarget.value)}
            onBlur={field.onBlur}
          />
        </FormField>
      )}
    />
  )
}
