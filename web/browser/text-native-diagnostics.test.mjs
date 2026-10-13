import assert from 'node:assert/strict'
import { existsSync, mkdtempSync, readFileSync, rmSync, statSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import Reporter, { PHASES, failureRecord, recordFirstFailure, textPhase } from './text-native-reporter.mjs'
import { ERROR_KINDS } from './nearby-native-diagnostics.mjs'

function isolated(run) {
  const dir = mkdtempSync(join(tmpdir(), 'text-fixed-unit-'))
  const oldSession = process.env.SOBA_TRANSFER_BROWSER_SESSION, oldFailure = process.env.SOBA_TEXT_BROWSER_FAILURE
  process.env.SOBA_TRANSFER_BROWSER_SESSION = join(dir, 'session.json')
  process.env.SOBA_TEXT_BROWSER_FAILURE = join(dir, 'text-native-failure.json')
  return Promise.resolve().then(() => run(dir)).finally(() => {
    if (oldSession === undefined) delete process.env.SOBA_TRANSFER_BROWSER_SESSION; else process.env.SOBA_TRANSFER_BROWSER_SESSION = oldSession
    if (oldFailure === undefined) delete process.env.SOBA_TEXT_BROWSER_FAILURE; else process.env.SOBA_TEXT_BROWSER_FAILURE = oldFailure
    rmSync(dir, { recursive: true })
  })
}
const step = phase => ({ category: 'test.step', title: `text:${phase}` })

test('text failure schema permits only fixed phase and existing error class', () => {
  for (const phase of PHASES) for (const kind of ERROR_KINDS) assert.deepEqual(failureRecord(phase, kind), { schema: 1, phase, errorKind: kind })
  for (const [phase, kind] of [['synthetic-private', 'timeout'], ['detail-exact', 'synthetic-private'], [null, 'timeout'], ['detail-exact', 1]]) assert.throws(() => failureRecord(phase, kind))
})

test('the first failed late action survives cleanup and reporter failure', () => isolated(async dir => {
  const reporter = new Reporter()
  reporter.onBegin({}, { allTests: () => [{}] })
  reporter.onStepBegin({}, {}, step('detail-exact'))
  reporter.onStepEnd({}, { status: 'failed' }, { error: { message: 'Timeout 8000ms exceeded\nlocator resolved to 0 elements synthetic-private-body', stack: 'synthetic-private-path' } })
  reporter.onStepBegin({}, {}, step('body-complete'))
  reporter.onError({ message: 'Target page has been closed synthetic-private-token' })
  reporter.onTestEnd({}, { status: 'failed', retry: 0, errors: [{ message: 'synthetic-private-cleanup' }] })
  await reporter.onEnd({ status: 'failed' })
  const raw = readFileSync(join(dir, 'text-native-failure.json'), 'utf8')
  assert.deepEqual(JSON.parse(raw), { schema: 1, phase: 'detail-exact', errorKind: 'locator_missing' })
  assert.ok(!raw.includes('synthetic-private'))
  assert.equal(statSync(join(dir, 'text-native-failure.json')).mode & 0o077, 0)
  assert.deepEqual(JSON.parse(readFileSync(join(dir, 'transfer-browser-result.json'))), { schema: 1, accepted: false, observed: 1, passed: 0, errors: 1 })
}))

test('fixture setup and post-body cleanup are distinguished without body failure', () => isolated(async dir => {
  const reporter = new Reporter()
  reporter.onStepBegin({}, {}, { category: 'test.step', title: 'text:synthetic-private-token' })
  assert.equal(reporter.phase, 'fixture-setup')
  reporter.onStepEnd({}, { status: 'failed' }, { error: { message: "browserType.launch: Executable doesn't exist synthetic-private-path" } })
  assert.deepEqual(JSON.parse(readFileSync(join(dir, 'text-native-failure.json'))), { schema: 1, phase: 'fixture-setup', errorKind: 'browser_launch' })
  rmSync(join(dir, 'text-native-failure.json'))
  reporter.onStepBegin({}, {}, step('body-complete'))
  reporter.onStepEnd({}, { status: 'failed' }, { error: { message: 'Target page has been closed synthetic-private-token' } })
  assert.deepEqual(JSON.parse(readFileSync(join(dir, 'text-native-failure.json'))), { schema: 1, phase: 'fixture-cleanup', errorKind: 'browser_closed' })
}))

test('success retains the unchanged one-case receipt and never creates failure', () => isolated(async dir => {
  const reporter = new Reporter()
  reporter.onBegin({}, { allTests: () => [{}] })
  for (const phase of PHASES) reporter.onStepBegin({}, {}, step(phase))
  reporter.onStepEnd({}, { status: 'passed' }, {})
  reporter.onTestEnd({}, { status: 'passed', retry: 0, errors: [] })
  assert.equal(await reporter.onEnd({ status: 'passed' }), undefined)
  assert.equal(existsSync(join(dir, 'text-native-failure.json')), false)
  assert.deepEqual(JSON.parse(readFileSync(join(dir, 'transfer-browser-result.json'))), { schema: 1, accepted: true, observed: 1, passed: 1, errors: 0 })
}))

test('phase markers are fixed empty steps and diagnostics do not replace original failure', async () => {
  const seen = []
  await textPhase({ step: async (name, run) => { seen.push(name); await run() } }, 'outbox-status-ja')
  assert.deepEqual(seen, ['text:outbox-status-ja'])
  await assert.rejects(textPhase({}, 'synthetic-private'))
  await isolated(dir => {
    const file = join(dir, 'text-native-failure.json')
    recordFirstFailure(file, 'outbox-fit-ja', 'assertion')
    recordFirstFailure(file, 'fixture-cleanup', 'timeout')
    assert.deepEqual(JSON.parse(readFileSync(file)), { schema: 1, phase: 'outbox-fit-ja', errorKind: 'assertion' })
    process.env.SOBA_TEXT_BROWSER_FAILURE = join(dir, 'absent', 'text-native-failure.json')
    assert.doesNotThrow(() => new Reporter().onError({ message: 'synthetic-private' }))
  })
})

test('the complete case has distinct late body and cleanup boundaries', () => {
  const source = readFileSync(new URL('./text-outbox-native.acceptance.mjs', import.meta.url), 'utf8')
  const expected = ['session', 'open-peer', 'queue-mode', 'prepare', 'review-exact', 'review-consent', 'review-fit', 'review-checkpoint', 'commit', 'lost-response', 'recover', 'recovery-checkpoint', 'outbox-open', 'outbox-status-en', 'detail-request', 'detail-exact', 'detail-receipt-en', 'detail-fit-en', 'escape', 'locale-ja', 'outbox-open-ja', 'outbox-status-ja', 'outbox-receipt-ja', 'outbox-fit-ja', 'command-counts', 'runtime-counts', 'result-write', 'body-complete']
  assert.deepEqual([...source.matchAll(/await phase\('([^']+)'\)/g)].map(match => match[1]), expected)
  for (const phase of expected) assert.ok(PHASES.includes(phase))
  const config = readFileSync(new URL('../playwright.text-native.config.mjs', import.meta.url), 'utf8')
  assert.ok(config.includes("reporter: './browser/text-native-reporter.mjs'"))
  assert.ok(!config.includes('timeout') && !config.includes('launchOptions') && !config.includes('chromiumSandbox'))
})
