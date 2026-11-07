import TransferNativeReporter from './transfer-native-reporter.mjs'
import { PHASES, errorKind, recordSessionFailure } from './outbox-native-diagnostics.mjs'

// The existing exact-one-case success receipt is unchanged. Failure details
// stay private; only a validated phase and error category may reach the Go log.
export default class OutboxNativeReporter extends TransferNativeReporter {
  onTestEnd(test, result) {
    if (result.status !== 'passed') {
      const phase = (result.annotations || []).filter(value => value.type === 'outbox-phase' && PHASES.includes(value.description)).at(-1)?.description || 'launch'
      recordSessionFailure(phase, errorKind(result.errors?.[0], result.status))
    }
    super.onTestEnd(test, result)
  }
  async onEnd(result) {
    if (result.status !== 'passed') recordSessionFailure('launch', 'unexpected')
    return super.onEnd(result)
  }
}
