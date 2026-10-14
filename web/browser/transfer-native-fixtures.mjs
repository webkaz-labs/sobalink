import assert from 'node:assert/strict'
import { readFile, stat } from 'node:fs/promises'
import { test as base, expect } from '@playwright/test'
import { drainRoutesBeforeCleanup } from './fixture-lifecycle.mjs'
export { expect }
const bounded = value => typeof value === 'string' && value.length > 0 && value.length <= 256
export const test = base.extend({
  app: async ({ page }, use) => {
    let session
    try {
      const file = process.env.SOBA_TRANSFER_BROWSER_SESSION, info = await stat(file)
      assert.ok(info.isFile() && (info.mode & 0o077) === 0 && info.size > 0 && info.size <= 16_384)
      session = JSON.parse(await readFile(file, 'utf8'))
      const viewer = session.viewer, url = new URL(viewer.url)
      assert.ok(session.schema === 1 && viewer.schema === 1 && url.protocol === 'http:' && url.hostname === '127.0.0.1' && url.port && url.pathname === '/' && !url.search && !url.hash)
      assert.ok(typeof viewer.code === 'string' && /^[A-Za-z0-9_-]{32}$/.test(viewer.code))
      assert.ok(bounded(session.peerName) && bounded(session.peerId) && /^[a-f0-9]{64}$/.test(session.processId))
      if (session.dispatch === true || session.outbox === true || session.textOutbox === true) {
        assert.ok(Array.isArray(session.recipients) && session.recipients.length === 2)
        for (const peer of session.recipients) assert.ok(bounded(peer.id) && bounded(peer.name))
        assert.equal(session.recipients[0].id, session.peerId)
        assert.notEqual(session.recipients[0].id, session.recipients[1].id)
        assert.notEqual(session.recipients[0].name, session.recipients[1].name)
      } else {
        for (const target of [session.selected, session.untouched]) assert.ok(bounded(target.id) && bounded(target.name) && ['incoming', 'outgoing'].includes(target.direction))
        assert.notEqual(session.selected.id, session.untouched.id)
      }
    } catch { throw Error('Private transfer browser descriptor invalid; contents withheld') }
    let retries = 0, errors = 0
    page.on('pageerror', () => errors++)
    page.on('request', request => {
      if (new URL(request.url()).pathname !== '/api/command') return
      try {
        const command = JSON.parse(request.postData())
        if (command.name === 'transfer.retry') {
          retries++
          assert.deepEqual(command.payload, { transferId: session.selected.id, expected: { processId: session.processId, peerId: session.peerId, direction: session.selected.direction } })
        }
      } catch { errors++ }
    })
    try {
      try {
        const response = await page.goto(session.viewer.url)
        assert.ok(response && response.status() === 200 && (response.headers()['content-security-policy'] || '').includes("script-src 'self'"))
        await page.locator('.login-panel input').fill(session.viewer.code)
        await page.locator('.login-panel button[type="submit"]').click()
        await page.locator('.workspace').waitFor(); session.viewer.code = ''
      } catch { throw Error('Real transfer browser authentication failed; private details withheld') }
      await use({ session, counts: () => ({ retries, errors }) })
    } finally {
      session.viewer.code = ''
      await drainRoutesBeforeCleanup(page, async () => { await page.close() })
      assert.equal(errors, 0, 'Actual transfer UI request or runtime failed')
    }
  },
})
