import { writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'

export const PHASES = Object.freeze(['launch', 'descriptor', 'authenticate-a', 'authenticate-b', 'open-a', 'start-a', 'cancel-a', 'checkpoint-cancelled', 'restart-a', 'open-b', 'start-b', 'manual-a', 'manual-b', 'plan-a', 'plan-b', 'fingerprint-a', 'fingerprint-b', 'checkpoint-review', 'approve-a', 'recover-a', 'checkpoint-one-approved', 'approve-b', 'connected-a', 'connected-b', 'checkpoint-connected', 'cleanup', 'core-start', 'core-cancelled', 'core-review', 'core-one-approved', 'core-connected', 'core-cleanup'])
export const ERROR_KINDS = Object.freeze(['timeout', 'assertion', 'selector_ambiguous', 'browser_launch', 'browser_closed', 'runtime', 'command_boundary', 'checkpoint', 'cleanup', 'interrupted', 'unexpected', 'core_evidence', 'evidence_unavailable'])
export function failureRecord(phase, errorKind) {
  if (!PHASES.includes(phase) || !ERROR_KINDS.includes(errorKind)) throw Error('Invalid fixed nearby failure classification')
  return { schema: 1, phase, errorKind }
}
export function errorKind(error, status) {
  if (status === 'interrupted') return 'interrupted'
  if (status === 'timedOut' || error?.name === 'TimeoutError') return 'timeout'
  const message = typeof error?.message === 'string' ? error.message : ''
  if (/strict mode violation/i.test(message)) return 'selector_ambiguous'
  if (/browserType\.launch|Failed to launch|Executable doesn't exist/i.test(message)) return 'browser_launch'
  if (/Target.*closed|has been closed/i.test(message)) return 'browser_closed'
  if (/timed out|Timeout \d/i.test(message)) return 'timeout'
  if (error?.name === 'AssertionError' || /expect\(|Expected:|AssertionError/.test(message)) return 'assertion'
  return 'unexpected'
}
export function recordFirstFailure(directory, phase, kind) {
  const record = failureRecord(phase, kind), path = join(directory, 'nearby-browser-failure.json')
  try { writeFileSync(path, JSON.stringify(record) + '\n', { mode: 0o600, flag: 'wx' }) }
  catch (error) { if (error.code !== 'EEXIST') throw error }
}
export function recordSessionFailure(phase, kind) {
  // Diagnostics must never interrupt the original failure or owner cleanup.
  try { recordFirstFailure(dirname(process.env.SOBA_TRANSFER_BROWSER_SESSION), phase, kind); return true }
  catch { return false }
}
