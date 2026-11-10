import { createRequire } from 'node:module'
import { AxeBuilder } from '@axe-core/playwright'
import { expect, type Page } from '@playwright/test'

// Shared helper for the accessibility checks: runs axe on the page as it is
// now, once in each theme, and fails with a readable list of violations.

const TAGS = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa', 'best-practice']

// axe-core as @axe-core/playwright resolves it, plus one configuration
// change: the best-practice "region" rule counts menus, listboxes and
// tooltips as regions, as it already does dialogs. They are popups that
// portal to <body> next to their trigger, and are not page content that
// belongs in a landmark.
const require = createRequire(import.meta.url)
const axeCore = createRequire(require.resolve('@axe-core/playwright'))('axe-core') as {
  source: string
}
const axeSource = `${axeCore.source}
axe.configure({ checks: [{ id: 'region', options: { regionMatcher:
  'dialog, [role=dialog], [role=alertdialog], svg, [role=menu], [role=listbox], [role=tooltip]' } }] });`

type Violation = Awaited<ReturnType<AxeBuilder['analyze']>>['violations'][number]

function describe(violations: Violation[]): string[] {
  return violations.flatMap((v) =>
    v.nodes.map(
      (n) =>
        `${v.id}: ${n.target.join(' ')} ${n.html.slice(0, 160)} -- ${n.failureSummary?.replace(/\s+/g, ' ') ?? ''}`,
    ),
  )
}

/**
 * Checks the page with axe in the dark and the light theme. The theme follows
 * prefers-color-scheme (the default "System" preference); motion is reduced
 * so colour transitions do not leave half-blended colours for the contrast
 * check.
 */
export async function expectAccessible(page: Page, label: string) {
  for (const colorScheme of ['dark', 'light'] as const) {
    await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' })
    // Let the theme switch and any pending transition settle.
    await page.waitForTimeout(50)
    const results = await new AxeBuilder({ page, axeSource }).withTags(TAGS).analyze()
    // Soft, so one run lists every page's violations.
    expect.soft(describe(results.violations), `${label} (${colorScheme})`).toEqual([])
  }
}
