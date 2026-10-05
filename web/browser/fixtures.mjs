import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { spawn } from 'node:child_process'
import { mkdir, mkdtemp, readFile, readdir, realpath, rm, stat, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, relative, resolve, sep } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { test as base, expect } from '@playwright/test'
import { assertSeparateArtifacts, safeArtifactName, validateSession, CAPTURE_FORBIDDEN_SELECTOR, PRIVATE_VALUE_SELECTOR, privateControlsAreEmpty } from './fixture-safety.mjs'

export { expect }

export async function openDetailsSection(section) {
  await expect(section).toHaveCount(1)
  if (await section.getAttribute('open') === null) await section.locator(':scope > summary').click()
  await expect(section).toHaveAttribute('open', '')
}

function installInstrumentation() {
  if (window.__sobaQA) return
  const qa = { commands: [], errors: 0, cspViolations: 0, uploads: 0, uploadIDs: [], duplicateUploadIdentity: false }
  window.__sobaQA = qa
  window.addEventListener('error', () => qa.errors++)
  window.addEventListener('unhandledrejection', () => qa.errors++)
  window.addEventListener('securitypolicyviolation', () => qa.cspViolations++)
  const originalFetch = window.fetch
  window.fetch = function (input, init) {
    const path = new URL(typeof input === 'string' ? input : input.url, location.href).pathname
    if (path === '/api/command' && init?.body) {
      try { const command = JSON.parse(init.body); if (typeof command.name === 'string') qa.commands.push(command.name) } catch {}
    }
    return originalFetch.apply(this, arguments)
  }
  const originalSend = XMLHttpRequest.prototype.send
  XMLHttpRequest.prototype.send = function (body) {
    if (body instanceof FormData && body.has('manifest')) {
      qa.uploads++
      const id = body.get('requestId')
      qa.duplicateUploadIdentity = qa.uploadIDs.includes(id)
      qa.uploadIDs.push(id)
    }
    return originalSend.apply(this, arguments)
  }
}

async function waitForExit(exit, timeout) {
  let timer
  try {
    return await Promise.race([exit, new Promise((_, reject) => { timer = setTimeout(() => reject(new Error('The isolated application did not exit within the acceptance timeout')), timeout) })])
  } finally { clearTimeout(timer) }
}

export const test = base.extend({
  scenario: ['studio', { option: true }],
  expectShutdown: [false, { option: true }],
  app: async ({ page, scenario, expectShutdown }, use, testInfo) => {
    assert.ok(!expectShutdown || scenario === 'studio', 'Shutdown acceptance requires a fresh active Studio fixture')
    const binary = process.env.SOBA_E2E_BINARY
    assert.ok(binary, 'SOBA_E2E_BINARY must name the compiled isolated Go harness')
    assert.ok((await stat(binary)).isFile(), 'The isolated Go harness must exist before browser acceptance')
    const privateRoot = await mkdtemp(join(tmpdir(), 'soba-playwright-'))
    const output = resolve(process.env.SOBA_SCREENSHOT_DIR || join(tmpdir(), 'sobalink-playwright-screenshots'))
    await mkdir(output, { recursive: true, mode: 0o700 })
    assertSeparateArtifacts(await realpath(output), await realpath(privateRoot))
    const sessionFile = join(privateRoot, 'session.json')
    const child = spawn(binary, ['--scenario', scenario, '--session-file', sessionFile], { stdio: ['ignore', 'ignore', 'ignore'] })
    let exited = false
    const exit = new Promise(resolveExit => {
      child.once('error', () => { exited = true; resolveExit({ code: null, signal: 'spawn-error' }) })
      child.once('exit', (code, signal) => { exited = true; resolveExit({ code, signal }) })
    })
    let session
    let authenticated = false
    let cleanShutdownVerified = false
    let usedFixture = false
    let runtimeErrors = 0
    let cspErrors = 0
    page.on('pageerror', () => runtimeErrors++)
    page.on('console', entry => { if (entry.type() === 'error' && /content security policy|refused to/i.test(entry.text())) cspErrors++ })
    try {
      await page.addInitScript(installInstrumentation)
      for (let attempt = 0; attempt < 200; attempt++) {
        if (exited) throw new Error('The isolated Go harness exited before its private session was ready')
        try {
          const info = await stat(sessionFile)
          assert.equal(info.mode & 0o077, 0, 'Fixture session must not grant group or other access')
          let value
          try { value = JSON.parse(await readFile(sessionFile, 'utf8')) } catch { throw new Error('Private fixture session could not be decoded; contents withheld') }
          session = validateSession(value, scenario)
          break
        } catch (error) {
          if (error.code !== 'ENOENT') throw error
        }
        await delay(100)
      }
      assert.ok(session, 'The isolated Go fixture did not become ready within the acceptance timeout')
      // Never enable tracing, screenshots, recordings, HARs or storage exports
      // while the private login control is present. Keep login failures generic;
      // Playwright call logs can otherwise repeat the entered access code.
      try {
        const response = await page.goto(session.url)
        const csp = response.headers()['content-security-policy'] || ''
        assert.match(csp, /script-src 'self'/)
        assert.match(csp, /style-src 'self'/)
        assert.equal(csp.includes("'unsafe-inline'"), false)
        await page.getByLabel('Local access code', { exact: true }).fill(session.code)
        await page.getByRole('button', { name: 'Open sobalink', exact: true }).click()
        await page.locator('.workspace').waitFor()
        await expect(page.locator('.login-panel')).toHaveCount(0)
        authenticated = true
      } catch { throw new Error('Private fixture authentication or production CSP verification failed; details withheld') }
      await page.evaluate(installInstrumentation)
      const assertCaptureAllowed = async () => {
        assert.ok(authenticated, 'Evidence capture requires the authenticated workspace')
        await expect(page.locator('.workspace')).toBeVisible()
        await expect(page.locator(CAPTURE_FORBIDDEN_SELECTOR)).toHaveCount(0)
        const safe = await page.locator(PRIVATE_VALUE_SELECTOR).evaluateAll(privateControlsAreEmpty)
        assert.ok(safe, 'Private controls must be absent or empty before evidence capture')
        await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), { message: 'The page must not overflow horizontally' }).toBe(true)
      }
      const app = {
        // This helper alone reads the sealed synthetic update. It never returns
        // it, adds it to session metadata, or emits raw Playwright fill errors.
        async fillRouteUpdate(input) {
          try {
            assert.equal(scenario, 'routes')
            const root = await realpath(privateRoot)
            const path = await realpath(session.routeUpdateFile)
            const childPath = relative(root, path)
            assert.ok(childPath && childPath !== '..' && !childPath.startsWith(`..${sep}`) && !childPath.startsWith(sep))
            assert.equal(path, resolve(session.routeUpdateFile), 'Fixture input must not traverse a symlink')
            const info = await stat(path)
            assert.ok(info.isFile() && (info.mode & 0o077) === 0 && info.size > 0 && info.size <= 131072)
            const envelope = JSON.parse(await readFile(path, 'utf8'))
            assert.ok(typeof envelope.update === 'string' && envelope.update.length > 0 && Buffer.byteLength(envelope.update, 'utf8') <= 65536)
            await input.fill(envelope.update)
          } catch { throw new Error('Private route fixture input could not be prepared; details withheld') }
        },
        receiveDirectory: session.receiveDirectory,
        localServicePort: session.localServicePort,
        async expectState(predicate, description = 'Real Go state confirms the reviewed result') {
          await expect.poll(async () => {
            const state = await page.evaluate(async () => {
              const response = await fetch('/api/state')
              if (!response.ok) return null
              const { csrfToken: _privateToken, ...state } = await response.json()
              return state
            })
            return state !== null && Boolean(predicate(state))
          }, { message: description }).toBe(true)
        },
        count(name) { return page.evaluate(name => (window.__sobaQA?.commands || []).filter(command => command === name).length, name) },
        async capture(name) {
          await assertCaptureAllowed()
          const filename = `${safeArtifactName(name)}.png`
          await page.screenshot({ path: join(output, filename), fullPage: true })
          testInfo.annotations.push({ type: 'safe-artifact', description: filename })
        },
        async captureForm(name) {
          await this.capture(name)
          await page.locator('dialog .modal-actions').scrollIntoViewIfNeeded()
          await this.capture(`${name}-actions`)
        },
        async writeMetrics(name, data) {
          await assertCaptureAllowed()
          const filename = `${safeArtifactName(name)}.json`
          await writeFile(join(output, filename), `${JSON.stringify(data, null, 2)}\n`, { mode: 0o600 })
          testInfo.annotations.push({ type: 'safe-artifact', description: filename })
        },
        async appearance(locale, theme) {
          await page.locator('.app-header .header-actions button.icon-button').click()
          await page.locator('dialog select').selectOption(locale)
          const name = locale === 'ja' ? theme === 'light' ? 'ライト' : 'ダーク' : theme === 'light' ? 'Light' : 'Dark'
          await page.getByRole('button', { name, exact: true }).click()
          await page.keyboard.press('Escape')
          await expect(page.locator('dialog[open]')).toHaveCount(0)
          await expect(page.locator('html')).toHaveAttribute('lang', locale)
          await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
        },
        async closeDetails() {
          if (await page.locator('.details-panel').isVisible()) await page.locator('.details-heading button').click()
          await expect(page.locator('.details-panel')).toHaveCount(0)
        },
        async openDetails() {
          if (!await page.locator('.details-panel').isVisible()) await page.locator('.conversation-header button[aria-expanded]').click()
          await expect(page.locator('.details-panel')).toBeVisible()
        },
        async openPeer(section = 'exchange') {
          await this.closeDetails()
          await page.setViewportSize({ width: 1440, height: 960 })
          await page.locator('.device-row').first().click()
          if (section === 'exchange') {
            await page.locator('.device-sections button').nth(1).click()
            await expect(page.locator('.composer textarea')).toBeVisible()
          } else {
            await page.locator('.device-sections button').first().click()
            await expect(page.locator('.device-overview')).toBeVisible()
          }
        },
        async privateDirectory(name) {
          safeArtifactName(name)
          const root = await realpath(session.receiveDirectory)
          const directory = join(root, name)
          assert.equal(relative(root, directory), name, 'Fixture directory must stay below the receiving root')
          await mkdir(directory, { mode: 0o700 })
          assert.equal(await realpath(directory), directory, 'Fixture directory cannot traverse a symlink')
          return directory
        },
        async sourceFile(name, content) {
          safeArtifactName(name)
          const file = join(privateRoot, name)
          await writeFile(file, content, { mode: 0o600 })
          return file
        },
        async verifyReceived(directory, filename, content) {
          safeArtifactName(filename)
          const root = await realpath(session.receiveDirectory)
          const destination = await realpath(directory)
          const within = relative(root, destination)
          assert.ok(destination === root || (within && !within.startsWith('..') && !within.includes(sep)), 'Reviewed receive folder must be the fixture root or a direct fixture subdirectory')
          const batches = await readdir(destination, { withFileTypes: true })
          assert.equal(batches.length, 1, 'One accepted offer must create one receiving batch')
          assert.ok(batches[0].isDirectory() && !batches[0].isSymbolicLink(), 'Saved batch must be a real fixture directory')
          const batch = await realpath(join(destination, batches[0].name))
          assert.equal(relative(destination, batch), batches[0].name, 'Saved batch must remain in the reviewed destination')
          const file = await realpath(join(batch, filename))
          assert.ok(relative(destination, file).startsWith(`${batches[0].name}${sep}`), 'Saved file must remain inside the reviewed receiving batch')
          assert.equal(await readFile(file, 'utf8'), content, 'Saved fixture bytes must match the offered source')
        },
        async expectStopped() {
          const result = await waitForExit(exit, 20_000)
          assert.equal(result.code, 0, 'The actual Go harness must exit successfully after the reviewed Stop')
          assert.equal(result.signal, null, 'Whole-app Stop must finish without terminating the fixture externally')
          cleanShutdownVerified = true
        },
      }
      await use(app)
      usedFixture = true
      if (testInfo.status === testInfo.expectedStatus) {
        assert.equal(runtimeErrors, 0, 'Browser acceptance must not introduce uncaught exceptions')
        assert.equal(cspErrors, 0, 'Browser acceptance must not violate production CSP')
        await expect.poll(() => page.evaluate(() => window.__sobaQA?.errors === 0 && window.__sobaQA?.cspViolations === 0), { message: 'Production UI must retain runtime and CSP health' }).toBe(true)
        for (const name of ['network.login', 'lan.invite', 'lan.join']) assert.equal(await app.count(name), 0, 'Browser acceptance must not enroll or pair a real device')
        if (expectShutdown && !cleanShutdownVerified) await app.expectStopped()
      } else if (authenticated) {
        const identity = createHash('sha256').update(testInfo.testId).digest('hex').slice(0, 12)
        const name = `failed-${testInfo.title.replace(/[^a-z0-9]+/gi, '-').slice(0, 80)}-${testInfo.line}-${identity}`
        try { await app.capture(name) } catch { /* Never relax privacy checks for failure evidence. */ }
      }
    } finally {
      // Fallback cleanup never counts as the shutdown assertion above.
      if (!exited) child.kill('SIGTERM')
      let cleanupResult
      try { cleanupResult = await waitForExit(exit, 5_000) } catch { child.kill('SIGKILL'); cleanupResult = await waitForExit(exit, 5_000) }
      await rm(privateRoot, { recursive: true, force: true })
      if (usedFixture && testInfo.status === testInfo.expectedStatus) {
        assert.equal(cleanupResult.code, 0, 'The isolated Go fixture must close without an error')
        assert.equal(cleanupResult.signal, null, 'Fixture cleanup must finish without forced termination')
      }
    }
  },
})
