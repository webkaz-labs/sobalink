import { defineConfig } from '@playwright/test'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

// Playwright can create an error-context snapshot even when screenshots and
// traces are off. Disable that snapshot and keep disposable framework output
// separate from the explicit sanitized artifact directory.
process.env.PLAYWRIGHT_NO_COPY_PROMPT = '1'
const privateOutput = mkdtempSync(join(tmpdir(), 'soba-playwright-results-'))

export default defineConfig({
  testDir: './browser',
  testMatch: '**/*.spec.mjs',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  outputDir: privateOutput,
  preserveOutput: 'never',
  timeout: 60_000,
  expect: { timeout: 10_000 },
  reporter: './browser/reporter.mjs',
  use: {
    browserName: 'chromium',
    headless: true,
    locale: 'en-US',
    viewport: { width: 1440, height: 960 },
    colorScheme: 'light',
    screenshot: 'off',
    trace: 'off',
    video: 'off',
  },
})
