import assert from 'node:assert/strict'
import { lstat, readFile, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { test as base, expect } from '@playwright/test'
import { CAPTURE_FORBIDDEN_SELECTOR, PRIVATE_VALUE_SELECTOR, privateControlsAreEmpty, safeArtifactName } from './fixture-safety.mjs'
export { expect }

async function privateJSON(root, name, limit) {
  const path = join(root, name), info = await lstat(path)
  assert.ok(info.isFile() && !info.isSymbolicLink() && info.uid === process.getuid() && (info.mode & 0o077) === 0 && info.size > 0 && info.size <= limit, 'Protected bounded fixture input required')
  return JSON.parse(await readFile(path, 'utf8'))
}
function exactKeys(value, keys) {
  return value && typeof value === 'object' && !Array.isArray(value) && Object.keys(value).length === keys.length && keys.every(key => Object.hasOwn(value, key))
}
export const test = base.extend({
  app: async ({ page, context }, use, testInfo) => {
    const root = process.env.SOBA_RESOURCE_BROWSER_PRIVATE_ROOT
    const info = await lstat(root)
    assert.ok(info.isDirectory() && !info.isSymbolicLink() && info.uid === process.getuid() && (info.mode & 0o077) === 0, 'Owned private fixture root required')
    for (const name of ['screenshot', 'trace', 'video']) assert.equal(testInfo.project.use[name], 'off', 'Automatic capture forbidden')
    assert.equal(testInfo.project.use.serviceWorkers, 'block', 'Service workers forbidden')
    let session
    try {
      session = await privateJSON(root, 'session.json', 16_384)
      assert.ok(exactKeys(session, ['schema', 'url', 'code', 'processID', 'assets']) && session.schema === 1)
      const url = new URL(session.url)
      assert.ok(url.protocol === 'http:' && url.hostname === '127.0.0.1' && url.port && !url.username && !url.password && !url.search && !url.hash && url.pathname === '/' && session.url === url.origin)
      assert.ok(typeof session.code === 'string' && /^[A-Za-z0-9_-]{32}$/.test(session.code))
      assert.ok(typeof session.processID === 'string' && /^[a-f0-9]{64}$/.test(session.processID))
      assert.ok(Array.isArray(session.assets) && session.assets.length > 0 && session.assets.length <= 32 && session.assets.every(path => typeof path === 'string' && /^\/(assets\/[a-zA-Z0-9_.-]+|favicon\.svg|png-worker-manifest\.json)$/.test(path)))
    } catch { throw Error('Private fixture descriptor was invalid; contents withheld') }
    const observations = { schema: 1, requests: 0, stateRequests: 0, stateReady: 0, loginRequests: 0, listRequests: 0, listResponses: 0, snapshots: 0, snapshotResponses: 0, blocked: 0, errors: 0, captures: 0, completed: false, requestsJoined: false }
    const pending = new Set(), inflight = new Set(), commands = new Map()
    let catalog, localID = '', snapshotRequest, authenticated = false, closing = false, faviconReads = 0
    const track = task => { pending.add(task); task.finally(() => pending.delete(task)).catch(() => {}) }
    const fail = () => { observations.errors++ }
    const observeRequest = request => {
      inflight.add(request)
      observations.requests++
      if (observations.requests > 256) fail()
      try {
        const url = new URL(request.url()), method = request.method()
        if (url.origin !== session.url) { fail(); return }
        if (url.pathname === '/favicon.ico') {
          if (!['GET', 'HEAD'].includes(method) || ++faviconReads > 1) fail()
        } else if (url.pathname === '/api/state') {
          observations.stateRequests++
          if (method !== 'GET' || observations.stateRequests > 96) fail()
        } else if (url.pathname === '/api/session') {
          observations.loginRequests++
          if (method !== 'POST' || observations.loginRequests > 1) fail()
          // Do not read the login request body: it contains the real code.
        } else if (url.pathname === '/api/command') {
          const raw = request.postData()
          if (method !== 'POST' || !raw || Buffer.byteLength(raw) > 4096) throw Error()
          const command = JSON.parse(raw)
          if (!exactKeys(command, ['requestId', 'name', 'payload']) || typeof command.requestId !== 'string' || command.requestId.length < 1 || command.requestId.length > 128) throw Error()
          if (command.name === 'resource.list') {
            observations.listRequests++
            if (!exactKeys(command.payload, []) || observations.listRequests > 2) throw Error()
          } else if (command.name === 'resource.catalog.snapshot') {
            observations.snapshots++
            if (observations.snapshots !== 1 || !exactKeys(command.payload, ['schemaVersion', 'sources']) || command.payload.schemaVersion !== 1 || !Array.isArray(command.payload.sources) || command.payload.sources.length !== 3) throw Error()
            const [settings, service, transfers] = command.payload.sources
            if (!exactKeys(settings, ['kind', 'target']) || settings.kind !== 'local_settings' || !exactKeys(settings.target, ['schemaVersion', 'resourceId']) || settings.target.schemaVersion !== 1 || settings.target.resourceId !== localID || !exactKeys(service, ['kind']) || service.kind !== 'local_service' || !exactKeys(transfers, ['kind']) || transfers.kind !== 'transfer_activity') throw Error()
            snapshotRequest = command.payload
          } else throw Error()
          commands.set(request, command.name)
        }
      } catch { fail() }
    }
    const observeResponse = response => {
      track((async () => {
        try {
          const request = response.request(), url = new URL(response.url())
          if (url.pathname === '/api/state') {
            if (response.status() === 200) observations.stateReady++
            else if (response.status() !== 401 || authenticated) fail()
          }
          const command = commands.get(request)
          if (!command) return
          if (response.status() !== 200) throw Error()
          const body = await response.body()
          if (body.length === 0 || body.length > 1_048_576) throw Error()
          const envelope = JSON.parse(body.toString('utf8'))
          if (!exactKeys(envelope, ['ok', 'result']) || envelope.ok !== true) throw Error()
          if (command === 'resource.list') {
            observations.listResponses++
            const result = envelope.result
            if (!exactKeys(result, ['schemaVersion', 'resources']) || result.schemaVersion !== 1 || !Array.isArray(result.resources) || result.resources.length !== 1 || !/^[a-f0-9]{32}$/.test(result.resources[0].resourceId)) throw Error()
            const id = result.resources[0].resourceId
            if (localID && localID !== id) throw Error()
            localID = id
          } else {
            observations.snapshotResponses++
            if (catalog) throw Error()
            catalog = envelope.result
            await writeFile(join(root, 'catalog-result.json'), JSON.stringify(catalog), { flag: 'wx', mode: 0o600 })
            await writeFile(join(root, 'catalog-request.json'), JSON.stringify(snapshotRequest), { flag: 'wx', mode: 0o600 })
          }
        } catch { fail() }
      })())
    }
    page.on('request', observeRequest)
    page.on('requestfinished', request => inflight.delete(request))
    page.on('requestfailed', request => {
      inflight.delete(request)
      const expectedClose = closing && request.method() === 'GET' && new URL(request.url()).pathname === '/api/state'
      if (!expectedClose) fail()
    })
    page.on('response', observeResponse)
    page.on('pageerror', fail)
    page.on('console', entry => { if (entry.type() === 'error' && /content security policy|refused to/i.test(entry.text())) fail() })
    context.on('page', other => { if (other !== page) { fail(); void other.close().catch(fail) } })
    page.on('download', download => { fail(); void download.cancel().catch(fail) })
    await context.routeWebSocket('**/*', socket => { observations.blocked++; socket.close() })
    await context.route('**/*', async route => {
      try {
        const request = route.request(), url = new URL(request.url())
        const read = request.method() === 'GET' || request.method() === 'HEAD'
        const pathOK = read && (url.pathname === '/' || url.pathname === '/favicon.ico' || session.assets.includes(url.pathname) || url.pathname === '/api/state') || request.method() === 'POST' && ['/api/session', '/api/command'].includes(url.pathname)
        if (url.origin !== session.url || url.protocol !== 'http:' || url.hostname !== '127.0.0.1' || url.username || url.password || url.search || url.hash || !pathOK) throw Error()
        if (url.pathname === '/api/command') {
          // An unexpected UI command fails and is blocked before reaching a
          // state-changing route. This is not a passing no-admission oracle.
          const raw = request.postData()
          if (!raw || Buffer.byteLength(raw) > 4096) throw Error()
          const command = JSON.parse(raw)
          if (!exactKeys(command, ['requestId', 'name', 'payload']) || !['resource.list', 'resource.catalog.snapshot'].includes(command.name) || typeof command.requestId !== 'string' || command.requestId.length < 1 || command.requestId.length > 128) throw Error()
          if (command.name === 'resource.list' && !exactKeys(command.payload, [])) throw Error()
          if (command.name === 'resource.catalog.snapshot') {
            const expected = { schemaVersion: 1, sources: [{ kind: 'local_settings', target: { schemaVersion: 1, resourceId: localID } }, { kind: 'local_service' }, { kind: 'transfer_activity' }] }
            if (JSON.stringify(command.payload) !== JSON.stringify(expected)) throw Error()
          }
        }
        await route.continue()
      } catch { observations.blocked++; await route.abort('blockedbyclient').catch(fail) }
    })
    const audit = () => {
      assert.equal(observations.errors, 0, 'Private request or browser audit failed')
      assert.equal(observations.blocked, 0, 'Unexpected browser destination or route')
    }
    try {
      try {
        const response = await page.goto(session.url)
        assert.ok(response && response.status() === 200 && (response.headers()['content-security-policy'] || '').includes("script-src 'self'"))
        await page.getByLabel('Local access code', { exact: true }).fill(session.code)
        await page.getByRole('button', { name: 'Open sobalink', exact: true }).click()
        await page.locator('.workspace').waitFor()
        await expect(page.locator('.login-panel')).toHaveCount(0)
        authenticated = true
        session.code = ''
      } catch { throw Error('Real local browser authentication failed; private details withheld') }
      await use({
        count(name) { audit(); return name === 'resource.list' ? observations.listRequests : name === 'resource.catalog.snapshot' ? observations.snapshots : 0 },
        stateReady() { audit(); return observations.stateReady },
        localID() { audit(); assert.ok(localID); return localID },
        async catalog() { await Promise.all([...pending]); audit(); assert.ok(catalog); return catalog },
        async captureRegion(name, locator) {
          audit(); assert.ok(authenticated && observations.captures < 4)
          await expect(page.locator(CAPTURE_FORBIDDEN_SELECTOR)).toHaveCount(0)
          assert.ok(await page.locator(PRIVATE_VALUE_SELECTOR).evaluateAll(privateControlsAreEmpty), 'Private controls forbid capture')
          const text = await locator.innerText()
          assert.ok(!text.includes(localID) && !text.includes(session.processID) && !/[a-f0-9]{32,64}/.test(text), 'Selector-free region required')
          const output = join(root, 'captures', `${safeArtifactName(name)}.png`)
          await locator.screenshot({ path: output })
          const info = await lstat(output)
          assert.ok(info.isFile() && !info.isSymbolicLink() && info.size > 0 && info.size <= 4 * 1024 * 1024, 'Bounded authentic capture required')
          observations.captures++
        },
        complete() { audit(); observations.completed = true },
      })
    } finally {
      // Drain response readers, close the page to stop production polling, then
      // drain routes/readers before context/browser teardown and Go Core.Close.
      await Promise.all([...pending])
      closing = true
      await page.close().catch(fail)
      await context.unrouteAll({ behavior: 'wait' }).catch(fail)
      await Promise.all([...pending])
      observations.requestsJoined = inflight.size === 0 && pending.size === 0
      if (!observations.requestsJoined) fail()
      session.code = ''
      await writeFile(join(root, 'http-observations.json'), JSON.stringify(observations), { flag: 'wx', mode: 0o600 })
      assert.ok(observations.completed && observations.requestsJoined && observations.loginRequests === 1 && observations.listRequests === 2 && observations.listResponses === 2 && observations.snapshots === 1 && observations.snapshotResponses === 1 && observations.stateReady > 0 && observations.captures === 2, 'Exact B1 browser observations incomplete')
      audit()
    }
  },
})
