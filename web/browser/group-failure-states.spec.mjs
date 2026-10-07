import { test, expect } from './fixtures.mjs'

// UI → HTTP boundary evidence only. Authentication and the surrounding page use
// the isolated Go fixture; the reviewed group, mutation error and member states
// below are synthetic. These cases do not establish a real Core rollback.
for (const locale of ['en', 'ja']) {
  test(`${locale}: uncertain group result preserves reviewed identities and observed states`, async ({ page, app }) => {
    const ja = locale === 'ja'
    await app.appearance(locale, ja ? 'dark' : 'light')
    const services = ['running-fixture', 'prepared-fixture', 'failed-fixture', 'later-fixture'].map((name, index) => ({ id: `group-fixture-${index}`, name, direction: index === 0 ? 'share' : 'forward', backend: 'tailnet', network: 'tcp', ports: String(8000 + index), peerId: 'fixture-peer', peerIds: ['fixture-peer'], lifetime: 'finite', ttlSeconds: 60, loopbackHost: '127.0.0.1', discoverable: false, purpose: 'web' }))
    const before = services.map((service, index) => ({ ...service, status: index === 0 ? 'active' : 'saved' }))
    const after = services.map((service, index) => ({ ...service, status: ['active', 'stopped', 'failed', 'saved'][index] }))
    let attempted = false
    let stateUnavailable = false
    let starts = 0
    await page.route('**/api/state', async route => {
      if (stateUnavailable) return route.abort('failed')
      const response = await route.fetch()
      const state = await response.json()
      const observed = attempted ? after : before
      await route.fulfill({ response, json: { ...state, shares: observed.slice(0, 1), services: observed.slice(1) } })
    })
    await page.route('**/api/command', async route => {
      const command = route.request().postDataJSON()
      if (command.name === 'profile.export') return route.fulfill({ json: { ok: true, result: { disabled: true, revision: 'a'.repeat(64), profile: { version: 1, services, groups: [{ name: 'group-fixture', serviceIds: services.map(service => service.id) }] } } } })
      if (command.name === 'service.selection') return route.fulfill({ json: { ok: true, result: { services, states: before, revision: 'b'.repeat(64), group: 'group-fixture', ready: false, application: 'unverified' } } })
      if (command.name === 'services.start') {
        starts++; attempted = true
        return route.fulfill({ status: 409, json: { code: 'command_failed', error: 'Synthetic group failure: newly started members were stopped' } })
      }
      return route.continue()
    })
    await page.getByRole('button', { name: ja ? '保存済みサービス' : 'Saved services', exact: true }).click()
    await page.getByRole('combobox', { name: ja ? '保存済みグループ' : 'Saved group', exact: true }).selectOption('group-fixture')
    await page.getByRole('button', { name: ja ? '開始内容を確認' : 'Review start', exact: true }).click()
    await page.getByRole('button', { name: ja ? '確認したサービスを開始' : 'Start reviewed services', exact: true }).click()
    const region = page.getByRole('region', { name: ja ? '開始結果を確認できません' : 'Start not confirmed', exact: true })
    const refresh = region.getByRole('button', { name: ja ? '各サービスの状態を更新' : 'Refresh member states', exact: true })
    await expect(refresh).toBeEnabled()
    for (const [index, status] of (ja ? ['有効', '停止済み', '失敗', '設定を保存済み'] : ['Active', 'Stopped', 'Failed', 'Saved configuration']).entries()) {
      const member = region.getByRole('article', { name: services[index].name, exact: true })
      await expect(member).toContainText(services[index].id)
      await expect(member.getByRole('definition').last()).toHaveText(status)
    }
    await expect(page.locator('dialog [role=alert]')).toContainText('Synthetic group failure')
    for (const viewport of [{ width: 1440, height: 960, label: 'desktop' }, { width: 390, height: 844, label: '390' }]) {
      await page.setViewportSize({ width: viewport.width, height: viewport.height })
      await region.getByRole('heading').scrollIntoViewIfNeeded()
      await app.capture(`group-failure-${locale}-${viewport.label}`)
      await region.locator(':scope > .modal-actions').scrollIntoViewIfNeeded()
      await app.capture(`group-failure-${locale}-${viewport.label}-actions`)
    }
    stateUnavailable = true
    await refresh.click()
    await expect(region).toContainText(ja ? '各サービスの現在の状態は不明' : 'Current member states are unknown')
    for (const service of services) await expect(region.getByRole('article', { name: service.name, exact: true }).getByRole('definition').last()).toHaveText(ja ? '不明' : 'Unknown')
    stateUnavailable = false
    await refresh.click()
    await expect(region.getByRole('article', { name: 'running-fixture', exact: true }).getByRole('definition').last()).toHaveText(ja ? '有効' : 'Active')
    expect(starts).toBe(1)
    expect(await app.count('services.start')).toBe(1)
    expect(await app.count('services.stop')).toBe(0)
    await region.getByRole('button', { name: ja ? '結果を閉じる' : 'Dismiss result', exact: true }).click()
    await expect(region).toHaveCount(0)
    await expect(page.getByRole('button', { name: ja ? '確認したサービスを開始' : 'Start reviewed services', exact: true })).toHaveCount(0)
    await page.keyboard.press('Escape')
    await expect(page.getByRole('dialog')).toHaveCount(0)
  })
}
