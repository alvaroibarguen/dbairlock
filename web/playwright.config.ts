import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { defineConfig, devices } from '@playwright/test'

// End-to-end tests run against the real binary (`make e2e` builds it first),
// with a throwaway data directory.
const port = 18080
const baseURL = `http://127.0.0.1:${port}`
const dataDir = mkdtempSync(join(tmpdir(), 'dbairlock-e2e-'))

export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? 'github' : 'list',
  use: { baseURL, trace: 'retain-on-failure' },
  projects: [
    {
      name: 'desktop',
      use: { ...devices['Desktop Chrome'], viewport: { width: 1280, height: 800 } },
    },
    {
      name: 'mobile',
      use: { ...devices['Desktop Chrome'], viewport: { width: 375, height: 800 } },
    },
  ],
  webServer: {
    command: `../bin/dbairlock serve --data-dir ${dataDir} --listen 127.0.0.1:${port}`,
    url: `${baseURL}/healthz`,
    reuseExistingServer: false,
    timeout: 30_000,
  },
})
