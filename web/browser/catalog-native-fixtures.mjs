import assert from 'node:assert/strict'
import { readFile, stat } from 'node:fs/promises'
import { test as base, expect } from '@playwright/test'
import { drainRoutesBeforeCleanup } from './fixture-lifecycle.mjs'
export { expect }

// The separate real-agent parent owns both agents and the private descriptor.
// This adapter never seeds status, grants, receipts or peer HTTP responses.
export const test = base.extend({
  app: async ({ page }, use) => {
    const file = process.env.SOBA_CATALOG_BROWSER_SESSION
    let session
    try {
      const info = await stat(file)
      assert.ok(info.isFile() && (info.mode & 0o077) === 0 && info.size > 0 && info.size <= 16_384)
      session = JSON.parse(await readFile(file, 'utf8'))
      const viewer = session.viewer, url = new URL(viewer.url)
      assert.ok(session.schema === 1 && viewer.schema === 1 && url.protocol === 'http:' && url.hostname === '127.0.0.1' && url.port && url.pathname === '/' && !url.search && !url.hash)
      assert.ok(typeof viewer.code === 'string' && /^[A-Za-z0-9_-]{32}$/.test(viewer.code))
      assert.ok(typeof session.peerName === 'string' && session.peerName && Number.isInteger(session.localPort) && session.localPort >= 1024 && session.localPort <= 65535)
      const selector = session.request.request
      assert.ok(/^[a-f0-9]{64}$/.test(session.request.peerKey) && selector.protocolVersion === 1 && selector.target.schemaVersion === 1 && /^[a-f0-9]{32}$/.test(selector.target.resourceId) && /^[a-f0-9]{32}$/.test(selector.grantId) && Number.isSafeInteger(selector.grantRevision) && selector.grantRevision > 0)
      assert.ok(/^[a-f0-9]{32}$/.test(session.serviceId) && (!session.viewOnlyServiceId || /^[a-f0-9]{32}$/.test(session.viewOnlyServiceId)) && typeof session.expectedContent === 'string' && session.expectedContent.length > 0 && session.expectedContent.length <= 4096)
    } catch { throw Error('Private catalog browser descriptor invalid; contents withheld') }
    let lists = 0, connects = 0, errors = 0
    page.on('pageerror', () => errors++)
    page.on('request', request => {
      if (new URL(request.url()).pathname !== '/api/command') return
      try {
        const command = JSON.parse(request.postData())
        if (command.name === 'resource.catalog.view.list') {
          lists++
          assert.deepEqual(command.payload, session.request)
        }
        if (command.name === 'service.connect') {
          connects++
          assert.equal(command.payload.peerId, session.request.peerKey)
          assert.equal(command.payload.catalogView.serviceId, session.serviceId)
        }
      } catch { errors++ }
    })
    try {
      try {
        const response = await page.goto(session.viewer.url)
        assert.ok(response && response.status() === 200 && (response.headers()['content-security-policy'] || '').includes("script-src 'self'"))
        await page.locator('.login-panel input').fill(session.viewer.code)
        await page.locator('.login-panel button[type="submit"]').click()
        await page.locator('.workspace').waitFor()
        session.viewer.code = ''
      } catch { throw Error('Real catalog browser authentication failed; private details withheld') }
      await use({ session, counts: () => ({ lists, connects, errors }) })
    } finally {
      session.viewer.code = ''
      await drainRoutesBeforeCleanup(page, async () => { await page.close() })
      assert.equal(errors, 0, 'Actual catalog UI requests or runtime failed')
    }
  },
})
