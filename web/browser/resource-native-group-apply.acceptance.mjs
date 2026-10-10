import assert from 'node:assert/strict'
import { lstat, readFile, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { test as base, expect } from '@playwright/test'

// The real Core owns the prepared two-target review. This case retrieves and
// explicitly applies it; it does not claim browser draft/preview authoring.
const names = ['resource.group.review.current', 'resource.group.apply', 'resource.group.status']
const maxReply = 139_264
function exactKeys(value, keys) {
  return value && typeof value === 'object' && !Array.isArray(value) && Object.keys(value).length === keys.length && keys.every(key => Object.hasOwn(value, key))
}
function commandFrom(request) {
  const raw = request.postData()
  assert.ok(request.method() === 'POST' && raw && Buffer.byteLength(raw) <= 4096)
  const value = JSON.parse(raw)
  assert.ok(exactKeys(value, ['requestId', 'name', 'payload']) && names.includes(value.name) && typeof value.requestId === 'string' && value.requestId.length > 0 && value.requestId.length <= 128)
  return value
}
const test = base.extend({
  app: async ({ page, context }, use, testInfo) => {
    const root = process.env.SOBA_RESOURCE_BROWSER_PRIVATE_ROOT
    const info = await lstat(root)
    assert.ok(info.isDirectory() && !info.isSymbolicLink() && info.uid === process.getuid() && (info.mode & 0o077) === 0, 'Owned private fixture root required')
    for (const name of ['screenshot', 'trace', 'video']) assert.equal(testInfo.project.use[name], 'off', 'Automatic capture forbidden')
    assert.equal(testInfo.project.use.serviceWorkers, 'block', 'Service workers forbidden')
    let session
    try {
      const path = join(root, 'session.json'), file = await lstat(path)
      assert.ok(file.isFile() && !file.isSymbolicLink() && file.uid === process.getuid() && (file.mode & 0o077) === 0 && file.size > 0 && file.size <= 16_384)
      session = JSON.parse(await readFile(path, 'utf8'))
      assert.ok(exactKeys(session, ['schema', 'url', 'code', 'processID', 'assets']) && session.schema === 1)
      const url = new URL(session.url)
      assert.ok(url.protocol === 'http:' && url.hostname === '127.0.0.1' && url.port && !url.username && !url.password && !url.search && !url.hash && url.pathname === '/' && session.url === url.origin)
      assert.ok(typeof session.code === 'string' && /^[A-Za-z0-9_-]{32}$/.test(session.code))
      assert.ok(typeof session.processID === 'string' && /^[a-f0-9]{64}$/.test(session.processID))
      assert.ok(Array.isArray(session.assets) && session.assets.length > 0 && session.assets.length <= 32 && session.assets.every(path => typeof path === 'string' && /^\/(assets\/[a-zA-Z0-9_.-]+|favicon\.svg|png-worker-manifest\.json)$/.test(path)))
    } catch { throw Error('Private fixture descriptor was invalid; contents withheld') }
    const observations = { schema: 1, requests: 0, stateRequests: 0, stateReady: 0, loginRequests: 0, currentRequests: 0, currentResponses: 0, applyRequests: 0, applyResponses: 0, statusRequests: 0, statusResponses: 0, blocked: 0, errors: 0, captures: 0, completed: false, requestsJoined: false }
    const pending = new Set(), inflight = new Set(), commands = new Map(), requestIDs = new Set(), order = []
    let current, run, status, authenticated = false, closing = false, faviconReads = 0
    const fail = () => { observations.errors++ }
    const track = task => { pending.add(task); task.finally(() => pending.delete(task)).catch(() => {}) }
    const expectedApply = () => ({ schemaVersion: 1, reviewId: current.prepared.reviewId, reviewRevision: current.prepared.review.revision, executionPeers: current.prepared.review.executionPeers, confirm: true })
    const validatePayload = command => {
      if (command.name === names[0]) assert.deepEqual(command.payload, { schemaVersion: 1 })
      else {
        assert.ok(current && current.state === 'current' && current.prepared)
        assert.deepEqual(command.payload, command.name === names[1] ? expectedApply() : { schemaVersion: 1, runId: current.prepared.reviewId })
      }
    }
    page.on('request', request => {
      inflight.add(request); observations.requests++
      if (observations.requests > 256) fail()
      try {
        const url = new URL(request.url()), method = request.method()
        assert.equal(url.origin, session.url)
        if (url.pathname === '/favicon.ico') assert.ok(['GET', 'HEAD'].includes(method) && ++faviconReads <= 1)
        else if (url.pathname === '/api/state') { observations.stateRequests++; assert.ok(method === 'GET' && observations.stateRequests <= 96) }
        else if (url.pathname === '/api/session') { observations.loginRequests++; assert.ok(method === 'POST' && observations.loginRequests === 1) } // Never read the access-code body.
        else if (url.pathname === '/api/command') {
          const command = commandFrom(request); validatePayload(command)
          assert.ok(!requestIDs.has(command.requestId)); requestIDs.add(command.requestId)
          assert.equal(command.name, names[order.length]); order.push(command.name)
          observations[`${['current', 'apply', 'status'][names.indexOf(command.name)]}Requests`]++
          commands.set(request, command)
        }
      } catch { fail() }
    })
    page.on('requestfinished', request => inflight.delete(request))
    page.on('requestfailed', request => {
      inflight.delete(request)
      const expectedClose = closing && request.method() === 'GET' && new URL(request.url()).pathname === '/api/state'
      if (!expectedClose) fail()
    })
    page.on('response', response => {
      track((async () => {
        try {
          const request = response.request(), url = new URL(response.url())
          if (url.pathname === '/api/state') {
            if (response.status() === 200) observations.stateReady++
            else if (response.status() !== 401 || authenticated) fail()
          }
          const command = commands.get(request)
          if (!command) return
          assert.equal(response.status(), 200)
          const body = await response.body()
          assert.ok(body.length > 0 && body.length <= maxReply + 128)
          const envelope = JSON.parse(body.toString('utf8'))
          assert.ok(exactKeys(envelope, ['ok', 'result']) && envelope.ok === true && Buffer.byteLength(JSON.stringify(envelope.result)) <= maxReply)
          const result = envelope.result
          if (command.name === names[0]) {
            assert.ok(!current && exactKeys(result, ['schemaVersion', 'state', 'prepared']) && result.schemaVersion === 1 && result.state === 'current')
            current = result; observations.currentResponses++
            await writeFile(join(root, 'current-result.json'), JSON.stringify(result), { flag: 'wx', mode: 0o600 })
          } else if (command.name === names[1]) {
            assert.ok(!run); run = result; observations.applyResponses++
            await writeFile(join(root, 'apply-request.json'), JSON.stringify(command.payload), { flag: 'wx', mode: 0o600 })
            await writeFile(join(root, 'apply-result.json'), JSON.stringify(result), { flag: 'wx', mode: 0o600 })
          } else {
            assert.ok(run && !status); assert.deepEqual(result, run)
            status = result; observations.statusResponses++
            await writeFile(join(root, 'status-result.json'), JSON.stringify(result), { flag: 'wx', mode: 0o600 })
          }
        } catch { fail() }
      })())
    })
    page.on('pageerror', fail)
    page.on('console', entry => { if (entry.type() === 'error' && /content security policy|refused to/i.test(entry.text())) fail() })
    context.on('page', other => { if (other !== page) { fail(); void other.close().catch(fail) } })
    page.on('download', download => { fail(); void download.cancel().catch(fail) })
    await context.routeWebSocket('**/*', socket => { observations.blocked++; socket.close() })
    await context.route('**/*', async route => {
      try {
        const request = route.request(), url = new URL(request.url()), method = request.method()
        const read = method === 'GET' || method === 'HEAD'
        const pathOK = read && (url.pathname === '/' || url.pathname === '/favicon.ico' || session.assets.includes(url.pathname) || url.pathname === '/api/state') || method === 'POST' && ['/api/session', '/api/command'].includes(url.pathname)
        assert.ok(url.origin === session.url && url.protocol === 'http:' && url.hostname === '127.0.0.1' && !url.username && !url.password && !url.search && !url.hash && pathOK)
        if (url.pathname === '/api/command') {
          validatePayload(commandFrom(request))
          // Blocking unexpected commands is containment, not a Core-admission oracle.
          assert.equal(observations.errors, 0)
        }
        await route.continue()
      } catch { observations.blocked++; await route.abort('blockedbyclient').catch(fail) }
    })
    const audit = () => {
      assert.equal(observations.errors, 0, 'Private group request or browser audit failed')
      assert.equal(observations.blocked, 0, 'Unexpected browser destination or route')
    }
    const settled = async getValue => { await Promise.all([...pending]); audit(); const value = getValue(); assert.ok(value); return value }
    try {
      try {
        const response = await page.goto(session.url)
        assert.ok(response && response.status() === 200 && (response.headers()['content-security-policy'] || '').includes("script-src 'self'"))
        await page.getByLabel('Local access code', { exact: true }).fill(session.code)
        await page.getByRole('button', { name: 'Open sobalink', exact: true }).click()
        await page.locator('.workspace').waitFor()
        await expect(page.locator('.login-panel')).toHaveCount(0)
        authenticated = true; session.code = ''
      } catch { throw Error('Real local browser authentication failed; private details withheld') }
      await use({
        count() { audit(); return order.length },
        stateReady() { audit(); return observations.stateReady },
        current() { return settled(() => current) },
        run() { return settled(() => run) },
        status() { return settled(() => status) },
        complete() { audit(); assert.deepEqual(order, names); observations.completed = true },
      })
    } finally {
      await Promise.all([...pending])
      closing = true
      await page.close().catch(fail)
      await context.unrouteAll({ behavior: 'wait' }).catch(fail)
      await Promise.all([...pending])
      observations.requestsJoined = inflight.size === 0 && pending.size === 0
      if (!observations.requestsJoined) fail()
      session.code = ''
      await writeFile(join(root, 'http-observations.json'), JSON.stringify(observations), { flag: 'wx', mode: 0o600 })
      assert.ok(observations.completed && observations.requestsJoined && observations.loginRequests === 1 && observations.stateReady > 0 && observations.captures === 0, 'Exact group browser observations incomplete')
      for (const kind of ['current', 'apply', 'status']) assert.ok(observations[`${kind}Requests`] === 1 && observations[`${kind}Responses`] === 1, 'Exact group command counts incomplete')
      audit()
    }
  },
})

async function detailValue(scope, label, value) {
  await expect(scope.locator('dt').filter({ hasText: new RegExp(`^${label}$`) }).locator('xpath=following-sibling::dd[1]')).toHaveText(String(value))
}
test('resource-native-group-two-target-apply-en', async ({ page, app }) => {
  await expect(page.locator('html')).toHaveAttribute('lang', 'en')
  const open = page.getByRole('button', { name: 'Group transfer settings', exact: true })
  await open.click()
  let dialog = page.getByRole('dialog', { name: 'Group transfer settings', exact: true })
  await expect(dialog).toBeVisible()
  const before = app.stateReady()
  await expect.poll(() => app.stateReady()).toBeGreaterThan(before)
  expect(app.count()).toBe(0)
  await expect(dialog.getByRole('button', { name: 'Apply reviewed subset once', exact: true })).toHaveCount(0)
  await page.keyboard.press('Escape')
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(open).toBeFocused()
  await page.keyboard.press('Enter')
  dialog = page.getByRole('dialog', { name: 'Group transfer settings', exact: true })
  expect(app.count()).toBe(0)
  await dialog.getByRole('button', { name: 'Find current unused review', exact: true }).click()
  const review = dialog.getByRole('region', { name: 'Review every selected device', exact: true })
  await expect(review).toBeVisible()
  const current = await app.current(), prepared = current.prepared
  assert.equal(prepared.admissionState, 'prepared')
  assert.equal(prepared.review.rows.length, 2)
  assert.equal(prepared.review.selection.members.length, 2)
  assert.deepEqual(prepared.review.executionPeers, prepared.review.rows.map(row => row.peerKey))
  const expected = [{ transferConcurrentFiles: { mode: 'limited', value: 3 }, transferConcurrentPerPeer: { mode: 'limited', value: 1 } }, { transferConcurrentFiles: { mode: 'limited', value: 4 }, transferConcurrentPerPeer: { mode: 'limited', value: 2 } }]
  assert.deepEqual(prepared.review.selection.template.settings, expected[0])
  const rows = review.locator('.group-review-rows > li')
  await expect(rows).toHaveCount(2)
  for (let i = 0; i < 2; i++) {
    const row = prepared.review.rows[i], member = prepared.review.selection.members[i], ui = rows.nth(i)
    assert.equal(row.state, 'ready'); assert.equal(row.peerKey, member.peerKey)
    assert.deepEqual(row.reply.preview.requested, expected[i])
    assert.deepEqual(member.override ?? null, i === 0 ? null : expected[1])
    await expect(ui).toContainText('Ready for review')
    await expect(ui.getByRole('checkbox')).toBeChecked()
    for (const [j, field] of ['transferConcurrentFiles', 'transferConcurrentPerPeer'].entries()) {
      const values = ui.locator('.resource-values > section').nth(j)
      await detailValue(values, 'Requested', expected[i][field].value)
      await detailValue(values, 'Effective', row.reply.preview.effective[field])
      await detailValue(ui, `Shared template: ${j === 0 ? 'Concurrent files' : 'Concurrent files per device'}`, expected[0][field].value)
    }
    await expect(ui.locator(':scope > dl > div > dt').filter({ hasText: /^Device override$/ }).locator('xpath=following-sibling::dd[1]')).toHaveText(i === 0 ? ['Inherit template', 'Inherit template'] : ['4', '2'])
    await ui.locator('details > summary').click()
    for (const [label, value] of [['Resource ID', member.selector.target.resourceId], ['Grant ID', member.selector.grantId], ['Current grant revision', member.selector.grantRevision], ['Operation ID', row.reply.preview.operationId], ['Base revision', row.reply.preview.baseRevision], ['Review revision', row.reply.preview.reviewRevision]]) await detailValue(ui, label, value)
    await ui.locator('details > summary').click()
  }
  const knownID = await review.getByRole('textbox', { name: 'Known run ID', exact: true }).inputValue()
  assert.equal(knownID, prepared.reviewId)
  const confirmation = review.getByRole('checkbox', { name: 'I reviewed all devices, exclusions, requested and effective settings, and this exact execution subset.', exact: true })
  const apply = review.getByRole('button', { name: 'Apply reviewed subset once', exact: true })
  await expect(confirmation).not.toBeChecked(); await expect(apply).toBeDisabled()
  expect(app.count()).toBe(1)
  await confirmation.check()
  await apply.click()
  const history = dialog.getByRole('region', { name: 'Historical group evidence', exact: true })
  await expect(history.locator('.group-evidence > li')).toHaveCount(2, { timeout: 30_000 })
  const run = await app.run()
  assert.equal(run.runId, knownID); assert.deepEqual(run.review, prepared.review)
  assert.equal(run.activity, 'idle'); assert.equal(run.localDurability, 'durable')
  assert.equal(run.summary.allApplied, true)
  for (const key of ['selected', 'executable', 'applied']) assert.equal(run.summary[key], 2)
  for (let i = 0; i < 2; i++) {
    const row = run.evidence.members[i], ui = history.locator('.group-evidence > li').nth(i)
    assert.equal(row.request.apply.operationId, prepared.review.rows[i].reply.preview.operationId)
    assert.equal(row.target.operationId, row.request.apply.operationId)
    assert.deepEqual(row.target.outcome, { status: 'applied', configuration: 'durable', accounting: 'succeeded', transfer: 'succeeded' })
    assert.equal(row.target.evidenceDurable, true); assert.equal(row.localDurability, 'durable'); assert.equal(row.status.state, 'not_queried')
    await detailValue(ui, 'Local dispatch', 'Observed')
    await detailValue(ui, 'Local publication durability', 'Durable')
    await detailValue(ui, 'Retained target outcome', 'Historically applied')
    await detailValue(ui, 'Terminal evidence is durable', 'Yes')
    await detailValue(ui, 'Latest status observation', 'Not queried')
  }
  await history.locator('details > summary').filter({ hasText: /^Historical executable-subset summary$/ }).click()
  for (const label of ['Selected devices', 'Executable devices', 'Applied']) await detailValue(history.locator('details').filter({ has: page.getByText('Historical executable-subset summary', { exact: true }) }), label, 2)
  await expect(history.getByRole('textbox', { name: 'Known run ID', exact: true })).toHaveValue(knownID)
  expect(app.count()).toBe(2)
  await history.getByRole('button', { name: 'Read local run status', exact: true }).click()
  await expect.poll(() => app.count()).toBe(3)
  await expect(history.getByRole('button', { name: 'Read local run status', exact: true })).toBeEnabled()
  assert.deepEqual(await app.status(), run)
  await page.keyboard.press('Escape')
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await open.click()
  await expect(page.getByRole('dialog', { name: 'Group transfer settings', exact: true })).toBeVisible()
  await expect(page.locator('.group-evidence > li')).toHaveCount(0)
  expect(app.count()).toBe(3)
  app.complete()
})
