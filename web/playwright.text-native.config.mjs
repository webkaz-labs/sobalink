import existing from './playwright.transfer-native.config.mjs'
export default {
  ...existing,
  reporter: './browser/text-native-reporter.mjs',
  testMatch: '**/text-outbox-native.acceptance.mjs',
}
