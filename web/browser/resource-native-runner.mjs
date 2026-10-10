import assert from 'node:assert/strict'
import { lstat, writeFile } from 'node:fs/promises'
import { join } from 'node:path'

// Adapted from product-activation-reporter's closed result contract. Raw errors,
// request bodies, call logs, titles, paths and attachments are never serialized.
const selected = 'resource-native-local-catalog-en-settings'
export default class ResourceNativeReporter {
  constructor() { this.started = false; this.observed = 0; this.passed = 0; this.errors = 0; this.selectionValid = false; this.unexpected = false }
  onBegin(_config, suite) {
    const tests = suite.allTests()
    this.selectionValid = tests.length === 1 && tests[0].title === selected && tests[0].expectedStatus === 'passed'
  }
  onTestBegin(test) {
    if (this.started || test.title !== selected) this.unexpected = true
    this.started = true
  }
  onError() { this.errors++ }
  onTestEnd(test, result) {
    this.observed++
    if (test.title !== selected || !this.started || this.observed !== 1 || result.retry !== 0 || test.expectedStatus !== 'passed' || !Array.isArray(result.attachments) || result.attachments.length !== 0) this.unexpected = true
    if (result.status === 'passed') this.passed++
  }
  async onEnd(result) {
    const root = process.env.SOBA_RESOURCE_BROWSER_PRIVATE_ROOT
    if (!root) return { status: 'failed' }
    const info = await lstat(root)
    assert.ok(info.isDirectory() && !info.isSymbolicLink() && info.uid === process.getuid() && (info.mode & 0o077) === 0, 'Protected result root required')
    const accepted = this.selectionValid && this.started && !this.unexpected && this.observed === 1 && this.passed === 1 && this.errors === 0 && result.status === 'passed'
    const value = { schema: 1, expected: 1, observed: this.observed, passed: this.passed, errors: this.errors, selectionValid: this.selectionValid, unexpected: this.unexpected, accepted }
    await writeFile(join(root, 'browser-summary.json'), JSON.stringify(value), { flag: 'wx', mode: 0o600 })
    return accepted ? undefined : { status: 'failed' }
  }
}
