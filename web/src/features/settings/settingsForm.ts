import { z } from 'zod'
import type { SecretSlot, SettingKey, Settings, SettingsUpdate } from '@/api'
import type { SecretChange, SecretStatusView } from '@/ui'

// No compiled validators: zod would otherwise probe for `new Function`,
// which the server's Content-Security-Policy forbids (no 'unsafe-eval') and
// reports as a violation even though zod catches the error.
z.config({ jitless: true })

/*
 * A generic settings form: a group declares its fields, and this module
 * derives the form's zod schema, its initial values and the PATCH body.
 * Validation mirrors the server (internal/settings/registry.go and the
 * bounds in api/openapi.yaml, which a Go contract test keeps equal), and the
 * messages match the server's, so a field reads the same whichever side
 * rejected it. The server stays authoritative: its field errors are shown on
 * the same fields.
 */

interface FieldBase {
  label: string
  help?: string
}

export type SettingField =
  | (FieldBase & {
      kind: 'text'
      key: SettingKey
      maxLength: number
      required?: boolean
      noSpaces?: boolean
      mono?: boolean
      placeholder?: string
    })
  | (FieldBase & { kind: 'url'; key: SettingKey; maxLength: number; placeholder?: string })
  | (FieldBase & { kind: 'int'; key: SettingKey; min: number; max: number; unit?: string })
  | (FieldBase & { kind: 'bool'; key: SettingKey })
  | (FieldBase & {
      kind: 'list'
      key: SettingKey
      maxItems: number
      itemMaxLength: number
      item: { pattern: RegExp; message: (value: string) => string }
      placeholder?: string
    })
  | (FieldBase & { kind: 'choice'; key: SettingKey; options: readonly ChoiceOption[] })
  | (FieldBase & { kind: 'multi'; key: SettingKey; options: readonly ChoiceOption[] })
  | (FieldBase & { kind: 'secret'; key: SecretSlot })

export interface ChoiceOption {
  value: string
  label: string
  help?: string
}

export interface SettingsGroup {
  id: string
  title: string
  /** One sentence under the title. */
  description: string
  /** A warning shown above the fields, for settings that can lock people out. */
  warning?: string
  fields: SettingField[]
}

export type FormValues = Record<string, string | boolean | string[] | SecretChange>

/** Form field name for a setting. Dots would make react-hook-form nest values. */
export function formName(field: Pick<SettingField, 'kind' | 'key'>): string {
  const base = field.key.replaceAll('.', '__')
  return field.kind === 'secret' ? `secret__${base}` : base
}

const MAX_SECRET_BYTES = 4096

const between = (min: number, max: number) => `Must be between ${min} and ${max}.`

function isHttpUrl(value: string): boolean {
  if (value === '') return true
  try {
    const u = new URL(value)
    return (u.protocol === 'https:' || u.protocol === 'http:') && u.hostname !== ''
  } catch {
    return false
  }
}

function hasExtras(value: string): boolean {
  if (value === '') return false
  try {
    const u = new URL(value)
    return u.username !== '' || u.password !== '' || value.includes('?') || value.includes('#')
  } catch {
    return false
  }
}

/** Splits a list textarea into trimmed, non-empty entries (one per line or comma-separated). */
export function splitList(value: string): string[] {
  return value
    .split(/[\n,]/)
    .map((s) => s.trim())
    .filter((s) => s !== '')
}

function fieldSchema(field: SettingField): z.ZodType {
  switch (field.kind) {
    case 'text': {
      let s = z.string().trim()
      if (field.required) s = s.min(1, 'A value is required.')
      s = s.max(field.maxLength, `Must be at most ${field.maxLength} characters.`)
      if (field.noSpaces) s = s.regex(/^\S*$/, 'Must not contain spaces.')
      return s
    }
    case 'url':
      return z
        .string()
        .trim()
        .max(field.maxLength, `Must be at most ${field.maxLength} characters.`)
        .refine(isHttpUrl, {
          message:
            'Must be an absolute http:// or https:// URL, for example https://dbairlock.example.com.',
        })
        .refine((v) => !hasExtras(v), {
          message: 'Must not contain credentials, a query or a fragment.',
        })
    case 'int':
      return z
        .string()
        .trim()
        .regex(/^-?\d+$/, 'Must be a whole number.')
        .refine((v) => Number(v) >= field.min && Number(v) <= field.max, {
          message: between(field.min, field.max),
        })
    case 'bool':
      return z.boolean()
    case 'choice': {
      const allowed = field.options.map((o) => o.value)
      return z.string().refine((v) => allowed.includes(v), {
        message: `Must be one of: ${allowed.join(', ')}.`,
      })
    }
    case 'multi': {
      const allowed = field.options.map((o) => o.value)
      return z.array(z.string()).refine((v) => v.every((s) => allowed.includes(s)), {
        message: `Each entry must be one of: ${allowed.join(', ')}.`,
      })
    }
    case 'list':
      return z.string().superRefine((value, ctx) => {
        const items = splitList(value).map((s) => s.toLowerCase())
        if (items.length > field.maxItems) {
          ctx.addIssue({ code: 'custom', message: `At most ${field.maxItems} entries.` })
          return
        }
        const bad = items.find((s) => s.length > field.itemMaxLength || !field.item.pattern.test(s))
        if (bad !== undefined) ctx.addIssue({ code: 'custom', message: field.item.message(bad) })
      })
    case 'secret':
      return z
        .union([
          z
            .string()
            .min(1, 'Enter a value, or cancel.')
            .refine((v) => new TextEncoder().encode(v).length <= MAX_SECRET_BYTES, {
              message: `Must be at most ${MAX_SECRET_BYTES} bytes.`,
            }),
          z.null(),
        ])
        .optional()
  }
}

/** The zod schema for one group's form. */
export function groupSchema(group: SettingsGroup) {
  const shape: Record<string, z.ZodType> = {}
  for (const field of group.fields) shape[formName(field)] = fieldSchema(field)
  return z.object(shape)
}

/** Form values for a group from the server's settings. Secrets start unchanged. */
export function toFormValues(group: SettingsGroup, settings: Settings): FormValues {
  const out: FormValues = {}
  for (const field of group.fields) {
    const name = formName(field)
    if (field.kind === 'secret') {
      out[name] = undefined
      continue
    }
    const value: unknown = settings.values[field.key]
    if (field.kind === 'bool') out[name] = value === true
    else if (field.kind === 'multi')
      out[name] = Array.isArray(value) ? value.filter((v) => typeof v === 'string') : []
    else if (field.kind === 'list') out[name] = Array.isArray(value) ? value.join('\n') : ''
    else if (typeof value === 'string' || typeof value === 'number') out[name] = String(value)
    else out[name] = ''
  }
  return out
}

/**
 * The PATCH body for the fields the person changed. Unchanged fields are
 * omitted so they stay as they are on the server.
 */
export function toUpdate(
  group: SettingsGroup,
  values: FormValues,
  dirty: Partial<Record<string, unknown>>,
): SettingsUpdate {
  const settingValues: Record<string, unknown> = {}
  const secrets: Record<string, string | null> = {}
  for (const field of group.fields) {
    const name = formName(field)
    const value = values[name]
    if (field.kind === 'secret') {
      if (typeof value === 'string' || value === null) secrets[field.key] = value
      continue
    }
    if (dirty[name] === undefined) continue
    switch (field.kind) {
      case 'int':
        settingValues[field.key] = Number(value)
        break
      case 'list':
        settingValues[field.key] = splitList(typeof value === 'string' ? value : '')
        break
      case 'bool':
        settingValues[field.key] = value === true
        break
      case 'multi': {
        // In the order the options are listed, as the server stores them.
        const picked = Array.isArray(value) ? value : []
        settingValues[field.key] = field.options
          .map((o) => o.value)
          .filter((v) => picked.includes(v))
        break
      }
      default:
        settingValues[field.key] = typeof value === 'string' ? value : ''
    }
  }
  const body: SettingsUpdate = {}
  if (Object.keys(settingValues).length > 0) body.values = settingValues
  if (Object.keys(secrets).length > 0) body.secrets = secrets
  return body
}

/** Maps the server's secret status to the SecretField's view. */
export function secretStatus(settings: Settings, slot: SecretSlot): SecretStatusView {
  const s = settings.secrets[slot]
  return { isSet: s.is_set, setBy: s.set_by, setAt: s.set_at }
}

/**
 * Settings that exist but are not shown. `auth.oidc_issuer` only lets tests
 * point sign-in at a fake provider on a loopback address.
 */
export const hiddenSettings: SettingKey[] = ['auth.oidc_issuer']

export const notifyFormats: readonly ChoiceOption[] = [
  { value: 'generic', label: 'Generic JSON', help: 'One flat JSON object per event.' },
  { value: 'slack', label: 'Slack', help: 'The body Slack incoming webhooks accept.' },
]

export const notifyEvents: readonly ChoiceOption[] = [
  { value: 'change.submitted', label: 'Change submitted for review' },
  { value: 'change.approved', label: 'Change approved' },
  { value: 'change.rejected', label: 'Change rejected' },
  { value: 'change.executed', label: 'Change executed' },
  { value: 'change.failed', label: 'Change failed' },
  { value: 'sequence.completed', label: 'Sequence completed: every step executed' },
  {
    value: 'sequence.stopped',
    label: 'Sequence stopped: a step failed, was rejected, expired or was cancelled',
  },
]

const domainPattern = /^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$/

/** The settings groups shown in Settings, in order. Bounds mirror the registry. */
export const settingsGroups: SettingsGroup[] = [
  {
    id: 'general',
    title: 'General',
    description: 'How this instance names itself and where people reach it.',
    fields: [
      {
        kind: 'text',
        key: 'instance.name',
        label: 'Instance name',
        help: 'Shown in the dashboard and on the sign-in page.',
        required: true,
        maxLength: 64,
      },
      {
        kind: 'url',
        key: 'instance.public_url',
        label: 'Public URL',
        help: 'The address people use to open dbairlock. Sign-in redirects and links in notifications use it.',
        maxLength: 2048,
        placeholder: 'https://dbairlock.example.com',
      },
    ],
  },
  {
    id: 'sign-in',
    title: 'Sign-in',
    description: 'Google sign-in for everyone on the team.',
    warning:
      'A mistake here can lock everyone out, including you. Check the client ID, secret and domains before saving. If it happens, run "dbairlock recover" on the host: it prints a one-time link that reopens these settings.',
    fields: [
      {
        kind: 'text',
        key: 'auth.google.client_id',
        label: 'Google client ID',
        maxLength: 255,
        noSpaces: true,
        mono: true,
        placeholder: '1234567890-abc.apps.googleusercontent.com',
      },
      {
        kind: 'secret',
        key: 'auth.google.client_secret',
        label: 'Google client secret',
        help: 'Write-only. Once saved it cannot be viewed again, only replaced or cleared.',
      },
      {
        kind: 'list',
        key: 'auth.allowed_domains',
        label: 'Allowed domains',
        help: 'Google Workspace domains whose accounts may sign in. Press Enter after each.',
        maxItems: 50,
        itemMaxLength: 253,
        item: {
          pattern: domainPattern,
          message: (v) => `"${v}" is not a domain name, for example example.com.`,
        },
        placeholder: 'example.com',
      },
      {
        kind: 'int',
        key: 'auth.session_hours',
        label: 'Session length',
        help: 'How long a sign-in lasts without activity.',
        min: 1,
        max: 720,
        unit: 'hours',
      },
      {
        kind: 'bool',
        key: 'mcp.oauth_enabled',
        label: 'Agents can sign in with OAuth',
        help: 'MCP clients such as Claude Code sign in through the browser instead of using an API token. Needs the public URL. Turning it off refuses connected apps at once; API tokens keep working.',
      },
    ],
  },
  {
    id: 'review-policy',
    title: 'Policy defaults',
    description:
      'Defaults for connections created from now on. Preview age applies to every connection immediately.',
    fields: [
      {
        kind: 'int',
        key: 'policy.default_required_approvals',
        label: 'Required approvals',
        help: 'Approvals a change needs before it runs. The author never counts.',
        min: 1,
        max: 5,
      },
      {
        kind: 'int',
        key: 'policy.default_max_rows',
        label: 'Maximum rows per change',
        min: 1,
        max: 10000,
        unit: 'rows',
      },
      {
        kind: 'int',
        key: 'policy.default_approval_ttl_hours',
        label: 'Approval lifetime',
        help: 'A change still waiting for review after this long expires.',
        min: 1,
        max: 720,
        unit: 'hours',
      },
      {
        kind: 'bool',
        key: 'policy.default_store_row_images',
        label: 'Store before and after rows',
        help: 'Keeps the affected rows with each change for the audit trail.',
      },
      {
        kind: 'bool',
        key: 'policy.default_auto_execute',
        label: 'Run automatically when approved',
        help: 'Otherwise someone starts the run after the last approval.',
      },
      {
        kind: 'int',
        key: 'policy.preview_max_age_minutes',
        label: 'Maximum preview age',
        help: 'An approval needs a preview at most this old.',
        min: 1,
        max: 60,
        unit: 'minutes',
      },
    ],
  },
  {
    id: 'limits',
    title: 'Limits',
    description:
      'Timeouts applied to every statement dbairlock runs on a target, and how often each agent token may call the MCP server.',
    fields: [
      {
        kind: 'int',
        key: 'limits.statement_timeout_ms',
        label: 'Statement timeout',
        min: 100,
        max: 300000,
        unit: 'ms',
      },
      {
        kind: 'int',
        key: 'limits.lock_timeout_ms',
        label: 'Lock timeout',
        help: 'A change waiting longer than this for a row lock fails instead of blocking traffic.',
        min: 100,
        max: 60000,
        unit: 'ms',
      },
      {
        kind: 'int',
        key: 'limits.mcp_calls_per_minute',
        label: 'Agent calls',
        help: 'MCP tool calls each API token may make.',
        min: 1,
        max: 1000,
        unit: 'per minute',
      },
      {
        kind: 'int',
        key: 'limits.mcp_previews_per_hour',
        label: 'Agent previews',
        help: 'Each preview is a dry run on the target database, with brief row locks.',
        min: 1,
        max: 1000,
        unit: 'per hour',
      },
      {
        kind: 'int',
        key: 'limits.mcp_submissions_per_hour',
        label: 'Agent submissions',
        help: 'Changes each API token may submit.',
        min: 1,
        max: 1000,
        unit: 'per hour',
      },
      {
        kind: 'list',
        key: 'instance.trusted_proxies',
        label: 'Trusted proxies',
        help: 'IP ranges of the load balancers in front of dbairlock. Their X-Forwarded-For header gives the client address in the audit log. Leave empty if nothing sits in front.',
        maxItems: 50,
        itemMaxLength: 49,
        item: {
          pattern: /^[0-9a-f:.]+(\/\d{1,3})?$/,
          message: (v) => `"${v}" is not an IP range, for example 10.0.0.0/8.`,
        },
        placeholder: '10.0.0.0/8',
      },
    ],
  },
  {
    id: 'notifications',
    title: 'Notifications',
    description:
      'Posts to a webhook when changes are submitted, reviewed and run. Notifications may arrive late or not at all; the audit log is the record.',
    fields: [
      { kind: 'bool', key: 'notify.enabled', label: 'Send notifications' },
      {
        kind: 'secret',
        key: 'notify.webhook_url',
        label: 'Webhook URL',
        help: 'Stored as a secret because chat webhook URLs carry a credential. Write-only.',
      },
      {
        kind: 'choice',
        key: 'notify.format',
        label: 'Format',
        options: notifyFormats,
      },
      {
        kind: 'multi',
        key: 'notify.events',
        label: 'Events',
        help: 'Messages carry the change number, title, database, people, status and a link. Never SQL, rows or reasons.',
        options: notifyEvents,
      },
    ],
  },
]
