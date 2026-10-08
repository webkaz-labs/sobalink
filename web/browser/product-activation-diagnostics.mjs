// Closed evidence vocabulary only. No raw errors, names, paths, URLs, codes,
// capabilities or attachments cross this boundary. Diagnostics do not add an
// acceptance predicate: the existing product and native cleanup gates decide.
export const caseIds = Object.freeze(['product-web', 'product-cli'])
export const statuses = Object.freeze(['not-started', 'passed', 'failed', 'timedOut', 'skipped', 'interrupted'])
export const workPhases = Object.freeze(['not-started', 'preflight', 'native-ready', 'old-login', 'full-panel', 'review', 'popup', 'helper', 'restart-confirm', 'management-ready', 'old-exit', 'successor-start', 'open-before', 'open-request', 'open-observed', 'open-disabled', 'fresh-login', 'cli-start', 'cli-pty-complete', 'status', 'controller-proof', 'complete'])
export const failurePhases = Object.freeze(['none', 'unknown', ...workPhases])
export const cleanupPhases = Object.freeze(['not-started', 'context', 'supervisor', 'proof', 'safety', 'remove', 'complete'])
export const cleanupFailurePhases = Object.freeze(['none', 'unknown', ...cleanupPhases])
export const exits = Object.freeze(['not-observed', 'zero', 'one', 'race', 'other', 'signal', 'spawn-error', 'profile-rejected', 'watchdog', 'mode-rejected', 'owner-failed', 'supervisor-failed', 'registration-failed'])
export const supervisorFailures = Object.freeze(['none', 'origin-write', 'observer-wait', 'start-observe', 'progress-write', 'wait-error', 'child-exit', 'extra-observe', 'extra-close'])
export const resourceFailures = Object.freeze(['none', 'slave-close', 'master-close', 'capture-error', 'helper-exit', 'result-parse', 'result-write'])
export const nativeStages = Object.freeze(['not-started', 'owner-start', 'peer-open', 'peer-review', 'peer-apply', 'foreground', 'identity', 'management-announced', 'cli-launch', 'review-read', 'owner-status', 'peer-status', 'review-binding', 'network-ready', 'persisted-context', 'pair-binding', 'proof-written', 'cli-terminal', 'cli-review', 'cli-review-binding', 'cli-apply', 'cli-complete'])
export const nativeStatuses = Object.freeze(['unobserved', 'idle', 'restart-required', 'preparing', 'exchanging', 'local-confirmed', 'connected', 'network-started', 'failed', 'cancelled', 'other'])
export const nativeRoles = Object.freeze(['old', 'successor', 'cli'])
export const booleanKeys = Object.freeze(['workCompleted', 'workFailed', 'cleanupFailed', 'oldReadyObserved', 'oldExitObserved', 'successorStartObserved', 'cliCompleteObserved', 'controllerProofObserved', 'peerConfirmed', 'ownerConfirmed', 'ordinaryReady', 'originalReviewPreserved', 'supervisorStarted', 'supervisorExited', 'proofRead', 'stopRequested', 'supervisorDeadlineExpired', 'noWaitableChildren', 'allDescendantsReaped', 'registeredNativeExits', 'successorRegistered', 'contextClosed', 'profileRemoved', 'outputOverflow', 'privateOutputDetected'])
export const counterKeys = Object.freeze(['registeredChildren', 'observedExits', 'reaped', 'blockedRequests', 'runtimeErrors'])
const lifecycleKeys = ['workPhase', 'firstFailurePhase', 'cleanupPhase', 'firstCleanupFailurePhase', 'supervisorExit', 'oldExit', 'helperExit', 'successorExit', 'supervisorFailure', 'resourceFailure', ...booleanKeys, ...counterKeys, 'native']
const nativeKeys = ['available', 'invalid', 'stage', 'failed', 'ownerStatus', 'peerStatus']
function exact(value, keys) { return value && typeof value === 'object' && !Array.isArray(value) && Object.keys(value).length === keys.length && keys.every(key => Object.hasOwn(value, key)) }
export function counter(value) { return Number.isSafeInteger(value) && value >= 0 ? Math.min(value, 255) : 0 }
export function nativeObservation() { return { available: false, invalid: false, stage: 'not-started', failed: false, ownerStatus: 'unobserved', peerStatus: 'unobserved' } }
export function readNativeObservation(value) {
  if (!exact(value, ['schema', 'stage', 'failed', 'ownerStatus', 'peerStatus']) || value.schema !== 1 || !nativeStages.includes(value.stage) || typeof value.failed !== 'boolean' || !nativeStatuses.includes(value.ownerStatus) || !nativeStatuses.includes(value.peerStatus)) throw Error('Invalid fixed native observation')
  return { available: true, invalid: false, stage: value.stage, failed: value.failed, ownerStatus: value.ownerStatus, peerStatus: value.peerStatus }
}
function validateNative(value) {
  if (!exact(value, nativeRoles)) throw Error('Invalid fixed native inventory')
  return Object.fromEntries(nativeRoles.map(role => {
    const row = value[role]
    if (!exact(row, nativeKeys) || ['available', 'invalid', 'failed'].some(key => typeof row[key] !== 'boolean') || !nativeStages.includes(row.stage) || !nativeStatuses.includes(row.ownerStatus) || !nativeStatuses.includes(row.peerStatus) || (row.available && row.invalid) || (!row.available && (row.stage !== 'not-started' || row.failed || row.ownerStatus !== 'unobserved' || row.peerStatus !== 'unobserved'))) throw Error('Invalid fixed native status')
    return [role, Object.fromEntries(nativeKeys.map(key => [key, row[key]]))]
  }))
}
export function lifecycle() {
  return { workPhase: 'not-started', firstFailurePhase: 'none', cleanupPhase: 'not-started', firstCleanupFailurePhase: 'none', supervisorExit: 'not-observed', oldExit: 'not-observed', helperExit: 'not-observed', successorExit: 'not-observed', supervisorFailure: 'none', resourceFailure: 'none', ...Object.fromEntries(booleanKeys.map(key => [key, false])), ...Object.fromEntries(counterKeys.map(key => [key, 0])), native: Object.fromEntries(nativeRoles.map(role => [role, nativeObservation()])) }
}
export function validateLifecycle(value) {
  if (!exact(value, lifecycleKeys) || !workPhases.includes(value.workPhase) || !failurePhases.includes(value.firstFailurePhase) || !cleanupPhases.includes(value.cleanupPhase) || !cleanupFailurePhases.includes(value.firstCleanupFailurePhase) || ['supervisorExit', 'oldExit', 'helperExit', 'successorExit'].some(key => !exits.includes(value[key])) || !supervisorFailures.includes(value.supervisorFailure) || !resourceFailures.includes(value.resourceFailure) || booleanKeys.some(key => typeof value[key] !== 'boolean') || counterKeys.some(key => !Number.isInteger(value[key]) || value[key] < 0 || value[key] > 255)) throw Error('Invalid fixed product lifecycle')
  return { ...Object.fromEntries(lifecycleKeys.filter(key => key !== 'native').map(key => [key, value[key]])), native: validateNative(value.native) }
}
export function phase(value, next) {
  if (!workPhases.includes(next)) throw Error('Unknown fixed work phase')
  value.workPhase = next
}
export function failWork(value) {
  value.workFailed = true
  if (value.firstFailurePhase === 'none') value.firstFailurePhase = value.workPhase === 'not-started' ? 'unknown' : value.workPhase
}
export function cleanupPhase(value, next) {
  if (!cleanupPhases.includes(next)) throw Error('Unknown fixed cleanup phase')
  value.cleanupPhase = next
}
export function failCleanup(value) {
  value.cleanupFailed = true
  if (value.firstCleanupFailurePhase === 'none') value.firstCleanupFailurePhase = value.cleanupPhase === 'not-started' ? 'unknown' : value.cleanupPhase
}
export function exitCategory(code) {
  if (code === null) return 'signal'
  return new Map([[0, 'zero'], [1, 'one'], [66, 'race'], [91, 'profile-rejected'], [92, 'watchdog'], [93, 'mode-rejected'], [94, 'owner-failed'], [95, 'supervisor-failed'], [96, 'registration-failed']]).get(code) || 'other'
}
export function initialCases() { return caseIds.map(id => ({ id, started: false, status: 'not-started', diagnosticAvailable: false, diagnosticRejected: false, lifecycle: lifecycle() })) }
export function validateCases(value) {
  if (!Array.isArray(value) || value.length !== 2) throw Error('Invalid fixed product inventory')
  return value.map((row, index) => {
    if (!exact(row, ['id', 'started', 'status', 'diagnosticAvailable', 'diagnosticRejected', 'lifecycle']) || row.id !== caseIds[index] || ['started', 'diagnosticAvailable', 'diagnosticRejected'].some(key => typeof row[key] !== 'boolean') || !statuses.includes(row.status) || (!row.started && row.status === 'passed') || (row.diagnosticAvailable && row.diagnosticRejected)) throw Error('Invalid fixed product case')
    return { id: caseIds[index], started: row.started, status: row.status, diagnosticAvailable: row.diagnosticAvailable, diagnosticRejected: row.diagnosticRejected, lifecycle: validateLifecycle(row.lifecycle) }
  })
}
export function emptyDiagnostics() { return { summaryAvailable: false, selectionValid: false, unexpected: false, globalErrors: 0, observed: 0, passed: 0, cases: initialCases() } }
export function validateDiagnostics(value) {
  if (!exact(value, ['summaryAvailable', 'selectionValid', 'unexpected', 'globalErrors', 'observed', 'passed', 'cases']) || ['summaryAvailable', 'selectionValid', 'unexpected'].some(key => typeof value[key] !== 'boolean') || !Number.isInteger(value.globalErrors) || value.globalErrors < 0 || value.globalErrors > 255 || !Number.isInteger(value.observed) || value.observed < 0 || value.observed > 2 || !Number.isInteger(value.passed) || value.passed < 0 || value.passed > value.observed) throw Error('Invalid fixed product diagnostics')
  const cases = validateCases(value.cases)
  if (cases.filter(row => row.status !== 'not-started').length !== value.observed || cases.filter(row => row.status === 'passed').length !== value.passed) throw Error('Inconsistent fixed product counts')
  return { summaryAvailable: value.summaryAvailable, selectionValid: value.selectionValid, unexpected: value.unexpected, globalErrors: value.globalErrors, observed: value.observed, passed: value.passed, cases }
}
