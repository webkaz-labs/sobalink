import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { caseIds, lifecycle, validateLifecycle, validateCases, emptyDiagnostics, validateDiagnostics, counter, advanceLifecycle, failLifecycle, exitCategory, passedLifecycle } from './activation-diagnostics.mjs'
import { activationCases } from './activation-contract.mjs'
import Reporter from './activation-reporter.mjs'

test('fixed seven IDs and empty observations are safe and complete', () => {
  const d = emptyDiagnostics()
  assert.deepEqual(validateDiagnostics(d), d)
  assert.equal(new Set(caseIds).size, 7)
  assert.equal(activationCases.length, 7)
  assert.equal(counter(9999), 255)
  assert.equal(counter(-1), 0)
  assert.equal(counter('synthetic private value'), 0)
})

test('lifecycle boundary rejects unknown fields, strings, types and ranges', () => {
  for (const change of [{ error: 'synthetic private value' }, { stage: 'https://example.invalid/private' }, { failureStage: '/private/path' }, { supervisorExit: 1 }, { reaped: true }, { reaped: -1 }, { reaped: 256 }, { runtimeErrors: 1.5 }, { proofRead: 1 }]) {
    assert.throws(() => validateLifecycle({ ...lifecycle(), ...change }))
  }
  for (const key of Object.keys(lifecycle())) {
    const value = lifecycle(); delete value[key]; assert.throws(() => validateLifecycle(value))
  }
})

test('case boundary rejects duplicates, arbitrary names, status and nested leaks', () => {
  const mutate = fn => { const value = emptyDiagnostics().cases; fn(value); assert.throws(() => validateCases(value)) }
  mutate(v => v.pop())
  mutate(v => { v[1].id = v[0].id })
  mutate(v => { v[0].id = 'synthetic secret' })
  mutate(v => { v[0].status = 'maybe-passed' })
  mutate(v => { v[0].status = 'passed' })
  mutate(v => { v[0].lifecycle.token = 'synthetic secret' })
  mutate(v => { v[0].attachment = 'synthetic secret' })
  for (const change of [{ observed: true }, { globalErrors: 256 }, { observed: 1 }, { passed: 1 }, { extra: 'synthetic secret' }]) assert.throws(() => validateDiagnostics({ ...emptyDiagnostics(), ...change }))
})

async function reported(configure) {
  const root = await mkdtemp(join(tmpdir(), 'fixed-web-report-'))
  const previous = process.env.SOBA_ACTIVATION_PRIVATE_RUN
  process.env.SOBA_ACTIVATION_PRIVATE_RUN = root
  try {
    const reporter = new Reporter()
    const tests = activationCases.map(title => ({ title, expectedStatus: 'passed', annotations: [] }))
    reporter.onBegin({}, { allTests: () => tests })
    configure(reporter, tests)
    const outcome = await reporter.onEnd({ status: 'passed' })
    return { outcome, raw: await readFile(join(root, 'sanitized-summary.json'), 'utf8') }
  } finally {
    if (previous === undefined) delete process.env.SOBA_ACTIVATION_PRIVATE_RUN
    else process.env.SOBA_ACTIVATION_PRIVATE_RUN = previous
    await rm(root, { recursive: true, force: true })
  }
}
function passedLife() { return { ...lifecycle(), stage: 'finished', workCompleted: true, stopRequested: true, noWaitableChildren: true, registeredChildren: 1, observedExits: 1, reaped: 1, supervisorExit: 'zero', ...Object.fromEntries(['supervisorStarted', 'supervisorExited', 'proofRead', 'allDescendantsReaped', 'registeredNativeExits', 'contextClosed', 'profileRemoved'].map(k => [k, true])) } }
function complete(reporter, item, status = 'passed', life = passedLife()) {
  reporter.onTestBegin(item)
  item.annotations.push({ type: 'activation-sanitized', description: JSON.stringify(life) })
  reporter.onTestEnd(item, { status, retry: 0 })
}

test('reporter records only fixed IDs and observed statuses', async () => {
  const { raw, outcome } = await reported((reporter, items) => items.forEach((item, i) => complete(reporter, item, i === 2 ? 'failed' : 'passed', { ...lifecycle(), stage: 'cleanup-proof', failureStage: 'body' })))
  const result = JSON.parse(raw)
  assert.equal(outcome.status, 'failed'); assert.equal(result.accepted, false)
  assert.equal(result.diagnostics.observed, 7); assert.equal(result.diagnostics.passed, 6)
  assert.equal(result.diagnostics.cases[2].id, 'decline'); assert.equal(result.diagnostics.cases[2].status, 'failed')
  assert.equal(raw.includes(activationCases[0]), false)
})

test('reporter rejects poisoned annotations and never serializes private errors', async () => {
  const secret = 'SYNTHETIC-PRIVATE-DO-NOT-EXPORT'
  const { raw } = await reported((reporter, items) => {
    items.forEach(item => complete(reporter, item, 'failed', { ...lifecycle(), token: secret }))
    reporter.onError({ message: secret, stack: secret, url: secret })
  })
  assert.equal(raw.includes(secret), false)
  const result = JSON.parse(raw)
  assert.equal(result.accepted, false); assert.equal(result.diagnostics.unexpected, true)
  assert.equal(result.diagnostics.globalErrors, 1)
  assert.equal(validateDiagnostics(result.diagnostics).observed, 7)
})

test('seven exact passes remain required; skips and retries cannot pass', async () => {
  const good = await reported((r, items) => items.forEach(item => complete(r, item)))
  assert.equal(JSON.parse(good.raw).accepted, true)
  const skipped = await reported((r, items) => items.forEach((item, i) => complete(r, item, i ? 'passed' : 'skipped')))
  assert.equal(JSON.parse(skipped.raw).accepted, false)
  const retry = await reported((r, items) => { items.forEach(item => complete(r, item)); r.onTestEnd(items[0], { status: 'passed', retry: 1 }) })
  assert.equal(JSON.parse(retry.raw).accepted, false)
})

test('first work stage survives cleanup and known fixture exit codes stay bounded', () => {
  const value = lifecycle()
  advanceLifecycle(value, 'review-request'); failLifecycle(value)
  advanceLifecycle(value, 'cleanup-proof'); failLifecycle(value)
  assert.equal(value.workStage, 'review-request')
  assert.equal(value.failureStage, 'review-request')
  assert.equal(value.stage, 'cleanup-proof')
  assert.throws(() => advanceLifecycle(value, 'synthetic-private-path'))
  for (const [code, name] of [[0, 'zero'], [66, 'race'], [91, 'profile-rejected'], [92, 'watchdog'], [93, 'mode-rejected'], [94, 'owner-failed'], [95, 'supervisor-failed'], [96, 'registration-failed'], [null, 'signal'], [999, 'other'], ['private', 'other']]) assert.equal(exitCategory(code), name)
})

test('supervisor diagnostic counters never replace joined cleanup evidence', () => {
  const value = { ...passedLife(), registeredChildren: 2, observedExits: 2, reaped: 1 }
  assert.equal(passedLifecycle(value), true)
  value.allDescendantsReaped = false
  assert.equal(passedLifecycle(value), false)
})

test('synthetic locale label excludes option text while retaining exact label lookup', async () => {
  const html = await readFile(new URL('./activation-app/index.html', import.meta.url), 'utf8')
  assert.equal((html.match(/<label for="locale">Locale<\/label>/g) || []).length, 1)
  assert.equal((html.match(/<select id="locale">/g) || []).length, 1)
  assert.match(html, /<label for="locale">Locale<\/label><select id="locale"><option value="en">English<\/option><option value="ja">日本語<\/option><\/select>/)
  const fixture = await readFile(new URL('./activation-fixtures.mjs', import.meta.url), 'utf8')
  assert.ok(fixture.includes("page.getByLabel('Locale', { exact: true }).selectOption(locale)"))
})
