import assert from 'node:assert/strict'
import { lstatSync } from 'node:fs'
import { isAbsolute, join } from 'node:path'
import { defineConfig } from '@playwright/test'

// Exact acceptance only. Importing this config must never install a browser,
// start a dev server, or create the private execution root.
const root = process.env.SOBA_RESOURCE_BROWSER_PRIVATE_ROOT
const chromium = process.env.SOBA_RESOURCE_BROWSER_CHROMIUM
assert.equal(process.platform, 'linux', 'Reviewed Linux browser ownership required')
assert.equal(process.env.SOBALINK_RUN_RESOURCE_BROWSER_NATIVE, 'reviewed-offline-catalog-settings-v1', 'Exact browser opt-in required')
assert.ok(root && isAbsolute(root) && chromium && isAbsolute(chromium), 'Exact private browser inputs required')
const owner = lstatSync(root)
assert.ok(owner.isDirectory() && !owner.isSymbolicLink() && owner.uid === process.getuid() && (owner.mode & 0o077) === 0, 'Protected owned browser root required')
assert.ok(!process.env.DEBUG && !process.env.PWDEBUG && !process.env.NODE_OPTIONS && !process.env.NODE_PATH, 'Ambient runtime overrides forbidden')
process.env.PLAYWRIGHT_NO_COPY_PROMPT = '1'
export default defineConfig({
  testDir: './browser',
  testMatch: '**/resource-native-local-catalog.acceptance.mjs',
  fullyParallel: false, workers: 1, retries: 0,
  globalTimeout: 75_000, timeout: 60_000,
  expect: { timeout: 8_000 },
  outputDir: join(root, 'framework-private'), preserveOutput: 'never',
  reporter: './browser/resource-native-runner.mjs',
  use: {
    browserName: 'chromium', headless: true,
    launchOptions: { executablePath: chromium, chromiumSandbox: true, timeout: 15_000 },
    locale: 'en-US', colorScheme: 'light', viewport: { width: 1440, height: 960 },
    serviceWorkers: 'block', acceptDownloads: false,
    screenshot: 'off', trace: 'off', video: 'off',
    actionTimeout: 8_000, navigationTimeout: 8_000,
  },
})
