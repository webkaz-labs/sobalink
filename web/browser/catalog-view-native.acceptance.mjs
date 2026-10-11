import assert from 'node:assert/strict'
import { test, expect } from './catalog-native-fixtures.mjs'

function commandResponse(name, origin) {
  const expectedOrigin = new URL(origin).origin
  return response => {
    if (new URL(response.url()).origin !== expectedOrigin || new URL(response.url()).pathname !== '/api/command') return false
    try { return JSON.parse(response.request().postData()).name === name } catch { return false }
  }
}

test('catalog-native-separate-agents-view-review-connect-en-ja', async ({ page, app, request }) => {
  const { session } = app
  if (await page.locator('html').getAttribute('lang') !== 'en') await page.locator('.locale-toggle').click()
  await expect(page.locator('html')).toHaveAttribute('lang', 'en')
  await page.locator('.device-row').filter({ hasText: session.peerName }).click()
  await page.locator('.device-sections').getByRole('button', { name: 'Services', exact: true }).click()
  const catalog = page.getByRole('region', { name: 'Permitted shared services', exact: true })
  await expect(catalog).toContainText('Permission to view the list does not grant connection permission.')
  const selector = session.request.request
  await catalog.getByLabel('Resource ID', { exact: true }).fill(selector.target.resourceId)
  await catalog.getByLabel('View grant ID', { exact: true }).fill(selector.grantId)
  await catalog.getByLabel('Grant revision', { exact: true }).fill(String(selector.grantRevision))
  const listed = page.waitForResponse(commandResponse('resource.catalog.view.list', session.viewer.url))
  await catalog.getByRole('button', { name: 'Fetch services', exact: true }).click()
  assert.equal((await listed).status(), 200)
  const selected = catalog.locator('li').filter({ hasText: session.serviceId })
  await expect(selected.getByRole('button', { name: 'Review connection', exact: true })).toBeEnabled()
  if (session.viewOnlyServiceId) {
    const viewOnly = catalog.locator('li').filter({ hasText: session.viewOnlyServiceId })
    await expect(viewOnly).toContainText('View only; connection is not permitted')
    await expect(viewOnly.getByRole('button', { name: 'Review connection', exact: true })).toBeDisabled()
  }
  assert.equal(app.counts().connects, 0)
  await page.locator('.locale-toggle').click()
  await expect(page.locator('html')).toHaveAttribute('lang', 'ja')
  await page.setViewportSize({ width: 390, height: 844 })
  const japanese = page.getByRole('region', { name: '許可された共有一覧', exact: true })
  await expect(japanese).toContainText('閲覧許可は接続許可を与えません。')
  await japanese.locator('li').filter({ hasText: session.serviceId }).getByRole('button', { name: '接続をレビュー', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'サービスに接続', exact: true })
  await expect(dialog).toContainText('提供元・世代・接続許可を開始前に再確認')
  await dialog.getByLabel('接続の名前', { exact: false }).fill('browser-reviewed')
  await dialog.getByLabel('ローカルの開始ポート', { exact: true }).fill(String(session.localPort))
  const started = page.waitForResponse(commandResponse('service.connect', session.viewer.url))
  await dialog.getByRole('button', { name: '接続を開始', exact: true }).click()
  const response = await started
  assert.equal(response.status(), 200)
  const result = await response.json()
  assert.ok(result.ok === true && result.result && typeof result.result.endpoint === 'string', 'Actual service start must succeed')
  const endpoint = new URL(`http://${result.result.endpoint}`)
  assert.ok(endpoint.hostname === '127.0.0.1' && Number(endpoint.port) === session.localPort, 'Actual forward must use reviewed local endpoint')
  const application = await request.get(endpoint.href)
  assert.equal(application.status(), 200)
  assert.ok(await application.text() === session.expectedContent, 'Separate-agent application bytes must match')
  await expect(page.getByRole('dialog')).toHaveCount(0)
  assert.deepEqual(app.counts(), { lists: 1, connects: 1, errors: 0 })
})
