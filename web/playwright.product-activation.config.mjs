import { defineConfig } from '@playwright/test'
import { join, isAbsolute } from 'node:path'

const root = process.env.SOBA_PRODUCT_ACTIVATION_PRIVATE_RUN
const chromium = process.env.SOBA_ACTIVATION_CHROMIUM
if (process.platform !== 'linux' || process.env.SOBA_PRODUCT_ACTIVATION_ACCEPTANCE !== '1') throw new Error('Explicit Linux product activation opt-in required')
if (!chromium || !isAbsolute(chromium)) throw new Error('An independently verified installed Chromium is required')
if (!root || !isAbsolute(root)) throw new Error('Use the reviewed private product activation runner')
// Raw framework diagnostics are private transient data. Only the fixed report
// reconstructed by product-activation-runner.mjs may leave that private root.
process.env.PLAYWRIGHT_NO_COPY_PROMPT = '1'
export default defineConfig({
  testDir: './browser', testMatch: '**/product-activation-native.acceptance.mjs', fullyParallel: false, workers: 1, retries: 0,
  globalTimeout: 360000, outputDir: join(root, 'playwright-private'), preserveOutput: 'never', timeout: 150000,
  expect: { timeout: 15000 }, reporter: './browser/product-activation-reporter.mjs',
  use: {
    browserName: 'chromium', headless: true, locale: 'en-US', launchOptions: { executablePath: chromium },
    serviceWorkers: 'block', screenshot: 'off', trace: 'off', video: 'off', actionTimeout: 15000, navigationTimeout: 15000,
  },
})
