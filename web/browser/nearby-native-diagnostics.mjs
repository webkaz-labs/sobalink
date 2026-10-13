import { writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'

export const PHASES = Object.freeze(['launch', 'descriptor', 'authenticate-a', 'authenticate-b', 'open-a', 'start-a', 'cancel-a', 'checkpoint-cancelled', 'restart-a', 'open-b', 'start-b', 'manual-a', 'manual-b', 'plan-a', 'plan-b', 'fingerprint-a', 'fingerprint-b', 'checkpoint-review', 'approve-a', 'recover-a', 'checkpoint-one-approved', 'approve-b', 'connected-a', 'connected-b', 'checkpoint-connected', 'cleanup', 'core-start', 'core-cancelled', 'core-review', 'core-one-approved', 'core-connected', 'core-cleanup', 'manual-edit-a', 'manual-edit-b', 'manual-submit-a', 'manual-submit-b', 'fingerprint-read-a', 'fingerprint-read-b', 'fingerprint-initial-a', 'fingerprint-initial-b', 'fingerprint-mismatch-edit-a', 'fingerprint-mismatch-edit-b', 'fingerprint-mismatch-blocked-a', 'fingerprint-mismatch-blocked-b', 'fingerprint-match-edit-a', 'fingerprint-match-edit-b', 'fingerprint-verify-a', 'fingerprint-verify-b', 'fingerprint-consent-a', 'fingerprint-consent-b', 'fingerprint-unverify-a', 'fingerprint-unverify-b', 'fingerprint-ready-a', 'fingerprint-ready-b', 'approve-send-a', 'approve-response-a', 'approve-uncertain-a', 'recover-request-a', 'recover-receipt-a', 'approve-send-b', 'approve-receipt-b'])
export const ERROR_KINDS = Object.freeze(['timeout', 'assertion', 'selector_ambiguous', 'browser_launch', 'browser_closed', 'runtime', 'command_boundary', 'checkpoint', 'cleanup', 'interrupted', 'unexpected', 'core_evidence', 'evidence_unavailable', 'locator_missing', 'element_disabled', 'element_hidden', 'element_not_editable', 'element_detached', 'pointer_intercepted', 'element_unstable'])
export function failureRecord(phase, errorKind) {
  if (!PHASES.includes(phase) || !ERROR_KINDS.includes(errorKind)) throw Error('Invalid fixed nearby failure classification')
  return { schema: 1, phase, errorKind }
}
export function errorKind(error, status) {
  if (status === 'interrupted') return 'interrupted'
  const message = typeof error?.message === 'string' ? error.message : ''
  if (/strict mode violation/i.test(message)) return 'selector_ambiguous'
  if (/browserType\.launch|Failed to launch|Executable doesn't exist/i.test(message)) return 'browser_launch'
  if (/Target.*closed|has been closed/i.test(message)) return 'browser_closed'
  // These are pinned Playwright call-log outcomes, not guesses from selector
  // text. A bare wait/timeout does not establish a missing or disabled element.
  // Keep the last explicit actionability observation when retries report more
  // than one state. The message itself never leaves this classification helper.
  const outcomes = [
    [/element\(s\) not found|locator resolved to 0 elements/gi, 'locator_missing'],
    [/(?:element|option being selected) is not enabled/gi, 'element_disabled'],
    [/element is not visible/gi, 'element_hidden'],
    [/element is not editable/gi, 'element_not_editable'],
    [/element was detached from the DOM/gi, 'element_detached'],
    [/intercepts pointer events/gi, 'pointer_intercepted'],
    [/element is not stable/gi, 'element_unstable'],
  ]
  let observed, last = -1
  for (const [pattern, kind] of outcomes) for (const match of message.matchAll(pattern)) {
    if (match.index > last) { last = match.index; observed = kind }
  }
  if (observed) return observed
  if (status === 'timedOut' || error?.name === 'TimeoutError' || /timed out|Timeout \d/i.test(message)) return 'timeout'
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
