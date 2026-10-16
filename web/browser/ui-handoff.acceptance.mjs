// No screenshots, traces, videos, HARs, raw errors or URL/token output.
// Run only through TestUILaunchRealBrowser, against isolated fictional state.
import { chromium } from 'playwright'
import { createInterface } from 'node:readline'
const lines = createInterface({ input: process.stdin })[Symbol.asyncIterator]()
const read = async () => JSON.parse((await lines.next()).value)
const requireSafe = (condition) => { if (!condition) throw new Error('UI handoff assertion failed') }
let browser, stage = 'launch'
try {
  browser = await chromium.launch({ ...(process.env.SOBA_CHROMIUM_PATH ? { executablePath: process.env.SOBA_CHROMIUM_PATH } : {}), headless: true, args: ['--no-sandbox'], chromiumSandbox: false })
  stage = 'context'
  const context = await browser.newContext({ locale: 'en-US' })
  const page = await context.newPage()
  let posts = 0, leaked = false, stateBeforeSession = false, postResponded = false, runtimeErrors = 0
  const codes = []
  page.on('pageerror', () => runtimeErrors++)
  page.on('request', request => {
    if (codes.some(code => request.url().includes(code) || (request.headers().referer || '').includes(code))) leaked = true
    if (request.url().endsWith('/api/session') && request.method() === 'POST') { posts++; postResponded = false }
    if (request.url().endsWith('/api/state') && !postResponded) stateBeforeSession = true
  })
  // fetch resolves when response headers (including Set-Cookie) arrive.
  // Observe that boundary, rather than merely observing POST dispatch.
  page.on('response', response => {
    if (response.url().endsWith('/api/session') && response.request().method() === 'POST') postResponded = true
  })
  const launch = async access => {
    codes.push(access.code)
    await page.goto(`${access.url}/#handoff=${access.code}`)
    await page.locator('.workspace').waitFor({ timeout: 10000 })
    requireSafe(page.url() === `${access.url}/`)
    requireSafe(await page.locator('.login-panel').count() === 0)
    requireSafe(await page.evaluate(code => !document.body.textContent.includes(code) && !JSON.stringify(localStorage).includes(code) && !JSON.stringify(sessionStorage).includes(code), access.code))
  }
  stage = 'initial'
  const first = await read()
  await launch(first)
  requireSafe(posts === 1 && !stateBeforeSession && !leaked)
  stage = 'cookie'
  const cookie = (await context.cookies()).find(value => value.name === 'soba_session')
  requireSafe(cookie?.httpOnly && cookie.sameSite === 'Strict')
  stage = 'history'
  await page.reload(); await page.locator('.workspace').waitFor()
  await page.goto('about:blank'); await page.goBack(); await page.locator('.workspace').waitFor()
  requireSafe(posts === 1 && !page.url().includes('#'))
  stage = 'reuse'
  console.log('rotate'); const second = await read()
  // External launches load a document; changing only a hash in this existing
  // test page would exercise same-document navigation instead of startup.
  await page.goto('about:blank')
  await launch(second)
  const reused = (await context.cookies()).find(value => value.name === 'soba_session')
  requireSafe(posts === 2 && reused?.value === cookie.value && reused.expires === cookie.expires)
  stage = 'replay'
  const fresh = await browser.newContext({ locale: 'en-US' })
  const other = await fresh.newPage()
  await other.goto(`${second.url}/#handoff=${second.code}`)
  await other.locator('.login-panel').waitFor({ timeout: 10000 })
  requireSafe(!other.url().includes('#') && await other.locator('.workspace').count() === 0)
  stage = 'expiry'
  console.log('expire'); const expired = await read()
  await other.goto('about:blank')
  await other.goto(`${expired.url}/#handoff=${expired.code}`)
  await other.locator('.login-panel').waitFor({ timeout: 10000 })
  requireSafe(!other.url().includes('#'))
  stage = 'manual'
  console.log('rotate'); const manual = await read()
  await other.getByLabel('Local access code', { exact: true }).fill(manual.code)
  await other.getByRole('button', { name: 'Open sobalink', exact: true }).click()
  await other.locator('.workspace').waitFor({ timeout: 10000 })
  requireSafe(!leaked && runtimeErrors === 0)
  await fresh.close(); await context.close()
  console.log('pass')
} catch {
  console.log(`fail:${stage}`)
  process.exitCode = 1
} finally { if (browser) await browser.close() }
process.stdin.destroy()
