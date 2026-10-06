import { test, expect } from './fixtures.mjs'

// Every card is synthetic. Existing fixture identities are disposable; this
// spec never reads a user profile or the fixture's sealed private route update.
const publicKey = 'b2'.repeat(32)
const pin = 'c3'.repeat(32)
const recipientName = 'Synthetic card recipient'
const encode = card => `soba-card1.${Buffer.from(JSON.stringify(card), 'utf8').toString('base64url')}`
const recipientCard = mode => encode({ version: 1, mode, publicKey, name: recipientName, ...(mode === 'lan'
  ? { relay: { address: '192.0.2.99:443', certificateSHA256: pin } }
  : { endpoint: '192.168.50.3:48444' }) })
const text = locale => locale === 'ja' ? {
  open: '端末カードを交換', close: '端末カードを閉じる', alias: /^共有する表示名/,
  export: '公開カードを書き出す', exported: '公開カードのテキスト', qr: 'ローカル生成のQRコードを付ける', qrAlt: '公開端末カードのQRコード', download: 'カードをテキストファイルに保存',
  input: '公開カードのテキストを貼り付け', file: 'カードのテキストファイルを開く', inspect: '公開カードの内容を確認', review: '相手のカードを確認',
  unverified: '本人確認：未確認', unknown: 'アドレスの鮮度：不明', cancel: 'カードの確認を取り消す', use: '相手を下書きに入力', used: '相手の下書きを入力しました。',
  invalid: '有効な公開端末カードではありません。', tooLarge: '入力の上限1,040 byteを超えています。', wrongMode: 'このカードは別の接続方式用です。',
  recipientKey: '招待する相手の公開ID', recipientName: '相手のデバイス名', createInvitation: '5分間有効な招待を作成', identity: '書出しにはこの方式の既存IDが必要です。',
} : {
  open: 'Exchange device cards', close: 'Close device cards', alias: /^Alias to share/,
  export: 'Export public card', exported: 'Public card text', qr: 'Include a local QR code', qrAlt: 'Public device card QR code', download: 'Save card text file',
  input: 'Paste public card text', file: 'Open card text file', inspect: 'Review public card', review: 'Review recipient card',
  unverified: 'Identity: unverified', unknown: 'Address freshness: unknown', cancel: 'Cancel card review', use: 'Use recipient in draft', used: 'Recipient draft filled.',
  invalid: 'This is not a valid public device card.', tooLarge: 'The card exceeds the 1,040-byte input limit.', wrongMode: 'This card is for the other network mode.',
  recipientKey: 'Recipient’s public ID', recipientName: 'Recipient’s device name', createInvitation: 'Create a 5-minute invitation', identity: 'Export needs an existing identity for this mode.',
}
async function openCards(page, mode, labels) {
  await page.locator('.sidebar-title button').click()
  await page.locator(`dialog input[value="${mode}"]`).check()
  const cards = page.locator('dialog .device-cards')
  await cards.getByRole('button', { name: labels.open, exact: true }).click()
  await expect(cards.locator('.device-card-panel')).toBeVisible()
  return cards
}
async function unchangedState(page) {
  // Return only a comparison string, excluding credentials, paths, messages and
  // runtime diagnostics. Assertion failures never include the snapshot itself.
  return page.evaluate(async () => {
    const response = await fetch('/api/state')
    if (!response.ok) throw new Error('Synthetic fixture state unavailable')
    const state = await response.json()
    return JSON.stringify({ appearanceWrites: (window.__sobaQA?.commands || []).filter(name => name === 'settings.update').length, network: state.settings?.network, lan: state.lan && {
      configured: state.lan.configured, publicKey: state.lan.publicKey, relay: state.lan.relay, policy: state.lan.policy,
    }, directLAN: state.directLAN, peers: state.peers.map(peer => ({ id: peer.id, trusted: peer.trusted, verified: peer.verified, autosave: peer.autosave?.enabled })), services: state.services.length, shares: state.shares.length })
  })
}
async function assertReadOnly(page, before) {
  expect(await unchangedState(page) === before, 'Card actions must preserve saved network scope, identity and trust').toBe(true)
  const unexpected = await page.evaluate(() => (window.__sobaQA?.commands || []).filter(name => ![
    'settings.update', // Test-only appearance selection, before card operations.
    'lan.addresses', 'lan.policy.get', 'device-card.export', 'device-card.inspect',
  ].includes(name)))
  expect(unexpected, 'No invitation, identity, listener, endpoint or trust mutation is allowed').toEqual([])
  expect(await page.evaluate(() => window.__sobaQA?.uploads || 0)).toBe(0)
}
async function assertNarrow(page, cards) {
  await page.setViewportSize({ width: 390, height: 844 })
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true)
  await expect.poll(() => cards.evaluate(element => element.scrollWidth <= element.clientWidth + 1)).toBe(true)
}
async function inspect(page, cards, labels, mode) {
  await cards.getByRole('textbox', { name: labels.input, exact: true }).fill(recipientCard(mode))
  await cards.getByRole('button', { name: labels.inspect, exact: true }).click()
  const review = cards.getByRole('region', { name: labels.review, exact: true })
  await expect(review).toBeVisible()
  await expect(review).toBeFocused()
  await expect(review).toContainText(labels.unverified)
  await expect(review).toContainText(labels.unknown)
  await expect(review).toContainText(publicKey)
  await expect(review).toContainText(recipientName)
  await expect(review.locator('a')).toHaveCount(0)
  return review
}

// This existing fixture prepares an offline synthetic LAN identity before the
// browser starts. Export and inspection below call the actual authenticated Core.
test.describe('public device cards with an existing synthetic LAN identity', () => {
  test.use({ scenario: 'routes' })
  for (const locale of ['en', 'ja']) {
    const labels = text(locale)
    test(`${locale}: explicit LAN alias export, local QR and text download fit a narrow view without mutations`, async ({ page, app }) => {
      await app.appearance(locale, locale === 'ja' ? 'dark' : 'light')
      const before = await unchangedState(page), cards = await openCards(page, 'lan', labels)
      await expect(cards.getByRole('textbox', { name: labels.alias })).toHaveValue('')
      await expect(cards.getByRole('checkbox', { name: labels.qr, exact: true })).not.toBeChecked()
      await expect(cards.getByRole('button', { name: labels.export, exact: true })).toBeDisabled()
      await cards.getByRole('textbox', { name: labels.alias }).fill('Synthetic browser alias')
      await cards.getByRole('checkbox', { name: labels.qr, exact: true }).check()
      await cards.getByRole('button', { name: labels.export, exact: true }).click()
      const exported = cards.getByRole('textbox', { name: labels.exported, exact: true })
      await expect(exported).toHaveValue(/^soba-card1\.[A-Za-z0-9_-]+$/)
      const encoded = await exported.inputValue()
      const value = JSON.parse(Buffer.from(encoded.slice('soba-card1.'.length), 'base64url').toString('utf8'))
      expect(Object.keys(value)).toEqual(['version', 'mode', 'publicKey', 'name'])
      expect(value.mode).toBe('lan'); expect(value.name).toBe('Synthetic browser alias')
      expect(/^[a-f0-9]{64}$/.test(value.publicKey)).toBe(true)
      await app.expectState(state => state.lan?.publicKey === value.publicKey, 'Export reads the existing disposable identity')
      const qr = cards.getByRole('img', { name: labels.qrAlt, exact: true })
      await expect(qr).toBeVisible()
      expect(await qr.evaluate(svg => {
        const size = svg.viewBox.baseVal.width, path = svg.querySelector('path')?.getAttribute('d') || ''
        const cells = [...path.matchAll(/M(\d+) (\d+)h1v1h-1z/g)]
        return size >= 29 && size <= 185 && (size - 29) % 4 === 0 && cells.length > 0 && cells.every(([, x, y]) => Number(x) >= 4 && Number(y) >= 4 && Number(x) < size - 4 && Number(y) < size - 4) && svg.querySelector('rect')?.getAttribute('fill') === '#fff'
      }), 'The displayed bitmap retains its white four-module quiet zone').toBe(true)
      await app.capture(`device-card-export-${locale}-desktop`)
      await assertNarrow(page, cards); await qr.scrollIntoViewIfNeeded()
      await app.capture(`device-card-export-${locale}-390`)
      const downloadEvent = page.waitForEvent('download')
      await cards.getByRole('button', { name: labels.download, exact: true }).click()
      const download = await downloadEvent
      expect(download.suggestedFilename()).toBe('sobalink-lan-device-card.txt')
      const stream = await download.createReadStream(), chunks = []
      expect(stream !== null).toBe(true)
      for await (const chunk of stream) chunks.push(chunk)
      expect(Buffer.concat(chunks).toString('utf8') === encoded, 'Downloaded public text must match the reviewed card').toBe(true)
      await download.delete()
      expect(await app.count('device-card.export')).toBe(1)
      expect(await app.count('device-card.inspect')).toBe(0)
      await cards.getByRole('button', { name: labels.close, exact: true }).click()
      await cards.getByRole('button', { name: labels.open, exact: true }).click()
      await expect(cards.getByRole('textbox', { name: labels.alias })).toHaveValue('')
      await expect(cards.getByRole('textbox', { name: labels.exported, exact: true })).toHaveCount(0)
      await assertReadOnly(page, before)
    })
    test(`${locale}: bounded LAN text/file review recovers from invalid input, cancels and fills only an inert recipient draft`, async ({ page, app }) => {
      await app.appearance(locale, locale === 'ja' ? 'dark' : 'light')
      const before = await unchangedState(page), cards = await openCards(page, 'lan', labels)
      const input = cards.getByRole('textbox', { name: labels.input, exact: true })
      const recipient = page.getByRole('textbox', { name: labels.recipientKey, exact: true })
      const name = page.getByRole('textbox', { name: labels.recipientName, exact: true })
      await expect(recipient).toHaveValue(''); await expect(name).toHaveValue('')
      for (const [invalid, message] of [
        ['soba-lan1.synthetic-not-an-invitation', labels.invalid],
        ['soba-card1.A', labels.invalid],
        [recipientCard('direct-lan'), labels.wrongMode],
      ]) {
        await input.fill(invalid); await cards.getByRole('button', { name: labels.inspect, exact: true }).click()
        await expect(cards.getByRole('alert')).toContainText(message); await expect(input).toHaveValue('')
        await expect(cards.getByRole('alert')).not.toContainText(invalid)
      }
      await input.fill('x'.repeat(1041)); await expect(cards.getByRole('alert')).toContainText(labels.tooLarge); await expect(input).toHaveValue('')
      expect(await app.count('device-card.inspect')).toBe(0)
      const file = cards.locator('input[type=file]')
      await file.setInputFiles({ name: 'synthetic-oversized.txt', mimeType: 'text/plain', buffer: Buffer.alloc(1041, 'x') })
      await expect(cards.getByRole('alert')).toContainText(labels.tooLarge); await expect(input).toHaveValue('')
      await file.setInputFiles({ name: 'synthetic-invalid-utf8.txt', mimeType: 'text/plain', buffer: Buffer.from([255]) })
      await expect(cards.getByRole('alert')).toContainText(labels.invalid); await expect(input).toHaveValue('')
      await file.setInputFiles({ name: 'synthetic-device-card.txt', mimeType: 'text/plain', buffer: Buffer.from(recipientCard('lan')) })
      await expect(input).toHaveValue(recipientCard('lan'))
      expect(await app.count('device-card.inspect')).toBe(0)
      await cards.getByRole('button', { name: labels.inspect, exact: true }).click()
      const review = cards.getByRole('region', { name: labels.review, exact: true })
      await expect(review).toBeVisible(); await expect(review).toContainText(labels.unverified); await expect(review).toContainText(labels.unknown)
      await expect(review).toContainText('192.0.2.99:443'); await expect(review).toContainText(pin)
      await expect(recipient).toHaveValue(''); await expect(name).toHaveValue('')
      await assertNarrow(page, cards)
      // Do not capture while the private-input gate contains even synthetic text.
      await review.getByRole('button', { name: labels.cancel, exact: true }).click()
      await expect(review).toHaveCount(0); await expect(recipient).toHaveValue(''); await expect(name).toHaveValue('')
      await inspect(page, cards, labels, 'lan')
      await cards.getByRole('button', { name: labels.use, exact: true }).click()
      await expect(cards.getByRole('status')).toContainText(labels.used)
      await expect(input).toHaveValue(''); await expect(review).toHaveCount(0)
      await expect(recipient).toHaveValue(publicKey); await expect(name).toHaveValue(recipientName)
      // The fixture has no running pairing listener; card application must not
      // make the separate Create invitation action ready or invoke it itself.
      await expect(page.getByRole('button', { name: labels.createInvitation, exact: true })).toBeDisabled()
      await recipient.scrollIntoViewIfNeeded(); await app.capture(`device-card-draft-${locale}-390`)
      expect(await app.count('device-card.inspect')).toBe(2)
      await page.keyboard.press('Escape'); await expect(page.getByRole('dialog')).toHaveCount(0)
      await page.locator('.sidebar-title button').click()
      await expect(page.getByRole('textbox', { name: labels.recipientKey, exact: true })).toHaveValue(publicKey)
      await expect(page.getByRole('textbox', { name: labels.recipientName, exact: true })).toHaveValue(recipientName)
      await assertReadOnly(page, before)
    })
  }
})

test.describe('direct LAN public cards before identity setup', () => {
  test.use({ scenario: 'offline' })
  for (const locale of ['en', 'ja']) {
    test(`${locale}: direct LAN inspection is read-only before setup and mode navigation discards the review`, async ({ page, app }) => {
      const labels = text(locale)
      await app.appearance(locale, locale === 'ja' ? 'dark' : 'light')
      const before = await unchangedState(page), cards = await openCards(page, 'direct-lan', labels)
      await cards.getByRole('textbox', { name: labels.alias }).fill('Synthetic browser alias')
      await expect(cards.getByRole('button', { name: labels.export, exact: true })).toBeDisabled()
      await expect(cards).toContainText(labels.identity)
      const review = await inspect(page, cards, labels, 'direct-lan')
      await expect(review).toContainText('192.168.50.3:48444')
      await assertNarrow(page, cards)
      await review.getByRole('button', { name: labels.cancel, exact: true }).click()
      await expect(review).toHaveCount(0)
      await inspect(page, cards, labels, 'direct-lan')
      await page.locator('dialog input[value="lan"]').check()
      await page.locator('dialog input[value="direct-lan"]').check()
      await expect(page.locator('.device-card-review')).toHaveCount(0)
      await cards.getByRole('button', { name: labels.open, exact: true }).click()
      await expect(cards.getByRole('textbox', { name: labels.input, exact: true })).toHaveValue('')
      await inspect(page, cards, labels, 'direct-lan')
      await cards.getByRole('button', { name: labels.use, exact: true }).click()
      await expect(cards.getByRole('status')).toContainText(labels.used)
      await expect(cards.getByRole('textbox', { name: labels.input, exact: true })).toHaveValue('')
      await app.capture(`device-card-direct-before-setup-${locale}-390`)
      expect(await app.count('device-card.export')).toBe(0)
      expect(await app.count('device-card.inspect')).toBe(3)
      await assertReadOnly(page, before)
      // This fixture cannot render the direct-LAN invitation form until setup.
      // Its exact key/name draft write is covered by component integration tests;
      // this browser case verifies the explicit action, notice and no mutations.
    })
  }
})
