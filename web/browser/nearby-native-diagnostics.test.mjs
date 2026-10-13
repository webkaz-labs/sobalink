import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync, statSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { PHASES, ERROR_KINDS, errorKind, failureRecord, recordFirstFailure } from './nearby-native-diagnostics.mjs'

test('failure evidence contains only closed phase and error kind', () => {
  for (const phase of PHASES) for (const kind of ERROR_KINDS) assert.deepEqual(failureRecord(phase, kind), { schema: 1, phase, errorKind: kind })
  for (const [phase, kind] of [['synthetic-private-path', 'timeout'], ['start-a', 'synthetic-private-body'], ['', 'timeout'], ['start-a', null]]) {
    assert.throws(() => failureRecord(phase, kind))
  }
})
test('error classification never returns raw error data', () => {
  const privateText = 'synthetic-private-contact-and-token'
  for (const [error, status, kind] of [
    [{ name: 'TimeoutError', message: privateText }, undefined, 'timeout'],
    [{ message: 'strict mode violation ' + privateText }, undefined, 'selector_ambiguous'],
    [{ message: 'browserType.launch ' + privateText }, undefined, 'browser_launch'],
    [{ message: 'Target page has been closed ' + privateText }, undefined, 'browser_closed'],
    [{ message: 'expect(locator).toBeVisible() ' + privateText }, undefined, 'assertion'],
    [{ message: privateText }, 'timedOut', 'timeout'],
    [{ message: privateText }, 'interrupted', 'interrupted'],
    [{ message: privateText }, undefined, 'unexpected'],
  ]) assert.equal(errorKind(error, status), kind)
})
test('first failure survives later cleanup and reporter failures', () => {
  const dir = mkdtempSync(join(tmpdir(), 'nearby-diagnostic-unit-'))
  try {
    const file = join(dir, 'nearby-browser-failure.json')
    recordFirstFailure(dir, 'fingerprint-b', 'assertion')
    recordFirstFailure(dir, 'cleanup', 'browser_closed')
    recordFirstFailure(dir, 'launch', 'unexpected')
    assert.deepEqual(JSON.parse(readFileSync(file, 'utf8')), { schema: 1, phase: 'fingerprint-b', errorKind: 'assertion' })
    assert.equal(statSync(file).mode & 0o077, 0)
  } finally { rmSync(dir, { recursive: true }) }
})
test('no success or raw browser fields exist in the failure schema', () => {
  const value = failureRecord('recover-a', 'timeout')
  assert.deepEqual(Object.keys(value).sort(), ['errorKind', 'phase', 'schema'])
  assert.equal(Object.hasOwn(value, 'accepted'), false)
})
test('reporter failure exports only the first phase and fixed kind', async () => {
  const { default: Reporter } = await import('./nearby-native-reporter.mjs')
  const dir = mkdtempSync(join(tmpdir(), 'nearby-reporter-unit-')), old = process.env.SOBA_TRANSFER_BROWSER_SESSION
  process.env.SOBA_TRANSFER_BROWSER_SESSION = join(dir, 'session.json')
  try {
    const reporter = new Reporter()
    reporter.onBegin({}, { allTests: () => [{}] })
    reporter.onStepBegin({}, {}, { category: 'test.step', title: 'nearby:plan-b' })
    reporter.onTestEnd({}, { status: 'failed', retry: 0, errors: [{ message: 'strict mode violation synthetic-private-selector', stack: 'synthetic-private-path' }] })
    reporter.onStepBegin({}, {}, { category: 'test.step', title: 'nearby:cleanup' })
    reporter.onError({ message: 'synthetic-private-token' })
    await reporter.onEnd({ status: 'failed' })
    assert.deepEqual(JSON.parse(readFileSync(join(dir, 'nearby-browser-failure.json'), 'utf8')), { schema: 1, phase: 'plan-b', errorKind: 'selector_ambiguous' })
    assert.deepEqual(JSON.parse(readFileSync(join(dir, 'transfer-browser-result.json'), 'utf8')), { schema: 1, accepted: false, observed: 1, passed: 0, errors: 1 })
  } finally { if (old === undefined) delete process.env.SOBA_TRANSFER_BROWSER_SESSION; else process.env.SOBA_TRANSFER_BROWSER_SESSION = old; rmSync(dir, { recursive: true }) }
})
test('reporter success retains the exact pass receipt and creates no failure', async () => {
  const { default: Reporter } = await import('./nearby-native-reporter.mjs')
  const dir = mkdtempSync(join(tmpdir(), 'nearby-reporter-unit-')), old = process.env.SOBA_TRANSFER_BROWSER_SESSION
  process.env.SOBA_TRANSFER_BROWSER_SESSION = join(dir, 'session.json')
  try {
    const reporter = new Reporter()
    reporter.onBegin({}, { allTests: () => [{}] })
    reporter.onTestEnd({}, { status: 'passed', retry: 0, errors: [] })
    assert.equal(await reporter.onEnd({ status: 'passed' }), undefined)
    assert.deepEqual(JSON.parse(readFileSync(join(dir, 'transfer-browser-result.json'), 'utf8')), { schema: 1, accepted: true, observed: 1, passed: 1, errors: 0 })
    assert.throws(() => statSync(join(dir, 'nearby-browser-failure.json')), { code: 'ENOENT' })
  } finally { if (old === undefined) delete process.env.SOBA_TRANSFER_BROWSER_SESSION; else process.env.SOBA_TRANSFER_BROWSER_SESSION = old; rmSync(dir, { recursive: true }) }
})
