import { writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { activationCases } from './activation-contract.mjs'
// Only this fixed schema crosses the private artifact boundary. Never serialize
// exceptions, names not in the reviewed set, paths, attachments or call logs.
export default class ActivationReporter {
  constructor() { this.results = new Map(); this.globalErrors = 0; this.selectionValid = false; this.unexpected = false }
  onBegin(_config, suite) {
    const tests = suite.allTests()
    this.selectionValid = tests.length === 7 && new Set(tests.map(test => test.title)).size === 7 && tests.every(test => activationCases.includes(test.title) && test.expectedStatus === 'passed')
  }
  onError() { this.globalErrors++ }
  onTestEnd(test, result) {
    if (!activationCases.includes(test.title) || this.results.has(test.title) || result.retry !== 0 || test.expectedStatus !== 'passed') this.unexpected = true
    if (activationCases.includes(test.title)) this.results.set(test.title, result.status === 'passed' && result.retry === 0)
  }
  async onEnd(result) {
    const accepted = this.selectionValid && !this.unexpected && this.globalErrors === 0 && result.status === 'passed' && this.results.size === 7 && activationCases.every(name => this.results.get(name) === true)
    const summary = { schema: 1, expected: 7, observed: this.results.size, passed: [...this.results.values()].filter(Boolean).length, globalErrors: this.globalErrors, selectionValid: this.selectionValid, unexpected: this.unexpected, runnerPassed: result.status === 'passed', accepted }
    const root = process.env.SOBA_ACTIVATION_PRIVATE_RUN
    if (!root) return { status: 'failed' }
    await writeFile(join(root, 'sanitized-summary.json'), JSON.stringify(summary), { mode: 0o600 })
    return accepted ? undefined : { status: 'failed' }
  }
}
