import assert from 'node:assert/strict'
import { lstat, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

// Raw errors, request bodies, call logs, paths, attachments, message/stack/value
// and snippets remain private. Only exact owned source units may be labeled.
const selected = 'resource-native-local-catalog-en-settings'
const unavailable = Object.freeze({ status: 'unavailable', unit: 'unavailable', line: 0, column: 0 })
const noFailure = Object.freeze({ status: 'none', unit: 'none', line: 0, column: 0 })
const statuses = new Set(['failed', 'timedOut', 'skipped', 'interrupted'])
const units = new Map([
  [fileURLToPath(new URL('./resource-native-local-catalog.acceptance.mjs', import.meta.url)), 'case'],
  [fileURLToPath(new URL('./resource-native-fixtures.mjs', import.meta.url)), 'fixture'],
  [fileURLToPath(new URL('../playwright.resource-native.config.mjs', import.meta.url)), 'config'],
  [fileURLToPath(import.meta.url), 'reporter'],
])
function diagnostic(error, status) {
  const location = error?.location
  if (!statuses.has(status) || !location || typeof location.file !== 'string' || !units.has(location.file)
    || !Number.isSafeInteger(location.line) || location.line < 1 || location.line > 100_000
    || !Number.isSafeInteger(location.column) || location.column < 1 || location.column > 10_000) return unavailable
  return { status, unit: units.get(location.file), line: location.line, column: location.column }
}
export default class ResourceNativeReporter {
  constructor() { this.started = false; this.observed = 0; this.passed = 0; this.errors = 0; this.selectionValid = false; this.unexpected = false; this.firstFailure = null }
  onBegin(_config, suite) {
    const tests = suite.allTests()
    this.selectionValid = tests.length === 1 && tests[0].title === selected && tests[0].expectedStatus === 'passed'
  }
  onTestBegin(test) {
    if (this.started || test.title !== selected) this.unexpected = true
    this.started = true
  }
  onError(error) { this.errors++; this.firstFailure ??= diagnostic(error, 'failed') }
  onTestEnd(test, result) {
    this.observed++
    if (test.title !== selected || !this.started || this.observed !== 1 || result.retry !== 0 || test.expectedStatus !== 'passed' || !Array.isArray(result.attachments) || result.attachments.length !== 0) this.unexpected = true
    if (result.status === 'passed') this.passed++
    else this.firstFailure ??= diagnostic(Array.isArray(result.errors) ? result.errors[0] : undefined, result.status)
  }
  async onEnd(result) {
    const root = process.env.SOBA_RESOURCE_BROWSER_PRIVATE_ROOT
    if (!root) return { status: 'failed' }
    const info = await lstat(root)
    assert.ok(info.isDirectory() && !info.isSymbolicLink() && info.uid === process.getuid() && (info.mode & 0o077) === 0, 'Protected result root required')
    const accepted = this.selectionValid && this.started && !this.unexpected && this.observed === 1 && this.passed === 1 && this.errors === 0 && result.status === 'passed'
    const value = { schema: 2, expected: 1, observed: this.observed, passed: this.passed, errors: this.errors, selectionValid: this.selectionValid, unexpected: this.unexpected, accepted, diagnostic: accepted ? noFailure : this.firstFailure ?? unavailable }
    await writeFile(join(root, 'browser-summary.json'), JSON.stringify(value), { flag: 'wx', mode: 0o600 })
    return accepted ? undefined : { status: 'failed' }
  }
}
