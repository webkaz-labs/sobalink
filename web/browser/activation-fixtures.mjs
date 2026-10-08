import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { lstat, mkdtemp, readFile, readdir, rm, stat, writeFile } from 'node:fs/promises'
import { dirname, join, resolve, isAbsolute } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { test as base, expect } from '@playwright/test'
import { lifecycle, validateLifecycle, counter, workStages, exits, exitCategory, advanceLifecycle, failLifecycle } from './activation-diagnostics.mjs'
export { expect }
async function exists(path) { try { await stat(path); return true } catch (error) { if (error.code === 'ENOENT') return false; throw error } }
async function privateJSON(path) {
  const parent = await lstat(dirname(path))
  assert.ok(parent.isDirectory() && !parent.isSymbolicLink() && parent.uid === process.getuid() && (parent.mode & 0o077) === 0, 'Protected fixture writer directory required')
  const info = await lstat(path)
  assert.ok(info.isFile() && !info.isSymbolicLink() && info.uid === process.getuid() && info.size <= 8192 && (info.mode & 0o077) === 0, 'Protected bounded fixture data required')
  return JSON.parse(await readFile(path, 'utf8'))
}
async function until(predicate, deadline, description) {
  while (Date.now() < deadline) { if (await predicate()) return; await delay(50) }
  throw new Error(description)
}
async function bounded(operation, deadline) {
  let timer
  try { return await Promise.race([operation, new Promise((_, reject) => { timer = setTimeout(() => reject(new Error('Private fixture deadline exceeded')), Math.max(1, deadline - Date.now())) })]) }
  finally { clearTimeout(timer) }
}
export const test = base.extend({
  activationCase: ['normal', { option: true }],
  activation: async ({ page, context, activationCase }, use, testInfo) => {
    const deadline = Date.now() + 150000 // shared setup/body/cleanup deadline
    testInfo.setTimeout(120000) // reserve part of the same budget for teardown
    let dir, child, exited = false, exitCode, output = '', leak = false, outputOverflow = false, privateOutputDetected = false, blockedRequests = 0, runtimeErrors = 0, noSuccessorExpected = false
    const diagnostic = lifecycle()
    const mark = stage => advanceLifecycle(diagnostic, stage)
    const failedStage = () => failLifecycle(diagnostic)
    const secrets = []
    const root = process.env.SOBA_ACTIVATION_PRIVATE_RUN
    try {
      mark('preflight')
      assert.equal(process.platform, 'linux', 'Native subreaper fixture is Linux-only')
      assert.ok(root && isAbsolute(root), 'Use the reviewed private runner')
      assert.ok(!process.env.DEBUG && !process.env.PWDEBUG, 'Debug logging is forbidden')
      for (const key of ['trace', 'screenshot', 'video']) assert.equal(testInfo.project.use[key], 'off', 'Private capture must stay disabled')
      assert.equal(testInfo.project.use.serviceWorkers, 'block', 'Service workers must be blocked')
      assert.equal(process.env.SOBA_WEB_ACTIVATION_ACCEPTANCE, '1', 'Explicit acceptance opt-in required')
      assert.ok(['normal', 'pause-before-stop', 'lost-ack'].includes(activationCase), 'Unknown synthetic case')
      const binary = resolve(process.env.SOBA_WEB_ACTIVATION_BINARY || ''), assets = resolve(process.env.SOBA_WEB_ACTIVATION_ASSETS || '')
      assert.ok(process.env.SOBA_WEB_ACTIVATION_BINARY && process.env.SOBA_WEB_ACTIVATION_ASSETS, 'Prepared fixture artifacts required')
      assert.ok((await stat(binary)).isFile() && (await stat(join(assets, 'index.html'))).isFile(), 'Prepared fixture artifacts required')
      dir = await mkdtemp(join(root, 'sr-')) // cleanup ownership begins immediately
      await writeFile(join(dir, 'fixture-owned'), 'synthetic-web-activation-only', { mode: 0o600 })
      await context.route('**/*', async route => {
        try {
          const url = new URL(route.request().url())
          const origins = await privateJSON(join(dir, '.fixture-origins', 'owned-origins.json'))
          if (url.protocol !== 'http:' || url.hostname !== '127.0.0.1' || url.username || url.password || url.search || url.hash || !Array.isArray(origins) || !origins.includes(url.origin)) throw Error('unowned origin')
          await route.continue()
        } catch { blockedRequests++; await route.abort('blockedbyclient').catch(() => {}) }
      })
      await context.routeWebSocket('**/*', socket => { blockedRequests++; socket.close() })
      const observePage = p => p.on('pageerror', () => runtimeErrors++)
      observePage(page); context.on('page', observePage)
      const env = { SOBA_ACTIVATION_CASE: activationCase, SOBALINK_WEB_ACTIVATION_FIXTURE: '1', SOBA_ACTIVATION_PROFILE: dir, SOBA_ACTIVATION_ASSETS: assets, HOME: dir, TMPDIR: dir, TMP: dir, TEMP: dir, GORACE: 'halt_on_error=1 exitcode=66 atexit_sleep_ms=0' }
      mark('native-start')
      child = spawn(binary, ['--web-activation-supervisor'], { cwd: dir, env, stdio: ['ignore', 'pipe', 'pipe'] })
      diagnostic.supervisorStarted = Boolean(child.pid)
      const outputChunk = chunk => { output += chunk.toString(); if (output.length > 65536) { leak = true; outputOverflow = true; output = output.slice(-65536) } }
      child.stdout.on('data', outputChunk); child.stderr.on('data', outputChunk)
      child.once('error', () => { exited = true; exitCode = -1; diagnostic.supervisorExit = 'spawn-error' }); child.once('exit', code => { exited = true; exitCode = code; diagnostic.supervisorExit = exitCategory(code) })
    await context.addInitScript(() => {
      const original = window.fetch
      let token = ''
      window.fetch = async function (input, init) {
        const response = await original.call(this, input, init)
        if (new URL(typeof input === 'string' ? input : input.url, location.href).pathname === '/claim' && response.ok) {
          const result = await response.clone().json(); token = result.token
        }
        return response
      }
      // Observation only: requests still reach the real production helper.
      // Return booleans/status only; never return capability or code to a log.
      window.__activationProbe = async path => {
        if (!['/status', '/ack', '/open'].includes(path) || !token) return { status: 0, containsCode: false }
        const response = await original(path, { method: 'POST', credentials: 'omit', cache: 'no-store', headers: { 'Content-Type': 'application/json', 'X-Handoff-Token': token }, body: '{}' })
        const body = await response.text()
        let containsCode = false
        try { containsCode = typeof JSON.parse(body).code === 'string' } catch {}
        return { status: response.status, containsCode }
      }
      window.addEventListener('pagehide', () => { token = '' })
    })
      mark('native-ready')
      await until(async () => exited || await exists(join(dir, 'session.json')), Math.min(deadline, Date.now() + 10000), 'Synthetic old owner readiness timed out')
      assert.ok(!exited, 'Synthetic old owner failed before readiness')
      const info = await stat(join(dir, 'session.json'))
      if (process.platform !== 'win32') assert.equal(info.mode & 0o077, 0, 'Private fixture session permissions required')
      const session = JSON.parse(await readFile(join(dir, 'session.json'), 'utf8'))
      const url = new URL(session.url)
      assert.ok(session.syntheticOwner && url.protocol === 'http:' && url.hostname === '127.0.0.1' && !url.search && !url.hash, 'Invalid synthetic session descriptor')
      secrets.push(session.code)
      const signIn = async (target, address, code) => {
        try {
          await target.goto(address)
          await target.getByLabel('Local access code', { exact: true }).fill(code)
          await target.getByRole('button', { name: 'Sign in', exact: true }).click()
          await expect(target.locator('#workspace')).toBeVisible()
          await expect(target.locator('#code')).toHaveValue('')
        } catch { throw new Error('Private synthetic normal-login acceptance failed; details withheld') }
      }
      mark('old-login')
      await signIn(page, session.url, session.code)
      session.code = ''
      mark('body')
      await bounded(use({
        phase(stage) { assert.ok(workStages.includes(stage), 'Fixed work stage required'); mark(stage) },
        bodyPassed() { diagnostic.workCompleted = true },
        bodyFailed() { diagnostic.workFailed = true; failedStage() },
        async popup(locale = 'en') {
          mark('review-request')
          await page.getByLabel('Locale', { exact: true }).selectOption(locale)
          await page.getByRole('button', { name: 'Review activation', exact: true }).click()
          await expect(page.getByRole('button', { name: 'Open restart window', exact: true })).toBeEnabled()
          mark('popup-request')
          const opened = page.waitForEvent('popup')
          await page.getByRole('button', { name: 'Open restart window', exact: true }).click()
          const popup = await opened
          mark('handoff-ready')
          await expect(popup.locator('#continue')).toBeVisible()
          mark('body')
          return popup
        },
        async normalSuccessorLogin(popup) {
          mark('successor-login')
          try {
            await expect(popup.locator('#management')).toBeVisible()
            const address = await popup.locator('#management').inputValue()
            const text = await popup.locator('#code').textContent()
            const code = text.slice(text.indexOf(': ') + 2)
            assert.ok(code && !address.includes(code), 'Private code URL isolation failed')
            secrets.push(code)
            // page.goto is address-bar-style navigation, not a cross-origin link.
            const fresh = await context.newPage(); await signIn(fresh, address, code)
            await expect(fresh.locator('#owner')).toHaveText('Synthetic successor owner')
            return fresh
          } catch { throw new Error('Private successor normal-login acceptance failed; details withheld') }
        },
        async opened() { return exists(join(dir, 'browser-opened')) },
        async successorStarted() { return exists(join(dir, 'successor-started')) },
        async oldExited() { try { return (await privateJSON(join(dir, '.fixture-supervisor', 'native-progress.json'))).oldExited === true } catch (error) { if (error.code === 'ENOENT') return false; throw new Error('Private native progress validation failed; details withheld') } },
        expectNoSuccessor() { noSuccessorExpected = true },
        async waitingAtStop() { return exists(join(dir, 'stop-admission-waiting')) },
        async permitStop() { await writeFile(join(dir, 'permit-stop'), 'permit synthetic admission check', { mode: 0o600 }) },
        async sessionStopDenied() { return exists(join(dir, 'session-stop-denied')) },
      }), deadline - 30000)
    } catch { diagnostic.workFailed = true; failedStage(); throw new Error('Synthetic Web activation fixture failed; private details withheld') }
    finally {
      if (testInfo.status !== 'passed' && !diagnostic.workCompleted) { diagnostic.workFailed = true; failedStage() }
      try {
      if (dir) {
        let nativeComplete = !child, cleanupFailed = false
        try {
          // Closing the browser context requests cancellation and denies further
          // traffic. The same deadline bounds teardown; waits cannot stack.
          mark('cleanup-context')
          await bounded(context.close(), Math.min(deadline, Date.now() + 5000))
          diagnostic.contextClosed = true
        } catch { failedStage(); cleanupFailed = true }
        try {
          if (child) {
            mark('cleanup-supervisor')
            await writeFile(join(dir, 'stop-fixture'), 'stop synthetic descendants', { mode: 0o600 })
            await until(() => exited, deadline, 'Native supervisor exit unconfirmed')
            mark('cleanup-proof')
            const proof = await privateJSON(join(dir, '.fixture-supervisor', 'exit-proof.json'))
            diagnostic.proofRead = true
            for (const key of ['allDescendantsReaped', 'registeredNativeExits', 'successorRegistered']) diagnostic[key] = proof[key] === true
            diagnostic.reaped = counter(proof.reaped)
            diagnostic.oldExit = exits.includes(proof.oldExit) ? proof.oldExit : 'not-observed'
            for (const key of ['registeredChildren', 'observedExits']) diagnostic[key] = counter(proof[key])
            for (const key of ['stopRequested', 'supervisorDeadlineExpired', 'noWaitableChildren']) diagnostic[key] = proof[key] === true
            nativeComplete = proof.allDescendantsReaped === true && proof.registeredNativeExits === true
            assert.ok(nativeComplete && proof.success === true && exitCode === 0, 'Native descendant cleanup failed')
            if (noSuccessorExpected) assert.equal(proof.successorRegistered, false, 'A late successor owner was registered')
            assert.ok(!(await readdir(dir)).some(name => name.startsWith('.upgrade-')), 'Acknowledgement cleanup failed')
          }
          mark('cleanup-safety')
          const log = await exists(join(dir, 'startup.log')) ? await readFile(join(dir, 'startup.log'), 'utf8') : ''
          for (const secret of secrets) if (output.includes(secret) || log.includes(secret)) { leak = true; privateOutputDetected = true }
          assert.equal(leak, false, 'Private value or excessive output reached child logs')
          assert.equal(blockedRequests, 0, 'Browser attempted an unowned origin or websocket')
          assert.equal(runtimeErrors, 0, 'Browser runtime health failed')
        } catch { failedStage(); cleanupFailed = true }
        // Never remove profiles based on a marker or a cancellation request.
        // All descendants, including failed/unregistered starts, must be reaped.
        if (nativeComplete) {
          mark('cleanup-remove')
          await bounded(rm(dir, { recursive: true, force: true }), deadline).then(() => { diagnostic.profileRemoved = true }).catch(() => { failedStage(); cleanupFailed = true })
        }
        if (cleanupFailed) throw new Error('Private fixture cleanup or safety gate failed; details withheld')
      }
      mark('finished')
      } finally {
        diagnostic.supervisorExited = exited
        diagnostic.blockedRequests = counter(blockedRequests)
        diagnostic.runtimeErrors = counter(runtimeErrors)
        diagnostic.outputOverflow = outputOverflow
        diagnostic.privateOutputDetected = privateOutputDetected
        testInfo.annotations.push({ type: 'activation-sanitized', description: JSON.stringify(validateLifecycle(diagnostic)) })
      }
    }
  },
})
