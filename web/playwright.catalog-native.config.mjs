import existing from './playwright.config.mjs'

// Separate real-agent parent owns startup/shutdown. Preserve established
// sandbox-disabled SDK default, single worker, no retries and private output.
export default {
  ...existing,
  testMatch: '**/catalog-view-native.acceptance.mjs',
  globalTimeout: 75_000,
  reporter: './browser/catalog-native-reporter.mjs',
  expect: { timeout: 8_000 },
  use: {
    ...existing.use,
    launchOptions: { ...(process.env.SOBA_CATALOG_BROWSER_CHROMIUM ? { executablePath: process.env.SOBA_CATALOG_BROWSER_CHROMIUM } : {}), timeout: 15_000 },
    serviceWorkers: 'block', acceptDownloads: false,
    actionTimeout: 8_000, navigationTimeout: 8_000,
  },
}
