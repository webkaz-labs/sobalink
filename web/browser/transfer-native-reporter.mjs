import { writeFile } from 'node:fs/promises'
import { dirname, join } from 'node:path'
export default class TransferNativeReporter {
  constructor() { this.observed = 0; this.passed = 0; this.errors = 0 }
  onBegin(_config, suite) { this.expected = suite.allTests().length }
  onError() { this.errors++ }
  onTestEnd(_test, result) { this.observed++; if (result.status === 'passed' && result.retry === 0) this.passed++ }
  async onEnd(result) {
    const accepted = this.expected === 1 && this.observed === 1 && this.passed === 1 && this.errors === 0 && result.status === 'passed'
    await writeFile(join(dirname(process.env.SOBA_TRANSFER_BROWSER_SESSION), 'transfer-browser-result.json'), JSON.stringify({ schema: 1, accepted, observed: this.observed, passed: this.passed, errors: this.errors }), { flag: 'wx', mode: 0o600 })
    return accepted ? undefined : { status: 'failed' }
  }
}
