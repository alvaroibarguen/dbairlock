import { defineConfig, devices } from '@playwright/test'
import { ADMIN_STATE } from './e2e/state.ts'

// End-to-end tests run against the real binary (`make e2e` builds it first)
// with a throwaway data directory and a fake Google (e2e/fake-oidc.ts), both
// started by e2e/global-setup.ts. The "setup" project completes the setup
// wizard first and saves the admin's session for the other projects.
const port = 18080
const baseURL = `http://127.0.0.1:${port}`

export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? 'github' : 'list',
  globalSetup: './e2e/global-setup.ts',
  use: { baseURL, trace: 'retain-on-failure' },
  projects: [
    {
      name: 'setup',
      testMatch: /setup\.spec\.ts/,
      use: { ...devices['Desktop Chrome'], viewport: { width: 1280, height: 800 } },
    },
    {
      name: 'desktop',
      testIgnore: /setup\.spec\.ts/,
      dependencies: ['setup'],
      use: {
        ...devices['Desktop Chrome'],
        viewport: { width: 1280, height: 800 },
        storageState: ADMIN_STATE,
      },
    },
    {
      name: 'mobile',
      testIgnore: /setup\.spec\.ts/,
      dependencies: ['setup'],
      use: {
        ...devices['Desktop Chrome'],
        viewport: { width: 375, height: 800 },
        storageState: ADMIN_STATE,
      },
    },
  ],
})
