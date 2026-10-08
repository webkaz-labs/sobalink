import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { chmod, mkdtemp, readFile, readdir, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve, relative } from 'node:path'

async function run() {
  assert.equal(process.argv.length, 2, 'Acceptance selection cannot be overridden')
  assert.equal(process.platform, 'linux', 'Subreaper fixture is Linux-only')
  assert.equal(process.env.SOBA_WEB_ACTIVATION_ACCEPTANCE, '1', 'Explicit opt-in required')
  assert.ok(!process.env.DEBUG && !process.env.PWDEBUG, 'Debug logging forbidden')
  assert.ok(process.env.SOBA_ACTIVATION_CHROMIUM, 'Verified installed Chromium path required')
  const report = resolve(process.env.SOBA_ACTIVATION_REPORT || 'activation-sanitized-result.json')
  let root, child, childExited = false, exitCode = -1, timedOut = false, outputOverflow = false, bytes = 0, summary, cleanupProven = false
  try {
    root = await mkdtemp(join(tmpdir(), 'sa-')); await chmod(root, 0o700)
    assert.ok(relative(root, report).startsWith('..'), 'Report must be outside private runtime')
    child = spawn(process.execPath, ['node_modules/@playwright/test/cli.js', 'test', '--config', 'playwright.activation.config.mjs'], { env: { PATH: process.env.PATH || '', HOME: root, TMPDIR: root, LANG: 'C.UTF-8', SOBA_WEB_ACTIVATION_ACCEPTANCE: '1', SOBA_WEB_ACTIVATION_BINARY: process.env.SOBA_WEB_ACTIVATION_BINARY || '', SOBA_WEB_ACTIVATION_ASSETS: process.env.SOBA_WEB_ACTIVATION_ASSETS || '', SOBA_ACTIVATION_CHROMIUM: process.env.SOBA_ACTIVATION_CHROMIUM, SOBA_ACTIVATION_PRIVATE_RUN: root }, stdio: ['ignore', 'pipe', 'pipe'] })
    const discard = chunk => { bytes += chunk.length; if (bytes > 1 << 20) outputOverflow = true }
    child.stdout.on('data', discard); child.stderr.on('data', discard)
    let soft, hard
    try {
      exitCode = await new Promise(resolveExit => {
        child.once('error', () => { childExited = !child.pid; resolveExit(-1) })
        child.once('exit', code => { childExited = true; resolveExit(code ?? -1) })
        // Signals target only this wrapper's exact Playwright child. There is
        // no product force-kill or assumed cleanup after timeout.
        soft = setTimeout(() => { timedOut = true; child.kill('SIGTERM') }, 21 * 60_000)
        hard = setTimeout(() => { timedOut = true; child.stdout.destroy(); child.stderr.destroy(); child.unref(); resolveExit(-2) }, 23 * 60_000)
      })
    } finally { clearTimeout(soft); clearTimeout(hard) }
    try { summary = JSON.parse(await readFile(join(root, 'sanitized-summary.json'), 'utf8')) } catch {}
  } finally {
    if (root) {
      // No deletion while Playwright or an unproved native profile survives.
      // Raw error-context files remain private and categorically unexportable.
      cleanupProven = (!child || (childExited && exitCode === 0 && summary?.accepted === true)) && !(await readdir(root)).some(name => name.startsWith('sr-'))
      if (cleanupProven) await rm(root, { recursive: true, force: true })
    }
  }
  const schemaValid = summary && summary.schema === 1 && summary.expected === 7 && summary.observed === 7 && summary.passed === 7 && summary.globalErrors === 0 && summary.selectionValid === true && summary.unexpected === false && summary.runnerPassed === true && summary.accepted === true
  const accepted = exitCode === 0 && !timedOut && !outputOverflow && cleanupProven && Boolean(schemaValid)
  await writeFile(report, JSON.stringify({ schema: 1, accepted, expected: 7, allSevenPassed: Boolean(schemaValid), runnerExitedSuccessfully: exitCode === 0, timedOut, outputOverflow, nativeCleanupProven: cleanupProven, scope: 'Linux real helper/HTTP/browser with synthetic owners; Core and OS-open excluded' }), { mode: 0o600 })
  console.log(accepted ? 'PASS: seven synthetic-owner cases and private cleanup verified.' : 'FAIL: synthetic-owner acceptance or private cleanup not verified. Raw diagnostics are never exportable.')
  process.exitCode = accepted ? 0 : 1
}
run().catch(() => { console.log('FAIL: private acceptance runner failed; raw diagnostics withheld.'); process.exitCode = 1 })
