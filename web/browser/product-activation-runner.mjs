import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { chmod, lstat, mkdtemp, readFile, readdir, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve, relative, isAbsolute } from 'node:path'
import { productActivationScope } from './product-activation-contract.mjs'

function validSummary(summary) {
  const keys = ['schema', 'expected', 'observed', 'passed', 'globalErrors', 'selectionValid', 'unexpected', 'runnerPassed', 'accepted']
  return summary && Object.keys(summary).length === keys.length && keys.every(key => Object.hasOwn(summary, key)) && summary.schema === 1 && summary.expected === 2 && summary.observed === 2 && summary.passed === 2 && summary.globalErrors === 0 && summary.selectionValid === true && summary.unexpected === false && summary.runnerPassed === true && summary.accepted === true
}

function cleanScopeProof(proof) {
  const keys = ['schema', 'complete', 'descendantsReaped', 'playwrightExit', 'forced', 'deadlineExceeded', 'errors', 'observed', 'reaped']
  return proof && Object.keys(proof).length === keys.length && keys.every(key => Object.hasOwn(proof, key)) && proof.schema === 1 && proof.complete === true && proof.descendantsReaped === true && proof.playwrightExit === 0 && proof.forced === false && proof.deadlineExceeded === false && proof.errors === 0 && Number.isSafeInteger(proof.observed) && proof.observed > 0 && Number.isSafeInteger(proof.reaped) && proof.reaped > 0
}

async function privateReport(root, name) {
  const path = join(root, name), info = await lstat(path)
  assert.ok(info.isFile() && !info.isSymbolicLink() && info.uid === process.getuid() && info.size <= 4096 && (info.mode & 0o077) === 0, 'Protected bounded private report required')
  return JSON.parse(await readFile(path, 'utf8'))
}

async function run() {
  assert.equal(process.argv.length, 2, 'Acceptance selection cannot be overridden')
  assert.equal(process.platform, 'linux', 'Subreaper fixture is Linux-only')
  assert.equal(process.env.SOBA_PRODUCT_ACTIVATION_ACCEPTANCE, '1', 'Explicit product acceptance opt-in required')
  assert.ok(!process.env.DEBUG && !process.env.PWDEBUG, 'Debug logging forbidden')
  assert.ok(process.env.SOBA_ACTIVATION_CHROMIUM && isAbsolute(process.env.SOBA_ACTIVATION_CHROMIUM), 'Verified installed Chromium path required')
  assert.ok(process.env.SOBA_WEB_ACTIVATION_BINARY && isAbsolute(process.env.SOBA_WEB_ACTIVATION_BINARY), 'Prepared product acceptance binary required')
  const report = resolve(process.env.SOBA_PRODUCT_ACTIVATION_REPORT || 'product-activation-sanitized-result.json')
  let root, child, scopeExit, childExited = false, exitCode = -1, timedOut = false, outputOverflow = false, bytes = 0, summary, scopeProof, cleanupProven = false
  let cancellationRequested = false, cancellationMarkerFailed = false, cancellationWrite
  const writeCancellation = () => {
    if (!root || cancellationWrite) return cancellationWrite
    cancellationWrite = writeFile(join(root, 'product-scope-cancel'), 'cancel-owned-product-scope-v1', { mode: 0o600, flag: 'wx' }).catch(() => { cancellationMarkerFailed = true })
    return cancellationWrite
  }
  const cancelOwnedScope = () => {
    cancellationRequested = true
    process.exitCode = 1
    void writeCancellation()
  }
  // Ordinary cancellation must not default-exit this wrapper while native
  // descendants remain owned. SIGKILL or executor destruction cannot provide
  // this graceful join guarantee.
  process.on('SIGTERM', cancelOwnedScope)
  process.on('SIGINT', cancelOwnedScope)
  try {
    root = await mkdtemp(join(tmpdir(), 'spa-')); await chmod(root, 0o700)
    if (cancellationRequested) await writeCancellation()
    const relativeReport = relative(root, report)
    assert.ok(relativeReport === '..' || relativeReport.startsWith('../'), 'Report must be outside private runtime')
    await writeFile(join(root, 'product-scope-owned'), 'product-browser-scope-v1', { mode: 0o600, flag: 'wx' })
    // No await separates this final cancellation check from child creation.
    if (!cancellationRequested) {
      child = spawn(process.env.SOBA_WEB_ACTIVATION_BINARY, ['--product-browser-scope'], {
        env: {
          PATH: process.env.PATH || '', HOME: root, TMPDIR: root, LANG: 'C.UTF-8',
          SOBALINK_WEB_ACTIVATION_FIXTURE: '1', SOBA_PRODUCT_ACTIVATION_ACCEPTANCE: '1',
          SOBA_PRODUCT_NODE_EXECUTABLE: process.execPath, SOBA_WEB_ACTIVATION_BINARY: process.env.SOBA_WEB_ACTIVATION_BINARY,
          SOBA_ACTIVATION_CHROMIUM: process.env.SOBA_ACTIVATION_CHROMIUM, SOBA_PRODUCT_ACTIVATION_PRIVATE_RUN: root,
        },
        stdio: ['ignore', 'pipe', 'pipe'],
      })
      scopeExit = new Promise(resolveExit => {
        let code = -1, errored = false
        child.once('error', () => { errored = true })
        child.once('exit', value => { childExited = true; code = value ?? -1 })
        // Wait for pipe drainage as well as exact scope exit before deciding
        // whether excessive output or native cleanup invalidated this run.
        child.once('close', () => resolveExit(errored ? -1 : code))
      })
      const discard = chunk => { bytes += chunk.length; if (bytes > 1 << 20) outputOverflow = true }
      child.stdout.on('data', discard); child.stderr.on('data', discard)
      // The native scope owns every descendant, including detached Chromium
      // groups. Runtime expiry begins TERM at 375s, KILL at 390s and failed-
      // unjoined reporting at 405s. Cancellation begins the same owned cleanup
      // immediately, with KILL after 15s and failed-unjoined reporting after
      // 30s. In either case the scope stays attached until ECHILD. Node has no
      // direct-kill, timer, unref or detach path that can abandon this join.
      exitCode = await scopeExit
      try { summary = await privateReport(root, 'sanitized-summary.json') } catch {}
      try { scopeProof = await privateReport(root, 'scope-proof.json') } catch {}
      timedOut = scopeProof?.deadlineExceeded === true
    }
  } finally {
    try {
      // Even a later wrapper error or failed marker write cannot bypass the
      // exact native join. Signal handlers remain installed throughout it.
      if (scopeExit) exitCode = await scopeExit
      if (cancellationRequested) await writeCancellation()
      await cancellationWrite
    } finally {
      process.removeListener('SIGTERM', cancelOwnedScope)
      process.removeListener('SIGINT', cancelOwnedScope)
    }
    if (root) {
      // Failed runs retain private diagnostics for process-scope containment;
      // neither these files nor native profile contents are exportable.
      cleanupProven = Boolean(!cancellationRequested && !cancellationMarkerFailed && childExited && exitCode === 0 && !timedOut && !outputOverflow && validSummary(summary) && cleanScopeProof(scopeProof)) && !(await readdir(root)).some(name => name.startsWith('sr-'))
      if (cleanupProven) await rm(root, { recursive: true, force: true })
    }
  }
  const schemaValid = validSummary(summary)
  const accepted = !cancellationRequested && !cancellationMarkerFailed && exitCode === 0 && !timedOut && !outputOverflow && cleanupProven && Boolean(schemaValid) && Boolean(cleanScopeProof(scopeProof))
  await writeFile(report, JSON.stringify({ schema: 1, accepted, expected: 2, allSelectedPassed: Boolean(schemaValid), runnerExitedSuccessfully: exitCode === 0, timedOut, outputOverflow, nativeCleanupProven: cleanupProven, scope: productActivationScope }), { mode: 0o600 })
  console.log(accepted ? 'PASS: two production-entry cases and private cleanup verified.' : 'FAIL: product-entry acceptance or private cleanup not verified. Raw diagnostics are never exportable.')
  process.exitCode = accepted ? 0 : 1
}
run().catch(() => { console.log('FAIL: private product acceptance runner failed; raw diagnostics withheld.'); process.exitCode = 1 })
