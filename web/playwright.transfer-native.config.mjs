import existing from './playwright.config.mjs'
export default {
  ...existing,
  // Also applies to nearby/text configs that inherit these native defaults.
  projects: undefined,
  workers: 1,
  testMatch: process.env.SOBA_TRANSFER_BROWSER_OUTBOX === '1' ? '**/outbox-native.acceptance.mjs' : process.env.SOBA_TRANSFER_BROWSER_DISPATCH === '1' ? '**/transfer-dispatch-native.acceptance.mjs' : '**/transfer-group-native.acceptance.mjs',
  globalTimeout: 75_000,
  reporter: process.env.SOBA_TRANSFER_BROWSER_OUTBOX === '1' ? './browser/outbox-native-reporter.mjs' : './browser/transfer-native-reporter.mjs',
  expect: { timeout: 8_000 },
  use: {
    ...existing.use,
    launchOptions: { ...(process.env.SOBA_TRANSFER_BROWSER_CHROMIUM ? { executablePath: process.env.SOBA_TRANSFER_BROWSER_CHROMIUM } : {}), timeout: 15_000 },
    serviceWorkers: 'block', acceptDownloads: false,
    actionTimeout: 8_000, navigationTimeout: 8_000,
  },
}
