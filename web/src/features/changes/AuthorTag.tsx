import type { ChangeSummary } from '@/api'
import { AgentBadge, cn } from '@/ui'

function initials(name: string, email: string): string {
  const src = name.trim() || email
  const parts = src.split(/[\s@._-]+/).filter(Boolean)
  return ((parts[0]?.[0] ?? '?') + (parts[1]?.[0] ?? '')).toUpperCase()
}

/** A person: avatar initials and name. Removed people are marked. */
export function PersonTag({
  person,
  className,
}: {
  person: ChangeSummary['author']
  className?: string
}) {
  return (
    <span
      className={cn(
        'inline-flex h-[22px] items-center gap-1.5 rounded-sm border border-seam bg-raise pr-2 pl-0.5 text-xs text-dim',
        className,
      )}
    >
      <span
        aria-hidden="true"
        className="grid size-[18px] place-items-center rounded-full bg-seam font-mono text-[9.5px] font-medium text-dim"
      >
        {initials(person.name, person.email)}
      </span>
      <span className="text-text">{person.name || person.email}</span>
      {person.name && <span className="hidden sm:inline">{person.email}</span>}
      {person.removed && <span className="text-red">removed</span>}
    </span>
  )
}

/**
 * Who submitted a change: a person, or a person's agent. Agent authorship
 * carries the violet AGENT badge with the token name and its owner, so it is
 * never mistaken for a person's own change.
 */
export function AuthorTag({
  change,
  className,
}: {
  change: Pick<ChangeSummary, 'author' | 'via' | 'agent_name'>
  className?: string
}) {
  if (change.via === 'mcp') {
    return (
      <AgentBadge
        agent={change.agent_name ?? 'agent'}
        owner={change.author.email}
        className={className}
      />
    )
  }
  return <PersonTag person={change.author} className={className} />
}
