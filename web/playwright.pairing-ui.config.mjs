import { defineConfig } from '@playwright/test'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
process.env.PLAYWRIGHT_NO_COPY_PROMPT = '1'
export default defineConfig({
  testDir: './browser', testMatch: 'pairing-v2.acceptance.mjs', fullyParallel: false, workers: 1, retries: 0,
  outputDir: mkdtempSync(join(tmpdir(), 'sobalink-pairing-ui-')), preserveOutput: 'never', reporter: 'list',
  use: { browserName: 'chromium', headless: true, screenshot: 'off', trace: 'off', video: 'off', launchOptions: { ...(process.env.SOBA_CHROMIUM_PATH ? { executablePath: process.env.SOBA_CHROMIUM_PATH } : {}), args: ['--no-sandbox'] } },
  webServer: { command: 'npm run dev -- --port 5178 --strictPort', url: 'http://127.0.0.1:5178', reuseExistingServer: false },
})
