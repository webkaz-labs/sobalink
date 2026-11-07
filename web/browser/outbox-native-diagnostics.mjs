import { writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { ERROR_KINDS, errorKind } from './nearby-native-diagnostics.mjs'
export { ERROR_KINDS, errorKind }

// These annotations describe test operations, never runtime values or selectors.
export const PHASES = Object.freeze(['launch', 'authenticate', 'open-english', 'focus-english', 'select-recipients', 'prepare', 'commit-lost', 'recover-status', 'reload-japanese', 'open-japanese', 'focus-japanese', 'admission-bridge', 'observe-receipts', 'refresh-completed', 'result'])
export function markPhase(testInfo, phase) {
  if (!PHASES.includes(phase)) throw Error('Invalid fixed outbox phase')
  testInfo.annotations.push({ type: 'outbox-phase', description: phase })
}
export function failureRecord(phase, kind) {
  if (!PHASES.includes(phase) || !ERROR_KINDS.includes(kind)) throw Error('Invalid fixed outbox failure classification')
  return { schema: 1, phase, errorKind: kind }
}
export function recordSessionFailure(phase, kind) {
  // Diagnostics must not interrupt the original failure or process cleanup.
  try {
    const record = failureRecord(phase, kind)
    writeFileSync(join(dirname(process.env.SOBA_TRANSFER_BROWSER_SESSION), 'outbox-browser-failure.json'), JSON.stringify(record) + '\n', { mode: 0o600, flag: 'wx' })
    return true
  } catch { return false }
}
