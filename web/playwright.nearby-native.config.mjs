import existing from './playwright.transfer-native.config.mjs'

// Keep the established Chromium/sandbox/CSP/private-output baseline unchanged.
export default {
  ...existing,
  testMatch: '**/nearby-setup-native.acceptance.mjs',
  timeout: 70_000,
  reporter: './browser/nearby-native-reporter.mjs',
}
