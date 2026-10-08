// Closed diagnostic vocabulary only. Never accept raw errors, paths, URLs,
// capabilities, login codes, attachments or arbitrary test names.
export const caseIds = Object.freeze(['normal-en', 'normal-ja', 'decline', 'closed-popup', 'logout-before-confirm', 'logout-before-stop', 'lost-ack'])
export const statuses = Object.freeze(['not-started', 'passed', 'failed', 'timedOut', 'skipped', 'interrupted'])
export const workStages = Object.freeze(['not-started', 'preflight', 'native-start', 'native-ready', 'old-login', 'body', 'review-request', 'popup-request', 'handoff-ready', 'restart-confirm', 'management-ready', 'old-exit', 'successor-start', 'open-not-automatic', 'open-request', 'open-observed', 'open-consumed', 'successor-login'])
export const stages = Object.freeze([...workStages, 'cleanup-context', 'cleanup-supervisor', 'cleanup-proof', 'cleanup-safety', 'cleanup-remove', 'finished'])
export const exits = Object.freeze(['not-observed', 'zero', 'one', 'race', 'other', 'signal', 'spawn-error', 'profile-rejected', 'watchdog', 'mode-rejected', 'owner-failed', 'supervisor-failed', 'registration-failed'])
const booleans = ['workCompleted', 'workFailed', 'stopRequested', 'supervisorDeadlineExpired', 'noWaitableChildren', 'supervisorStarted', 'supervisorExited', 'proofRead', 'allDescendantsReaped', 'registeredNativeExits', 'successorRegistered', 'contextClosed', 'profileRemoved', 'outputOverflow', 'privateOutputDetected']
const counters = ['registeredChildren', 'observedExits', 'reaped', 'blockedRequests', 'runtimeErrors']
const keys = ['stage', 'workStage', 'failureStage', 'supervisorExit', 'oldExit', ...booleans, ...counters]
export function counter(value) { return Number.isSafeInteger(value) && value >= 0 ? Math.min(value, 255) : 0 }
export function lifecycle() { return { stage: 'not-started', workStage: 'not-started', oldExit: 'not-observed', failureStage: 'not-started', supervisorExit: 'not-observed', ...Object.fromEntries(booleans.map(k => [k, false])), ...Object.fromEntries(counters.map(k => [k, 0])) } }
function exact(value, names) { return value && typeof value === 'object' && !Array.isArray(value) && Object.keys(value).length === names.length && names.every(k => Object.hasOwn(value, k)) }
export function validateLifecycle(value) {
  if (!exact(value, keys) || !stages.includes(value.stage) || !workStages.includes(value.workStage) || !exits.includes(value.oldExit) || !stages.includes(value.failureStage) || !exits.includes(value.supervisorExit) || booleans.some(k => typeof value[k] !== 'boolean') || counters.some(k => !Number.isInteger(value[k]) || value[k] < 0 || value[k] > 255)) throw Error('Invalid fixed lifecycle schema')
  return Object.fromEntries(keys.map(k => [k, value[k]]))
}
export function initialCases() { return caseIds.map(id => ({ id, started: false, status: 'not-started', lifecycle: lifecycle() })) }
export function validateCases(value) {
  if (!Array.isArray(value) || value.length !== 7) throw Error('Invalid fixed case inventory')
  return value.map((row, i) => {
    if (!exact(row, ['id', 'started', 'status', 'lifecycle']) || row.id !== caseIds[i] || typeof row.started !== 'boolean' || !statuses.includes(row.status) || (!row.started && row.status === 'passed')) throw Error('Invalid fixed case status')
    return { id: caseIds[i], started: row.started, status: row.status, lifecycle: validateLifecycle(row.lifecycle) }
  })
}
export function emptyDiagnostics() { return { summaryAvailable: false, selectionValid: false, unexpected: false, globalErrors: 0, observed: 0, passed: 0, cases: initialCases() } }
export function validateDiagnostics(value) {
  if (!exact(value, ['summaryAvailable', 'selectionValid', 'unexpected', 'globalErrors', 'observed', 'passed', 'cases']) || ['summaryAvailable', 'selectionValid', 'unexpected'].some(k => typeof value[k] !== 'boolean') || !Number.isInteger(value.globalErrors) || value.globalErrors < 0 || value.globalErrors > 255 || !Number.isInteger(value.observed) || value.observed < 0 || value.observed > 7 || !Number.isInteger(value.passed) || value.passed < 0 || value.passed > value.observed) throw Error('Invalid fixed diagnostic schema')
  const cases = validateCases(value.cases)
  if (cases.filter(row => row.status !== 'not-started').length !== value.observed || cases.filter(row => row.status === 'passed').length !== value.passed) throw Error('Inconsistent fixed counts')
  return { summaryAvailable: value.summaryAvailable, selectionValid: value.selectionValid, unexpected: value.unexpected, globalErrors: value.globalErrors, observed: value.observed, passed: value.passed, cases }
}

export function passedLifecycle(value) {
  const life = validateLifecycle(value)
  return life.workCompleted && !life.workFailed && life.stage === 'finished' && life.failureStage === 'not-started' && life.supervisorExit === 'zero' && ['supervisorStarted', 'supervisorExited', 'proofRead', 'allDescendantsReaped', 'registeredNativeExits', 'contextClosed', 'profileRemoved'].every(k => life[k] === true) && life.blockedRequests === 0 && life.runtimeErrors === 0 && !life.outputOverflow && !life.privateOutputDetected
}

export function exitCategory(code) {
  if (code === null) return 'signal'
  return new Map([[0, 'zero'], [1, 'one'], [66, 'race'], [91, 'profile-rejected'], [92, 'watchdog'], [93, 'mode-rejected'], [94, 'owner-failed'], [95, 'supervisor-failed'], [96, 'registration-failed']]).get(code) || 'other'
}

export function advanceLifecycle(value, stage) {
  if (!stages.includes(stage)) throw Error('Unknown fixed stage')
  value.stage = stage
  if (workStages.includes(stage)) value.workStage = stage
}
export function failLifecycle(value) {
  if (value.failureStage === 'not-started') value.failureStage = value.stage
}
