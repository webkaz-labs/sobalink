import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { assertSeparateArtifacts, safeArtifactName, validateSession, CAPTURE_FORBIDDEN_SELECTOR, PRIVATE_VALUE_SELECTOR, privateControlsAreEmpty } from './fixture-safety.mjs'
import SafeReporter from './reporter.mjs'

const sentinel = 'PRIVATE_FIXTURE_SENTINEL_MUST_NOT_APPEAR'
const valid = { url: 'http://127.0.0.1:43210/', code: sentinel, scenario: 'studio', receiveDirectory: '/tmp/fixture/received', capabilities: ['service-lifecycle', 'failed-upload-retry', 'application-stop'], localServicePort: 43919 }
function rejected(value, scenario = 'studio') {
  assert.throws(() => validateSession(value, scenario), error => !error.message.includes(sentinel))
}
test('accepts the expected isolated Go fixture without exposing its private values', () => {
  assert.equal(validateSession(valid, 'studio').localServicePort, 43919)
})
test('rejects external, nonnumeric and credential-bearing fixture URLs', () => {
  for (const url of ['https://127.0.0.1/', 'http://localhost/', 'http://192.0.2.1/', `http://user:${sentinel}@127.0.0.1/`, `http://127.0.0.1/?code=${sentinel}`, `http://127.0.0.1/#${sentinel}`, sentinel]) rejected({ ...valid, url })
})
test('requires the exact scenario, capabilities and safe reserved port', () => {
  rejected({ ...valid, scenario: 'other' })
  rejected({ ...valid, capabilities: ['service-lifecycle'] })
  for (const localServicePort of [0, 22, 65536, 43919.5, sentinel]) rejected({ ...valid, localServicePort })
  rejected(valid, 'offline')
})
test('recovery scenarios require their own capabilities and isolated service port', () => {
  for (const [scenario, capability] of [['receive-legacy', 'legacy-receive-review'], ['receive-damaged', 'damaged-receive-index']]) {
    const recovery = { ...valid, scenario, capabilities: ['service-lifecycle', 'receive-recovery', 'saved-autosave', capability] }
    assert.equal(validateSession(recovery, scenario).scenario, scenario)
    rejected(recovery)
    rejected({ ...valid, scenario }, scenario)
    rejected({ ...recovery, localServicePort: 0 }, scenario)
    rejected({ ...recovery, capabilities: recovery.capabilities.join(',') }, scenario)
    for (const missing of recovery.capabilities) rejected({ ...recovery, capabilities: recovery.capabilities.filter(value => value !== missing) }, scenario)
    rejected(recovery, scenario === 'receive-legacy' ? 'receive-damaged' : 'receive-legacy')
  }
})
test('requires an absolute receiving root and nonempty access code', () => {
  rejected({ ...valid, receiveDirectory: `relative/${sentinel}` })
  rejected({ ...valid, code: '' })
})
test('artifact names cannot select private paths or nested files', () => {
  assert.equal(safeArtifactName('graph-ja-dark-390.png'), 'graph-ja-dark-390.png')
  for (const name of ['../session.json', '/tmp/session.json', 'a/b', '..', '']) assert.throws(() => safeArtifactName(name))
})
test('artifacts cannot overlap the private runtime in either direction', () => {
  assertSeparateArtifacts('/tmp/artifacts', '/tmp/private-fixture')
  for (const output of ['/tmp/private-fixture', '/tmp/private-fixture/images', '/tmp']) assert.throws(() => assertSeparateArtifacts(output, '/tmp/private-fixture'))
})

test('result reports omit private error details and never mark discovery-only runs as accepted', async context => {
  context.mock.method(console, 'log', () => {})
  const directory = await mkdtemp(join(tmpdir(), 'soba-reporter-safety-'))
  const previous = process.env.SOBA_SCREENSHOT_DIR
  process.env.SOBA_SCREENSHOT_DIR = directory
  try {
    const reporter = new SafeReporter()
    reporter.onBegin({}, { allTests: () => [1] })
    await reporter.onEnd({ status: 'passed' })
    const path = join(directory, 'playwright-report.json')
    assert.equal(JSON.parse(await readFile(path, 'utf8')).requiredCoverageComplete, false)
    reporter.onTestEnd({ titlePath: () => ['fixture privacy'], location: { file: '/private/workspace/workflows.spec.mjs', line: 10 } }, { status: 'failed', duration: 1, errors: [{ message: sentinel, stack: `${sentinel}\n    at Object.test (/private/workspace/workflows.spec.mjs:123:7)\n    at privateValue (/private/workspace/${sentinel}.mjs:9:9)\n    at another (/private/workspace/other.spec.mjs:4:5)` }], attachments: [{ body: sentinel }] })
    await reporter.onEnd({ status: 'failed' })
    const result = await readFile(path, 'utf8')
    assert.equal(result.includes(sentinel), false)
    assert.equal(result.includes('/private/workspace'), false)
    assert.equal(JSON.parse(result).requiredCoverageComplete, false)
    assert.deepEqual(JSON.parse(result).tests[0].failureLocations, [{ file: 'workflows.spec.mjs', line: 123, column: 7 }])
  } finally {
    if (previous === undefined) delete process.env.SOBA_SCREENSHOT_DIR
    else process.env.SOBA_SCREENSHOT_DIR = previous
    await rm(directory, { recursive: true, force: true })
  }
})

test('capture excludes credential forms and all nonempty private values without serializing them', () => {
  for (const selector of ['.login-panel', '.auth-private', '.proxy-credentials', '[data-private=proxy-credential]']) assert.ok(CAPTURE_FORBIDDEN_SELECTOR.includes(selector))
  for (const selector of ['input[type=password]', '.private-copy', '[data-private]']) assert.ok(PRIVATE_VALUE_SELECTOR.includes(selector))
  assert.equal(privateControlsAreEmpty([]), true)
  assert.equal(privateControlsAreEmpty([{ value: '', textContent: '' }]), true)
  assert.equal(privateControlsAreEmpty([{ value: sentinel, textContent: '' }]), false)
  assert.equal(privateControlsAreEmpty([{ value: '', textContent: sentinel }]), false)
})
