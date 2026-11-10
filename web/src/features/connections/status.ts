import type { ConnectionTestResult } from '@/api'

export type TestTone = 'green' | 'amber' | 'red' | 'mute'

/** How the last test of a connection reads in the list: its tone and a short label. */
export function testStatus(t: ConnectionTestResult | undefined): { tone: TestTone; label: string } {
  if (!t) return { tone: 'mute', label: 'Not tested' }
  if (!t.ok) return { tone: 'red', label: 'Last test failed' }
  if (t.warnings.length > 0) {
    return {
      tone: 'amber',
      label: `Connected with ${t.warnings.length} warning${t.warnings.length === 1 ? '' : 's'}`,
    }
  }
  return { tone: 'green', label: 'Connected' }
}

export const dotClass: Record<TestTone, string> = {
  green: 'bg-green',
  amber: 'bg-amber',
  red: 'bg-red',
  mute: 'bg-mute',
}
