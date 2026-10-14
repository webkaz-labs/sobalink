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

test('actionability classifies only explicit pinned Playwright outcomes and keeps timeout fallback', () => {
  const hidden = 'synthetic-private-selector-and-token'
  const outcomes = [
    ['element(s) not found', 'locator_missing'],
    ['locator resolved to 0 elements', 'locator_missing'],
    ['element is not enabled', 'element_disabled'],
    ['option being selected is not enabled', 'element_disabled'],
    ['element is not visible', 'element_hidden'],
    ['element is not editable', 'element_not_editable'],
    ['element was detached from the DOM, retrying', 'element_detached'],
    [`<div>${hidden}</div> intercepts pointer events`, 'pointer_intercepted'],
    ['element is not stable', 'element_unstable'],
  ]
  for (const [observation, expected] of outcomes) {
    const error = { name: 'TimeoutError', message: `locator.fill: Timeout 8000ms exceeded.\nCall log:\n  - waiting for ${hidden}\n  - ${observation}`, stack: hidden }
    assert.equal(errorKind(error), expected)
    assert.equal(errorKind(error, 'timedOut'), expected)
    assert.equal(JSON.stringify(failureRecord('fingerprint-match-edit-a', errorKind(error))).includes(hidden), false)
  }
  for (const message of [`Timeout 8000ms exceeded.\nCall log:\n  - waiting for ${hidden}`, `Timeout 8000ms exceeded: selector ${hidden} disabled`, 'Timeout 8000ms exceeded: element is not a known state']) assert.equal(errorKind({ message }), 'timeout')
  assert.equal(errorKind({ message: hidden }), 'unexpected')
  assert.equal(errorKind({ message: 'element is not visible' }, 'interrupted'), 'interrupted')
  assert.equal(errorKind({ message: 'element is not enabled\nelement was detached from the DOM, retrying' }), 'element_detached')
  assert.equal(errorKind({ message: 'element was detached from the DOM, retrying\nelement is not enabled' }), 'element_disabled')
})

test('actionability phrases are bound to the installed pinned Playwright source', async () => {
  const { createRequire } = await import('node:module')
  const bundle = readFileSync(createRequire(import.meta.url).resolve('playwright-core/lib/coreBundle'), 'utf8')
  for (const phrase of ['element(s) not found', 'locator resolved to 0 elements', 'option being selected is not enabled', 'element is not visible', 'element was detached from the DOM, retrying', 'intercepts pointer events', 'element is not ${result2.missingState}']) assert.ok(bundle.includes(phrase))
  for (const prefix of ['manual-edit', 'manual-submit', 'fingerprint-read', 'fingerprint-initial', 'fingerprint-mismatch-edit', 'fingerprint-mismatch-blocked', 'fingerprint-match-edit', 'fingerprint-verify', 'fingerprint-consent', 'fingerprint-unverify', 'fingerprint-ready']) {
    for (const side of ['a', 'b']) assert.ok(PHASES.includes(`${prefix}-${side}`))
  }
  for (const value of ['approve-send-a', 'approve-response-a', 'approve-uncertain-a', 'recover-request-a', 'recover-receipt-a', 'approve-send-b', 'approve-receipt-b']) assert.ok(PHASES.includes(value))
})
