import { defineConfig } from '@playwright/test'
import { join, isAbsolute } from 'node:path'
const root = process.env.SOBA_ACTIVATION_PRIVATE_RUN
const chromium = process.env.SOBA_ACTIVATION_CHROMIUM
if (!chromium || !isAbsolute(chromium)) throw new Error('An independently verified installed Chromium is required')
if (!root || !isAbsolute(root)) throw new Error('Use the reviewed private activation runner')
// This suppresses snapshots only. Raw framework diagnostics remain private
// transient data owned by activation-runner.mjs, never exportable artifacts.
process.env.PLAYWRIGHT_NO_COPY_PROMPT = '1'
export default defineConfig({
  testDir: './browser', testMatch: '**/activation-native.acceptance.mjs', fullyParallel: false, workers: 1, retries: 0,
  globalTimeout: 20 * 60_000, outputDir: join(root, 'playwright-private'), preserveOutput: 'never', timeout: 180000,
  expect: { timeout: 15000 }, reporter: './browser/activation-reporter.mjs',
  use: { browserName: 'chromium', headless: true, launchOptions: { executablePath: chromium }, serviceWorkers: 'block', screenshot: 'off', trace: 'off', video: 'off' },
})
