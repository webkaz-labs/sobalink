import assert from 'node:assert/strict'
import { mkdtemp, mkdir, readFile, readdir, rm, symlink } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { JSDOM } from 'jsdom'
import { initialScreenshots, safeInitialDialog, initialText, opaqueDialogClip, SCREENSHOTS, screenshotManifest } from './nearby-native-screenshots.mjs'

// DOM/export unit tests only; these never claim to be real browser screenshots.
function fixture(locale = 'en') {
  const spec = SCREENSHOTS.find(value => value.locale === locale)
  const alias = locale === 'en' ? 'Synthetic peer A' : 'Synthetic peer B'
  const chooseScope = locale === 'en' ? 'Choose a network' : 'ネットワークを選択'
  const dom = new JSDOM(`<dialog open><div class="modal-body"><section class="nearby-setup"><input value="${alias}"><select><option>${chooseScope}</option><option>127.0.0.1 · 127.0.0.0/8</option></select><input type="number" value="48445"><input type="number" value="48443"><input type="radio"><input type="radio"><input type="checkbox" disabled></section></div></dialog>`)
  const dialog = dom.window.document.querySelector('dialog')
  dialog.style.cssText = 'background-color: rgb(255, 255, 255); opacity: 1; background-image: none; background-clip: border-box; border-top-left-radius: 12px; border-top-right-radius: 12px; border-bottom-left-radius: 12px; border-bottom-right-radius: 12px;'
  dialog.getBoundingClientRect = () => ({ x: 8, y: 8, right: spec.viewportWidth - 8, bottom: 836 })
  let screenshots = 0
  const device = { locale, alias, commands: [{ name: 'setup.scopes' }, { name: 'setup.status' }], page: {
    viewportSize: () => ({ width: spec.viewportWidth, height: spec.viewportHeight }),
    getByRole: role => { assert.equal(role, 'dialog'); return {
      evaluate: (fn, argument) => fn(dialog, argument),
      locator: selector => ({ evaluate: fn => fn(dialog.querySelector(selector)) }),
    } },
    screenshot: options => { assert.deepEqual(options, { type: 'png', clip: { x: 20, y: 8, width: spec.viewportWidth - 40, height: 828 }, animations: 'disabled', caret: 'hide', scale: 'css', timeout: 5000 }); screenshots++; return Buffer.from('unit-test-only') },
  } }
  return { dom, dialog, device, expected: { alias, chooseScope, allowedText: initialText(locale) }, screenshots: () => screenshots }
}
for (const locale of ['en', 'ja']) test(`only the untouched synthetic initial form is eligible: ${locale}`, () => {
  const fixtureValue = fixture(locale), { dom, dialog, expected } = fixtureValue
  try {
    assert.equal(safeInitialDialog(dialog, expected), true)
    for (const html of ['<textarea>private</textarea>', '<figure class="nearby-qr"></figure>', '<div class="invitation-card"></div>', '<time></time>', '<img>', '<a href="https://example.invalid">link</a>', '<input type="hidden">']) {
      const node = dom.window.document.createElement('div'); node.innerHTML = html; dialog.append(node)
      assert.equal(safeInitialDialog(dialog, expected), false); node.remove()
    }
    for (const text of ['sobalink-setup-contact-v1:private', 'sobalink-setup-verify-v1:private', 'https://example.invalid', '/tmp/private', 'Unexpected device owner']) {
      const node = dom.window.document.createTextNode(text); dialog.append(node)
      assert.equal(safeInitialDialog(dialog, expected), false); node.remove()
    }
    const alias = dialog.querySelector('input'); alias.value = 'other'; assert.equal(safeInitialDialog(dialog, expected), false); alias.value = expected.alias
    const host = dialog.querySelector('input[type=radio]'); host.checked = true; assert.equal(safeInitialDialog(dialog, expected), false); host.checked = false
    const option = dialog.querySelectorAll('option')[1]; option.textContent = '192.0.2.1'; assert.equal(safeInitialDialog(dialog, expected), false)
  } finally { dom.window.close() }
})
test('opaque crop excludes rounded corner background without modifying the UI', () => {
  const { dom, dialog } = fixture()
  try {
    assert.deepEqual(opaqueDialogClip(dialog), { x: 20, y: 8, width: 335, height: 828 })
    for (const [key, unsafe, safe] of [['backgroundColor', 'transparent', 'rgb(255, 255, 255)'], ['opacity', '0.5', '1'], ['borderTopLeftRadius', '13px', '12px']]) {
      dialog.style[key] = unsafe; assert.equal(opaqueDialogClip(dialog), null); dialog.style[key] = safe
    }
  } finally { dom.window.close() }
})
test('capture is optional, literal, once per locale, and never after mutation', async () => {
  const root = await mkdtemp(join(tmpdir(), 'nearby-ui-unit-')), a = fixture(), b = fixture('ja')
  try {
    const disabled = initialScreenshots(); await disabled.capture(a.device); assert.equal(a.screenshots(), 0)
    const rejected = initialScreenshots(root); await rejected.capture({ ...a.device, commands: [{ name: 'setup.start' }] })
    await rejected.capture(a.device); assert.equal(a.screenshots(), 0); assert.equal(await rejected.stage(), false)
    const images = initialScreenshots(root); await images.capture(a.device); await images.capture(a.device); await images.capture(b.device)
    assert.equal(a.screenshots(), 1); assert.equal(b.screenshots(), 1); assert.equal(await images.stage(), true)
    assert.deepEqual((await readdir(root)).sort(), ['screenshots.json', ...SCREENSHOTS.map(spec => spec.file)].sort())
    assert.deepEqual(JSON.parse(await readFile(join(root, 'screenshots.json'), 'utf8')), screenshotManifest())
    assert.equal(await images.stage(), false)
  } finally { a.dom.window.close(); b.dom.window.close(); await rm(root, { recursive: true, force: true }) }
})
test('unsafe capture or staging emits no raw details and cannot supply acceptance', async () => {
  const root = await mkdtemp(join(tmpdir(), 'nearby-ui-unit-')), a = fixture(), b = fixture('ja')
  try {
    const target = join(root, 'target'), link = join(root, 'link'); await mkdir(target, { mode: 0o700 }); await symlink(target, link)
    const images = initialScreenshots(link); await images.capture(a.device); await images.capture(b.device)
    assert.equal(await images.stage(), false); assert.deepEqual(await readdir(target), [])
    const failing = initialScreenshots(target); await failing.capture({ ...a.device, alias: 'Unexpected identity' }); await failing.capture(b.device)
    assert.equal(await failing.stage(), false); assert.deepEqual(await readdir(target), [])
    assert.equal(screenshotManifest().acceptanceEvidence, false)
  } finally { a.dom.window.close(); b.dom.window.close(); await rm(root, { recursive: true, force: true }) }
})
test('capture timing and ordinary acceptance assertions remain separate', async () => {
  const source = await readFile(new URL('./nearby-setup-native.acceptance.mjs', import.meta.url), 'utf8')
  assert.ok(source.indexOf('await captureInitial(device)') < source.indexOf("const result = await commandResult(page, 'setup.start'"))
  for (const stage of ['cancelled', 'review', 'one-approved', 'connected']) assert.ok(source.includes(`await checkpoint('${stage}')`))
  const config = await readFile(new URL('../playwright.nearby-native.config.mjs', import.meta.url), 'utf8')
  assert.ok(!config.includes('screenshot:')); assert.ok(!config.includes('chromiumSandbox'))
})
