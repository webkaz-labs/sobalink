import { mkdir, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { basename, join, resolve } from 'node:path'

function failureCategory(errors = []) {
  const text = errors.map(error => error.message || '').join('\n')
  if (!errors.length) return undefined
  if (/browserType\.launch|Executable doesn't exist/.test(text)) return 'browser-launch'
  if (/Private fixture authentication/.test(text)) return 'private-authentication-or-csp'
  if (/Go harness.*ready|fixture.*ready|SOBA_E2E_BINARY|Go harness must exist/.test(text)) return 'fixture-start'
  if (/application did not exit|Go harness must exit|Go fixture must close|forced termination/.test(text)) return 'fixture-shutdown'
  if (/runtime and CSP|uncaught exceptions|violate production CSP/.test(text)) return 'browser-runtime-health'
  if (/expect\(/.test(text)) return 'assertion'
  if (/timeout|timed out/i.test(text)) return 'timeout'
  return 'fixture-or-test-error'
}

function failureLocations(errors, testFile) {
  const allowed = new Set([basename(testFile), 'fixtures.mjs'])
  const locations = new Map()
  for (const error of errors || []) {
    // Extract only allowlisted source basenames and numeric coordinates. Never
    // publish assertion values, call logs, function names or private paths.
    for (const frame of (error.stack || '').slice(-16_000).split('\n')) {
      if (!/^\s+at\s/.test(frame)) continue
      const match = frame.match(/[/\\]([a-z0-9_.-]+\.mjs):([1-9]\d{0,5}):([1-9]\d{0,5})\)?\s*$/i)
      if (!match || !allowed.has(match[1])) continue
      const location = { file: match[1], line: Number(match[2]), column: Number(match[3]) }
      locations.set(JSON.stringify(location), location)
      if (locations.size === 4) return [...locations.values()]
    }
  }
  return [...locations.values()]
}

// Only this deliberately small report is an upload artifact. It never serializes
// Playwright call logs, raw stacks, attachments, private values or page state.
export default class SafeReporter {
  constructor() { this.tests = []; this.startedAt = new Date().toISOString() }
  onBegin(_config, suite) { this.expectedTests = suite.allTests().length; console.log(`Collected ${this.expectedTests} browser acceptance tests`) }
  onTestEnd(test, result) {
    const artifacts = (test.annotations || []).filter(annotation => annotation.type === 'safe-artifact' && /^[a-z0-9][a-z0-9_.-]*\.(png|json)$/i.test(annotation.description || '') && !annotation.description.includes('..')).map(annotation => annotation.description)
    const record = { name: test.titlePath().filter(Boolean).join(' > '), status: result.status, durationMs: result.duration, file: basename(test.location.file), line: test.location.line, failureCategory: failureCategory(result.errors), failureLocations: failureLocations(result.errors, test.location.file), artifacts }
    this.tests.push(record)
    console.log(`${result.status.toUpperCase()} ${record.name}`)
    if (record.failureCategory) {
      const locations = record.failureLocations.map(location => `${location.file}:${location.line}:${location.column}`).join(',')
      console.log(`DIAGNOSTIC ${record.failureCategory}${locations ? ` ${locations}` : ''}`)
    }
  }
  async onEnd(result) {
    const output = resolve(process.env.SOBA_SCREENSHOT_DIR || join(tmpdir(), 'sobalink-playwright-screenshots'))
    await mkdir(output, { recursive: true, mode: 0o700 })
    const report = { tool: 'Playwright Test', version: '1.63.0', fixture: 'Production Go HTTP with fictional in-process peers', sourceCommit: process.env.GITHUB_SHA || null, workflowRun: process.env.GITHUB_RUN_ID || null, startedAt: this.startedAt, finishedAt: new Date().toISOString(), status: result.status, expectedTests: this.expectedTests, requiredCoverageComplete: result.status === 'passed' && this.tests.length > 0 && this.tests.length === this.expectedTests && this.tests.every(test => test.status === 'passed'), tests: this.tests, limits: ['Synthetic composition events do not establish native IME behavior.', 'Fixture peers do not establish enrollment, real-device delivery, relay hosting or pairing.', 'Ready service rules do not establish remote application compatibility.', 'Native clipboard/folder dialogs, installed binaries, OS login and suspend/resume remain untested.'] }
    await writeFile(join(output, 'playwright-report.json'), `${JSON.stringify(report, null, 2)}\n`, { mode: 0o600 })
  }
}
