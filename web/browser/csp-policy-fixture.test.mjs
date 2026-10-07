import assert from 'node:assert/strict'
import test from 'node:test'
import vm from 'node:vm'
import { CSP_PROBE_ROOT, createCspPolicyFixture } from './csp-policy-fixture.mjs'

const documentCSP = "default-src 'none'; script-src 'self'; connect-src 'self'"
const workerCSP = "default-src 'none'; script-src 'self' 'wasm-unsafe-eval'; connect-src 'none'; worker-src 'none'"
const fixtures = createCspPolicyFixture({ documentCSP, workerCSP })

test('synthetic policy responses preserve the supplied CSP exactly without copying asset metadata', () => {
  const supplied = createCspPolicyFixture({ documentCSP: `${documentCSP}; base-uri 'none'`, workerCSP: `${workerCSP}; base-uri 'none'` })
  assert.equal(supplied.get(`${CSP_PROBE_ROOT}index.html`).headers['content-security-policy'], `${documentCSP}; base-uri 'none'`)
  assert.equal(supplied.get(`${CSP_PROBE_ROOT}worker.js`).headers['content-security-policy'], `${workerCSP}; base-uri 'none'`)
  for (const [path, response] of supplied) {
    assert.ok(path.startsWith(CSP_PROBE_ROOT))
    assert.deepEqual(Object.keys(response.headers).sort(), ['cache-control', 'content-security-policy'])
    if (path.endsWith('.js')) { assert.equal(response.contentType, 'text/javascript'); new vm.Script(response.body) }
  }
  assert.match(supplied.get(`${CSP_PROBE_ROOT}index.html`).body, /<script type="module" src="\.\/document.js"><\/script>/)
  assert.throws(() => createCspPolicyFixture({ documentCSP, workerCSP: '' }))
  assert.throws(() => createCspPolicyFixture({ documentCSP: '', workerCSP }))
})

// This is a bounded test of fixture event wiring and denial classification.
// These mocked events do not establish browser CSP enforcement.
function runtime(scope, { emit = true, reject = true, disposition = 'enforce', wrongDirective = false, wrongTarget = false, socketTarget = 'exact', asyncErrors = false } = {}) {
  const listeners = new Map(), calls = [], scheduled = new Set()
  let finish
  const done = new Promise(resolve => { finish = resolve })
  const dispatch = (name, data) => { for (const listener of [...(listeners.get(name) || [])]) listener(data) }
  const blocked = (name, directive, target) => {
    calls.push(name)
    let blockedURI = target
    if (name === 'socket' && socketTarget === 'http') blockedURI = target.replace(/^ws/, 'http')
    if (name === 'socket' && socketTarget === 'origin') blockedURI = new URL(target).origin
    if (name === 'socket' && socketTarget === 'other-path') blockedURI = new URL('other-socket', target).href
    if (emit) queueMicrotask(() => dispatch('securitypolicyviolation', {
      disposition,
      effectiveDirective: wrongDirective ? 'img-src' : directive,
      blockedURI: wrongTarget ? 'unrelated-target' : blockedURI,
    }))
    if (reject) throw new Error('synthetic operation rejection')
  }
  const sandbox = {
    URL, Uint8Array,
    location: new URL(`http://127.0.0.1:43210${CSP_PROBE_ROOT}${scope === 'document' ? 'index.html' : 'worker.js'}`),
    addEventListener(name, listener) { if (!listeners.has(name)) listeners.set(name, new Set()); listeners.get(name).add(listener) },
    removeEventListener(name, listener) { listeners.get(name)?.delete(listener) },
    setTimeout(callback, delay) { const timer = setTimeout(() => { scheduled.delete(timer); callback() }, Math.min(delay, 10)); scheduled.add(timer); return timer },
    clearTimeout(timer) { clearTimeout(timer); scheduled.delete(timer) },
    eval() { return blocked('eval', 'script-src', 'eval') },
    WebAssembly: { async compile() { if (scope === 'document') blocked('wasm', 'script-src', 'wasm-eval'); else calls.push('wasm') } },
    fetch(url) { return blocked('fetch', 'connect-src', url) },
    WebSocket: class {
      constructor(url) {
        try { blocked('socket', 'connect-src', url) } catch (error) {
          if (!asyncErrors) throw error
          queueMicrotask(() => this.onerror?.()); return
        }
        queueMicrotask(() => this.onopen?.())
      }
      close() {}
    },
    Worker: class {
      constructor(url) {
        if (scope !== 'worker') return
        try { blocked('child', 'worker-src', url) } catch (error) {
          if (!asyncErrors) throw error
          queueMicrotask(() => this.onerror?.({ preventDefault() {} })); return
        }
        queueMicrotask(() => this.onmessage?.({ data: 'ready' }))
      }
      postMessage() { queueMicrotask(() => this.onmessage?.({ data: 'synthetic worker result' })) }
      terminate() {}
    },
    postMessage(result) { finish(result) },
  }
  Object.defineProperty(sandbox, '__sobaCspPolicyResult', { set: finish })
  sandbox.self = sandbox
  const context = vm.createContext(sandbox)
  new vm.Script(fixtures.get(`${CSP_PROBE_ROOT}${scope}.js`).body).runInContext(context)
  return {
    calls,
    async run() {
      dispatch(scope === 'document' ? 'load' : 'message', { data: 'run-policy-checks' })
      try { return JSON.parse(JSON.stringify(await done)) }
      finally { for (const timer of scheduled) clearTimeout(timer) }
    },
    listenerCount() { return listeners.get('securitypolicyviolation')?.size || 0 },
  }
}

test('document probes wait for a load task and require enforced eval and WASM violations', async () => {
  const probe = runtime('document')
  assert.deepEqual(probe.calls, [])
  assert.deepEqual(await probe.run(), { documentChecks: { jsBlocked: true, wasmBlocked: true }, workerChecks: 'synthetic worker result' })
  assert.equal(probe.listenerCount(), 0)
})

test('worker probes wait for a message task and correlate each rejected operation with its own CSP violation', async () => {
  const probe = runtime('worker')
  assert.deepEqual(probe.calls, [])
  assert.deepEqual(await probe.run(), { wasmAllowed: true, jsBlocked: true, fetchBlocked: true, socketBlocked: true, childBlocked: true })
  assert.deepEqual(probe.calls, ['eval', 'wasm', 'fetch', 'socket', 'child'])
  assert.equal(probe.listenerCount(), 0)
})

test('WebSocket CSP reports accept only the exact socket or HTTP handshake URL', async () => {
  for (const socketTarget of ['http', 'origin', 'other-path']) {
    const probe = runtime('worker', { socketTarget })
    assert.deepEqual(await probe.run(), { wasmAllowed: true, jsBlocked: true, fetchBlocked: true, socketBlocked: socketTarget === 'http', childBlocked: true })
  }
})

test('asynchronous socket and child errors require their own enforced CSP events', async () => {
  for (const emit of [true, false]) {
    const probe = runtime('worker', { asyncErrors: true, emit })
    assert.deepEqual(await probe.run(), { wasmAllowed: true, jsBlocked: emit, fetchBlocked: emit, socketBlocked: emit, childBlocked: emit })
    assert.equal(probe.listenerCount(), 0)
  }
})

test('document eval and WASM failures alone cannot count as CSP enforcement', async () => {
  const probe = runtime('document', { emit: false })
  assert.deepEqual(await probe.run(), { documentChecks: { jsBlocked: false, wasmBlocked: false }, workerChecks: 'synthetic worker result' })
})

for (const [label, options] of [
  ['network errors without CSP events', { emit: false }],
  ['report-only events', { disposition: 'report' }],
  ['unrelated directives', { wrongDirective: true }],
  ['unrelated blocked targets', { wrongTarget: true }],
  ['successful operations with a violation event', { reject: false }],
]) test(`${label} cannot count as a policy denial`, async () => {
  const probe = runtime('worker', options)
  assert.deepEqual(await probe.run(), { wasmAllowed: true, jsBlocked: false, fetchBlocked: false, socketBlocked: false, childBlocked: false })
  assert.equal(probe.listenerCount(), 0)
})
