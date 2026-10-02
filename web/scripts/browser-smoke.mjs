import assert from 'node:assert/strict'
import { mkdir, readFile, stat } from 'node:fs/promises'
import { chromium } from 'playwright-core'

// A private harness session uses actual Go HTTP/session/CSRF and two in-process peers.
// The optional fixture mode only verifies frontend behavior. Neither mode proves real-device enrollment.
const flag = process.argv.indexOf('--session-file')
const sessionFile = flag >= 0 ? process.argv[flag + 1] : process.env.SOBA_E2E_SESSION_FILE
let session
if (sessionFile) {
  const info = await stat(sessionFile)
  assert.equal(info.mode & 0o077, 0, 'harness session file must be private')
  session = JSON.parse(await readFile(sessionFile, 'utf8'))
  const url = new URL(session.url)
  assert.equal(url.protocol, 'http:')
  assert.equal(url.hostname, '127.0.0.1', 'harness must use numeric loopback')
  assert.equal(typeof session.code, 'string')
}
const base = session?.url || process.env.SOBA_WEB_URL || 'http://127.0.0.1:4177'
const output = process.env.SOBA_SCREENSHOT_DIR || '/tmp/sobalink-web-screenshots'
await mkdir(output, { recursive: true })
const browser = await chromium.launch({ executablePath: process.env.CHROMIUM_PATH || undefined, headless: true, args: ['--no-sandbox'] })
const context = await browser.newContext({ viewport: { width: 1440, height: 960 }, locale: 'en-US', colorScheme: 'light' })
const page = await context.newPage()
const consoleErrors = []
const commands = []
page.on('pageerror', error => consoleErrors.push(error.message))
page.on('console', entry => { if (entry.type() === 'error' && /content security policy|refused to/i.test(entry.text())) consoleErrors.push('Content Security Policy violation') })
page.on('request', request => { if (new URL(request.url()).pathname === '/api/command') commands.push(request.postDataJSON()) })
const peer = { id: 'p-studio', name: 'Studio', networks: ['tailnet'], online: true, verified: true, trusted: true, bridge: true, path: 'direct', address: '100.64.0.2', fingerprint: 'test-identity-fingerprint' }
const fixture = {
  csrfToken: 'synthetic-test-token', self: { name: 'This device', status: 'online', networks: ['tailnet', 'lan'] },
  peers: [peer, { ...peer, id: 'p-notebook', name: 'Notebook', networks: ['lan'], path: 'direct', trusted: false }, { ...peer, id: 'p-service', name: 'Service host', bridge: false, path: 'relay', trusted: false }],
  messages: [
    { id: 'm1', peerId: peer.id, direction: 'incoming', text: 'Here are the notes and the images for review.', status: 'received', createdAt: '2026-10-02T10:02:00Z' },
    { id: 'm2', peerId: peer.id, direction: 'outgoing', text: 'Thanks. I’ll take a look after the files arrive.', status: 'sent', createdAt: '2026-10-02T10:03:00Z' },
  ],
  transfers: [{ id: 't1', peerId: peer.id, direction: 'incoming', name: 'Design notes', entries: [{ path: 'notes.txt', size: 3000, kind: 'file' }, { path: 'images', size: 0, kind: 'directory' }, { path: 'images/reference.png', size: 241000, kind: 'file' }], totalBytes: 244000, completedBytes: 0, status: 'offered', createdAt: '2026-10-02T10:04:00Z' }],
  services: [], shares: [], availableServices: [{ id: 'observed-service', peerId: peer.id, name: 'Web app', network: 'tcp', ports: '8080', expiresAt: '2026-10-02T11:00:00Z', status: 'active', application: 'unverified' }],
  settings: { network: 'tailnet', receiveDirectory: '/tmp/received' },
}
let locked = true
if (!session) await page.route('**/api/**', async route => {
  const path = new URL(route.request().url()).pathname
  if (path === '/api/session') { locked = false; await route.fulfill({ json: { csrfToken: fixture.csrfToken } }); return }
  if (locked) { await route.fulfill({ status: 401, json: { code: 'unauthenticated' } }); return }
  if (path === '/api/state') { await route.fulfill({ json: fixture }); return }
  await route.fulfill({ json: { ok: true } })
})
const pageResponse = await page.goto(base)
if (session) {
  const csp = pageResponse.headers()['content-security-policy'] || ''
  assert.match(csp, /script-src 'self'/, 'real Go page must enforce script CSP')
  assert.match(csp, /style-src 'self'/, 'real Go page must enforce style CSP')
  assert.equal(csp.includes("'unsafe-inline'"), false)
}
await page.getByLabel('Local access code').fill(session?.code || 'local-test-code')
await page.getByRole('button', { name: 'Open sobalink', exact: true }).click()
await page.getByRole('button', { name: /Studio/ }).click()
await page.getByRole('button', { name: 'Open device details' }).click()
await page.screenshot({ path: `${output}/conversation-en.png`, fullPage: true })
assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, 'desktop must not overflow horizontally')
const input = page.getByRole('textbox', { name: 'Write a message…' })
await input.fill('Browser draft')
await input.press('Enter')
assert.equal(commands.length, 0, 'plain Enter must not send')
await input.dispatchEvent('keydown', { key: 'Enter', code: 'Enter', ctrlKey: true, isComposing: true })
assert.equal(commands.length, 0, 'IME composition must not send')
await page.getByRole('complementary', { name: 'Device details' }).getByRole('button', { name: 'Close device details' }).click()
await page.getByRole('button', { name: 'Connect to a service', exact: true }).click()
await page.getByRole('textbox', { name: /^Connection name/ }).fill('preview')
await page.getByRole('textbox', { name: 'Ports', exact: true }).fill('8000-8064')
assert.equal(await page.getByRole('button', { name: 'Start connection', exact: true }).isDisabled(), true)
await page.screenshot({ path: `${output}/service-validation-en.png`, fullPage: true })
await page.keyboard.press('Escape')
assert.equal(await page.getByRole('dialog').count(), 0, 'Escape closes the native dialog')
assert.equal(await page.getByRole('button', { name: 'Connect to a service', exact: true }).evaluate(el => el === document.activeElement), true, 'dialog restores focus')
if (session) {
  await input.fill('Browser check: explicit send')
  const acknowledgement = page.waitForResponse(response =>
    new URL(response.url()).pathname === '/api/command' &&
    response.request().method() === 'POST' &&
    response.request().postDataJSON()?.name === 'message.send')
  await page.getByRole('button', { name: 'Send message', exact: true }).click()
  const accepted = await acknowledgement
  assert.equal(accepted.status(), 200, 'the real message command must be acknowledged')
  assert.equal((await accepted.json()).ok, true, 'the real message command must succeed')
  await page.locator('.timeline .message-bubble p').filter({ hasText: /^Browser check: explicit send$/ }).waitFor()
  await page.waitForFunction(() => document.querySelector('.composer textarea')?.value === '', null, { timeout: 10000 })
  assert.equal(commands.filter(item => item.name === 'message.send').length, 1, 'explicit send must issue one real command')
  assert.equal(await input.inputValue(), '', 'acknowledged send clears its draft')
  await input.fill('Browser draft')
}
await page.setViewportSize({ width: 390, height: 844 })
await page.screenshot({ path: `${output}/conversation-en-mobile.png`, fullPage: true })
assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, 'English mobile must not overflow horizontally')
await page.setViewportSize({ width: 1440, height: 960 })
await page.getByRole('button', { name: 'Preferences', exact: true }).click()
await page.getByRole('dialog').getByRole('combobox', { name: 'Language', exact: true }).selectOption('ja')
await page.getByRole('button', { name: 'ダーク', exact: true }).click()
await page.getByRole('button', { name: '閉じる', exact: true }).click()
await page.screenshot({ path: `${output}/conversation-ja-dark-desktop.png`, fullPage: true })
await page.setViewportSize({ width: 390, height: 844 })
await page.screenshot({ path: `${output}/conversation-ja-dark-mobile.png`, fullPage: true })
assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, 'mobile must not overflow horizontally')
await page.getByRole('button', { name: 'デバイス一覧へ', exact: true }).click()
await page.screenshot({ path: `${output}/devices-ja-dark-mobile.png`, fullPage: true })
await page.getByRole('button', { name: /Studio/ }).click()
assert.match(await page.getByRole('textbox', { name: 'メッセージを入力…' }).inputValue(), /Browser draft/, 'back preserves unsent draft')
await page.getByRole('button', { name: '詳細を表示', exact: true }).click()
await page.screenshot({ path: `${output}/details-ja-dark-mobile.png`, fullPage: true })
assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, 'detail panel must not overflow horizontally')
assert.deepEqual(consoleErrors, [], 'browser console must have no uncaught errors')
await browser.close()
console.log(`Browser smoke checks passed (${session ? 'actual Go HTTP with in-process peers' : 'synthetic frontend fixture'}). Sanitized screenshots: ${output}`)
