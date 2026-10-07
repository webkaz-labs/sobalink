// These separate synthetic scripts test enforcement of CSP values fetched from
// the real server. They do not replace or instrument the shipped PNG reader.
export const CSP_PROBE_ROOT = '/__soba_csp_policy_probe__/'

// Serialized into ordinary external scripts below, never run by page.evaluate
// or worker.evaluate. Keep this function self-contained.
async function policyChecks(scope) {
  const emptyWasm = () => Uint8Array.of(0, 97, 115, 109, 1, 0, 0, 0)
  const denied = async (operation, directive, target) => {
    let timer, sawViolation
    const violation = new Promise(resolve => { sawViolation = resolve })
    const listener = event => {
      if (event.disposition !== 'enforce' || event.effectiveDirective !== directive) return
      // WebSocket reporting may use its HTTP(S) handshake URL. Accept that
      // exact equivalent, preserving the host and path for this operation.
      const matches = event.blockedURI === target || (/^wss?:/.test(target) && event.blockedURI === target.replace(/^ws/, 'http'))
      if (matches) sawViolation(true)
    }
    self.addEventListener('securitypolicyviolation', listener)
    let rejected = false
    try { await operation() } catch { rejected = true }
    try {
      const enforced = await Promise.race([violation, new Promise(resolve => { timer = setTimeout(() => resolve(false), 1000) })])
      return rejected && enforced
    } finally {
      clearTimeout(timer)
      self.removeEventListener('securitypolicyviolation', listener)
    }
  }
  const jsBlocked = await denied(() => eval('1'), 'script-src', 'eval')
  if (scope === 'document') {
    const wasmBlocked = await denied(() => WebAssembly.compile(emptyWasm()), 'script-src', 'wasm-eval')
    return { jsBlocked, wasmBlocked }
  }
  let wasmAllowed = false
  try { await WebAssembly.compile(emptyWasm()); wasmAllowed = true } catch {}
  const fetchURL = new URL('fetch', self.location.href).href
  const fetchBlocked = await denied(() => fetch(fetchURL), 'connect-src', fetchURL)
  const socketURL = new URL('socket', self.location.href)
  socketURL.protocol = socketURL.protocol === 'https:' ? 'wss:' : 'ws:'
  const socketBlocked = await denied(() => new Promise((resolve, reject) => {
    const socket = new WebSocket(socketURL.href)
    const finish = failed => {
      clearTimeout(timer); socket.onopen = null; socket.onerror = null; socket.close()
      if (failed) reject(new Error('socket denied'))
      else resolve()
    }
    const timer = setTimeout(() => finish(false), 1000)
    socket.onerror = () => finish(true)
    socket.onopen = () => finish(false)
  }), 'connect-src', socketURL.href)
  const childURL = new URL('child.js', self.location.href).href
  const childBlocked = await denied(() => new Promise((resolve, reject) => {
    const child = new Worker(childURL, { type: 'module' })
    const finish = failed => {
      clearTimeout(timer); child.onerror = null; child.onmessage = null; child.terminate()
      if (failed) reject(new Error('child worker denied'))
      else resolve()
    }
    const timer = setTimeout(() => finish(false), 1000)
    child.onerror = event => { event.preventDefault(); finish(true) }
    child.onmessage = () => finish(false)
  }), 'worker-src', childURL)
  return { wasmAllowed, jsBlocked, fetchBlocked, socketBlocked, childBlocked }
}

function documentProbe() {
  self.addEventListener('load', async () => {
    const documentChecks = await policyChecks('document')
    const worker = new Worker('./worker.js', { type: 'module' })
    const timer = setTimeout(() => {
      worker.terminate()
      self.__sobaCspPolicyResult = { documentChecks, workerChecks: null }
    }, 10_000)
    worker.onmessage = event => {
      clearTimeout(timer); worker.terminate()
      self.__sobaCspPolicyResult = { documentChecks, workerChecks: event.data }
    }
    worker.postMessage('run-policy-checks')
  }, { once: true })
}

function workerProbe() {
  self.addEventListener('message', async event => {
    if (event.data === 'run-policy-checks') self.postMessage(await policyChecks('worker'))
  }, { once: true })
}

export function createCspPolicyFixture({ documentCSP, workerCSP }) {
  if (!documentCSP || !workerCSP) throw new Error('Policy probes require both real response CSP values')
  // Copy only CSP, not real response content lengths or unrelated asset headers.
  const response = (body, contentType, csp) => ({ status: 200, body, contentType, headers: { 'content-security-policy': csp, 'cache-control': 'no-store' } })
  const script = bootstrap => `${policyChecks.toString()}\n(${bootstrap.toString()})();\n`
  return new Map([
    [`${CSP_PROBE_ROOT}index.html`, response('<!doctype html><title>Synthetic CSP policy probes</title><script type="module" src="./document.js"></script>', 'text/html', documentCSP)],
    [`${CSP_PROBE_ROOT}document.js`, response(script(documentProbe), 'text/javascript', documentCSP)],
    [`${CSP_PROBE_ROOT}worker.js`, response(script(workerProbe), 'text/javascript', workerCSP)],
    // If a denied API unexpectedly succeeds, these benign endpoints make that
    // distinguishable from an unrelated missing resource or worker parse error.
    [`${CSP_PROBE_ROOT}fetch`, response('synthetic probe response', 'text/plain', documentCSP)],
    [`${CSP_PROBE_ROOT}child.js`, response("self.postMessage('synthetic child ready')", 'text/javascript', workerCSP)],
  ])
}
