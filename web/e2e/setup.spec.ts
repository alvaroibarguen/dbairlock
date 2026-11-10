import { expect, type Page, test } from '@playwright/test'
import { CLIENT_ID, CLIENT_SECRET } from './fake-oidc.ts'
import { ADMIN_STATE, MEMBER_STATE } from './state.ts'

/** Clicks through the fake Google's account chooser. */
async function chooseAccount(page: Page, email: string) {
  await expect(page.getByRole('heading', { name: 'Choose an account' })).toBeVisible()
  await page.getByRole('link', { name: email }).click()
}

async function signIn(page: Page, email: string) {
  await page.goto('/signin')
  await page.getByRole('button', { name: 'Sign in with Google' }).click()
  await chooseAccount(page, email)
  await expect(page).toHaveURL(/\/changes$/)
}

test('setup wizard, sign out, sign in, create and revoke a token', async ({ page }) => {
  const code = process.env.E2E_SETUP_CODE ?? ''
  const issuer = process.env.E2E_OIDC_ISSUER ?? ''
  expect(code).not.toBe('')

  // A new instance sends everyone to the wizard.
  await page.goto('/changes')
  await expect(page).toHaveURL(/\/setup$/)

  // Step 1: a wrong code is refused, the code from the log is accepted.
  const codeInput = page.getByLabel('Setup code')
  await codeInput.fill('AAAAA-BBBBB-CCCCC-DDDDD')
  await page.getByRole('button', { name: 'Continue' }).click()
  await expect(page.getByText('That code is not valid. Check it and try again.')).toBeVisible()
  await codeInput.fill(code)
  await page.getByRole('button', { name: 'Continue' }).click()

  // Step 2: the sign-in settings. The issuer is not in the dashboard; the
  // suite points it at the fake provider through the same API, with the
  // setup session the browser now holds.
  await expect(page.getByLabel('Public URL')).toHaveValue('http://127.0.0.1:18080')
  const put = await page.request.put('/api/setup/signin', {
    data: {
      public_url: 'http://127.0.0.1:18080',
      client_id: CLIENT_ID,
      allowed_domains: ['example.com'],
      client_secret: CLIENT_SECRET,
      oidc_issuer: issuer,
    },
  })
  expect(put.status()).toBe(200)
  await expect(page.getByText('http://127.0.0.1:18080/api/auth/google/callback')).toBeVisible()
  await page.getByLabel('Google client ID').fill(CLIENT_ID)
  await page
    .getByRole('group', { name: 'Google client secret' })
    .getByPlaceholder('Value')
    .fill(CLIENT_SECRET)
  await page.getByLabel('Allowed domains').fill('example.com')
  await page.getByLabel('Allowed domains').press('Enter')
  await page.getByRole('button', { name: 'Save and continue' }).click()

  // Step 3: a real sign-in makes the first admin and ends setup.
  await page.getByRole('button', { name: 'Sign in with Google to finish' }).click()
  await chooseAccount(page, 'ada@example.com')
  await expect(page).toHaveURL(/\/changes$/)
  await page.getByRole('button', { name: 'User menu' }).first().click()
  await expect(page.getByText('ada@example.com').first()).toBeVisible()

  // Sign out, then back in.
  await page.getByRole('menuitem', { name: 'Sign out', exact: true }).click()
  await expect(page).toHaveURL(/\/signin$/)
  await page.goto('/changes')
  await expect(page).toHaveURL(/\/signin\?next=%2Fchanges$/)
  await signIn(page, 'ada@example.com')

  // A wrong-domain account is refused with a plain reason.
  const other = await page.context().browser()?.newContext()
  if (other) {
    const p = await other.newPage()
    await p.goto('/signin')
    await p.getByRole('button', { name: 'Sign in with Google' }).click()
    await chooseAccount(p, 'eve@other.test')
    await expect(p.getByRole('alert')).toContainText('not in a domain allowed')
    await other.close()
  }

  // Create a token: shown once with the MCP configuration, then revoke it.
  await page.goto('/settings/api-tokens')
  await page.getByRole('button', { name: 'Create token' }).click()
  const dialog = page.getByRole('dialog', { name: 'Create an API token' })
  await dialog.getByLabel('Name').fill('e2e agent')
  await dialog.getByRole('button', { name: 'Create token' }).click()
  const shown = page.getByRole('dialog', { name: 'Token "e2e agent" created' })
  await expect(shown.getByText(/^dbal_pat_[0-9A-Za-z]{43}$/)).toBeVisible()
  await expect(shown.getByText(/"Authorization": "Bearer dbal_pat_/)).toBeVisible()
  await shown.getByRole('button', { name: 'Done' }).click()
  const row = page.getByRole('row', { name: /e2e agent/ })
  await expect(row).toBeVisible()
  await row.getByRole('button', { name: 'Revoke' }).click()
  await page
    .getByRole('dialog', { name: 'Revoke "e2e agent"?' })
    .getByRole('button', { name: 'Revoke token' })
    .click()
  await expect(page.getByText('You have no active tokens.')).toBeVisible()

  await page.context().storageState({ path: ADMIN_STATE })

  // A second person, a member, signs in once; the change tests review as them.
  const member = await page.context().browser()?.newContext()
  if (member) {
    const p = await member.newPage()
    await signIn(p, 'bob@example.com')
    await member.storageState({ path: MEMBER_STATE })
    await member.close()
  }
})
