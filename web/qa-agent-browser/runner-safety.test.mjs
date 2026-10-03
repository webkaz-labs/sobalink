import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { chmod, mkdtemp, readFile, readdir, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'

const runner = fileURLToPath(new URL('../scripts/agent-browser-journeys.mjs', import.meta.url))
const marker = 'PRIVATE_QA_SENTINEL_MUST_NEVER_APPEAR'

// These tests deliberately fail before the first CLI invocation. They do not
// launch a daemon/browser and do not count as browser journey evidence.
async function rejectedSession(content, { mode = 0o600 } = {}) {
  const directory = await mkdtemp(join(tmpdir(), 'soba-agent-safety-'))
  try {
    const session = join(directory, 'private.json')
    const output = join(directory, 'artifacts')
    await writeFile(session, content, { mode })
    await chmod(session, mode)
    const child = spawnSync(process.execPath, [runner], {
      encoding: 'utf8', timeout: 10000,
      env: { ...process.env, SOBA_E2E_SESSION_FILE: session, SOBA_SCREENSHOT_DIR: output },
    })
    assert.equal(child.status, 1)
    const reportText = await readFile(join(output, 'agent-browser-report.json'), 'utf8')
    const report = JSON.parse(reportText)
    assert.equal(report.cliCalls, 0, 'preflight rejection must never launch a browser')
    assert.equal(report.requiredCoverageComplete, false)
    assert.equal(report.summary.failed, 1)
    assert.deepEqual(await readdir(output), ['agent-browser-report.json'])
    for (const value of [child.stdout, child.stderr, reportText]) assert.equal(value.includes(marker), false, 'private material must not enter diagnostics')
    return report
  } finally {
    await rm(directory, { recursive: true, force: true })
  }
}

test('malformed private session is rejected without echoing source text', async () => {
  await rejectedSession(`{"code":"${marker}",`)
})

test('unsafe fixture URL is rejected without echoing credential components', async () => {
  await rejectedSession(JSON.stringify({ url: `http://name:${marker}@127.0.0.1:12345`, code: 'unused' }))
})

test('publicly readable session is rejected before reading its values', async () => {
  await rejectedSession(JSON.stringify({ url: 'http://127.0.0.1:12345', code: marker }), { mode: 0o644 })
})

test('missing required fixture capability fails closed instead of skipping coverage', async () => {
  const report = await rejectedSession(JSON.stringify({ url: 'http://127.0.0.1:12345', code: marker, scenario: 'studio', capabilities: [] }))
  assert.match(report.journeys[0].assertion, /required capability/)
})

test('unknown fixture scenario fails closed before selecting any browser journey', async () => {
  const report = await rejectedSession(JSON.stringify({ url: 'http://127.0.0.1:12345', code: marker, scenario: 'other', capabilities: ['service-lifecycle', 'failed-upload-retry'] }))
  assert.match(report.journeys[0].assertion, /scenario must be exactly/)
})
