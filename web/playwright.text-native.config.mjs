import existing from './playwright.transfer-native.config.mjs'
export default {
  ...existing,
  testMatch: '**/text-outbox-native.acceptance.mjs',
}
