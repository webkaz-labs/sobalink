import { writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import TransferNativeReporter from './transfer-native-reporter.mjs'
import { ERROR_KINDS, errorKind } from './nearby-native-diagnostics.mjs'

export const PHASES = Object.freeze([
  'fixture-setup', 'session', 'open-peer', 'queue-mode', 'prepare', 'review-exact',
  'review-consent', 'review-fit', 'review-checkpoint', 'commit', 'lost-response',
  'recover', 'recovery-checkpoint', 'outbox-open', 'outbox-status-en', 'detail-request',
  'detail-exact', 'detail-receipt-en', 'detail-fit-en', 'escape', 'locale-ja',
  'outbox-open-ja', 'outbox-status-ja', 'outbox-receipt-ja', 'outbox-fit-ja',
  'command-counts', 'runtime-counts', 'result-write', 'body-complete', 'fixture-cleanup', 'unavailable',
])
export function failureRecord(phase, kind) {
  if (!PHASES.includes(phase) || !ERROR_KINDS.includes(kind)) throw Error('Invalid fixed text failure classification')
  return { schema: 1, phase, errorKind: kind }
}
export async function textPhase(test, phase) {
  failureRecord(phase, 'unexpected')
  await test.step(`text:${phase}`, async () => {})
}
export function recordFirstFailure(path, phase, kind) {
  const record = failureRecord(phase, kind)
  try { writeFileSync(path, JSON.stringify(record) + '\n', { mode: 0o600, flag: 'wx' }) }
  catch (error) { if (error.code !== 'EEXIST') throw error }
}

// Reuse the unchanged exact-one-case pass receipt. Only closed fixed failure
// values reach the diagnostic runner's private file; no framework data is copied.
export default class TextNativeReporter extends TransferNativeReporter {
  constructor() { super(); this.phase = 'fixture-setup' }
  failure(error, status, fallback) {
    const phase = this.phase === 'body-complete' ? 'fixture-cleanup' : this.phase
    try {
      const path = process.env.SOBA_TEXT_BROWSER_FAILURE || join(dirname(process.env.SOBA_TRANSFER_BROWSER_SESSION), 'text-native-failure.json')
      recordFirstFailure(path, phase, fallback || errorKind(error, status))
    } catch { /* Diagnostics must not replace the original failure or cleanup. */ }
  }
  onStepBegin(_test, _result, step) {
    if (step.category === 'test.step' && step.title.startsWith('text:')) {
      const phase = step.title.slice('text:'.length)
      if (PHASES.includes(phase)) this.phase = phase
    }
  }
  onStepEnd(_test, result, step) {
    // Capture the first failing action/expectation before later fixture cleanup.
    if (step.error) this.failure(step.error, result.status)
  }
  onError(error) { this.failure(error); super.onError(error) }
  onTestEnd(test, result) {
    if (result.status !== 'passed') this.failure(result.errors?.[0], result.status)
    super.onTestEnd(test, result)
  }
  async onEnd(result) {
    if (result.status !== 'passed') this.failure(undefined, result.status, 'evidence_unavailable')
    return super.onEnd(result)
  }
}
