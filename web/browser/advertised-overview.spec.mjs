import { test, expect } from './fixtures.mjs'

for (const locale of ['en', 'ja']) {
  const ja = locale === 'ja'
  test(`${locale}: advertised overview reviews one exact service, cancels, and starts once at compact widths`, async ({ page, app }) => {
    await app.appearance(locale, ja ? 'dark' : 'light')
    await app.openPeer('services'); await app.openDetails()
    await page.locator('.details-panel').getByRole('button', { name: ja ? '共有サービスを確認' : 'Check shared services', exact: true }).click()
    await app.expectState(state => state.availableServices.some(service => service.peerId === 'fixture-studio' && service.ports === '8080' && service.revision))
    await app.closeDetails()
    const starts = await app.count('service.connect'), trusts = await app.count('peer.trust')
    const row = page.locator('.available-services li').filter({ hasText: 'TCP 8080' })
    const review = row.getByRole('button', { name: ja ? /^接続を確認:/ : /^Review connection:/ })
    await expect(review).toHaveCount(1)
    const dialog = page.getByRole('dialog')
    for (const width of [375, 390, 844]) {
      await page.setViewportSize({ width, height: width === 844 ? 390 : 844 })
      await review.scrollIntoViewIfNeeded()
      await app.capture(`advertised-overview-${locale}-${width}`)
      await review.focus(); await page.keyboard.press('Enter')
      const selection = dialog.getByRole('combobox', { name: ja ? '利用できるサービス' : 'Available services', exact: true })
      // Verify the selected grant against the real Core response without
      // logging the identity, session, or advertised revision.
      expect(await selection.evaluate(async element => {
        const state = await (await fetch('/api/state')).json()
        return state.availableServices.some(service => service.id === element.value && service.peerId === 'fixture-studio' && service.ports === '8080')
      })).toBe(true)
      await expect(dialog.locator('input[aria-describedby=port-help]')).toHaveValue('8080')
      await expect(dialog.locator('input[aria-describedby=port-help]')).toBeDisabled()
      await dialog.locator('input[inputmode=numeric]').fill(String(app.localServicePort))
      await app.captureForm(`advertised-overview-review-${locale}-${width}`)
      await dialog.getByRole('button', { name: ja ? 'キャンセル' : 'Cancel', exact: true }).click()
      await expect(dialog).toHaveCount(0)
      await expect(review).toBeFocused()
      expect(await app.count('service.connect')).toBe(starts)
    }
    await review.click()
    await page.goBack()
    await expect(dialog).toHaveCount(0)
    await page.goForward()
    await expect(page.locator('.device-overview')).toBeVisible()
    await expect(dialog).toHaveCount(0)
    await review.click()
    await dialog.locator('input[required][maxlength="64"]').fill('overview-reviewed-service')
    await expect(dialog.locator('input[inputmode=numeric]')).toHaveValue(String(app.localServicePort))
    await dialog.locator('button[type=submit]').click()
    await app.expectState(state => state.services.some(service => service.name === 'overview-reviewed-service' && service.peerId === 'fixture-studio' && service.ports === '8080' && service.localPort === app.localServicePort && service.status === 'active'))
    await expect(dialog).toHaveCount(0)
    expect(await app.count('service.connect')).toBe(starts + 1)
    expect(await app.count('peer.trust')).toBe(trusts)
  })

  // Only observations are injected here. This is presentation coverage, not
  // evidence of a real transport failure or recovery on physical devices.
  test(`${locale}: failed snapshot refresh hides previous direct routes and recovers without changing permission`, async ({ page, app }) => {
    await app.appearance(locale, ja ? 'dark' : 'light')
    let failed = false
    await page.route('**/api/state', async route => {
      if (failed) { await route.abort('failed'); return }
      const response = await route.fetch(), state = await response.json()
      await route.fulfill({ response, json: { ...state, peers: state.peers.map(peer => ({ ...peer, online: true, path: 'direct' })) } })
    })
    await page.reload(); await app.openPeer('services')
    const header = page.locator('.conversation-header')
    await expect(header).toContainText(ja ? '直接接続' : 'Direct')
    const trustCommands = await app.count('peer.trust')
    failed = true
    await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')))
    await expect(page.locator('.stale-banner')).toBeVisible()
    await expect(header).toContainText(ja ? '経路が未確認' : 'Path unknown')
    await expect(header).not.toContainText(ja ? '直接接続' : 'Direct')
    await page.locator('.network-map-button').click()
    await expect(page.locator('.network-graph-edge[data-path=direct], .network-graph-line.is-reported, .network-graph-route-glyph')).toHaveCount(0)
    await expect(page.locator('.network-graph > [role=status]')).toBeVisible()
    await page.setViewportSize({ width: 390, height: 844 })
    await app.capture(`stale-route-observation-${locale}-390`)
    failed = false
    await page.locator('.stale-banner button').click()
    await expect(page.locator('.stale-banner')).toHaveCount(0)
    await expect(page.locator('.network-graph-edge[data-path=direct]').first()).toBeVisible()
    expect(await app.count('peer.trust')).toBe(trustCommands)
  })
}
