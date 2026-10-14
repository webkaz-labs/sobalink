import { lstat, writeFile } from 'node:fs/promises'
import { isAbsolute, join } from 'node:path'
import { nearbyText, nearbyNextAction } from '../src/nearby-i18n.ts'
import { translator } from '../src/i18n.ts'
import { lanTranslator } from '../src/lan-i18n.ts'

// Illustrations only. Never contribute to the browser/Core acceptance receipt.
// No capture is taken after this page has issued its first setup mutation.
export const SCREENSHOTS = Object.freeze([
  Object.freeze({ file: 'nearby-initial-en-375.png', locale: 'en', viewportWidth: 375, viewportHeight: 844 }),
  Object.freeze({ file: 'nearby-initial-ja-320.png', locale: 'ja', viewportWidth: 320, viewportHeight: 844 }),
])
export const screenshotManifest = () => ({ schema: 1, kind: 'nearby-initial-ui', acceptanceEvidence: false,
  checkpoint: 'before-first-setup-start', framing: 'visible-dialog-interior-side-crop-12px', masking: 'none', identities: 'synthetic-only', captures: SCREENSHOTS })
const requireSafe = value => { if (!value) throw Error('Initial screenshot safety check failed') }

// Positive initial-view text boundary, including collapsed details. Unexpected
// person/device names cannot be exported merely because they lack a token prefix.
export function initialText(locale) {
  const keys = ['title', 'intro', 'back', 'alias', 'aliasHint', 'scope', 'chooseScope', 'refreshScopes',
    'scopeDetails', 'localAddress', 'prefix', 'interfaceIndex', 'ports', 'bootstrapPort', 'relayPort',
    'hostChoice', 'hostHint', 'allowHost', 'otherHost', 'consent', 'consentHint', 'stopImpact', 'start',
    'trustSeparate', 'details', 'reason', 'nextAction']
  return [...keys.map(key => nearbyText(locale, key)), translator(locale)('chooseNetwork'),
    lanTranslator(locale)('stopApplication'), nearbyNextAction(locale, 'choose_scope'),
    '127.0.0.1 · 127.0.0.0/8', '127.0.0.1', '127.0.0.0/8', '127.0.0.1:48445',
    'synthetic-loopback', '1', '·', ':', '—']
}

// Crop only the outer 12px side strips, where rounded corners could reveal
// the page behind the dialog. Verify an opaque background and the actual radius;
// do not mask, repaint, resize, or change any UI content or application state.
export function opaqueDialogClip(dialog) {
  const style = dialog.ownerDocument.defaultView.getComputedStyle(dialog), rect = dialog.getBoundingClientRect()
  if (!/^rgb\(\d+, \d+, \d+\)$/.test(style.backgroundColor) || style.opacity !== '1' ||
      style.backgroundImage !== 'none' || style.backgroundClip !== 'border-box' ||
      ['borderTopLeftRadius', 'borderTopRightRadius', 'borderBottomLeftRadius', 'borderBottomRightRadius']
        .some(key => !/^\d+(?:\.\d+)?px$/.test(style[key]) || parseFloat(style[key]) > 12)) return null
  const x = Math.ceil(rect.x + 12), y = Math.ceil(rect.y)
  return { x, y, width: Math.floor(rect.right - 12) - x, height: Math.floor(rect.bottom) - y }
}

// Evaluated inside the real dialog. Return only a boolean, never page text.
export function safeInitialDialog(dialog, expected) {
  const setup = dialog.querySelector('.nearby-setup')
  if (!dialog.open || !setup || dialog.querySelectorAll('.nearby-setup').length !== 1 ||
      dialog.querySelector('textarea, .nearby-qr, .invitation-card, img, iframe, canvas, a, time')) return false
  const inputs = [...dialog.querySelectorAll('input')], selects = [...dialog.querySelectorAll('select')]
  if (inputs.length !== 6 || selects.length !== 1) return false
  const [alias, bootstrap, relay, host, otherHost, consent] = inputs
  if (alias.type !== 'text' || alias.value !== expected.alias || bootstrap.type !== 'number' || bootstrap.value !== '48445' ||
      relay.type !== 'number' || relay.value !== '48443' || host.type !== 'radio' || otherHost.type !== 'radio' ||
      host.checked || otherHost.checked || consent.type !== 'checkbox' || consent.checked || !consent.disabled) return false
  const options = [...selects[0].options]
  if (options.length !== 2 || options[0].textContent !== expected.chooseScope || options[1].textContent !== '127.0.0.1 · 127.0.0.0/8') return false
  const allowed = new Set(expected.allowedText.map(text => text.trim().replace(/\s+/g, ' ')))
  const walker = dialog.ownerDocument.createTreeWalker(dialog, 4)
  while (walker.nextNode()) {
    const text = walker.currentNode.textContent.trim().replace(/\s+/g, ' ')
    if (text && !allowed.has(text)) return false
  }
  return true
}

export function initialScreenshots(directory) {
  const captures = new Map(), attempted = new Set()
  return {
    async capture(device) {
      if (!directory || attempted.has(device.locale)) return
      attempted.add(device.locale)
      try {
        const spec = SCREENSHOTS.find(item => item.locale === device.locale)
        requireSafe(spec && device.alias === (spec.locale === 'en' ? 'Synthetic peer A' : 'Synthetic peer B'))
        requireSafe(!device.commands.some(command => !['setup.scopes', 'setup.status'].includes(command.name)))
        requireSafe(JSON.stringify(device.page.viewportSize()) === JSON.stringify({ width: spec.viewportWidth, height: spec.viewportHeight }))
        const dialog = device.page.getByRole('dialog')
        requireSafe(await dialog.evaluate(safeInitialDialog, { alias: device.alias, chooseScope: spec.locale === 'en' ? 'Choose a network' : 'ネットワークを選択', allowedText: initialText(spec.locale) }))
        await dialog.locator('.modal-body').evaluate(element => { element.scrollTop = 0 })
        const clip = await dialog.evaluate(opaqueDialogClip)
        requireSafe(clip && clip.x >= 0 && clip.y >= 0 && clip.width >= spec.viewportWidth - 56 && clip.width <= spec.viewportWidth - 24 &&
          clip.height >= 128 && clip.x + clip.width <= spec.viewportWidth && clip.y + clip.height <= spec.viewportHeight)
        const bytes = await device.page.screenshot({ type: 'png', clip, animations: 'disabled', caret: 'hide', scale: 'css', timeout: 5000 })
        requireSafe(Buffer.isBuffer(bytes) && bytes.length > 0 && bytes.length <= 2 * 1024 * 1024)
        captures.set(spec.file, bytes)
      } catch { /* An illustration failure cannot change acceptance evidence. */ }
    },
    async stage() {
      try {
        if (!directory || captures.size !== SCREENSHOTS.length) return false
        requireSafe(isAbsolute(directory))
        const info = await lstat(directory)
        requireSafe(info.isDirectory() && !info.isSymbolicLink() && (info.mode & 0o077) === 0 && info.uid === process.getuid())
        for (const spec of SCREENSHOTS) await writeFile(join(directory, spec.file), captures.get(spec.file), { flag: 'wx', mode: 0o600 })
        await writeFile(join(directory, 'screenshots.json'), JSON.stringify(screenshotManifest()) + '\n', { flag: 'wx', mode: 0o600 })
        return true
      } catch { return false } finally { captures.clear() }
    },
  }
}
