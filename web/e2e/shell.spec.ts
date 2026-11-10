import { expect, test } from './csp.ts'

// These run signed in as the admin created by setup.spec.ts.

test('loads the app shell on /changes', async ({ page }) => {
  await page.goto('/changes')
  await expect(page).toHaveTitle('dbairlock')
  await expect(page.getByRole('button', { name: /^Needs my review/ })).toHaveAttribute(
    'aria-pressed',
    'true',
  )
})

test('every page in the shell is reachable by keyboard', async ({ page }) => {
  await page.goto('/changes')
  // The first Tab stop is the skip link; it moves focus to the content.
  await page.keyboard.press('Tab')
  await expect(page.getByRole('link', { name: 'Skip to content' })).toBeFocused()
})

test('the users page lists the admin', async ({ page }) => {
  await page.goto('/settings/users')
  await expect(page.getByRole('table', { name: 'Users' })).toContainText('ada@example.com')
})
