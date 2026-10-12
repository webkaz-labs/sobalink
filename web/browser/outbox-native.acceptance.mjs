import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { rename, writeFile } from 'node:fs/promises'
import { dirname, join } from 'node:path'
import { test, expect } from './transfer-native-fixtures.mjs'

// Actual Core processes provide every state, content and receipt. Only a
// successful admission response is dropped; the browser never mocks delivery.
test('outbox-native-offline-reload-original-receipt-en-ja', async ({ page, app }) => {
  const { session } = app
  assert.equal(session.outbox, true)
  const selected = session.recipients.map(peer => peer.id), content = Buffer.from('synthetic-browser-outbox-content')
  let prepared, admitted, uploads = 0, commits = 0, checks = 0
  const state = async () => {
    const response = await page.request.get(new URL('/api/state', session.viewer.url).href)
    assert.equal(response.status(), 200); return response.json()
  }
  const fits = async () => {
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), true, 'Page overflowed narrow viewport')
    const dialog = page.getByRole('dialog')
    if (await dialog.count()) assert.equal(await dialog.evaluate(element => element.scrollWidth <= element.clientWidth + 1), true, 'Outbox required horizontal scrolling')
  }
  await page.route('**/api/outbox/prepare', async route => {
    uploads++; assert.equal(uploads, 1)
    const response = await route.fetch(); assert.equal(response.status(), 200)
    prepared = (await response.json()).result
    assert.match(prepared.requestId, /^ob1_\d{13}_[a-f0-9]{32}$/); assert.equal(prepared.outboxId, prepared.requestId)
    assert.ok(Date.parse(prepared.expiresAt) <= Number(prepared.requestId.slice(4, 17)) + 7 * 86400000)
    assert.equal(prepared.admission, 'prepared'); assert.equal(prepared.processId, session.processId)
    assert.deepEqual(prepared.peerIds, selected); assert.equal(prepared.entries.length, 1)
    assert.equal(prepared.entries[0].path, 'browser-outbox.txt')
    assert.equal(prepared.entries[0].sha256, createHash('sha256').update(content).digest('hex'))
    assert.equal(((await state()).transfers || []).length, 0)
    await route.fulfill({ response })
  })
  await page.route('**/api/command', async route => {
    const command = route.request().postDataJSON()
    if (command.name === 'outbox.commit') {
      commits++; assert.equal(commits, 1); assert.ok(prepared)
      assert.deepEqual(command.payload, { processId: session.processId, outboxId: prepared.outboxId, reviewRevision: prepared.reviewRevision, confirm: true })
      const response = await route.fetch(); assert.equal(response.status(), 200); admitted = (await response.json()).result
      assert.equal(admitted.admission, 'enqueued'); assert.equal(admitted.outboxId, prepared.outboxId)
      assert.deepEqual(admitted.children.map(c => c.deliveryId), prepared.children.map(c => c.deliveryId))
      assert.equal(admitted.deliveryOutcome, 'not_attempted'); await route.abort('failed')
    } else if (command.name === 'outbox.status') {
      checks++; assert.deepEqual(command.payload, { requestId: prepared.requestId })
      const response = await route.fetch(); assert.equal(response.status(), 200)
      const record = (await response.json()).result
      assert.equal(record.outboxId, admitted.outboxId); assert.deepEqual(record.children.map(c => c.deliveryId), admitted.children.map(c => c.deliveryId))
      await route.fulfill({ response })
    } else { assert.notEqual(command.name, 'outbox.discard'); await route.continue() }
  })
  if (await page.locator('html').getAttribute('lang') !== 'en') await page.locator('.locale-toggle').click()
  await page.setViewportSize({ width: 375, height: 844 })
  const open = page.locator('.primary-nav').getByRole('button', { name: /^Outbox/ })
  await open.click()
  await expect(page.getByRole('dialog')).toBeVisible(); await fits()
  await page.keyboard.press('Escape'); await expect(open).toBeFocused()
  await page.locator('.device-row').filter({ hasText: session.peerName }).click()
  await page.locator('.device-sections').getByRole('button', { name: 'Files & messages', exact: true }).click()
  await expect(page.locator('.composer textarea')).toBeDisabled()
  await page.getByRole('radio', { name: 'Send now', exact: true }).check()
  await page.getByRole('radio', { name: 'Queue for later', exact: true }).check()
  await expect(page.locator('.composer textarea')).toBeDisabled()
  await page.locator('input[type="file"]:not([webkitdirectory])').setInputFiles({ name: 'browser-outbox.txt', mimeType: 'text/plain', buffer: content })
  const review = page.getByRole('region', { name: 'Review your batch', exact: true })
  await review.getByRole('checkbox', { name: new RegExp(session.recipients[1].name) }).check()
  await review.getByRole('button', { name: 'Prepare queue review', exact: true }).click()
  const queue = review.getByRole('button', { name: /^Queue until/ })
  await expect(queue).toBeVisible(); assert.equal(commits, 0); await fits()
  await queue.click()
  await expect(page.getByText('Result not confirmed. Check the original request before starting another send.', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Check original request', exact: true }).click()
  await expect(page.getByText('Queued on this device. Delivery is not yet confirmed.', { exact: true })).toBeVisible()
  assert.equal(checks, 1)
  await page.locator('.locale-toggle').click(); await page.setViewportSize({ width: 320, height: 844 })
  await page.reload(); await page.locator('.workspace').waitFor()
  const openJapanese = page.locator('.primary-nav').getByRole('button', { name: /^送信待ち/ })
  await openJapanese.click(); await expect(page.getByRole('dialog')).toContainText('この端末の送信待ちに登録しました。相手への到着はまだ確認できていません。')
  await fits(); await page.keyboard.press('Escape'); await expect(openJapanese).toBeFocused(); await openJapanese.click()
  assert.deepEqual([uploads, commits, checks], [1, 1, 1])
  // The parent fixture now restarts the real receiving processes and grants
  // one explicit acceptance per original delivery, through normal Core APIs.
  const marker = join(dirname(process.env.SOBA_TRANSFER_BROWSER_SESSION), 'outbox-browser-admitted.json')
  await writeFile(`${marker}.tmp`, JSON.stringify({ prepared, admitted }), { flag: 'wx', mode: 0o600 })
  await rename(`${marker}.tmp`, marker)
  await expect.poll(async () => {
    const response = await page.request.post(new URL('/api/command', session.viewer.url).href, { headers: { 'X-CSRF-Token': (await state()).csrfToken, Origin: new URL(session.viewer.url).origin }, data: { requestId: `browser-observe-${Date.now()}`, name: 'outbox.status', payload: { outboxId: admitted.outboxId } } })
    assert.equal(response.status(), 200)
    const record = (await response.json()).result
    assert.deepEqual(record.children.map(c => c.deliveryId), admitted.children.map(c => c.deliveryId))
    return record.deliveryOutcome
  }, { timeout: 30_000 }).toBe('confirmed')
  await page.getByRole('button', { name: '更新', exact: true }).click()
  await expect(page.getByRole('dialog')).toContainText('処理の状態: 相手の端末に保存済み · 到着の確認状況: 保存を確認済み')
  await fits(); assert.deepEqual(app.counts(), { retries: 0, errors: 0 })
  await writeFile(join(dirname(process.env.SOBA_TRANSFER_BROWSER_SESSION), 'outbox-browser-result.json'), JSON.stringify({ schema: 1, prepared, admitted, widths: [375, 320], locales: ['en', 'ja'] }), { flag: 'wx', mode: 0o600 })
})
