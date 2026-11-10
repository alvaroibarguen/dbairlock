import type { Change } from '@/api'
import { ageInWords } from './format'

/** How fresh a change's preview is, on the server's clock. */
export interface Freshness {
  /** Milliseconds since the preview was taken; null without a preview. */
  age: number | null
  maxAge: number
  stale: boolean
  /** In the last minute before it goes stale. */
  ending: boolean
}

export function freshnessOf(change: Change, serverNow: number): Freshness {
  const maxAge = change.preview_max_age_seconds * 1000
  if (!change.previewed_at) return { age: null, maxAge, stale: true, ending: false }
  const age = Math.max(0, serverNow - Date.parse(change.previewed_at))
  return { age, maxAge, stale: age > maxAge, ending: age <= maxAge && maxAge - age <= 60_000 }
}

/** The sentence that explains why an approval needs a newer preview. The limit comes from the setting. */
export function staleMessage(f: Freshness): string {
  const limit = ageInWords(f.maxAge)
  if (f.age === null) {
    return `There is no preview yet. Approving requires a preview from the last ${limit}.`
  }
  // Just past the limit, the rounded age reads like the limit itself.
  const age = ageInWords(f.age)
  const shown = age === limit ? `more than ${age}` : age
  return `This preview is ${shown} old. Approving requires a preview from the last ${limit}, so you review the data as it is now.`
}

/** What the last preview update found, shown until the next one. */
export type RefreshNotice =
  { kind: 'unchanged' } | { kind: 'changed' } | { kind: 'refused'; message: string }
