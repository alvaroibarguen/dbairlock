import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { TriangleAlert } from 'lucide-react'
import { type KeyboardEvent, useEffect } from 'react'
import { Controller, type Resolver, useForm } from 'react-hook-form'
import { isApiError, queryKeys, type Settings, updateSettings } from '@/api'
import {
  Button,
  FormField,
  Input,
  SecretField,
  type SecretChange,
  Switch,
  TagInput,
  toast,
} from '@/ui'
import {
  type FormValues,
  formName,
  groupSchema,
  secretStatus,
  type SettingField,
  type SettingsGroup,
  splitList,
  toFormValues,
  toUpdate,
} from './settingsForm'

export interface SettingsGroupFormProps {
  group: SettingsGroup
  settings: Settings
}

/** One settings group as a form: fields from the group, zod validation, one atomic save. */
export function SettingsGroupForm({ group, settings }: SettingsGroupFormProps) {
  const queryClient = useQueryClient()
  const form = useForm<FormValues>({
    resolver: zodResolver(groupSchema(group)) as Resolver<FormValues>,
    defaultValues: toFormValues(group, settings),
    mode: 'onTouched',
  })
  const { control, handleSubmit, reset, setError, formState } = form

  // Pick up server changes (for example after another group saved) without
  // discarding what the person is typing.
  useEffect(() => {
    reset(toFormValues(group, settings), { keepDirtyValues: true })
  }, [group, settings, reset])

  const mutation = useMutation({
    mutationFn: updateSettings,
    onSuccess: (next) => {
      queryClient.setQueryData(queryKeys.settings(), next)
      reset(toFormValues(group, next))
      toast.success(`${group.title} settings saved.`)
    },
    onError: (error) => {
      if (isApiError(error) && error.fields.length > 0) {
        const names = new Map(group.fields.map((f) => [f.key as string, formName(f)]))
        for (const f of error.fields) {
          const name = names.get(f.field)
          if (name) setError(name, { type: 'server', message: f.message })
          else setError('root.server', { type: 'server', message: `${f.field}: ${f.message}` })
        }
        return
      }
      setError('root.server', {
        type: 'server',
        message: error instanceof Error ? error.message : 'The settings could not be saved.',
      })
    },
  })

  const onSubmit = handleSubmit((values) => {
    const body = toUpdate(group, values, formState.dirtyFields)
    if (body.values === undefined && body.secrets === undefined) return
    mutation.mutate(body)
  })

  const onKeyDown = (e: KeyboardEvent<HTMLFormElement>) => {
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
      e.preventDefault()
      void onSubmit()
    }
  }

  const rootError = formState.errors.root?.server?.message

  return (
    <form
      noValidate
      onSubmit={(e) => void onSubmit(e)}
      onKeyDown={onKeyDown}
      className="flex flex-col"
    >
      <div className="flex max-w-xl flex-col gap-5 px-6 py-6">
        <p className="text-base text-dim">{group.description}</p>
        {group.warning && (
          <div
            role="note"
            className="flex items-start gap-2 rounded-md border border-amber/35 bg-amber/8 px-3 py-2.5 text-base text-amber"
          >
            <TriangleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
            <span>{group.warning}</span>
          </div>
        )}
        {group.fields.map((field) => (
          <Controller
            key={field.key}
            name={formName(field)}
            control={control}
            render={({ field: f, fieldState }) => (
              <FieldControl
                field={field}
                settings={settings}
                value={f.value}
                onChange={f.onChange}
                onBlur={f.onBlur}
                name={f.name}
                error={fieldState.error?.message}
                disabled={mutation.isPending}
              />
            )}
          />
        ))}
      </div>
      <div className="flex max-w-xl flex-wrap items-center gap-3 border-t border-seam px-6 py-4">
        <Button
          type="submit"
          variant="primary"
          loading={mutation.isPending}
          disabled={!formState.isDirty}
          shortcut="⌘↵"
        >
          Save changes
        </Button>
        <Button
          variant="ghost"
          disabled={!formState.isDirty || mutation.isPending}
          onClick={() => reset(toFormValues(group, settings))}
        >
          Discard
        </Button>
        {rootError ? (
          <p role="alert" className="text-base text-red">
            {rootError}
          </p>
        ) : (
          formState.isDirty && <p className="text-xs text-mute">Unsaved changes</p>
        )}
      </div>
    </form>
  )
}

interface FieldControlProps {
  field: SettingField
  settings: Settings
  value: FormValues[string]
  onChange: (value: FormValues[string]) => void
  onBlur: () => void
  name: string
  error?: string
  disabled: boolean
}

function FieldControl({
  field,
  settings,
  value,
  onChange,
  onBlur,
  name,
  error,
  disabled,
}: FieldControlProps) {
  switch (field.kind) {
    case 'secret':
      return (
        <SecretField
          label={field.label}
          help={field.help}
          status={secretStatus(settings, field.key)}
          value={value as SecretChange}
          onChange={onChange}
          name={name}
          error={error}
          disabled={disabled}
        />
      )
    case 'bool':
      return (
        <div className="flex flex-col gap-1">
          <Switch
            label={field.label}
            name={name}
            checked={value === true}
            onCheckedChange={(checked) => onChange(checked)}
            onBlur={onBlur}
            disabled={disabled}
          />
          {field.help && <p className="pl-10 text-xs text-mute">{field.help}</p>}
        </div>
      )
    case 'list':
      return (
        <FormField label={field.label} help={field.help} error={error} name={name}>
          <TagInput
            mono
            name={name}
            value={splitList(typeof value === 'string' ? value : '')}
            normalize={(s) => s.trim().toLowerCase()}
            placeholder={field.placeholder}
            disabled={disabled}
            onChange={(tags) => onChange(tags.join('\n'))}
            onBlur={onBlur}
          />
        </FormField>
      )
    case 'int':
      return (
        <FormField
          label={field.label}
          help={[field.help, `${field.min} to ${field.max}${field.unit ? ` ${field.unit}` : ''}.`]
            .filter(Boolean)
            .join(' ')}
          error={error}
          name={name}
        >
          <div className="flex items-center gap-2">
            <Input
              mono
              inputMode="numeric"
              name={name}
              value={typeof value === 'string' ? value : ''}
              disabled={disabled}
              onChange={(e) => onChange(e.currentTarget.value)}
              onBlur={onBlur}
              className="w-32 text-right"
            />
            {field.unit && <span className="text-base text-mute">{field.unit}</span>}
          </div>
        </FormField>
      )
    case 'text':
    case 'url':
      return (
        <FormField label={field.label} help={field.help} error={error} name={name}>
          <Input
            mono={field.kind === 'url' || field.mono}
            type={field.kind === 'url' ? 'url' : 'text'}
            name={name}
            value={typeof value === 'string' ? value : ''}
            placeholder={field.placeholder}
            spellCheck={false}
            disabled={disabled}
            onChange={(e) => onChange(e.currentTarget.value)}
            onBlur={onBlur}
          />
        </FormField>
      )
  }
}
