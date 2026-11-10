import { type BrowserContext, test as base, expect } from '@playwright/test'

/**
 * `test` with an automatic check: any Content-Security-Policy violation in
 * any page of the test (including contexts it opens itself, such as a second
 * user's) fails the test. Import `test` and `expect` from here, not from
 * '@playwright/test'.
 */
export const test = base.extend<{ cspViolations: string[] }>({
  cspViolations: [
    async ({ context, browser }, use) => {
      const violations: string[] = []
      const watch = async (ctx: BrowserContext) => {
        // Init scripts are injected by the browser itself, not as inline
        // script, so the policy under test does not block this listener.
        await ctx.addInitScript(() => {
          document.addEventListener(
            'securitypolicyviolation',
            (e: SecurityPolicyViolationEvent) => {
              console.error(
                `CSP violation: ${e.effectiveDirective} blocked ${e.blockedURI || 'inline'} at ${e.sourceFile}:${e.lineNumber}`,
              )
            },
          )
        })
        ctx.on('console', (msg) => {
          const text = msg.text()
          if (
            msg.type() === 'error' &&
            (text.startsWith('CSP violation:') || text.includes('Content Security Policy'))
          ) {
            violations.push(`${msg.location().url}: ${text}`)
          }
        })
      }
      await watch(context)
      const newContext = browser.newContext.bind(browser)
      browser.newContext = async (...args) => {
        const ctx = await newContext(...args)
        await watch(ctx)
        return ctx
      }
      try {
        await use(violations)
      } finally {
        browser.newContext = newContext
      }
      expect(violations, 'Content-Security-Policy violations').toEqual([])
    },
    { auto: true },
  ],
})

export { expect }
export type { Browser, Locator, Page } from '@playwright/test'
