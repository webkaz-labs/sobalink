import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { isAbsolute, relative } from 'node:path'

export function validateSession(value, scenario) {
  assert.ok(value && typeof value === 'object', 'Fixture session must be an object; contents withheld')
  let url
  try { url = new URL(value.url) } catch { throw new Error('Fixture URL is invalid; contents withheld') }
  assert.ok(url.protocol === 'http:' && url.hostname === '127.0.0.1', 'Fixture must use numeric loopback HTTP')
  assert.ok(!(url.username || url.password || url.search || url.hash), 'Fixture URL must not contain credentials or query data')
  assert.ok(typeof value.code === 'string' && value.code.length > 0 && value.code.length <= 256, 'Fixture must provide a private access code')
  assert.ok(['studio', 'offline', 'saved-host', 'routes', 'receive-legacy', 'receive-damaged'].includes(scenario) && value.scenario === scenario, 'Fixture scenario must match the requested test')
  assert.ok(typeof value.receiveDirectory === 'string' && isAbsolute(value.receiveDirectory), 'Fixture must provide an absolute receiving directory')
  const required = {
    studio: ['service-lifecycle', 'failed-upload-retry', 'application-stop'],
    offline: ['offline-network'],
    'saved-host': ['offline-network', 'saved-host-review', 'production-saved-state'],
    routes: ['offline-network', 'route-authorization', 'prepared-route-edit'],
    'receive-legacy': ['service-lifecycle', 'receive-recovery', 'saved-autosave', 'legacy-receive-review'],
    'receive-damaged': ['service-lifecycle', 'receive-recovery', 'saved-autosave', 'damaged-receive-index'],
  }[scenario]
  assert.ok(Array.isArray(value.capabilities) && required.every(capability => value.capabilities.includes(capability)), 'Fixture is missing required acceptance capabilities')
  if (scenario === 'saved-host') {
    assert.ok(value.capabilities.length === required.length && new Set(value.capabilities).size === required.length, 'Saved-host review must not inherit other fixture capabilities')
    assert.ok(value.localServicePort === undefined && value.routePeerId === undefined && value.routeUpdateFile === undefined, 'Saved-host review must not inherit a service listener or paired route input')
  } else assert.ok(!value.capabilities.includes('saved-host-review') && !value.capabilities.includes('production-saved-state'), 'Saved-host capabilities require their own scenario')
  if (scenario === 'routes') {
    assert.ok(typeof value.routeUpdateFile === 'string' && isAbsolute(value.routeUpdateFile), 'Route fixture must provide a private absolute update-file path')
    assert.ok(typeof value.routePeerId === 'string' && /^[a-f0-9]{64}$/.test(value.routePeerId), 'Route fixture must provide an exact paired public ID')
  }
  if (!['offline', 'routes', 'saved-host'].includes(scenario)) assert.ok(Number.isInteger(value.localServicePort) && value.localServicePort >= 1024 && value.localServicePort <= 65535, 'Fixture must reserve a valid local service port')
  return { ...value, url: url.href }
}

export function safeArtifactName(name) {
  assert.ok(typeof name === 'string' && /^[a-z0-9][a-z0-9_.-]{0,110}$/i.test(name) && !name.includes('..'), 'Artifact name must be a bounded plain filename')
  return name
}

// Test identity, repeats and retries are independent of worker scheduling.
// Never place the raw test ID (which may contain private paths) in artifacts.
export function safeArtifactFilename(name, extension, testInfo) {
  safeArtifactName(name)
  assert.ok(['png', 'json'].includes(extension), 'Artifact type must be an approved image or metrics file')
  assert.ok(typeof testInfo.testId === 'string' && testInfo.testId.length > 0, 'Artifact requires a test identity')
  for (const value of [testInfo.repeatEachIndex, testInfo.retry]) assert.ok(Number.isSafeInteger(value) && value >= 0 && value <= 1_000_000, 'Artifact requires bounded repeat and retry indices')
  const identity = createHash('sha256').update(testInfo.testId).digest('hex').slice(0, 24)
  return `${name}-${identity}-n${testInfo.repeatEachIndex}-r${testInfo.retry}.${extension}`
}

export function assertSeparateArtifacts(output, privateRoot) {
  const child = relative(privateRoot, output)
  const parent = relative(output, privateRoot)
  assert.ok(child.startsWith('..') && parent.startsWith('..'), 'Artifacts and the private fixture runtime must be separate directories')
}

// Shared with the evidence gate so a new private field cannot bypass the check.
export const CAPTURE_FORBIDDEN_SELECTOR = '.login-panel, .signin-link a, .auth-private, .auth-qr, .proxy-credentials, [data-private=proxy-credential]'
export const PRIVATE_VALUE_SELECTOR = 'input[type=password], .private-copy, [data-private]'
export function privateControlsAreEmpty(elements) {
  return elements.every(element => !element.value && !(element.textContent || '').trim())
}
