import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { caseIds, lifecycle, validateLifecycle, validateCases, emptyDiagnostics, validateDiagnostics, counter, phase, failWork, cleanupPhase, failCleanup, nativeObservation, readNativeObservation, exitCategory } from './product-activation-diagnostics.mjs'
import { productActivationCases } from './product-activation-contract.mjs'
import Reporter from './product-activation-reporter.mjs'

test('fixed two-case inventory is complete with no invented observations', () => {
  const value = emptyDiagnostics()
  assert.deepEqual(validateDiagnostics(value), value)
  assert.deepEqual(caseIds, ['product-web', 'product-cli'])
  assert.equal(productActivationCases.length, 2)
  assert.equal(counter(9999), 255)
  for (const invalid of [-1, 1.5, true, 'private', Infinity]) assert.equal(counter(invalid), 0)
  for (const row of value.cases) for (const native of Object.values(row.lifecycle.native)) assert.deepEqual(native, nativeObservation())
})

test('lifecycle rejects unknown fields, strings, types, missing keys and ranges', () => {
  for (const change of [{ error: 'SYNTHETIC-PRIVATE' }, { workPhase: 'https://example.invalid/private' }, { firstFailurePhase: '/private/path' }, { cleanupPhase: 'maybe-clean' }, { firstCleanupFailurePhase: 'private-code' }, { supervisorExit: 1 }, { reaped: true }, { reaped: -1 }, { runtimeErrors: 256 }, { blockedRequests: 0.5 }, { proofRead: 1 }]) assert.throws(() => validateLifecycle({ ...lifecycle(), ...change }))
  for (const key of Object.keys(lifecycle())) { const value = lifecycle(); delete value[key]; assert.throws(() => validateLifecycle(value)) }
  for (const key of ['owner', 'helper', 'arbitrary']) { const value = lifecycle(); value.native[key] = nativeObservation(); assert.throws(() => validateLifecycle(value)) }
})

test('native files have exact closed shapes and unavailable is distinct from failed', () => {
  const valid = { schema: 1, stage: 'owner-status', failed: false, ownerStatus: 'network-started', peerStatus: 'exchanging' }
  assert.deepEqual(readNativeObservation(valid), { available: true, invalid: false, ...Object.fromEntries(Object.entries(valid).filter(([key]) => key !== 'schema')) })
  for (const patch of [{ schema: true }, { token: 'SYNTHETIC-PRIVATE' }, { stage: '/private/file' }, { ownerStatus: 'secret' }, { peerStatus: 1 }, { failed: 'false' }]) assert.throws(() => readNativeObservation({ ...valid, ...patch }))
  for (const key of Object.keys(valid)) { const value = { ...valid }; delete value[key]; assert.throws(() => readNativeObservation(value)) }
  for (const patch of [{ available: true, invalid: true }, { available: false, failed: true }, { available: false, stage: 'network-ready' }, { privatePath: 'SYNTHETIC-PRIVATE' }]) {
    const value = lifecycle(); Object.assign(value.native.old, patch); assert.throws(() => validateLifecycle(value))
  }
  const invalid = lifecycle(); invalid.native.old.invalid = true
  assert.equal(validateLifecycle(invalid).native.old.available, false)
})

test('case boundary rejects reordered IDs, duplicate inventory and nested leaks', () => {
  const mutate = change => { const value = emptyDiagnostics().cases; change(value); assert.throws(() => validateCases(value)) }
  mutate(value => value.pop())
  mutate(value => value.reverse())
  mutate(value => { value[1].id = value[0].id })
  mutate(value => { value[0].id = 'private-name' })
  mutate(value => { value[0].status = 'maybe-passed' })
  mutate(value => { value[0].status = 'passed' })
  mutate(value => { value[0].diagnosticAvailable = true; value[0].diagnosticRejected = true })
  mutate(value => { value[0].lifecycle.token = 'private-code' })
  for (const patch of [{ observed: true }, { globalErrors: 256 }, { observed: 1 }, { passed: 1 }, { attachment: 'private-code' }]) assert.throws(() => validateDiagnostics({ ...emptyDiagnostics(), ...patch }))
})

test('first work failure remains separate from all later cleanup failures', () => {
  const value = lifecycle()
  phase(value, 'review'); failWork(value)
  phase(value, 'helper'); failWork(value)
  cleanupPhase(value, 'context'); failCleanup(value)
  cleanupPhase(value, 'proof'); failCleanup(value)
  assert.equal(value.firstFailurePhase, 'review')
  assert.equal(value.firstCleanupFailurePhase, 'context')
  assert.equal(value.workPhase, 'helper')
  assert.equal(value.cleanupPhase, 'proof')
  assert.throws(() => phase(value, 'private-code'))
  assert.throws(() => cleanupPhase(value, 'private-path'))
  for (const [code, name] of [[0, 'zero'], [66, 'race'], [91, 'profile-rejected'], [92, 'watchdog'], [93, 'mode-rejected'], [94, 'owner-failed'], [95, 'supervisor-failed'], [96, 'registration-failed'], [null, 'signal'], [999, 'other'], ['private', 'other']]) assert.equal(exitCategory(code), name)
})

async function reported(configure) {
  const root = await mkdtemp(join(tmpdir(), 'fixed-product-report-'))
  const previous = process.env.SOBA_PRODUCT_ACTIVATION_PRIVATE_RUN
  process.env.SOBA_PRODUCT_ACTIVATION_PRIVATE_RUN = root
  try {
    const reporter = new Reporter()
    const tests = productActivationCases.map(title => ({ title, expectedStatus: 'passed', annotations: [] }))
    reporter.onBegin({}, { allTests: () => tests })
    configure(reporter, tests)
    const outcome = await reporter.onEnd({ status: 'passed' })
    const raw = await readFile(join(root, 'sanitized-summary.json'), 'utf8')
    return { outcome, raw, result: JSON.parse(raw) }
  } finally {
    if (previous === undefined) delete process.env.SOBA_PRODUCT_ACTIVATION_PRIVATE_RUN
    else process.env.SOBA_PRODUCT_ACTIVATION_PRIVATE_RUN = previous
    await rm(root, { recursive: true, force: true })
  }
}
function complete(reporter, item, status = 'passed', life) {
  reporter.onTestBegin(item)
  if (life) item.annotations.push({ type: 'product-activation-sanitized', description: JSON.stringify(life) })
  reporter.onTestEnd(item, { status, retry: 0, attachments: [] })
}

test('optional diagnostics do not replace or expand the original acceptance gate', async () => {
  const absent = await reported((reporter, items) => items.forEach(item => complete(reporter, item)))
  assert.equal(absent.result.accepted, true)
  assert.equal(absent.result.schema, 2)
  assert.equal(absent.result.diagnostics.cases[0].diagnosticAvailable, false)
  const evidenceOnly = await reported((reporter, items) => items.forEach(item => complete(reporter, item, 'passed', lifecycle())))
  assert.equal(evidenceOnly.result.accepted, true)
  assert.equal(evidenceOnly.result.diagnostics.cases[0].diagnosticAvailable, true)
  for (const status of ['failed', 'timedOut', 'skipped', 'interrupted']) {
    const failed = await reported((reporter, items) => items.forEach((item, index) => complete(reporter, item, index ? 'passed' : status, lifecycle())))
    assert.equal(failed.result.accepted, false)
    assert.equal(failed.result.diagnostics.cases[0].status, status)
    assert.equal(validateDiagnostics(failed.result.diagnostics).passed, 1)
  }
})

test('reporter rejects poisonous annotations without copying arbitrary data', async () => {
  const secret = 'SYNTHETIC-PRIVATE-DO-NOT-EXPORT'
  const { raw, result } = await reported((reporter, items) => {
    items.forEach(item => complete(reporter, item, 'failed', { ...lifecycle(), token: secret }))
    reporter.onError({ message: secret, stack: secret, path: secret })
  })
  assert.equal(raw.includes(secret), false)
  assert.equal(raw.includes(productActivationCases[0]), false)
  assert.equal(result.accepted, false)
  assert.equal(result.diagnostics.globalErrors, 1)
  assert.equal(result.diagnostics.cases[0].diagnosticRejected, true)
  assert.equal(validateDiagnostics(result.diagnostics).observed, 2)
})

test('duplicate results cannot overwrite the first recorded work failure', async () => {
  const first = lifecycle(); phase(first, 'popup'); failWork(first)
  const { result } = await reported((reporter, items) => {
    complete(reporter, items[0], 'failed', first)
    complete(reporter, items[1])
    items[0].annotations = [{ type: 'product-activation-sanitized', description: JSON.stringify(lifecycle()) }]
    reporter.onTestEnd(items[0], { status: 'passed', retry: 1, attachments: [] })
  })
  assert.equal(result.accepted, false)
  assert.equal(result.diagnostics.unexpected, true)
  assert.equal(result.diagnostics.cases[0].lifecycle.firstFailurePhase, 'popup')
  assert.equal(result.diagnostics.cases[0].status, 'failed')
})

test('unknown selections and duplicate or oversized annotations remain bounded', async () => {
  for (const annotation of ['x'.repeat(4097), '{invalid-json']) {
    const { raw, result } = await reported((reporter, items) => {
      items[0].annotations.push({ type: 'product-activation-sanitized', description: annotation })
      items.forEach(item => complete(reporter, item, 'failed'))
      reporter.onTestBegin({ title: 'private-test-name' })
    })
    assert.equal(raw.includes(annotation), false)
    assert.equal(raw.includes('private-test-name'), false)
    assert.equal(result.diagnostics.cases[0].diagnosticRejected, true)
  }
  const duplicate = await reported((reporter, items) => {
    items[0].annotations.push({ type: 'product-activation-sanitized', description: JSON.stringify(lifecycle()) })
    items.forEach(item => complete(reporter, item, 'failed', lifecycle()))
  })
  assert.equal(duplicate.result.diagnostics.cases[0].diagnosticRejected, true)
})
