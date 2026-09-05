import { defineConfig } from '@playwright/test'
import { fileURLToPath } from 'node:url'

export default defineConfig({
  testDir: './tests',
  fullyParallel: false,
  workers: 1,
  timeout: 30000,
  expect: { timeout: 10000 },
  reporter: [['list'], ['html', { open: 'never' }]],
  use: {
    baseURL: 'http://127.0.0.1:17316',
    locale: 'en-US',
    viewport: { width: 1440, height: 900 },
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    launchOptions: process.env.INGOT_TEST_CHROMIUM ? { executablePath: process.env.INGOT_TEST_CHROMIUM } : undefined,
  },
  webServer: [
    {
      command: 'node scripts/fixture.mjs',
      cwd: fileURLToPath(new URL('.', import.meta.url)),
      url: 'http://127.0.0.1:17316/api/state',
      reuseExistingServer: false,
      timeout: 120000,
    },
    {
      command: 'node scripts/fixture.mjs',
      cwd: fileURLToPath(new URL('.', import.meta.url)),
      env: { INGOT_WEBUI_FIXTURE_ADDR: '127.0.0.1:17317', INGOT_WEBUI_FIXTURE_RUN_ONLY: '1' },
      url: 'http://127.0.0.1:17317/api/state',
      reuseExistingServer: false,
      timeout: 120000,
    },
  ],
})
