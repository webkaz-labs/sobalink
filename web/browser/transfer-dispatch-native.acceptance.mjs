import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { writeFile } from 'node:fs/promises'
import { dirname, join } from 'node:path'
import { test, expect } from './transfer-native-fixtures.mjs'

test('dispatch-native-selected-recipients-lost-commit-en-ja', async ({ page, app }) => {
  const { session } = app
  assert.equal(session.dispatch, true)
  const selected = session.recipients.map(peer => peer.id)
  const content = Buffer.from('synthetic-browser-dispatch-content')
  let prepared, committed, uploads = 0, commits = 0, checks = 0
  const state = async () => {
    const response = await page.request.get(new URL('/api/state', session.viewer.url).href)
    assert.equal(response.status(), 200)
    return response.json()
  }
  // Observe genuine authenticated server replies. The only simulated fault is
  // loss of the first successful commit response on its way back to the UI.
  await page.route('**/api/upload-dispatch', async route => {
    uploads++
    assert.equal(uploads, 1)
    const response = await route.fetch()
    assert.equal(response.status(), 200)
    const body = await response.json()
    assert.equal(body.ok, true)
    prepared = body.result
    assert.equal(prepared.status, 'prepared')
    assert.equal(prepared.processId, session.processId)
    assert.deepEqual(prepared.peerIds, selected)
    assert.equal(prepared.entries.length, 1)
    assert.equal(prepared.entries[0].path, 'browser-dispatch.txt')
    assert.equal(prepared.entries[0].sha256, createHash('sha256').update(content).digest('hex'))
    assert.equal(((await state()).transfers || []).length, 0, 'Preparation must not create outgoing children')
    await route.fulfill({ response })
  })
  await page.route('**/api/command', async route => {
    const command = route.request().postDataJSON()
    if (command.name === 'transfer.dispatch.commit') {
      commits++
      assert.equal(commits, 1)
      assert.ok(prepared)
      assert.deepEqual(command.payload, { processId: session.processId, preparationId: prepared.preparationId, reviewRevision: prepared.reviewRevision, confirm: true })
      const response = await route.fetch()
      assert.equal(response.status(), 200)
      const body = await response.json()
      assert.equal(body.ok, true)
      committed = body.result
      assert.equal(committed.status, 'committed')
      assert.equal(committed.dispatchId, prepared.preparationId)
      assert.deepEqual(committed.children.map(child => child.peerId), selected)
      assert.equal(new Set(committed.children.map(child => child.transferId)).size, 2)
      await route.abort('failed')
    } else if (command.name === 'transfer.dispatch.status') {
      checks++
      assert.equal(checks, 1)
      assert.deepEqual(command.payload, { processId: session.processId, preparationId: prepared.preparationId })
      const response = await route.fetch()
      assert.equal(response.status(), 200)
      assert.deepEqual((await response.json()).result, committed)
      await route.fulfill({ response })
    } else {
      assert.notEqual(command.name, 'transfer.dispatch.discard')
      await route.continue()
    }
  })
  if (await page.locator('html').getAttribute('lang') !== 'en') await page.locator('.locale-toggle').click()
  await page.locator('.device-row').filter({ hasText: session.peerName }).click()
  await page.locator('.device-sections').getByRole('button', { name: 'Files & messages', exact: true }).click()
  await page.locator('input[type="file"]:not([webkitdirectory])').setInputFiles({ name: 'browser-dispatch.txt', mimeType: 'text/plain', buffer: content })
  const review = page.getByRole('region', { name: 'Review your batch', exact: true })
  const recipients = review.getByRole('group', { name: 'Recipients', exact: true })
  await expect(recipients.getByRole('checkbox')).toHaveCount(2)
  await expect(recipients.getByRole('checkbox', { name: new RegExp(session.recipients[0].name) })).toBeChecked()
  const second = recipients.getByRole('checkbox', { name: new RegExp(session.recipients[1].name) })
  await expect(second).not.toBeChecked()
  await second.check()
  await expect(second).toBeChecked()
  await expect(review).toContainText('browser-dispatch.txt')
  assert.equal(((await state()).transfers || []).length, 0)
  await review.getByRole('button', { name: 'Send batch', exact: true }).click()
  await expect(page.getByText('Sending was not confirmed. Check this request before sending again.', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Send batch', exact: true })).toBeDisabled()
  assert.deepEqual([uploads, commits, checks], [1, 1, 0])
  // Recover the same request explicitly in the other supported locale and a
  // narrow viewport; changing presentation must not prepare or send again.
  await page.locator('.locale-toggle').click()
  await page.setViewportSize({ width: 390, height: 844 })
  await expect(page.getByText('送信の受付を確認できませんでした。再送する前に、この要求を確認してください。', { exact: true })).toBeVisible()
  await expect(page.getByRole('region', { name: '送信内容を確認', exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'この要求を確認', exact: true }).click()
  const summary = page.getByRole('region', { name: '選択した端末への送達状況', exact: true })
  await expect(summary).toBeVisible()
  for (const peer of session.recipients) await expect(summary).toContainText(peer.name)
  await expect(page.getByRole('region', { name: '送信内容を確認', exact: true })).toHaveCount(0)
  const outgoing = (await state()).transfers
  assert.equal(outgoing.length, 2)
  assert.deepEqual(outgoing.map(row => row.id).sort(), committed.children.map(child => child.transferId).sort())
  for (const row of outgoing) assert.equal(row.dispatchId, committed.dispatchId)
  assert.deepEqual([uploads, commits, checks], [1, 1, 1])
  assert.deepEqual(app.counts(), { retries: 0, errors: 0 })
  await writeFile(join(dirname(process.env.SOBA_TRANSFER_BROWSER_SESSION), 'dispatch-browser-result.json'), JSON.stringify({ prepared, committed }), { flag: 'wx', mode: 0o600 })
})
