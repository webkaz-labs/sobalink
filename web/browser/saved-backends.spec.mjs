import { test, expect, openDetailsSection } from './fixtures.mjs'

test.describe('saved definitions across supported backends', () => {
  test.use({ scenario: 'offline' })
  for (const backend of ['direct-lan', 'mixed']) {
    for (const locale of ['en', 'ja']) {
      test(`${locale}: ${backend} saved group reloads and reviews without activation`, async ({ page, app }) => {
        const ja = locale === 'ja'
        await app.appearance(locale, ja ? 'dark' : 'light')
        await page.getByRole('button', { name: ja ? '保存済みサービス' : 'Saved services', exact: true }).click()
        await page.getByRole('button', { name: ja ? '接続定義を保存' : 'Save a connection', exact: true }).click()
        const dialog = page.getByRole('dialog')
        const name = `${backend}-fixture`
        await dialog.getByLabel(ja ? '接続の名前' : 'Connection name', { exact: true }).fill(name)
        await dialog.getByRole('combobox', { name: new RegExp(ja ? '保存するネットワーク' : 'Saved network') }).selectOption(backend)
        await dialog.getByRole('textbox', { name: new RegExp(ja ? '正確な端末 ID' : 'Exact device IDs') }).fill('fixture-absent')
        await dialog.getByLabel(ja ? 'ポート' : 'Ports', { exact: true }).fill('8080')
        await dialog.getByRole('button', { name: ja ? '停止状態の定義を確認' : 'Review stopped definition', exact: true }).click()
        // The reload reads persisted definitions. Runtime /api/state services
        // deliberately omit backend, so verify that field in profile.export.
        const savedProfile = page.waitForResponse(response => new URL(response.url()).pathname === '/api/command' && response.request().postDataJSON()?.name === 'profile.export')
        await dialog.getByRole('button', { name: ja ? '確認した定義を保存' : 'Save reviewed definition', exact: true }).click()
        const exported = await (await savedProfile).json()
        expect(exported.ok).toBe(true)
        expect(exported.result.disabled).toBe(true)
        expect(exported.result.profile.services).toEqual([expect.objectContaining({ name, backend, direction: 'forward', network: 'tcp', peerId: 'fixture-absent', ports: '8080' })])
        const savedID = exported.result.profile.services[0].id
        await app.expectState(state => state.services.some(service => service.id === savedID && service.name === name && service.status === 'saved'))
        await page.getByRole('button', { name: ja ? 'すべて選択' : 'Select all', exact: true }).click()
        const groupEditor = page.locator('dialog details').filter({ has: page.locator('summary', { hasText: ja ? '選択をグループとして保存' : 'Save this selection as a group' }) })
        await openDetailsSection(groupEditor)
        await groupEditor.getByRole('textbox', { name: ja ? 'グループ名' : 'Group name', exact: true }).fill('saved-fixture')
        await groupEditor.getByRole('button', { name: ja ? 'グループを保存' : 'Save group', exact: true }).click()
        await expect(dialog.getByText(ja ? 'サービスを開始せずにグループを保存しました' : 'Group saved without starting services', { exact: true })).toBeVisible()
        await page.getByRole('combobox', { name: ja ? '保存済みグループ' : 'Saved group', exact: true }).selectOption('saved-fixture')
        await page.getByRole('button', { name: ja ? '開始内容を確認' : 'Review start', exact: true }).click()
        const review = page.getByRole('region', { name: ja ? '選択したサービスを確認' : 'Review selected services', exact: true })
        await expect(review).toContainText(`${backend} · TCP`)
        await expect(review).toContainText('fixture-absent')
        for (const viewport of [{ width: 1440, height: 960, label: 'desktop' }, { width: 390, height: 844, label: '390' }]) {
          await page.setViewportSize({ width: viewport.width, height: viewport.height })
          await review.getByRole('heading', { name: ja ? '選択したサービスを確認' : 'Review selected services', exact: true }).scrollIntoViewIfNeeded()
          await app.capture(`saved-${backend}-${locale}-${viewport.label}`)
          // This dialog also has selection and footer actions. Capture the
          // reviewed start/cancel controls, not an arbitrary modal action row.
          await review.locator(':scope > .modal-actions').scrollIntoViewIfNeeded()
          await app.capture(`saved-${backend}-${locale}-${viewport.label}-actions`)
        }
        await review.getByRole('button', { name: ja ? 'キャンセル' : 'Cancel', exact: true }).click()
        await expect(review).toHaveCount(0)
        await page.keyboard.press('Escape')
        await expect(page.getByRole('dialog')).toHaveCount(0)
        await page.getByRole('button', { name: ja ? '保存済みサービス' : 'Saved services', exact: true }).click()
        await expect(page.locator('.definition-entry')).toContainText(name)
        expect(await app.count('services.start')).toBe(0)
        expect(await app.count('service.connect')).toBe(0)
        expect(await app.count('network.configure')).toBe(0)
      })
    }
  }
})
