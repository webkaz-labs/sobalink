import existing from './playwright.config.mjs'
export default {
  ...existing,
  testMatch: '**/transfer-group-native.acceptance.mjs',
  globalTimeout: 75_000,
  reporter: './browser/transfer-native-reporter.mjs',
  expect: { timeout: 8_000 },
  use: {
    ...existing.use,
    launchOptions: { ...(process.env.SOBA_TRANSFER_BROWSER_CHROMIUM ? { executablePath: process.env.SOBA_TRANSFER_BROWSER_CHROMIUM } : {}), timeout: 15_000 },
    serviceWorkers: 'block', acceptDownloads: false,
    actionTimeout: 8_000, navigationTimeout: 8_000,
  },
}
