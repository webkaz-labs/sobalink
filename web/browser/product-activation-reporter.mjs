import { writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { productActivationCases } from './product-activation-contract.mjs'

// Never serialize raw errors, attachment metadata, paths, call logs or titles
// outside this reviewed set. Unexpected selection and retries fail closed.
export default class ProductActivationReporter {
  constructor() { this.results = new Map(); this.globalErrors = 0; this.selectionValid = false; this.unexpected = false }
  onBegin(_config, suite) {
    const tests = suite.allTests()
    this.selectionValid = tests.length === 2 && new Set(tests.map(test => test.title)).size === 2 && tests.every(test => productActivationCases.includes(test.title) && test.expectedStatus === 'passed')
  }
  onError() { this.globalErrors++ }
  onTestEnd(test, result) {
    if (!productActivationCases.includes(test.title) || this.results.has(test.title) || result.retry !== 0 || test.expectedStatus !== 'passed' || result.attachments.length !== 0) this.unexpected = true
    if (productActivationCases.includes(test.title)) this.results.set(test.title, result.status === 'passed' && result.retry === 0)
  }
  async onEnd(result) {
    const accepted = this.selectionValid && !this.unexpected && this.globalErrors === 0 && result.status === 'passed' && this.results.size === 2 && productActivationCases.every(name => this.results.get(name) === true)
    const summary = { schema: 1, expected: 2, observed: this.results.size, passed: [...this.results.values()].filter(Boolean).length, globalErrors: this.globalErrors, selectionValid: this.selectionValid, unexpected: this.unexpected, runnerPassed: result.status === 'passed', accepted }
    const root = process.env.SOBA_PRODUCT_ACTIVATION_PRIVATE_RUN
    if (!root) return { status: 'failed' }
    await writeFile(join(root, 'sanitized-summary.json'), JSON.stringify(summary), { mode: 0o600 })
    return accepted ? undefined : { status: 'failed' }
  }
}
