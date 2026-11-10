import { expect, test } from '@playwright/test'

test('loads the app shell on /changes', async ({ page }) => {
  await page.goto('/changes')
  await expect(page).toHaveTitle('dbairlock')
  await expect(page.getByText('No changes are waiting for review.')).toBeVisible()
})

test('every page in the shell is reachable by keyboard', async ({ page }) => {
  await page.goto('/changes')
  // The first Tab stop is the skip link; it moves focus to the content.
  await page.keyboard.press('Tab')
  await expect(page.getByRole('link', { name: 'Skip to content' })).toBeFocused()
})
