import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { assertSeparateArtifacts, safeArtifactName, validateSession, CAPTURE_FORBIDDEN_SELECTOR, PRIVATE_VALUE_SELECTOR, privateControlsAreEmpty } from './fixture-safety.mjs'
import SafeReporter from './reporter.mjs'
import { drainRoutesBeforeCleanup } from './fixture-lifecycle.mjs'
import { createSyntheticPairingInterception, SYNTHETIC_PAIRING_INVITATION, verifyCommandsBeforeCleanup } from './synthetic-pairing.mjs'

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
  const outputLines = []
  context.mock.method(console, 'log', line => outputLines.push(line))
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
    assert.ok(outputLines.includes('DIAGNOSTIC fixture-or-test-error workflows.spec.mjs:123:7'))
    const consoleOutput = outputLines.join('\n')
    for (const privateText of [sentinel, '/private/workspace', 'privateValue', 'other.spec.mjs', 'another']) assert.equal(consoleOutput.includes(privateText), false)
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

test('paired routes require their own offline capabilities and private input location', () => {
  const routes = { ...valid, scenario: 'routes', localServicePort: 0, capabilities: ['offline-network', 'route-authorization', 'prepared-route-edit'], routePeerId: '1'.repeat(64), routeUpdateFile: '/tmp/fixture/route-update.json' }
  assert.equal(validateSession(routes, 'routes').routePeerId, routes.routePeerId)
  rejected(routes)
  for (const missing of routes.capabilities) rejected({ ...routes, capabilities: routes.capabilities.filter(value => value !== missing) }, 'routes')
  for (const routePeerId of ['', 'A'.repeat(64), sentinel]) rejected({ ...routes, routePeerId }, 'routes')
  for (const routeUpdateFile of ['', `relative/${sentinel}`]) rejected({ ...routes, routeUpdateFile }, 'routes')
})

test('fixture teardown waits for in-flight routes before stopping the backend', async () => {
  const route = Promise.withResolvers()
  const events = []
  let settled = false
  const teardown = drainRoutesBeforeCleanup({
    async unrouteAll(options) {
      assert.deepEqual(options, { behavior: 'wait' })
      events.push('drain-started')
      await route.promise
      events.push('route-settled')
    },
  }, async () => { events.push('backend-stopped') }).then(() => { settled = true })
  await Promise.resolve()
  assert.deepEqual(events, ['drain-started'])
  assert.equal(settled, false)
  route.resolve()
  await teardown
  assert.deepEqual(events, ['drain-started', 'route-settled', 'backend-stopped'])
  assert.equal(settled, true)
})

test('fixture teardown still cleans up and exposes route-drain rejection', async () => {
  const route = Promise.withResolvers()
  const failure = new Error('synthetic route failure')
  const events = []
  const teardown = drainRoutesBeforeCleanup({
    async unrouteAll(options) {
      assert.deepEqual(options, { behavior: 'wait' })
      events.push('drain-started')
      await route.promise
    },
  }, async () => { events.push('backend-stopped'); events.push('private-root-removed') })
  const rejection = assert.rejects(teardown, error => error === failure)
  await Promise.resolve()
  assert.deepEqual(events, ['drain-started'])
  route.reject(failure)
  await rejection
  assert.deepEqual(events, ['drain-started', 'backend-stopped', 'private-root-removed'])
})

test('fixture teardown exposes cleanup failures after routes drain', async () => {
  const failure = new Error('synthetic cleanup failure')
  const events = []
  await assert.rejects(drainRoutesBeforeCleanup({
    async unrouteAll(options) {
      assert.deepEqual(options, { behavior: 'wait' })
      events.push('routes-drained')
    },
  }, async () => { events.push('cleanup-started'); throw failure }), error => error === failure)
  assert.deepEqual(events, ['routes-drained', 'cleanup-started'])
})

test('fixture teardown preserves both route and cleanup failures', async () => {
  const route = Promise.withResolvers()
  const routeFailure = new Error('synthetic route failure')
  const cleanupFailure = new Error('synthetic cleanup failure')
  const events = []
  const teardown = drainRoutesBeforeCleanup({
    async unrouteAll(options) {
      assert.deepEqual(options, { behavior: 'wait' })
      events.push('drain-started')
      await route.promise
    },
  }, async () => { events.push('cleanup-started'); throw cleanupFailure })
  const rejection = assert.rejects(teardown, error => {
    assert.ok(error instanceof AggregateError)
    assert.equal(error.errors.length, 2)
    assert.equal(error.errors[0], routeFailure)
    assert.equal(error.errors[1], cleanupFailure)
    return true
  })
  await Promise.resolve()
  assert.deepEqual(events, ['drain-started'])
  route.reject(routeFailure)
  await rejection
  assert.deepEqual(events, ['drain-started', 'cleanup-started'])
})

function pairingRequest(name = 'lan.join', requestId = 'synthetic-request-id') {
  return { name, requestId, payload: { invitation: SYNTHETIC_PAIRING_INVITATION } }
}
function pairingRoute(request, { method = 'POST', terminalFailure, beforeTerminal } = {}) {
  const actions = []
  return {
    actions,
    request: () => ({ method: () => method, postDataJSON: () => request }),
    async fulfill(options) {
      actions.push({ fulfill: options })
      await beforeTerminal?.()
      if (terminalFailure) throw terminalFailure
    },
    async abort(code) {
      actions.push({ abort: code })
      await beforeTerminal?.()
      if (terminalFailure) throw terminalFailure
    },
    fetch() { actions.push({ forbidden: 'fetch' }); assert.fail('Protected synthetic commands must never fetch') },
    continue() { actions.push({ forbidden: 'continue' }); assert.fail('Protected synthetic commands must never continue') },
    fallback() { actions.push({ forbidden: 'fallback' }); assert.fail('Protected synthetic commands must never fall back') },
  }
}
async function pairingInterception(respond, scenario = 'offline') {
  const interception = createSyntheticPairingInterception(scenario)
  let handle
  await interception.install({ async route(pattern, handler) { assert.equal(pattern, '**/api/command'); handle = handler } }, respond)
  return { interception, handle }
}
const counts = values => async name => values[name] || 0
const privateFailure = error => !error.message.includes(sentinel) && /details withheld/.test(error.message)

test('synthetic pairing is explicit, offline-only and cannot replace an existing interceptor', async () => {
  for (const scenario of ['studio', 'routes', 'receive-legacy', 'receive-damaged', 'unknown']) {
    let installs = 0
    await assert.rejects(createSyntheticPairingInterception(scenario).install({ route() { installs++ } }, () => ({ json: {} })), /offline/)
    assert.equal(installs, 0)
  }
  const { interception } = await pairingInterception(() => ({ json: {} }))
  await assert.rejects(interception.install({ route() { assert.fail('Must not install twice') } }, () => ({ json: {} })), /one explicit responder/)
})

test('synthetic pairing records exact settled commands including aborted retries without changing raw counts', async () => {
  const seen = []
  const { interception, handle } = await pairingInterception(function (command) {
    assert.equal(arguments.length, 1)
    assert.deepEqual(Object.keys(command).sort(), ['name', 'payload', 'requestId'])
    assert.ok(Object.isFrozen(command) && Object.isFrozen(command.payload))
    seen.push(command)
    return seen.length === 2 ? { abort: 'failed' } : { json: { ok: true, result: { paired: command.name === 'lan.join' } } }
  })
  for (const name of ['lan.inspect', 'lan.join', 'lan.join']) {
    const route = pairingRoute(pairingRequest(name))
    await handle(route)
    assert.equal(route.actions.length, 1)
    if (seen.length === 2) assert.deepEqual(route.actions, [{ abort: 'failed' }])
    else {
      assert.equal(route.actions[0].fulfill.status, 200)
      assert.equal(route.actions[0].fulfill.contentType, 'application/json')
    }
  }
  const records = interception.records()
  assert.deepEqual(records.map(record => record.command), seen)
  assert.deepEqual(records.map(record => record.outcome), ['fulfilled', 'aborted', 'fulfilled'])
  assert.equal(records[1].command.requestId, records[2].command.requestId)
  assert.ok(Object.isFrozen(records) && records.every(Object.isFrozen))
  assert.throws(() => records.pop())
  assert.throws(() => { records[1].command.name = 'network.login' })
  const raw = { 'lan.inspect': 1, 'lan.join': 2 }
  await interception.verify(counts(raw))
  assert.deepEqual(raw, { 'lan.inspect': 1, 'lan.join': 2 })
  for (const values of [{ ...raw, 'lan.join': 3 }, { ...raw, 'lan.join': 1 }, { ...raw, 'lan.inspect': 2 }, { ...raw, 'network.login': 1 }, { ...raw, 'lan.invite': 1 }]) {
    await assert.rejects(interception.verify(counts(values)), /Every enrollment attempt/)
  }
})

test('default enrollment guard still rejects every unaccounted enrollment attempt', async () => {
  for (const scenario of ['offline', 'studio', 'routes']) {
    const interception = createSyntheticPairingInterception(scenario)
    await interception.verify(counts({}))
    for (const name of ['network.login', 'lan.invite', 'lan.join']) await assert.rejects(interception.verify(counts({ [name]: 1 })), /Every enrollment attempt/)
  }
})

test('synthetic pairing blocks real payloads, malformed requests, other commands and non-POST requests before the responder', async () => {
  const requests = [null, {}, { ...pairingRequest(), payload: { invitation: sentinel } }, { ...pairingRequest(), payload: { invitation: SYNTHETIC_PAIRING_INVITATION, other: sentinel } }, { ...pairingRequest(), requestId: '' }, { ...pairingRequest(), requestId: 123 }, { ...pairingRequest(), requestId: sentinel.repeat(30) }, { ...pairingRequest(), extra: sentinel }, ...['network.login', 'lan.invite', 'network.configure', 'unknown'].map(name => pairingRequest(name))]
  for (const request of requests) {
    const { interception, handle } = await pairingInterception(() => { assert.fail('Invalid data must not reach the responder') })
    const route = pairingRoute(request)
    await assert.rejects(handle(route), privateFailure)
    assert.deepEqual(route.actions, [{ abort: 'blockedbyclient' }])
    assert.deepEqual(interception.records(), [])
    await assert.rejects(interception.verify(counts({})), privateFailure)
  }
  const { handle } = await pairingInterception(() => { assert.fail('GET must not reach the responder') })
  const route = pairingRoute(pairingRequest(), { method: 'GET' })
  await assert.rejects(handle(route), privateFailure)
  assert.deepEqual(route.actions, [{ abort: 'blockedbyclient' }])
})

test('synthetic responders cannot request forwarding, fetch, fallback or arbitrary route options', async () => {
  for (const result of [undefined, {}, { continue: true }, { fetch: true }, { fallback: true }, { response: sentinel }, { json: {}, response: sentinel }, { abort: 'other' }, { abort: 'failed', json: {} }, { json: null }, { json: [] }]) {
    const { interception, handle } = await pairingInterception(() => result)
    const route = pairingRoute(pairingRequest())
    await assert.rejects(handle(route), privateFailure)
    assert.deepEqual(route.actions, [{ abort: 'blockedbyclient' }])
    assert.deepEqual(interception.records(), [])
    await assert.rejects(interception.verify(counts({})), privateFailure)
  }
})

test('thrown responders and rejected fulfill or abort actions cannot count as controlled interception', async () => {
  for (const respond of [() => { throw new Error(sentinel) }, () => ({ json: {} }), () => ({ abort: 'failed' })]) {
    const { interception, handle } = await pairingInterception(respond)
    const route = pairingRoute(pairingRequest(), { terminalFailure: new Error(sentinel) })
    await assert.rejects(handle(route), privateFailure)
    assert.deepEqual(interception.records(), [])
    await assert.rejects(interception.verify(counts({ 'lan.join': 1 })), privateFailure)
  }
  const interception = createSyntheticPairingInterception('offline')
  await assert.rejects(interception.install({ route() { throw new Error(sentinel) } }, () => ({ json: {} })), privateFailure)
  await assert.rejects(interception.verify(counts({})), privateFailure)
})

test('synthetic audit waits for the exact in-flight terminal action before mandatory cleanup', async () => {
  const terminal = Promise.withResolvers()
  const { interception, handle } = await pairingInterception(() => ({ json: {} }))
  const route = pairingRoute(pairingRequest(), { beforeTerminal: () => terminal.promise })
  const pending = handle(route)
  let cleaned = false
  const teardown = drainRoutesBeforeCleanup({
    async unrouteAll(options) { assert.deepEqual(options, { behavior: 'wait' }); await pending },
  }, () => verifyCommandsBeforeCleanup(interception, counts({ 'lan.join': 1 }), async () => { cleaned = true }))
  await Promise.resolve()
  assert.deepEqual(interception.records(), [])
  assert.equal(cleaned, false)
  terminal.resolve()
  await teardown
  assert.equal(interception.records().length, 1)
  assert.equal(cleaned, true)
})

test('failed-test safety audit still detects unaccounted enrollment and always cleans up', async () => {
  const interception = createSyntheticPairingInterception('offline')
  let cleaned = false
  await assert.rejects(verifyCommandsBeforeCleanup(interception, counts({ 'lan.join': 1 }), async () => { cleaned = true }), /Every enrollment attempt/)
  assert.equal(cleaned, true)
  cleaned = false
  await assert.rejects(verifyCommandsBeforeCleanup(interception, async () => { throw new Error('page unavailable') }, async () => { cleaned = true }), /page unavailable/)
  assert.equal(cleaned, true)
  cleaned = false
  await verifyCommandsBeforeCleanup(interception, null, async () => { cleaned = true })
  assert.equal(cleaned, true)
})

test('route, command audit and cleanup failures are all preserved', async () => {
  const routeFailure = new Error('synthetic drain failure'), cleanupFailure = new Error('synthetic cleanup failure')
  const interception = createSyntheticPairingInterception('offline')
  let cleaned = false
  await assert.rejects(drainRoutesBeforeCleanup({ async unrouteAll() { throw routeFailure } }, () => verifyCommandsBeforeCleanup(interception, counts({ 'lan.join': 1 }), async () => { cleaned = true; throw cleanupFailure })), error => {
    assert.ok(error instanceof AggregateError)
    assert.equal(error.errors[0], routeFailure)
    assert.ok(error.errors[1] instanceof AggregateError)
    assert.match(error.errors[1].errors[0].message, /Every enrollment attempt/)
    assert.equal(error.errors[1].errors[1], cleanupFailure)
    return true
  })
  assert.equal(cleaned, true)
})


test('saved-host review uses only its own offline production-state capability and no paired/listener input', () => {
  const saved = { ...valid, scenario: 'saved-host', localServicePort: undefined, capabilities: ['offline-network', 'saved-host-review', 'production-saved-state'] }
  assert.equal(validateSession(saved, 'saved-host').scenario, 'saved-host')
  for (const missing of saved.capabilities) rejected({ ...saved, capabilities: saved.capabilities.filter(value => value !== missing) }, 'saved-host')
  for (const extra of ['route-authorization', 'service-lifecycle', 'saved-autosave']) rejected({ ...saved, capabilities: [...saved.capabilities, extra] }, 'saved-host')
  rejected({ ...saved, capabilities: ['offline-network', 'saved-host-review', 'saved-host-review'] }, 'saved-host')
  rejected({ ...saved, localServicePort: 43919 }, 'saved-host')
  rejected({ ...saved, routePeerId: '1'.repeat(64) }, 'saved-host')
  rejected({ ...saved, routeUpdateFile: '/tmp/fixture/route-update.json' }, 'saved-host')
  rejected(saved, 'offline')
  rejected({ ...saved, scenario: 'offline' }, 'offline')
  rejected({ ...valid, capabilities: [...valid.capabilities, 'saved-host-review'] })
})

test('saved-host review cannot install the offline synthetic pairing interceptor', async () => {
  const interceptor = createSyntheticPairingInterception('saved-host')
  let routes = 0
  await assert.rejects(interceptor.install({ route: async () => { routes++ } }, async () => ({ status: 200, body: '{}' })))
  assert.equal(routes, 0)
})
