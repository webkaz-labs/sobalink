import { test, expect } from '@playwright/test'
// Rendered synthetic UI checks only. Native enrollment and network acceptance are separate gates.
const labels = {
  en: { removed: 'Removed devices (advanced)', removedLoad: 'Read current removed keys', removedSelect: 'Select this exact removed key', removedConsent: 'I reviewed this complete key and generation and allow only a fresh pairing attempt', reenroll: 'Allow fresh pairing for this key', alias: 'Name to show nearby', receive: 'Accept connection requests', consent: 'Allow this setup within the displayed range and duration', start: 'Find nearby devices', select: 'Review this device', save: 'Allow this saved connection and the ongoing host role shown above', ack: 'I understand the first-use limit and approve remembering this exact device key', request: 'Request this connection and save after approval', approve: 'Approve and save this exact connection', status: 'Check original session', reject: 'Reject this request', duration: 'Session duration (seconds)' },
  ja: { removed: '削除した端末（詳細設定）', removedLoad: '現在の削除済みの鍵を確認', removedSelect: 'この正確な削除済みの鍵を選ぶ', removedConsent: 'この鍵の全桁と世代番号を確認し、新しいペアリングの試行だけを許可する', reenroll: 'この鍵での新しいペアリングを許可', alias: '近くの端末に表示する名前', receive: '接続依頼を受け付ける', consent: '表示された範囲と期間でセットアップを許可する', start: '近くの端末を探す', select: 'この端末を確認', save: '上記の接続の保存と、継続するホスト役を許可する', ack: '初回の確認の限界を理解し、この相手の鍵を記憶することを許可する', request: 'この接続を依頼し、相手の承認後に保存', approve: 'この接続内容を承認して保存する', status: '元のセッションを確認', reject: 'この依頼を拒否', duration: 'セッションの有効期間（秒）' },
}
async function fits(page) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true)
  const controls = page.locator('.nearby-setup button:visible')
  for (const control of await controls.all()) expect((await control.boundingBox()).height).toBeGreaterThanOrEqual(44)
}
for (const [locale, width] of [['ja', 320], ['en', 320], ['ja', 375], ['en', 375], ['ja', 1280], ['en', 1280]]) {
  test(`${locale}-${width}-request-keyboard-layout`, async ({ page }) => {
    const c = labels[locale]; await page.setViewportSize({ width, height: 844 })
    await page.goto(`http://127.0.0.1:5178/browser/pairing-v2-app/?locale=${locale}`)
    // Advanced reenrollment remains collapsed until an explicit review.
    await expect(page.getByRole('button', { name: c.removedLoad, exact: true })).not.toBeVisible()
    await page.getByText(c.removed, { exact: true }).focus(); await page.keyboard.press('Enter')
    await page.getByRole('button', { name: c.removedLoad, exact: true }).click()
    await expect(page.getByText('8'.repeat(64), { exact: true })).toBeVisible()
    await page.getByRole('button', { name: c.removedSelect, exact: true }).click()
    await expect(page.getByLabel(c.removedConsent, { exact: true })).not.toBeChecked()
    await expect(page.getByRole('button', { name: c.reenroll, exact: true })).toBeDisabled()
    await page.getByLabel(c.removedConsent, { exact: true }).focus(); await page.keyboard.press('Space')
    await fits(page)
    await page.evaluate(() => { document.documentElement.style.fontSize = '200%' }); await fits(page)
    await page.evaluate(() => { document.documentElement.style.fontSize = '' })
    await page.getByRole('button', { name: c.reenroll, exact: true }).focus(); await page.keyboard.press('Enter')
    await expect.poll(() => page.evaluate(() => window.pairingFixture.commands)).toEqual([{ name: 'setup.reenroll', payload: { peerKey: '8'.repeat(64), expectedGeneration: 7, consent: true } }])
    await expect(page.getByLabel(c.removedConsent, { exact: true })).toHaveCount(0)
    await page.getByText(c.removed, { exact: true }).click()
    await page.getByLabel(c.alias, { exact: false }).fill('Peer A')
    await page.getByLabel(c.duration, { exact: true }).fill('1200')
    await page.getByLabel(c.consent, { exact: true }).focus(); await page.keyboard.press('Space')
    await fits(page)
    await page.getByRole('button', { name: c.start, exact: true }).focus(); await page.keyboard.press('Enter')
    await page.getByRole('button', { name: c.select, exact: true }).focus(); await page.keyboard.press('Enter')
    await expect(page.getByText('4'.repeat(64), { exact: true })).toBeVisible()
    await expect(page.getByText('10.44.0.3:48443', { exact: true })).toBeVisible()
    await expect(page.getByRole('button', { name: c.request, exact: true })).toBeDisabled()
    await page.getByLabel(c.save, { exact: true }).focus(); await page.keyboard.press('Space')
    await page.getByLabel(c.ack, { exact: true }).focus(); await page.keyboard.press('Space')
    await fits(page)
    // Simulate 200% text scaling in the same constrained width.
    await page.evaluate(() => { document.documentElement.style.fontSize = '200%' })
    await fits(page)
    await page.getByRole('button', { name: c.request, exact: true }).focus(); await page.keyboard.press('Enter')
    await expect.poll(() => page.evaluate(() => window.pairingFixture.commands.map(c => c.name))).toEqual(['setup.reenroll', 'setup.start', 'setup.inspect-target', 'setup.request'])
  })
  test(`${locale}-${width}-stable-incoming-card-no-focus-steal`, async ({ page }) => {
    const c = labels[locale]; await page.setViewportSize({ width, height: 844 })
    await page.goto(`http://127.0.0.1:5178/browser/pairing-v2-app/?locale=${locale}&case=receive`)
    const ack = page.getByLabel(c.ack, { exact: true }); await ack.focus(); await page.keyboard.press('Space')
    await page.getByLabel(c.save, { exact: true }).check()
    const approve = page.getByRole('button', { name: c.approve, exact: true }); await approve.focus()
    // Ordinary identical polls must retain the actual focused button node.
    await page.waitForTimeout(1700); await expect(approve).toBeFocused(); await fits(page)
    await page.evaluate(() => { const next = window.pairingFixture.get(); next.requestId = '7'.repeat(64); next.request.requestId = next.requestId; window.pairingFixture.replace(next) })
    await page.waitForTimeout(1700)
    await expect(approve).toBeDisabled(); await expect(page.getByText('6'.repeat(64), { exact: true })).toBeVisible()
    expect(await page.evaluate(() => window.pairingFixture.commands)).toEqual([])
  })
}
