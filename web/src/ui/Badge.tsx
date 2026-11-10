import type { ReactNode } from 'react'
import { cn } from './cn'

export type BadgeTone = 'neutral' | 'green' | 'amber' | 'red' | 'blue'

const tones: Record<BadgeTone, string> = {
  neutral: 'border-seam bg-raise text-dim',
  green: 'border-green/35 bg-green/8 text-green',
  amber: 'border-amber/35 bg-amber/8 text-amber',
  red: 'border-red/35 bg-red/8 text-red',
  blue: 'border-blue/35 bg-blue/8 text-blue',
}

const tagBase =
  'inline-flex h-[22px] shrink-0 items-center gap-1.5 rounded-sm border px-2 text-xs whitespace-nowrap'

export interface BadgeProps {
  tone?: BadgeTone
  mono?: boolean
  children: ReactNode
  className?: string
}

/** A tag: 5px radius, 8% tinted fill and 35% border in the state colour. */
export function Badge({ tone = 'neutral', mono, children, className }: BadgeProps) {
  return (
    <span className={cn(tagBase, tones[tone], mono && 'font-mono tabular', className)}>
      {children}
    </span>
  )
}

export type ChangeStatus =
  | 'draft'
  | 'pending_review'
  | 'approved'
  | 'executing'
  | 'executed'
  | 'failed'
  | 'rejected'
  | 'expired'
  | 'cancelled'

const statusMeta: Record<ChangeStatus, { label: string; tone: BadgeTone; dot: string }> = {
  draft: { label: 'Draft', tone: 'neutral', dot: 'bg-mute' },
  pending_review: { label: 'Pending review', tone: 'amber', dot: 'bg-amber' },
  approved: { label: 'Approved', tone: 'green', dot: 'bg-green' },
  executing: { label: 'Executing', tone: 'blue', dot: 'bg-blue' },
  executed: { label: 'Executed', tone: 'green', dot: 'bg-green' },
  failed: { label: 'Failed', tone: 'red', dot: 'bg-red' },
  rejected: { label: 'Rejected', tone: 'red', dot: 'bg-red' },
  expired: { label: 'Expired', tone: 'neutral', dot: 'bg-mute' },
  cancelled: { label: 'Cancelled', tone: 'neutral', dot: 'bg-mute' },
}

/** 7px status dot coloured by change status. Decorative; pair with text. */
export function StatusDot({ status, className }: { status: ChangeStatus; className?: string }) {
  return (
    <span
      aria-hidden="true"
      className={cn(
        'inline-block size-[7px] shrink-0 rounded-full',
        statusMeta[status].dot,
        className,
      )}
    />
  )
}

/** The change status as a tag with its dot. */
export function StatusBadge({ status, className }: { status: ChangeStatus; className?: string }) {
  const meta = statusMeta[status]
  return (
    <Badge tone={meta.tone} className={className}>
      <StatusDot status={status} />
      {meta.label}
    </Badge>
  )
}

export interface AgentBadgeProps {
  /** Agent name, for example "claude-code". */
  agent: string
  /** Email of the person whose token the agent used. */
  owner: string
  className?: string
}

/**
 * Authorship marker for agent-written changes: a violet mono AGENT label, then
 * the agent name and the token owner. Violet is reserved for agents.
 */
export function AgentBadge({ agent, owner, className }: AgentBadgeProps) {
  return (
    <span
      className={cn(tagBase, 'border-seam bg-raise pl-1 text-dim', className)}
      title={`Submitted by the agent "${agent}" acting as ${owner}`}
    >
      <span className="rounded-[3px] bg-violet/8 px-1.5 font-mono text-2xs font-medium tracking-[0.04em] text-violet">
        AGENT
      </span>
      <span>
        <span className="text-text">{agent}</span> via {owner}
      </span>
    </span>
  )
}
