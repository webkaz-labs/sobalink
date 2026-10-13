import assert from 'node:assert/strict'
import { test, expect } from './nearby-native-fixtures.mjs'

// Deliberately independent text expectations for both real rendered languages.
const labels = {
  en: {
    network: 'Set up network', open: 'Connect to a nearby device', back: 'Back to connection options',
    alias: 'Name to show nearby', scope: 'Network to use', ports: 'Connection ports', bootstrapPort: 'Setup port', relayPort: 'Relay port',
    host: 'Allow this device to host the selected connection', consent: 'Allow discovery and setup on this network for 120 seconds', start: 'Find nearby devices',
    local: 'This device’s setup information', peer: 'Other device’s setup information', import: 'Review copied device',
    unavailable: 'Nearby discovery is unavailable here. QR or copied setup information can still be used. Exchanging an address does not remove network restrictions.',
    review: 'Compare and approve this connection', fingerprint: 'Full connection fingerprint', otherFingerprint: 'Paste the other device’s full fingerprint',
    save: 'Allow this saved connection and the ongoing host role shown above', verified: 'I compared the complete fingerprint and the connection details on both devices',
    approve: 'Approve and save this exact connection', connected: 'Connection confirmed by both devices',
    trust: 'This approval does not change receiving trust. Review receiving trust separately in the device’s settings. Autosave and service sharing require their own permission.',
  },
  ja: {
    network: 'ネットワークを設定', open: '近くの端末と接続する', back: '接続方法の選択へ戻る',
    alias: '近くの端末に表示する名前', scope: '使用するネットワーク', ports: '接続ポート', bootstrapPort: 'セットアップ用ポート', relayPort: '中継用ポート',
    host: 'この端末が選択した接続のホストになることを許可する', consent: 'このネットワークで120秒間の探索とセットアップを許可する', start: '近くの端末を探す',
    local: 'この端末のセットアップ情報', peer: '相手の端末のセットアップ情報', import: 'コピーした相手を確認',
    unavailable: '近隣探索を利用できません。QRまたはコピーした接続情報は引き続き使用できます。アドレスの交換だけではネットワーク制限を解除できません。',
    review: '接続を照合して承認', fingerprint: '接続フィンガープリント全体', otherFingerprint: '相手のフィンガープリント全体を貼り付け',
    save: '上記の接続の保存と、継続するホスト役を許可する', verified: '両端末でフィンガープリント全体と接続内容を照合した',
    approve: 'この接続内容を承認して保存する', connected: '両端末で接続を確認しました',
    trust: 'この承認はメッセージやファイルの受信の信頼を変更しません。端末の設定で受信の信頼を別途確認してください。自動保存やサービス共有にはそれぞれ別の許可が必要です。',
  },
}
const commandResult = async (page, name, action) => {
  const response = page.waitForResponse(response => {
    if (new URL(response.url()).pathname !== '/api/command' || response.request().method() !== 'POST') return false
    return response.request().postDataJSON().name === name
  })
  await action()
  const result = await response
  assert.equal(result.status(), 200)
  const body = await result.json()
  assert.equal(body.ok, true)
  return body.result
}
const fits = async page => {
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), true, 'Page overflowed narrow viewport')
  assert.equal(await page.getByRole('dialog').evaluate(element => element.scrollWidth <= element.clientWidth + 1), true, 'Setup required horizontal scrolling')
}

// All setup commands and rendered results use real Core HTTP handlers. There
// are no fabricated states, prebuilt plans, invitations or trust grants. Only
// one actual successful approval response is dropped to exercise recovery.
test('nearby-native-manual-bilateral-consent-cancel-join-en-ja', async ({ app }) => {
  const { devices, checkpoint, phase } = app
  const [a, b] = devices
  const open = async device => {
    const c = labels[device.locale], { page } = device
    if (!(await page.getByRole('dialog').count())) await page.getByRole('button', { name: c.network, exact: true }).first().click()
    await page.getByRole('button', { name: c.open, exact: true }).click()
    await expect(page.getByLabel(c.scope, { exact: true })).toBeVisible()
  }
  const start = async device => {
    const c = labels[device.locale], { page } = device
    await page.getByRole('textbox', { name: new RegExp(`^${c.alias}`) }).fill(device.alias)
    await expect(page.getByLabel(c.scope, { exact: true }).locator('option')).toHaveCount(2)
    await page.getByLabel(c.scope, { exact: true }).selectOption({ label: '127.0.0.1 · 127.0.0.0/8' })
    await page.getByText(c.ports, { exact: true }).click()
    await page.getByLabel(c.bootstrapPort, { exact: true }).fill(String(device.bootstrapPort))
    await page.getByLabel(c.relayPort, { exact: true }).fill(String(device.relayPort))
    await page.getByRole('checkbox', { name: c.host, exact: true }).check()
    const button = page.getByRole('button', { name: c.start, exact: true })
    await expect(button).toBeDisabled()
    await page.getByRole('checkbox', { name: c.consent, exact: true }).check()
    await fits(page)
    const result = await commandResult(page, 'setup.start', () => button.click())
    assert.equal(result.discovery.state, 'unavailable')
    assert.equal(result.paired, false); assert.equal(result.connected, false); assert.equal(result.trusted, false)
    await expect(page.getByText(c.unavailable, { exact: true })).toBeVisible()
    await expect(page.getByRole('textbox', { name: c.local, exact: true })).toBeVisible()
    device.session = result
    return result
  }
  await phase('open-a'); await open(a)
  await phase('start-a')
  const cancelled = await start(a)
  await phase('cancel-a')
  const cancelResult = await commandResult(a.page, 'setup.cancel', () => a.page.getByRole('button', { name: labels.en.back, exact: true }).click())
  assert.equal(cancelResult.sessionId, cancelled.sessionId)
  assert.ok(['cancelling', 'cancelled'].includes(cancelResult.state))
  await expect(a.page.locator('.nearby-setup')).toHaveCount(0)
  await checkpoint('cancelled') // Go verifies actual join and listener release.
  await phase('restart-a'); await open(a); await start(a)
  assert.notEqual(a.session.sessionId, cancelled.sessionId)
  await phase('open-b'); await open(b)
  await phase('start-b'); await start(b)
  const contacts = await Promise.all(devices.map(d => d.page.getByRole('textbox', { name: labels[d.locale].local, exact: true }).inputValue()))
  for (const [index, device] of devices.entries()) {
    const c = labels[device.locale], { page } = device
    await phase(index === 0 ? 'manual-a' : 'manual-b')
    await page.getByLabel(c.peer, { exact: true }).fill(contacts[1 - index])
    const result = await commandResult(page, 'setup.manual', () => page.getByRole('button', { name: c.import, exact: true }).click())
    assert.equal(result.sessionId, device.session.sessionId)
    assert.equal(result.selected.sessionId, devices[1 - index].session.sessionId)
  }
  for (const [index, device] of devices.entries()) { await phase(index === 0 ? 'plan-a' : 'plan-b'); await expect(device.page.getByRole('region', { name: labels[device.locale].review, exact: true })).toBeVisible({ timeout: 20_000 }) }
  const fingerprints = await Promise.all(devices.map(d => d.page.getByRole('textbox', { name: labels[d.locale].fingerprint, exact: true }).inputValue()))
  assert.match(fingerprints[0], /^sobalink-setup-verify-v1:[a-f0-9]{64}$/)
  assert.equal(fingerprints[0], fingerprints[1])
  for (const [index, device] of devices.entries()) {
    const c = labels[device.locale], { page } = device
    await phase(index === 0 ? 'fingerprint-a' : 'fingerprint-b')
    const review = page.getByRole('region', { name: c.review, exact: true }), approve = review.getByRole('button', { name: c.approve, exact: true })
    await expect(approve).toBeDisabled()
    await review.getByLabel(c.otherFingerprint, { exact: true }).fill(`${fingerprints[1 - index].slice(0, -1)}${fingerprints[0].endsWith('0') ? '1' : '0'}`)
    await expect(review.getByRole('checkbox', { name: c.save, exact: true })).toBeDisabled()
    await expect(approve).toBeDisabled()
    await review.getByLabel(c.otherFingerprint, { exact: true }).fill(fingerprints[1 - index])
    await review.getByRole('checkbox', { name: c.verified, exact: true }).check()
    await expect(approve).toBeDisabled()
    await review.getByRole('checkbox', { name: c.save, exact: true }).check()
    await review.getByRole('checkbox', { name: c.verified, exact: true }).uncheck()
    await expect(approve).toBeDisabled()
    await review.getByRole('checkbox', { name: c.verified, exact: true }).check()
    await expect(approve).toBeEnabled()
    await expect(page.getByText(c.trust, { exact: true })).toBeVisible()
    await fits(page)
  }
  await checkpoint('review')
  await phase('approve-a')
  let approvalDrops = 0, acceptedApproval
  let acceptedResolve
  const accepted = new Promise(resolve => { acceptedResolve = resolve })
  await a.page.route('**/api/command', async route => {
    const command = route.request().postDataJSON()
    if (command.name !== 'setup.approve') { await route.continue(); return }
    approvalDrops++; assert.equal(approvalDrops, 1)
    const response = await route.fetch(); assert.equal(response.status(), 200)
    const body = await response.json(); assert.equal(body.ok, true)
    acceptedApproval = body.result
    assert.equal(acceptedApproval.sessionId, a.session.sessionId)
    assert.equal(acceptedApproval.localApproved, true)
    await route.abort('failed'); acceptedResolve()
  })
  await a.page.getByRole('button', { name: labels.en.approve, exact: true }).click()
  await accepted
  await expect(a.page.getByText('The last response was not confirmed. Check the original session before trying another action.', { exact: true })).toBeVisible()
  await expect(a.page.getByRole('button', { name: labels.en.approve, exact: true })).toBeDisabled()
  await expect(a.page.getByRole('button', { name: labels.en.start, exact: true })).toHaveCount(0)
  await phase('recover-a')
  const approvedA = await commandResult(a.page, 'setup.status', () => a.page.getByRole('button', { name: 'Check original session', exact: true }).click())
  assert.equal(approvedA.sessionId, a.session.sessionId)
  assert.equal(approvedA.plan.fingerprint, acceptedApproval.plan.fingerprint)
  assert.equal(approvedA.plan.revision, acceptedApproval.plan.revision)
  assert.equal(approvalDrops, 1)
  assert.equal(approvedA.localApproved, true); assert.equal(approvedA.remoteApproved, false)
  assert.equal(approvedA.paired, false); assert.equal(approvedA.connected, false)
  await checkpoint('one-approved') // Real remote receipt, still no backend/pair.
  await phase('approve-b')
  await commandResult(b.page, 'setup.approve', () => b.page.getByRole('button', { name: labels.ja.approve, exact: true }).click())
  for (const device of devices) {
    await phase(device === a ? 'connected-a' : 'connected-b')
    await expect(device.page.getByText(labels[device.locale].connected, { exact: true })).toBeVisible({ timeout: 30_000 })
    await fits(device.page)
    const starts = device.commands.filter(command => command.name === 'setup.start')
    assert.equal(starts.length, device === a ? 2 : 1)
    const approvals = device.commands.filter(command => command.name === 'setup.approve')
    assert.equal(approvals.length, 1)
    assert.deepEqual(approvals[0].payload, { sessionId: device.session.sessionId, revision: approvedA.plan.revision, fingerprint: fingerprints[0].slice('sobalink-setup-verify-v1:'.length), verified: true })
  }
  await checkpoint('connected') // Actual hello, signed receipt, saved pairs, no trust.
})
