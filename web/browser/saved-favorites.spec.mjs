import { test, expect, openDetailsSection } from './fixtures.mjs'

// Uses only the isolated fixture's fictional stopped definitions. This exercises
// real preference persistence after Core and Web are integrated, not transport.
for (const locale of ['en', 'ja']) {
  test(`${locale}: inert favorites retain full review and fit narrow layouts`, async ({ page, app }) => {
    const ja = locale === 'ja'
    await app.appearance(locale, ja ? 'dark' : 'light')
    await page.getByRole('button', { name: ja ? '保存済みサービス' : 'Saved services', exact: true }).click()
    const dialog = page.getByRole('dialog')
    for (const name of ['fixture-alpha', 'fixture-beta']) {
      await dialog.getByRole('button', { name: ja ? '接続定義を保存' : 'Save a connection', exact: true }).click()
      await dialog.getByLabel(ja ? '接続の名前' : 'Connection name', { exact: true }).fill(name)
      await dialog.getByRole('combobox', { name: new RegExp(ja ? '保存するネットワーク' : 'Saved network') }).selectOption('tailnet')
      await dialog.getByRole('textbox', { name: new RegExp(ja ? '正確な端末 ID' : 'Exact device IDs') }).fill('fixture-studio')
      await dialog.getByLabel(ja ? 'ポート' : 'Ports', { exact: true }).fill('8080')
      await dialog.getByRole('button', { name: ja ? '停止状態の定義を確認' : 'Review stopped definition', exact: true }).click()
      await dialog.getByRole('button', { name: ja ? '確認した定義を保存' : 'Save reviewed definition', exact: true }).click()
    }
    await dialog.getByRole('button', { name: ja ? 'すべて選択' : 'Select all', exact: true }).click()
    await openDetailsSection(dialog.locator('details').filter({ has: page.getByText(ja ? 'この選択をグループとして保存' : 'Save this selection as a group', { exact: true }) }))
    await dialog.getByLabel(ja ? 'グループ名' : 'Group name', { exact: true }).fill('Fixture_Set')
    await dialog.getByRole('button', { name: ja ? 'グループを保存' : 'Save group', exact: true }).click()
    await expect(dialog.getByText(ja ? 'サービスを開始せずにグループを保存しました' : 'Group saved without starting services', { exact: true })).toBeVisible()
    // The new group option confirms the post-save profile reload has finished.
    const savedGroup = dialog.getByRole('combobox', { name: ja ? '保存済みグループ' : 'Saved group', exact: true })
    await expect(savedGroup.locator('option[value="Fixture_Set"]')).toHaveText('Fixture_Set')
    const toggle = dialog.getByRole('button', { name: ja ? 'お気に入り' : 'Favorites', exact: true })
    await expect(toggle).toBeEnabled()
    await toggle.focus(); await expect(toggle).toBeFocused()
    await page.keyboard.press('Space')
    await expect(toggle).toHaveAttribute('aria-expanded', 'true')
    const favorites = dialog.getByRole('region', { name: ja ? 'お気に入り' : 'Favorites', exact: true })
    // Role names exclude nested option text; exact label-text matching does not.
    const target = favorites.getByRole('combobox', { name: ja ? 'お気に入りにするサービス・グループ' : 'Service or group to favorite', exact: true })
    const add = favorites.getByRole('button', { name: ja ? 'お気に入りに追加' : 'Add favorite', exact: true })
    for (const name of ['fixture-alpha', 'Fixture_Set']) {
      await target.selectOption({ label: name }); await add.click(); await expect(target).toBeEnabled()
    }
    const group = favorites.getByRole('button', { name: ja ? 'お気に入りを選択: グループ · Fixture_Set' : 'Select favorite: Group · Fixture_Set', exact: true })
    await group.click()
    await expect(savedGroup).toHaveValue('Fixture_Set')
    const filter = favorites.getByRole('button', { name: ja ? 'お気に入りのサービスだけ表示' : 'Favorite services only', exact: true })
    await filter.click()
    await expect(dialog.locator('.definition-entry')).toHaveCount(1)
    await expect(dialog.locator('.definition-navigation')).toContainText(ja ? '表示外の選択: 1' : 'Selected outside this view: 1')
    await dialog.getByRole('button', { name: ja ? '開始内容を確認' : 'Review start', exact: true }).click()
    const review = dialog.getByRole('region', { name: ja ? '選択したサービスを確認' : 'Review selected services', exact: true })
    await expect(review).toContainText('fixture-alpha'); await expect(review).toContainText('fixture-beta')
    await review.getByRole('button', { name: ja ? 'キャンセル' : 'Cancel', exact: true }).click()
    await toggle.click()
    await expect(dialog.locator('.definition-entry')).toHaveCount(2)
    await toggle.click(); await expect(group).toBeEnabled()
    await favorites.getByRole('button', { name: ja ? 'お気に入りから削除: グループ · Fixture_Set' : 'Remove favorite: Group · Fixture_Set', exact: true }).click()
    await expect(group).toHaveCount(0); await expect(target).toBeEnabled()
    // Removing a preference must not remove the actual saved group.
    await expect(savedGroup).toHaveValue('Fixture_Set')
    for (const viewport of [{ width: 1440, height: 960 }, { width: 375, height: 844 }, { width: 844, height: 390 }]) {
      await page.setViewportSize(viewport); await target.focus()
      await expect(target).toBeFocused()
      await expect.poll(() => favorites.evaluate(element => {
        const controls = [...element.querySelectorAll('button, select')]
        const bounds = document.activeElement.getBoundingClientRect()
        return element.scrollWidth <= element.clientWidth + 1 && controls.every(control => control.scrollWidth <= control.clientWidth + 1 && (innerWidth > 600 || control.getBoundingClientRect().height >= 44)) &&
          bounds.left >= 0 && bounds.right <= innerWidth && bounds.top >= 0 && bounds.bottom <= innerHeight
      }), { message: 'Favorites keep wrapped controls and keyboard focus inside desktop and narrow viewports' }).toBe(true)
      await app.capture(`saved-favorites-${locale}-${viewport.width}x${viewport.height}`)
    }
    await page.keyboard.press('Escape'); await expect(dialog).toHaveCount(0)
    await page.getByRole('button', { name: ja ? '保存済みサービス' : 'Saved services', exact: true }).click()
    await toggle.click()
    await expect(favorites.getByRole('button', { name: ja ? 'お気に入りを選択: サービス · fixture-alpha' : 'Select favorite: Service · fixture-alpha', exact: true })).toBeVisible()
    await expect(group).toHaveCount(0)
    for (const command of ['service.connect', 'service.share', 'services.start', 'services.stop', 'network.configure', 'startup.save']) expect(await app.count(command)).toBe(0)
    expect(await app.count('favorites.add')).toBe(2); expect(await app.count('favorites.remove')).toBe(1)
    expect(await app.count('service.save')).toBe(2); expect(await app.count('group.save')).toBe(1)
  })
}
