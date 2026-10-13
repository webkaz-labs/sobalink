import assert from 'node:assert/strict'
import { readFile, rename, stat, writeFile } from 'node:fs/promises'
import { dirname, join } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { test as base, expect } from '@playwright/test'
import { errorKind, failureRecord, recordSessionFailure } from './nearby-native-diagnostics.mjs'
import { drainRoutesBeforeCleanup } from './fixture-lifecycle.mjs'
export { expect }

export const test = base.extend({
  app: async ({ browser }, use, testInfo) => {
    let currentPhase = 'descriptor'
    const phase = async value => { failureRecord(value, 'unexpected'); currentPhase = value; await base.step(`nearby:${value}`, async () => {}) }
    const failure = (error, kind) => recordSessionFailure(currentPhase, kind || errorKind(error, testInfo.status))
    await phase('descriptor')
    let session
    try {
      const file = process.env.SOBA_TRANSFER_BROWSER_SESSION, info = await stat(file)
      assert.ok(info.isFile() && (info.mode & 0o077) === 0 && info.size > 0 && info.size <= 16_384)
      session = JSON.parse(await readFile(file, 'utf8'))
      assert.ok(session.schema === 1 && session.nearby === true && session.devices.length === 2)
      for (const [index, device] of session.devices.entries()) {
        const { viewer } = device, url = new URL(viewer.url)
        assert.ok(viewer.schema === 1 && url.protocol === 'http:' && url.hostname === '127.0.0.1' && url.port && url.pathname === '/' && !url.search && !url.hash)
        assert.match(viewer.code, /^[A-Za-z0-9_-]{32}$/)
        assert.equal(device.alias, ['Synthetic peer A', 'Synthetic peer B'][index])
        assert.equal(device.locale, ['en', 'ja'][index])
        for (const port of [device.bootstrapPort, device.relayPort]) assert.ok(Number.isSafeInteger(port) && port >= 1024 && port <= 65535)
      }
      assert.notEqual(session.devices[0].viewer.url, session.devices[1].viewer.url)
      assert.equal(new Set(session.devices.flatMap(d => [d.bootstrapPort, d.relayPort])).size, 4)
    } catch (error) { failure(error); throw Error('Private nearby browser descriptor invalid; contents withheld') }
    const devices = [], contexts = []
    let errors = 0
    try {
      for (const [index, device] of session.devices.entries()) {
        await phase(index === 0 ? 'authenticate-a' : 'authenticate-b')
        const context = await browser.newContext({ locale: device.locale === 'ja' ? 'ja-JP' : 'en-US', viewport: { width: [375, 320][index], height: 844 }, serviceWorkers: 'block', acceptDownloads: false })
        contexts.push(context)
        const page = await context.newPage(), commands = []
        page.on('pageerror', () => { errors++; recordSessionFailure(currentPhase, 'runtime') })
        page.on('request', request => {
          if (new URL(request.url()).pathname !== '/api/command') return
          try {
            const command = request.postDataJSON()
            assert.ok(['setup.scopes', 'setup.status', 'setup.start', 'setup.manual', 'setup.approve', 'setup.cancel'].includes(command.name))
            commands.push(command)
          } catch { errors++; recordSessionFailure(currentPhase, 'command_boundary') }
        })
        try {
          const response = await page.goto(device.viewer.url)
          assert.ok(response && response.status() === 200 && (response.headers()['content-security-policy'] || '').includes("script-src 'self'"))
          await page.locator('.login-panel input').fill(device.viewer.code)
          await page.locator('.login-panel button[type="submit"]').click()
          await page.locator('.workspace').waitFor()
          if (await page.locator('html').getAttribute('lang') !== device.locale) await page.locator('.locale-toggle').click()
          await expect(page.locator('html')).toHaveAttribute('lang', device.locale)
        } catch (error) { failure(error); throw Error('Real nearby browser authentication failed; private details withheld') }
        device.viewer.code = ''
        devices.push({ ...device, page, commands })
      }
      const checkpoint = async stage => {
        assert.ok(['cancelled', 'review', 'one-approved', 'connected'].includes(stage))
        await phase(`checkpoint-${stage}`)
        const root = dirname(process.env.SOBA_TRANSFER_BROWSER_SESSION)
        const marker = join(root, `nearby-${stage}.json`)
        await writeFile(`${marker}.tmp`, JSON.stringify({ schema: 1, stage }), { mode: 0o600, flag: 'wx' })
        await rename(`${marker}.tmp`, marker)
        const until = Date.now() + 15_000
        while (true) {
          try { assert.equal(await readFile(join(root, `nearby-${stage}.ok`), 'utf8'), 'verified\n'); return } catch (error) { if (error.code !== 'ENOENT') throw error }
          if (Date.now() >= until) { recordSessionFailure(currentPhase, 'checkpoint'); throw Error('Actual Core evidence checkpoint unavailable') }
          await delay(25)
        }
      }
      await use({ devices, checkpoint, phase })
    } catch (error) { failure(error); throw error } finally {
      // A test-body failure precedes teardown; preserve it before cleanup runs.
      if (testInfo.errors.length) failure(testInfo.errors[0])
      currentPhase = 'cleanup' // Do not add an awaited step that could interrupt teardown.
      try {
        for (const device of session.devices) device.viewer.code = ''
        for (const { page } of devices) await drainRoutesBeforeCleanup(page, async () => { await page.close() })
        for (const context of contexts) await context.close()
        assert.equal(errors, 0, 'Actual nearby UI runtime or command boundary failed')
      } catch (error) { failure(error, 'cleanup'); throw error }
    }
  },
})
