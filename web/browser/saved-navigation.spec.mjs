import { test, expect, openDetailsSection } from './fixtures.mjs'

// Fictional saved definitions only. Navigation must never start or stop services.
for (const locale of ['en', 'ja']) {
  test(`${locale}: saved search retains hidden group members, keyboard access and safe cancellation`, async ({ page, app }) => {
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
    const groupEditor = dialog.locator('details').filter({ has: page.getByText(ja ? 'この選択をグループとして保存' : 'Save this selection as a group', { exact: true }) })
    await openDetailsSection(groupEditor)
    await dialog.getByLabel(ja ? 'グループ名' : 'Group name', { exact: true }).fill('Fixture_Set')
    await dialog.getByRole('button', { name: ja ? 'グループを保存' : 'Save group', exact: true }).click()
    await expect(dialog.getByText(ja ? 'サービスを開始せずにグループを保存しました' : 'Group saved without starting services', { exact: true })).toBeVisible()
    // Role names exclude nested option text; exact label-text matching does not.
    const group = dialog.getByRole('combobox', { name: ja ? '保存済みグループ' : 'Saved group', exact: true })
    await group.selectOption('Fixture_Set')
    const selected = dialog.getByRole('button', { name: ja ? '選択したものだけ表示' : 'Selected only', exact: true })
    await expect(selected).toHaveAttribute('aria-pressed', 'true')
    const search = dialog.getByRole('searchbox', { name: ja ? '保存済みサービスを検索' : 'Find saved services', exact: true })
    await search.fill('beta')
    await expect(dialog.locator('.definition-entry')).toHaveCount(1)
    await expect(dialog.locator('.definition-navigation')).toContainText(ja ? '表示外の選択: 1' : 'Selected outside this view: 1')
    await dialog.getByRole('button', { name: ja ? '開始内容を確認' : 'Review start', exact: true }).click()
    const review = dialog.getByRole('region', { name: ja ? '選択したサービスを確認' : 'Review selected services', exact: true })
    await expect(review).toContainText('fixture-alpha')
    await expect(review).toContainText('fixture-beta')
    await review.getByRole('button', { name: ja ? 'キャンセル' : 'Cancel', exact: true }).click()
    await expect(review).toHaveCount(0)
    await search.fill('no-matching-fixture')
    await expect(dialog.locator('.definition-entry')).toHaveCount(0)
    await expect(dialog.getByRole('button', { name: ja ? '表示中のサービスを選択に追加' : 'Add visible services', exact: true })).toBeDisabled()
    await dialog.getByRole('button', { name: ja ? '選択したサービスを表示' : 'Show selection', exact: true }).click()
    await expect(search).toHaveValue('')
    await expect(dialog.locator('.definition-entry')).toHaveCount(2)
    await selected.focus()
    await page.keyboard.press('Space')
    await expect(selected).toHaveAttribute('aria-pressed', 'false')
    await expect(group).toHaveValue('Fixture_Set')
    const saveShare = dialog.getByRole('button', { name: ja ? '共有定義を保存' : 'Save a share', exact: true })
    const footer = dialog.locator(':scope > .modal-body > .modal-actions:last-child')
    for (const viewport of [{ width: 1440, height: 960 }, { width: 375, height: 844 }, { width: 390, height: 844 }, { width: 844, height: 390 }]) {
      await page.setViewportSize(viewport)
      // Re-enter the search by keyboard after each resize or action capture.
      await saveShare.focus()
      await expect(saveShare).toBeFocused()
      await page.keyboard.press('Tab')
      await expect(search).toBeFocused()
      await expect.poll(() => dialog.evaluate(element => {
        const body = element.querySelector('.modal-body')
        const navigation = element.querySelector('.definition-navigation')
        const fields = [...navigation.querySelectorAll('input, select')]
        const buttons = [...navigation.querySelectorAll('button')]
        const bounds = document.activeElement.getBoundingClientRect()
        return body.scrollWidth <= body.clientWidth + 1 && navigation.scrollWidth <= navigation.clientWidth + 1 &&
          fields.every(field => parseFloat(getComputedStyle(field).fontSize) >= 16) &&
          buttons.every(button => button.scrollWidth <= button.clientWidth + 1 && (innerWidth > 600 || button.getBoundingClientRect().height >= 44)) &&
          bounds.left >= 0 && bounds.right <= innerWidth && bounds.top >= 0 && bounds.bottom <= innerHeight
      }), { message: 'Saved navigation keeps inputs, wrapped labels and keyboard focus inside desktop and narrow viewports' }).toBe(true)
      await app.capture(`saved-navigation-${locale}-${viewport.width}x${viewport.height}`)
      await footer.scrollIntoViewIfNeeded()
      await app.capture(`saved-navigation-${locale}-${viewport.width}x${viewport.height}-actions`)
    }
    await page.keyboard.press('Escape')
    await expect(dialog).toHaveCount(0)
    for (const command of ['service.connect', 'service.share', 'services.start', 'services.stop', 'network.configure', 'startup.save']) expect(await app.count(command)).toBe(0)
    expect(await app.count('service.save')).toBe(2)
    expect(await app.count('group.save')).toBe(1)
  })
}
