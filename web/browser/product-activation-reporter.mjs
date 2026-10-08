import { writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { productActivationCases } from './product-activation-contract.mjs'
import { initialCases, statuses, validateLifecycle, validateDiagnostics, counter } from './product-activation-diagnostics.mjs'

// Never serialize raw errors, attachment metadata, paths, call logs or titles
// outside this reviewed set. Unexpected selection and retries fail closed.
export default class ProductActivationReporter {
  constructor() { this.results = new Map(); this.globalErrors = 0; this.selectionValid = false; this.unexpected = false; this.cases = initialCases() }
  onBegin(_config, suite) {
    const tests = suite.allTests()
    this.selectionValid = tests.length === 2 && new Set(tests.map(test => test.title)).size === 2 && tests.every(test => productActivationCases.includes(test.title) && test.expectedStatus === 'passed')
  }
  onTestBegin(test) {
    const index = productActivationCases.indexOf(test.title)
    if (index < 0 || this.cases[index].started) this.unexpected = true
    else this.cases[index].started = true
  }
  onError() { this.globalErrors++ }
  onTestEnd(test, result) {
    const index = productActivationCases.indexOf(test.title)
    if (index < 0 || this.results.has(test.title)) { this.unexpected = true; return }
    const row = this.cases[index]
    if (result.retry !== 0 || test.expectedStatus !== 'passed' || !Array.isArray(result.attachments) || result.attachments.length !== 0 || !row.started || !statuses.includes(result.status) || result.status === 'not-started') this.unexpected = true
    row.status = row.started && result.retry === 0 && statuses.includes(result.status) && result.status !== 'not-started' ? result.status : 'failed'
    this.results.set(test.title, row.status === 'passed')
    const annotations = Array.isArray(test.annotations) ? test.annotations.filter(item => item?.type === 'product-activation-sanitized') : []
    // Optional diagnostics explain existing results. Missing or rejected
    // annotations cannot promote a case, and do not create a new pass gate.
    if (annotations.length === 0) return
    try {
      if (annotations.length !== 1 || typeof annotations[0].description !== 'string' || annotations[0].description.length > 4096) throw Error('Invalid fixed annotation')
      row.lifecycle = validateLifecycle(JSON.parse(annotations[0].description))
      row.diagnosticAvailable = true
    } catch { row.diagnosticRejected = true }
  }
  async onEnd(result) {
    const accepted = this.selectionValid && !this.unexpected && this.globalErrors === 0 && result.status === 'passed' && this.results.size === 2 && productActivationCases.every(name => this.results.get(name) === true)
    const summary = { schema: 3, expected: 2, observed: this.results.size, passed: [...this.results.values()].filter(Boolean).length, globalErrors: this.globalErrors, selectionValid: this.selectionValid, unexpected: this.unexpected, runnerPassed: result.status === 'passed', accepted }
    summary.diagnostics = validateDiagnostics({ summaryAvailable: true, selectionValid: this.selectionValid, unexpected: this.unexpected, globalErrors: counter(this.globalErrors), observed: summary.observed, passed: summary.passed, cases: this.cases })
    const root = process.env.SOBA_PRODUCT_ACTIVATION_PRIVATE_RUN
    if (!root) return { status: 'failed' }
    await writeFile(join(root, 'sanitized-summary.json'), JSON.stringify(summary), { mode: 0o600 })
    return accepted ? undefined : { status: 'failed' }
  }
}
