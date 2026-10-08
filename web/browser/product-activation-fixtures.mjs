import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { lstat, mkdtemp, readFile, readdir, rm, stat, writeFile } from 'node:fs/promises'
import { join, isAbsolute } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { test as base, expect } from '@playwright/test'
export { expect }

async function exists(path) { try { await lstat(path); return true } catch (error) { if (error.code === 'ENOENT') return false; throw error } }
async function privateJSON(path) {
  const info = await lstat(path)
  assert.ok(info.isFile() && !info.isSymbolicLink() && info.size <= 8192 && (info.mode & 0o077) === 0, 'Protected bounded fixture data required')
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
function managementAddress(address) {
  const url = new URL(address)
  assert.ok(url.protocol === 'http:' && url.hostname === '127.0.0.1' && url.port && Number(url.port) > 0 && !url.username && !url.password && !url.search && !url.hash && url.pathname === '/' && [url.origin, `${url.origin}/`].includes(address), 'Exact bare loopback management address required')
  return address
}
function privateCode(code) {
  assert.ok(typeof code === 'string' && code.length > 0 && code.length <= 256 && code.trim() === code, 'Private normal-login code required')
  return code
}

export const test = base.extend({
  activationCase: ['product-core-web', { option: true }],
  activation: async ({ page, context, activationCase }, use, testInfo) => {
    const deadline = Date.now() + 150000 // one shared setup/body/cleanup budget
    testInfo.setTimeout(120000) // body cannot consume the cleanup reserve
    let dir, child, exited = false, exitCode, output = '', leak = false, blockedRequests = 0, runtimeErrors = 0, bodyPassed = false
    const secrets = []
    const root = process.env.SOBA_PRODUCT_ACTIVATION_PRIVATE_RUN
    try {
      assert.equal(process.platform, 'linux', 'Native subreaper fixture is Linux-only')
      assert.equal(process.env.SOBA_PRODUCT_ACTIVATION_ACCEPTANCE, '1', 'Explicit product acceptance opt-in required')
      assert.ok(root && isAbsolute(root), 'Use the reviewed private product runner')
      const rootInfo = await lstat(root)
      assert.ok(rootInfo.isDirectory() && !rootInfo.isSymbolicLink() && (rootInfo.mode & 0o077) === 0, 'Private runtime directory required')
      assert.ok(!process.env.DEBUG && !process.env.PWDEBUG, 'Debug logging is forbidden')
      for (const key of ['trace', 'screenshot', 'video']) assert.equal(testInfo.project.use[key], 'off', 'Private capture must stay disabled')
      assert.equal(testInfo.project.use.serviceWorkers, 'block', 'Service workers must be blocked')
      assert.ok(['product-core-web', 'product-core-cli'].includes(activationCase), 'Unknown product case')
      const binary = process.env.SOBA_WEB_ACTIVATION_BINARY
      assert.ok(binary && isAbsolute(binary) && (await stat(binary)).isFile(), 'Prepared product acceptance binary required')
      dir = await mkdtemp(join(root, 'sr-')) // ownership starts before any child
      // Existing supervisor marker; product mode additionally requires its
      // dedicated compile tag, explicit opt-in and exact case selector.
      await writeFile(join(dir, 'fixture-owned'), 'synthetic-web-activation-only', { mode: 0o600 })
      await context.route('**/*', async route => {
        try {
          const url = new URL(route.request().url())
          const origins = await privateJSON(join(dir, 'owned-origins.json'))
          if (url.protocol !== 'http:' || url.hostname !== '127.0.0.1' || url.username || url.password || url.search || url.hash || !Array.isArray(origins) || !origins.includes(url.origin)) throw Error('unowned origin')
          await route.continue()
        } catch { blockedRequests++; await route.abort('blockedbyclient').catch(() => {}) }
      })
      await context.routeWebSocket('**/*', socket => { blockedRequests++; socket.close() })
      const observePage = target => target.on('pageerror', () => runtimeErrors++)
      observePage(page); context.on('page', observePage)
      const env = {
        SOBA_PRODUCT_ACTIVATION_ACCEPTANCE: '1', SOBA_ACTIVATION_CASE: activationCase,
        SOBALINK_WEB_ACTIVATION_FIXTURE: '1', SOBA_ACTIVATION_PROFILE: dir,
        HOME: dir, TMPDIR: dir, TMP: dir, TEMP: dir, LANG: 'C.UTF-8',
        GORACE: 'halt_on_error=1 exitcode=66 atexit_sleep_ms=0',
      }
      // The mode uses production embedded assets. No synthetic app asset path,
      // API substitute, injected page script or network response fulfiller.
      child = spawn(binary, ['--web-activation-supervisor'], { cwd: dir, env, stdio: ['ignore', 'pipe', 'pipe'] })
      const outputChunk = chunk => { output += chunk.toString(); if (output.length > 65536) { leak = true; output = output.slice(-65536) } }
      child.stdout.on('data', outputChunk); child.stderr.on('data', outputChunk)
      child.once('error', () => { exited = true; exitCode = -1 }); child.once('exit', code => { exited = true; exitCode = code })
      await until(async () => exited || await exists(join(dir, 'session.json')), Math.min(deadline, Date.now() + 15000), 'Product old owner readiness timed out')
      assert.ok(!exited, 'Product old owner failed before readiness')
      const session = await privateJSON(join(dir, 'session.json'))
      assert.ok(session.productCore === true && /^[a-f0-9]{64}$/.test(session.peerId), 'Invalid product session descriptor')
      managementAddress(session.url); privateCode(session.code)
      secrets.push(session.code)
      const signIn = async (target, address, code) => {
        try {
          managementAddress(address); privateCode(code)
          assert.ok(!address.includes(code), 'Private code URL isolation required')
          await target.goto(address)
          await target.getByLabel('Local access code', { exact: true }).fill(code)
          await target.getByRole('button', { name: 'Open sobalink', exact: true }).click()
          await expect(target.locator('main.workspace')).toBeVisible()
          await expect(target.getByLabel('Local access code', { exact: true })).toHaveCount(0)
        } catch { throw new Error('Private product normal-login acceptance failed; details withheld') }
      }
      const freshLogin = async (address, code) => {
        privateCode(code)
        assert.ok(!secrets.includes(code), 'Successor must issue a fresh normal code')
        secrets.push(code)
        // Address-bar navigation; no cross-origin link or transferred session.
        const fresh = await context.newPage(); await signIn(fresh, address, code)
        return fresh
      }
      await signIn(page, session.url, session.code)
      session.code = ''
      await bounded(use({
        peerId: session.peerId,
        async normalSuccessorLogin(popup) {
          try {
            await expect(popup.locator('#management')).toBeVisible()
            const address = await popup.locator('#management').inputValue()
            const text = await popup.locator('#code').textContent()
            const prefix = 'Fresh one-time code: '
            assert.ok(text?.startsWith(prefix), 'Fresh private helper code required')
            return await freshLogin(address, text.slice(prefix.length))
          } catch { throw new Error('Private product successor normal-login acceptance failed; details withheld') }
        },
        async startCLI() {
          assert.equal(activationCase, 'product-core-cli', 'CLI marker requires its exact case')
          await writeFile(join(dir, 'product-cli-start'), 'start reviewed product CLI acceptance', { mode: 0o600, flag: 'wx' })
        },
        async normalCLISuccessorLogin() {
          try {
            assert.equal(activationCase, 'product-core-cli', 'CLI result requires its exact case')
            await until(async () => exited || await exists(join(dir, 'cli-result.json')), Math.min(deadline - 30000, Date.now() + 60000), 'Private CLI completion timed out')
            assert.ok(!exited, 'Native supervisor exited before CLI completion')
            const result = await privateJSON(join(dir, 'cli-result.json'))
            assert.equal(result.completed, true, 'Production CLI completion required')
            return await freshLogin(result.url, result.code)
          } catch { throw new Error('Private product CLI normal-login acceptance failed; details withheld') }
        },
        async opened() { return exists(join(dir, 'browser-opened')) },
        // This marker proves only fresh offline management, never networking.
        async successorStarted() { return exists(join(dir, 'successor-started')) },
        async oldExited() {
          try { return (await privateJSON(join(dir, 'native-progress.json'))).oldExited === true }
          catch (error) { if (error.code === 'ENOENT') return false; throw error }
        },
        async nativeActivationConfirmed() {
          try {
            const proof = await privateJSON(join(dir, 'product-activation.json'))
            return proof.schema === 1 && proof.peerConfirmed === true && proof.ownerConfirmed === true && proof.ordinaryReady === true && proof.originalReviewPreserved === true
          } catch (error) { if (error.code === 'ENOENT') return false; throw error }
        },
      }), deadline - 30000)
      bodyPassed = testInfo.status === 'passed' && testInfo.expectedStatus === 'passed'
    } catch { throw new Error('Product activation fixture failed; private details withheld') }
    finally {
      if (dir) {
        let nativeComplete = !child, cleanupFailed = false
        try { await bounded(context.close(), Math.min(deadline, Date.now() + 5000)) }
        catch { cleanupFailed = true }
        try {
          if (child) {
            await writeFile(join(dir, 'stop-fixture'), 'stop fixture-owned descendants', { mode: 0o600 })
            await until(() => exited, deadline, 'Native supervisor exit unconfirmed')
            const proof = await privateJSON(join(dir, 'exit-proof.json'))
            nativeComplete = proof.allDescendantsReaped === true && proof.registeredNativeExits === true
            assert.ok(nativeComplete && proof.success === true && exitCode === 0, 'Native descendant cleanup failed')
            assert.equal(proof.successorRegistered, true, 'Real successor registration required')
            assert.ok(!(await readdir(dir)).some(name => name.startsWith('.upgrade-')), 'Acknowledgement cleanup failed')
          }
          let log = ''
          if (await exists(join(dir, 'startup.log'))) {
            const info = await lstat(join(dir, 'startup.log'))
            assert.ok(info.isFile() && !info.isSymbolicLink() && info.size <= 65536 && (info.mode & 0o077) === 0, 'Private bounded startup log required')
            log = await readFile(join(dir, 'startup.log'), 'utf8')
          }
          for (const secret of secrets) if (output.includes(secret) || log.includes(secret)) leak = true
          assert.equal(leak, false, 'Private value or excessive output reached child logs')
          assert.equal(blockedRequests, 0, 'Browser attempted an unowned origin or websocket')
          assert.equal(runtimeErrors, 0, 'Browser runtime health failed')
        } catch { cleanupFailed = true }
        // A cancellation marker never proves exit. Reap proof includes failed
        // and unregistered starts before any native profile can be removed.
        if (nativeComplete && !cleanupFailed && bodyPassed) await bounded(rm(dir, { recursive: true, force: true }), deadline).catch(() => { cleanupFailed = true })
        if (cleanupFailed) throw new Error('Private product cleanup or safety gate failed; details withheld')
      }
    }
  },
})
