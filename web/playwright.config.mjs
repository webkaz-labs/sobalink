import { defineConfig } from '@playwright/test'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

// Playwright can create an error-context snapshot even when screenshots and
// traces are off. Disable that snapshot and keep disposable framework output
// separate from the explicit sanitized artifact directory.
process.env.PLAYWRIGHT_NO_COPY_PROMPT = '1'
const privateOutput = mkdtempSync(join(tmpdir(), 'soba-playwright-results-'))

// Only audited files without native port probes or listener start/restart enter
// the parallel phase. New files default to the serial remainder. Each test owns
// its browser context, Go child, profile, session and held ephemeral listeners.
const isolatedFiles = [
  'compact-screens', 'device-card-png', 'device-cards', 'direct-mixed',
  'empty-home', 'group-failure-states', 'lan-pairing-review', 'lan-policy',
  'layouts', 'message-uncertainty', 'readability',
  'receive-recovery', 'routes', 'saved-backends', 'saved-favorites',
  'saved-host', 'saved-navigation', 'setup-guide', 'sharing-templates',
  'startup-proxies', 'wan-candidates',
].map(name => `**/${name}.spec.mjs`)

export default defineConfig({
  testDir: './browser',
  testMatch: '**/*.spec.mjs',
  fullyParallel: false,
  workers: 2,
  projects: [
    { name: 'isolated', workers: 2, testMatch: isolatedFiles, teardown: 'serial' },
    // Teardown ordering runs this phase even when an isolated case fails.
    // A dependency would skip these independent checks after that failure.
    // Port probes and reservation release/rebind must have no other fixture
    // allocating ports alongside them. Native acceptance configs stay separate.
    { name: 'serial', testIgnore: isolatedFiles, workers: 1 },
  ],
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
