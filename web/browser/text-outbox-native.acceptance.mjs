import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { readFile, rename, writeFile } from 'node:fs/promises'
import { dirname, join } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { test, expect } from './transfer-native-fixtures.mjs'
import { textPhase } from './text-native-reporter.mjs'

// Every record comes from the actual authenticated Core. Only one successful
// local commit response is lost, after the server has accepted the request.
test('text-native-exact-review-lost-commit-original-status-en-ja', async ({ page, app }) => {
  const phase = value => textPhase(test, value)
  await phase('session')
  const { session } = app
  assert.equal(session.textOutbox, true)
  const body = '  synthetic browser text: "exact"\n合成テキスト\n'
  const digest = createHash('sha256').update(body).digest('hex')
  const root = dirname(process.env.SOBA_TRANSFER_BROWSER_SESSION)
  let prepared, admitted, recovered, prepares = 0, commits = 0, checks = 0, drops = 0
  const immutable = record => {
    for (const key of ['requestId', 'outboxId', 'messageId', 'peerId', 'reviewRevision', 'createdAt', 'text', 'textBytes', 'sha256']) assert.equal(record[key], prepared[key])
    assert.equal(Date.parse(record.expiresAt), Date.parse(prepared.expiresAt))
  }
  const checkpoint = async (stage, value) => {
    assert.ok(['review', 'recovered'].includes(stage))
    const path = join(root, `text-browser-${stage}.json`)
    await writeFile(`${path}.tmp`, JSON.stringify(value), { flag: 'wx', mode: 0o600 })
    await rename(`${path}.tmp`, path)
    const until = Date.now() + 30_000
    for (;;) {
      try { assert.equal(await readFile(join(root, `text-browser-${stage}.ok`), 'utf8'), 'verified\n'); return }
      catch (error) { if (error.code !== 'ENOENT') throw error }
      assert.ok(Date.now() < until, 'Actual text Core evidence checkpoint unavailable')
      await delay(25)
    }
  }
  const fits = async () => {
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), true)
    for (const region of [page.getByRole('dialog'), page.locator('.outbox-preparation')]) {
      if (await region.count()) assert.equal(await region.evaluate(element => element.scrollWidth <= element.clientWidth + 1), true)
    }
  }
  await page.route('**/api/command', async route => {
    const command = route.request().postDataJSON()
    if (command.name === 'message.queue.prepare') {
      assert.equal(++prepares, 1)
      assert.match(command.requestId, /^mt1_\d{13}_[a-f0-9]{32}$/)
      assert.deepEqual(command.payload, { processId: session.processId, peerId: session.peerId, text: body, expiresAt: command.payload.expiresAt })
      assert.equal(Date.parse(command.payload.expiresAt) - Number(command.requestId.slice(4, 17)), 3600000)
      const response = await route.fetch(); assert.equal(response.status(), 200)
      prepared = (await response.json()).result
      assert.equal(prepared.version, 1); assert.equal(prepared.kind, 'text_outbox')
      assert.equal(prepared.admission, 'prepared'); assert.equal(prepared.status, 'prepared')
      assert.equal(prepared.requestId, command.requestId); assert.equal(prepared.messageId, command.requestId); assert.equal(prepared.outboxId, command.requestId)
      assert.equal(prepared.peerId, session.peerId); assert.equal(prepared.processId, session.processId)
      assert.equal(prepared.text, body); assert.equal(prepared.textBytes, Buffer.byteLength(body)); assert.equal(prepared.sha256, digest)
      assert.equal(Date.parse(prepared.expiresAt), Date.parse(command.payload.expiresAt))
      assert.equal(prepared.deliveryOutcome, 'not_attempted'); assert.equal(prepared.receipt, undefined)
      await route.fulfill({ response })
    } else if (command.name === 'message.queue.commit') {
      assert.equal(++commits, 1); assert.ok(prepared)
      assert.deepEqual(command.payload, { outboxId: prepared.requestId, processId: session.processId, reviewRevision: prepared.reviewRevision, confirm: true })
      const response = await route.fetch(); assert.equal(response.status(), 200)
      admitted = (await response.json()).result; immutable(admitted); assert.equal(admitted.admission, 'enqueued')
      drops++; await route.abort('failed')
    } else if (command.name === 'message.queue.status') {
      assert.ok(admitted); assert.deepEqual(command.payload, { outboxId: prepared.requestId })
      const response = await route.fetch(); assert.equal(response.status(), 200)
      recovered = (await response.json()).result; immutable(recovered); assert.equal(recovered.admission, 'enqueued')
      checks++; await route.fulfill({ response })
    } else {
      assert.notEqual(command.name, 'message.send')
      assert.ok(!command.name.startsWith('message.queue.') || command.name === 'message.queue.list')
      assert.ok(!command.name.startsWith('outbox.') || command.name === 'outbox.list')
      assert.ok(!command.name.startsWith('transfer.'))
      await route.continue()
    }
  })
  await phase('open-peer')
  if (await page.locator('html').getAttribute('lang') !== 'en') await page.locator('.locale-toggle').click()
  await page.setViewportSize({ width: 375, height: 844 })
  await page.locator('.device-row').filter({ hasText: session.peerName }).click()
  await page.locator('.device-sections').getByRole('button', { name: 'Files & messages', exact: true }).click()
  const mode = page.getByRole('region', { name: 'Text sending mode', exact: true })
  await expect(mode.getByRole('radio', { name: 'Send text now', exact: true })).toBeChecked()
  assert.equal(prepares, 0); assert.equal(commits, 0)
  await phase('queue-mode')
  await mode.getByRole('radio', { name: 'Queue text', exact: true }).check()
  await mode.getByRole('combobox', { name: 'Sending deadline', exact: true }).selectOption('1')
  await page.locator('.composer textarea').fill(body)
  await phase('prepare')
  await page.getByRole('button', { name: 'Review queued text', exact: true }).click()
  await phase('review-exact')
  const review = page.getByRole('region', { name: 'Review queued text', exact: true })
  await expect(review).toBeVisible()
  assert.equal(await review.locator('.text-queue-body').textContent(), body)
  assert.equal(await review.locator('small.code-value').textContent(), session.peerId)
  assert.equal(await review.locator('.text-queue-review > p').first().locator('strong').textContent(), session.peerName)
  assert.equal(Date.parse(await review.locator('time').getAttribute('datetime')), Date.parse(prepared.expiresAt))
  await phase('review-consent')
  const consent = review.getByRole('checkbox', { name: 'I allow this device to keep this exact text locally and send it to the selected device until the deadline while the sending agent is running.', exact: true })
  const commit = review.getByRole('button', { name: 'Queue this text', exact: true })
  await expect(consent).not.toBeChecked(); await expect(commit).toBeDisabled()
  await expect(page.locator('.composer textarea')).toBeDisabled()
  assert.deepEqual([prepares, commits, checks], [1, 0, 0])
  await phase('review-fit'); await fits()
  await phase('review-checkpoint'); await checkpoint('review', { prepared })
  await phase('commit')
  await consent.check(); await expect(commit).toBeEnabled(); await commit.click()
  await phase('lost-response')
  await expect(review.getByText('The result is unconfirmed. Keep this text and its original request ID. Check that request before any new send.', { exact: true })).toBeVisible()
  assert.deepEqual([prepares, commits, drops, checks], [1, 1, 1, 0])
  await expect(review.getByRole('button', { name: 'Queue this text', exact: true })).toHaveCount(0)
  await expect(page.locator('.composer textarea')).toHaveValue(body)
  await phase('recover')
  await review.getByRole('button', { name: 'Check original text request', exact: true }).click()
  await expect(review).toHaveCount(0)
  await expect(page.locator('.composer textarea')).toHaveValue('')
  assert.deepEqual([prepares, commits, drops, checks], [1, 1, 1, 1])
  await phase('recovery-checkpoint')
  await checkpoint('recovered', { prepared, admitted, recovered, prepares, commits, drops, checks })
  // Core has now proved a real receiver save and matching receipt. Inspect that
  // same request through the actual outbox UI, in both supported narrow layouts.
  await phase('outbox-open')
  await mode.getByRole('button', { name: 'View text in outbox', exact: true }).click()
  const card = page.locator('[data-text-request-id]')
  await expect(card).toHaveCount(1)
  await expect(card).toHaveAttribute('data-text-request-id', prepared.requestId)
  await phase('outbox-status-en')
  await expect(card).toContainText('Work status: Receiver saved the text · Delivery evidence: Receiver-saved confirmation')
  await phase('detail-request')
  await card.getByRole('button', { name: 'Inspect original text', exact: true }).click()
  await phase('detail-exact')
  assert.equal(await card.locator('.text-queue-body').textContent(), body)
  await phase('detail-receipt-en')
  await expect(card).toContainText('A receiver-saved receipt confirms storage on the selected device. It is not a read receipt.')
  await phase('detail-fit-en'); await fits()
  await phase('escape'); await page.keyboard.press('Escape')
  await phase('locale-ja')
  await page.locator('.locale-toggle').click(); await page.setViewportSize({ width: 320, height: 844 })
  await phase('outbox-open-ja')
  await page.locator('.primary-nav').getByRole('button', { name: /^送信待ち/ }).click()
  await expect(card).toHaveCount(1)
  await phase('outbox-status-ja')
  await expect(card).toContainText('処理状況: 相手側で本文を保存済み · 到着の記録: 受信側の保存を確認済み')
  await phase('outbox-receipt-ja')
  await expect(card).toContainText('受信保存の確認は、選んだ相手の端末に保存されたことを示します。既読を示すものではありません。')
  await phase('outbox-fit-ja'); await fits()
  await phase('command-counts')
  assert.deepEqual([prepares, commits, drops, checks], [1, 1, 1, 2])
  await phase('runtime-counts')
  assert.deepEqual(app.counts(), { retries: 0, errors: 0 })
  await phase('result-write')
  await writeFile(join(root, 'text-browser-result.json'), JSON.stringify({ schema: 1, prepares, commits, drops, recoveryChecks: 1, detailChecks: 1, widths: [375, 320], locales: ['en', 'ja'] }), { flag: 'wx', mode: 0o600 })
  await phase('body-complete')
})
