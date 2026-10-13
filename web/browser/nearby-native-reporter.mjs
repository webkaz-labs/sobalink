import TransferNativeReporter from './transfer-native-reporter.mjs'
import { PHASES, errorKind, recordSessionFailure } from './nearby-native-diagnostics.mjs'

// Preserve existing exact-one-case pass evidence. The added file exists only
// on failure and contains no error message, selector, stack, or browser data.
export default class NearbyNativeReporter extends TransferNativeReporter {
  constructor() { super(); this.phase = 'launch' }
  onStepBegin(_test, _result, step) {
    if (step.category === 'test.step' && step.title.startsWith('nearby:')) {
      const phase = step.title.slice('nearby:'.length)
      if (PHASES.includes(phase)) this.phase = phase
    }
  }
  onError(error) { recordSessionFailure(this.phase, errorKind(error)); super.onError(error) }
  onTestEnd(test, result) {
    if (result.status !== 'passed') recordSessionFailure(this.phase, errorKind(result.errors?.[0], result.status))
    super.onTestEnd(test, result)
  }
  async onEnd(result) {
    if (result.status !== 'passed') recordSessionFailure(this.phase, 'unexpected')
    return super.onEnd(result)
  }
}
