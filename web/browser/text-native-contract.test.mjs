import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
const read = path => readFileSync(new URL(path, import.meta.url), 'utf8')

test('text native case reuses sealed one-case browser runner without changing launch policy', () => {
  const config = read('../playwright.text-native.config.mjs')
  assert.equal(config, "import existing from './playwright.transfer-native.config.mjs'\nexport default {\n  ...existing,\n  testMatch: '**/text-outbox-native.acceptance.mjs',\n}\n")
  const fixture = read('./transfer-native-fixtures.mjs')
  assert.ok(fixture.includes('session.textOutbox === true'))
  for (const guard of ['(info.mode & 0o077) === 0', "url.hostname === '127.0.0.1'", "script-src 'self'", 'session.viewer.code = \'\'', 'drainRoutesBeforeCleanup']) assert.ok(fixture.includes(guard))
  const runner = read('../../internal/core/transfer_group_process_integration_test.go')
  assert.ok(runner.includes('configuration = "playwright.text-native.config.mjs"'))
  assert.ok(runner.includes('command.Stdout, command.Stderr = io.Discard, io.Discard'))
  assert.ok(runner.includes('receipt.Observed != 1 || receipt.Passed != 1 || receipt.Errors != 0'))
})

test('text browser review and recovery bind the actual production selectors and commands', () => {
  const spec = read('./text-outbox-native.acceptance.mjs'), labels = read('../src/text-outbox-i18n.ts')
  for (const label of ['Text sending mode', 'Send text now', 'Queue text', 'Sending deadline', 'Review queued text', 'Queue this text', 'Check original text request', 'Inspect original text']) assert.ok(labels.includes(`'${label}'`) && spec.includes(`'${label}'`))
  assert.ok(spec.includes("getByRole('combobox', { name: 'Sending deadline', exact: true })"))
  assert.ok(!spec.includes("getByLabel('Sending deadline'"))
  assert.equal(spec.match(/test\('/g)?.length, 1)
  for (const name of ['prepare', 'commit', 'status']) assert.ok(spec.includes(`command.name === 'message.queue.${name}'`))
  assert.equal(spec.match(/await route.fetch\(\)/g)?.length, 3)
  assert.equal(spec.match(/await route.abort\('failed'\)/g)?.length, 1)
  assert.ok(spec.includes("assert.notEqual(command.name, 'message.send')"))
  assert.ok(spec.includes("await expect(consent).not.toBeChecked(); await expect(commit).toBeDisabled()"))
  assert.ok(spec.includes("await checkpoint('review', { prepared })"))
  assert.ok(spec.includes('assert.deepEqual([prepares, commits, drops, checks], [1, 1, 1, 1])'))
})

test('text browser exports only fixed successful evidence after real store proof and joins', () => {
  const source = read('../../internal/core/durable_text_browser_integration_test.go')
  const literal = source.match(/file.WriteString\(("(?:\\.|[^"\\])*")\)/)
  assert.ok(literal)
  const summary = JSON.parse(JSON.parse(literal[1]))
  assert.deepEqual(summary, { schema: 1, accepted: true, observed: 1, passed: 1, errors: 0, selectedRecipients: 1, explicitQueueMode: true, exactReview: true, explicitConsent: true, prepareCalls: 1, commitCalls: 1, commitResponseLost: true, originalStatusRecovered: true, receiverSavedRows: 1, untouchedPeerRows: 0, receiptMatched: true, locales: ['en', 'ja'], widths: [375, 320], browserCommandExited: true, coreChildrenJoined: true })
  for (const check of ['fixture.text-evidence', 'receiver.IncomingRows != 1', '!receiver.BodyExact', '!reflect.DeepEqual(receiver.Receipt, receipt)', 'untouched.IncomingRows != 0', 'observed.ExposurePossible || observed.RunActive']) assert.ok(source.includes(check))
  assert.ok(source.indexOf('p.stop(t)') < source.indexOf('SOBA_TEXT_BROWSER_SUMMARY'))
  assert.ok(source.includes('SOBALINK_RUN_TRANSFER_BROWSER_NATIVE') && source.includes('SOBALINK_RUN_DIRECT_INTEGRATION'))
})
