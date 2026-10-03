import { test, expect } from './fixtures.mjs'

// Only the existing fictional in-process peers are selected. These cases never
// enroll an account, start a proxy, or claim application compatibility.
for (const locale of ['en', 'ja']) {
  test(`${locale}: exact proxy review, private-input cancellation and no automatic start`, async ({ page, app }) => {
    const ja = locale === 'ja'
    await app.appearance(locale, ja ? 'dark' : 'light')
    await page.locator('.app-header .header-actions button.icon-button').click()
    await page.getByRole('button', { name: ja ? '高度な接続' : 'Advanced connections', exact: true }).click()
    const dialog = page.getByRole('dialog')
    await expect(dialog).toContainText(ja ? '実行中のプロキシはありません' : 'No running proxies')
    await dialog.getByLabel(ja ? 'プロキシ名' : 'Proxy name', { exact: true }).fill('fixture-reviewed-proxy')
    await dialog.getByLabel(ja ? '接続先の端末 1' : 'Target device 1', { exact: true }).selectOption('fixture-studio')
    await dialog.getByLabel(ja ? 'ローカルのプロキシポート' : 'Local proxy port', { exact: true }).fill(String(app.localServicePort))
    await dialog.getByRole('button', { name: ja ? '正確な範囲を確認' : 'Review exact scope', exact: true }).click()
    await expect(dialog.locator('.proxy-review')).toContainText(`127.0.0.1:${app.localServicePort}`)
    await expect(dialog.locator('.proxy-review')).toContainText('fixture-studio')
    await expect(dialog.locator('.proxy-review')).toContainText('TCP 443')
    await expect(dialog.locator('.proxy-credentials')).toHaveCount(0)
    expect(await app.count('proxy.start')).toBe(0)
    await app.capture(`proxy-review-${locale}-desktop`)
    await page.setViewportSize({ width: 390, height: 844 })
    await app.capture(`proxy-review-${locale}-390`)
    await dialog.getByRole('button', { name: ja ? '認証情報の入力へ' : 'Continue to authentication', exact: true }).click()
    // Fictional values only. The capture gate rejects this entire form, even
    // when inputs are empty, and traces/automatic error snapshots stay off.
    await dialog.getByLabel(ja ? '実行中のユーザー名' : 'Runtime username', { exact: true }).fill('fictional-proxy-user')
    await dialog.getByLabel(ja ? '実行中のパスワード' : 'Runtime password', { exact: true }).fill('fictional-proxy-password')
    await dialog.getByRole('button', { name: ja ? '範囲を編集' : 'Edit scope', exact: true }).click()
    await expect(dialog.locator('.proxy-credentials')).toHaveCount(0)
    await dialog.getByRole('button', { name: ja ? '正確な範囲を確認' : 'Review exact scope', exact: true }).click()
    await dialog.getByRole('button', { name: ja ? '認証情報の入力へ' : 'Continue to authentication', exact: true }).click()
    await expect(dialog.locator('[data-private=proxy-credential]')).toHaveCount(2)
    const cleared = await dialog.locator('[data-private=proxy-credential]').evaluateAll(fields => fields.every(field => field.value === ''))
    expect(cleared).toBe(true)
    await page.keyboard.press('Escape')
    await expect(page.locator('dialog[open]')).toHaveCount(0)
    expect(await app.count('proxy.start')).toBe(0)
    await app.capture(`proxy-cancelled-${locale}-390`)
  })
}

test('explicit TCP check records transport failure and preserves runtime history after stop', async ({ page, app }) => {
  await app.openPeer('services')
  await page.getByRole('button', { name: 'Connect to a service', exact: true }).click()
  await page.locator('dialog input[required][maxlength="64"]').fill('fixture-diagnostic-service')
  await page.locator('dialog input[aria-describedby=port-help]').fill('8080')
  await page.locator('dialog input[inputmode=numeric]').fill(String(app.localServicePort))
  await page.locator('dialog button[type=submit]').click()
  await app.expectState(state => state.services.some(service => service.name === 'fixture-diagnostic-service' && service.status === 'active'))
  await app.closeDetails()
  const row = page.locator('.device-overview .service-row').filter({ has: page.locator('strong', { hasText: /^fixture-diagnostic-service$/ }) })
  await row.locator('.service-diagnostics summary').click()
  expect(await app.count('diagnostics.run')).toBe(0)
  await row.getByRole('button', { name: 'Check TCP connection', exact: true }).click()
  await app.expectState(state => state.services.some(service => service.name === 'fixture-diagnostic-service' && service.diagnostic?.code === 'tcp_unreachable' && service.diagnostic?.application === 'unverified' && service.lastFailure?.code === 'tcp_unreachable'))
  await expect(row.locator('.diagnostic-result[role=status]')).toContainText('TCP transport: Unreachable')
  await expect(row.locator('.diagnostic-result[role=status]')).toContainText('Application: unverified')
  await app.capture('tcp-diagnostic-failure-en-desktop')
  await row.getByRole('button', { name: 'Stop', exact: true }).click()
  await app.expectState(state => state.services.some(service => service.name === 'fixture-diagnostic-service' && service.status === 'stopped' && service.lastFailure?.code === 'tcp_unreachable'))
  await expect(row.getByRole('button', { name: 'Check TCP connection', exact: true })).toHaveCount(0)
  await expect(row.getByRole('heading', { name: 'Last runtime failure', exact: true })).toBeVisible()
  expect(await app.count('diagnostics.run')).toBe(1)
})

for (const locale of ['en', 'ja']) {
  test(`${locale}: a confirmed discovery reply preserves metadata and manual service use`, async ({ page, app }) => {
    const ja = locale === 'ja'
    await app.appearance(locale, ja ? 'dark' : 'light')
    await app.openPeer('services')
    await app.openDetails()
    const observation = page.locator('.details-panel .discovery-observation')
    await expect(observation).toContainText(ja ? 'サービス情報を確認済み' : 'Service information confirmed')
    await app.expectState(state => state.peers.some(peer => peer.id === 'fixture-studio' && peer.discovery?.state === 'confirmed' && peer.discovery.services > 0))
    const before = await app.count('discovery.refresh')
    await observation.getByRole('button', { name: ja ? '共有サービスを確認' : 'Check shared services', exact: true }).click()
    await expect.poll(() => app.count('discovery.refresh')).toBe(before + 1)
    await app.capture(`discovery-confirmed-${locale}-desktop`)
    await app.closeDetails()
    await page.getByRole('button', { name: ja ? 'サービスに接続' : 'Connect to a service', exact: true }).click()
    await expect(page.locator('dialog .discovery-observation')).toContainText(ja ? 'サービス情報を確認済み' : 'Service information confirmed')
    await expect(page.locator('dialog input[aria-describedby=port-help]')).toBeEnabled()
    const selection = page.getByRole('dialog').getByRole('combobox', { name: ja ? '利用できるサービス' : 'Available services', exact: true })
    const advertised = selection.getByRole('option').filter({ hasText: /TCP 8080/ })
    await expect(advertised).toHaveCount(1)
    await selection.selectOption(await advertised.getAttribute('value'))
    await expect(page.locator('dialog input[aria-describedby=port-help]')).toBeDisabled()
    const review = page.locator('dialog .advertised-review')
    for (const width of [1180, 390, 375]) {
      await page.setViewportSize({ width, height: width <= 390 ? 844 : 960 })
      await review.scrollIntoViewIfNeeded()
      await expect.poll(() => review.evaluate(element => {
        const bounds = element.getBoundingClientRect()
        const dialog = element.closest('dialog').getBoundingClientRect()
        const children = [...element.children]
        return bounds.left >= dialog.left && bounds.right <= dialog.right && element.scrollWidth <= element.clientWidth + 1 && children.every((child, index) => {
          const box = child.getBoundingClientRect()
          const previous = index ? children[index - 1].getBoundingClientRect() : null
          return box.width > 0 && box.left >= bounds.left - 1 && box.right <= bounds.right + 1 && child.scrollWidth <= child.clientWidth + 1 && (!previous || box.top >= previous.bottom - 1)
        })
      }), { message: 'Advertised metadata and its review action must stack without clipping inside the dialog' }).toBe(true)
      await expect(review.getByRole('button', { name: ja ? '公開情報を更新して確認' : 'Refresh advertised review', exact: true })).toBeVisible()
      await app.capture(`advertised-review-${locale}-${width}`)
    }
    expect(await app.count('service.connect')).toBe(0)
    await page.keyboard.press('Escape')
  })
}
