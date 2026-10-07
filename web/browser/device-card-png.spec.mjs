import { createHash } from 'node:crypto'
import { crc32 } from 'node:zlib'
import { readFile } from 'node:fs/promises'
import { test, expect } from './fixtures.mjs'
import { CAPTURE_FORBIDDEN_SELECTOR, PRIVATE_VALUE_SELECTOR, privateControlsAreEmpty } from './fixture-safety.mjs'
import { CSP_PROBE_ROOT, createCspPolicyFixture } from './csp-policy-fixture.mjs'
const fixtureRoot = new URL('./png-fixtures/', import.meta.url)
const fixtures = JSON.parse(await readFile(new URL('manifest.json', fixtureRoot), 'utf8'))
const manifest = JSON.parse(await readFile(new URL('../dist/png-worker-manifest.json', import.meta.url), 'utf8'))
const text = locale => locale === 'ja' ? {
  open: '端末カードを交換', close: '端末カードを閉じる', png: '端末カードのPNGを開く', input: '公開カードのテキストを貼り付け', inspect: '公開カードの内容を確認', review: '相手のカードを確認', cancel: 'PNGの読取りを取り消す',
} : { open: 'Exchange device cards', close: 'Close device cards', png: 'Open a device-card PNG', input: 'Paste public card text', inspect: 'Review public card', review: 'Review recipient card', cancel: 'Cancel PNG reading' }
async function open(page, mode, labels) {
  await page.locator('.sidebar-title button').click()
  await page.locator(`dialog input[value="${mode}"]`).check()
  const cards = page.locator('dialog .device-cards')
  await cards.getByRole('button', { name: labels.open, exact: true }).click()
  return cards
}
async function bytes(fixture) {
  const data = await readFile(new URL(fixture.file, fixtureRoot))
  expect(createHash('sha256').update(data).digest('hex')).toBe(fixture.pngSHA256)
  return data
}
const withChunk = (image, name, payload) => {
  const chunk = Buffer.alloc(12 + payload.length); chunk.writeUInt32BE(payload.length); chunk.write(name, 4, 'ascii'); payload.copy(chunk, 8); chunk.writeUInt32BE(crc32(chunk.subarray(4, 8 + payload.length)), 8 + payload.length)
  return Buffer.concat([image.subarray(0, 33), chunk, image.subarray(33)])
}
const select = (cards, labels, buffer) => cards.getByLabel(labels.png, { exact: true }).setInputFiles({ name: 'synthetic-card.png', mimeType: 'image/png', buffer })
const commands = page => page.evaluate(() => (window.__sobaQA?.commands || []).filter(name => name.startsWith('device-card.') || /invite|trust|pair|connect|start/.test(name)))
test.describe('bounded local device-card PNG import', () => {
  test.use({ scenario: 'routes' })
  for (const locale of ['en', 'ja']) for (const mode of ['lan', 'direct-lan']) {
    test(`${locale} ${mode}: representative exact PNG bytes fill text before separate Review`, async ({ page, app }) => {
      await app.appearance(locale, 'light')
      const labels = text(locale), cards = await open(page, mode, labels)
      const external = []
      page.on('request', request => { if (new URL(request.url()).origin !== new URL(page.url()).origin) external.push(request.url()) })
      const baseline = await commands(page)
      for (const fixture of fixtures.filter(value => value.mode === mode)) {
        await select(cards, labels, await bytes(fixture))
        await expect(cards.getByRole('textbox', { name: labels.input, exact: true })).toHaveValue(fixture.expected)
        await expect(cards.getByRole('region', { name: labels.review, exact: true })).toHaveCount(0)
        expect(await commands(page)).toEqual(baseline)
      }
      expect(external).toEqual([])
      expect(await page.evaluate(() => window.__sobaQA?.uploads || 0)).toBe(0)
      await cards.getByRole('button', { name: labels.inspect, exact: true }).click()
      await expect(cards.getByRole('region', { name: labels.review, exact: true })).toBeFocused()
      expect((await commands(page)).slice(baseline.length)).toEqual(['device-card.inspect'])
      await page.setViewportSize({ width: 390, height: 844 })
      await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true)
    })
  }
  test('malformed, oversized and two-identical-code PNGs never select a recipient', async ({ page, app }) => {
    await app.appearance('en', 'light')
    const labels = text('en'), cards = await open(page, 'lan', labels), baseline = await commands(page)
    await select(cards, labels, Buffer.from('not a PNG'))
    await expect(cards.getByRole('alert')).toContainText('could not be read')
    await select(cards, labels, Buffer.alloc(8 * 1024 * 1024 + 1))
    await expect(cards.getByRole('alert')).toContainText('budget')
    const fixture = fixtures.find(value => value.id === 'lan_hint_false_minimum_size256')
    await select(cards, labels, withChunk(await bytes(fixture), 'acTL', Buffer.alloc(8)))
    await expect(cards.getByRole('alert')).toContainText('Animated PNG')
    await select(cards, labels, withChunk(await bytes(fixture), 'iCCP', Buffer.from([255])))
    await expect(cards.getByRole('textbox', { name: labels.input, exact: true })).toHaveValue(fixture.expected)
    const pair = await page.evaluate(async encoded => {
      const image = new Image(); image.src = `data:image/png;base64,${encoded}`; await image.decode()
      const canvas = document.createElement('canvas'); canvas.width = 576; canvas.height = 288
      const context = canvas.getContext('2d'); context.fillStyle = 'white'; context.fillRect(0, 0, 576, 288)
      context.drawImage(image, 0, 16); context.drawImage(image, 320, 16)
      return canvas.toDataURL('image/png').split(',')[1]
    }, (await bytes(fixture)).toString('base64'))
    await select(cards, labels, Buffer.from(pair, 'base64'))
    await expect(cards.getByRole('alert')).toContainText('More than one QR code')
    await expect(cards.getByRole('textbox', { name: labels.input, exact: true })).toHaveValue('')
    expect(await commands(page)).toEqual(baseline)
  })
  test('a saved screenshot of the generated public QR reads exactly and needs a separate Review', async ({ page, app }) => {
    await app.appearance('en', 'light')
    const labels = text('en'), cards = await open(page, 'lan', labels)
    await cards.getByRole('textbox', { name: /^Alias to share/ }).fill('Synthetic screenshot card')
    await cards.getByRole('checkbox', { name: 'Include a local QR code', exact: true }).check()
    await cards.getByRole('button', { name: 'Export public card', exact: true }).click()
    const output = cards.getByRole('textbox', { name: 'Public card text', exact: true })
    await expect(output).not.toHaveValue('')
    const expected = await output.inputValue()
    await expect(page.locator('.workspace')).toBeVisible()
    await expect(page.locator(CAPTURE_FORBIDDEN_SELECTOR)).toHaveCount(0)
    expect(await page.locator(PRIVATE_VALUE_SELECTOR).evaluateAll(privateControlsAreEmpty)).toBe(true)
    const screenshot = await cards.locator('.device-card-qr').screenshot()
    const baseline = await commands(page)
    await select(cards, labels, screenshot)
    await expect(cards.getByRole('textbox', { name: labels.input, exact: true })).toHaveValue(expected)
    expect(await commands(page)).toEqual(baseline)
  })
  test('cancel, edit, close and reselection discard delayed asset completion', async ({ page, app }) => {
    await app.appearance('en', 'light')
    const labels = text('en'), cards = await open(page, 'lan', labels)
    const buffer = await bytes(fixtures.find(value => value.mode === 'lan'))
    for (const boundary of ['cancel', 'edit', 'close', 'reselect']) {
      let release, intercepted
      const held = new Promise(resolve => { release = resolve }), seen = new Promise(resolve => { intercepted = resolve })
      const routeHandler = async route => { intercepted(); await held; await route.continue().catch(() => {}) }
      await page.route('**/*.wasm', routeHandler)
      await select(cards, labels, buffer); await seen
      if (boundary === 'cancel') await cards.getByRole('button', { name: labels.cancel, exact: true }).click()
      if (boundary === 'edit') await cards.getByRole('textbox', { name: labels.input, exact: true }).fill('new draft')
      if (boundary === 'close') { await cards.getByRole('button', { name: labels.close, exact: true }).click(); await cards.getByRole('button', { name: labels.open, exact: true }).click() }
      if (boundary === 'reselect') await select(cards, labels, Buffer.from('replacement invalid PNG'))
      release(); await page.unroute('**/*.wasm', routeHandler)
      if (boundary === 'reselect') await expect(cards.getByRole('alert')).toBeVisible()
      await expect(cards.getByRole('textbox', { name: labels.input, exact: true })).toHaveValue(boundary === 'edit' ? 'new draft' : '')
    }
  })
  test('shipped reader bytes are pinned and its fetched CSP enforces isolated synthetic policy probes', async ({ page, app, context }) => {
    await app.appearance('en', 'light')
    const documentResponse = await context.request.get(page.url())
    const documentCSP = documentResponse.headers()['content-security-policy']
    expect(documentCSP).not.toContain('wasm-unsafe-eval')
    const workerURL = new URL(`/${manifest.path}`, page.url()).href
    const workerResponse = await context.request.get(workerURL)
    const workerCSP = workerResponse.headers()['content-security-policy']
    expect(workerCSP).toContain("script-src 'self' 'wasm-unsafe-eval'")
    expect(createHash('sha256').update(await workerResponse.body()).digest('hex')).toBe(manifest.sha256)
    const unknown = await context.request.get(new URL('/assets/device-card-worker-unknown.js', page.url()).href)
    expect(unknown.headers()['content-security-policy']).not.toContain('wasm-unsafe-eval')
    // Ordinary external scripts avoid DevTools evaluation privileges. This is
    // policy-enforcement coverage using the actual fetched CSP values; the real
    // reader's hash above and PNG decoding in the other tests are separate proof.
    // No product script or response policy is replaced.
    const probe = await context.newPage()
    const responses = createCspPolicyFixture({ documentCSP, workerCSP })
    const origin = new URL(page.url()).origin
    const probePattern = `${origin}${CSP_PROBE_ROOT}**`
    const probeRoute = async route => {
      const response = responses.get(new URL(route.request().url()).pathname)
      if (response) await route.fulfill(response)
      else await route.abort()
    }
    try {
      await context.route(probePattern, probeRoute)
      await probe.goto(`${origin}${CSP_PROBE_ROOT}index.html`)
      // Evaluation only reads the completed result; all tested operations run
      // in ordinary document load / worker message event tasks.
      await expect.poll(() => probe.evaluate(() => window.__sobaCspPolicyResult), { timeout: 15_000 }).toEqual({
        documentChecks: { jsBlocked: true, wasmBlocked: true },
        workerChecks: { wasmAllowed: true, jsBlocked: true, fetchBlocked: true, socketBlocked: true, childBlocked: true },
      })
    } finally { await probe.close(); await context.unroute(probePattern, probeRoute) }
  })
})
