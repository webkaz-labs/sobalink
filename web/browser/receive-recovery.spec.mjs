import { test, expect } from './fixtures.mjs'

// Production Core is reopened from isolated fictional legacy/damaged profiles.
// The browser never receives old paths or tokens in the recovery DTO.
for (const locale of ['en', 'ja']) {
  test.describe(`${locale} legacy receive recovery`, () => {
    test.use({ scenario: 'receive-legacy' })
    test('requires review, preserves saved autosave and confirms the real receive state', async ({ page, app }) => {
      const ja = locale === 'ja'
      await app.appearance(locale, ja ? 'dark' : 'light')
      await app.expectState(state => state.receiveRecovery?.state === 'blocked' && state.receiveRecovery.code === 'legacy_review_required' && state.receiveRecovery.reservedBytes === null && state.peers.some(peer => peer.id === 'fixture-studio' && peer.autosave?.enabled && !peer.autosave.paused))
      await app.openPeer()
      await expect(page.locator('.composer textarea')).toBeEnabled()
      await expect(page.locator('.composer-tools button').first()).toBeEnabled()
      await app.openDetails()
      await expect(page.locator('.details-panel')).toContainText(ja ? '保存されている自動受信の設定' : 'Saved automatic receiving setting')
      await app.closeDetails()
      const opener = page.locator('.conversation .receive-recovery-notice button')
      await opener.click()
      let dialog = page.getByRole('dialog')
      const checkbox = dialog.getByRole('checkbox')
      const confirmName = ja ? '確認を保存して受信を再開' : 'Confirm review and resume receiving'
      await expect(checkbox).not.toBeChecked()
      await expect(dialog.getByRole('button', { name: confirmName, exact: true })).toBeDisabled()
      await expect(dialog).toContainText(ja ? '不明（空とは限りません）' : 'Unknown; this does not mean empty')
      await expect(dialog.locator('li')).toHaveCount(6)
      // Escape/back are inspection only, and reopening never preserves consent.
      await checkbox.check()
      await page.keyboard.press('Escape')
      await expect(opener).toBeFocused()
      await app.expectState(state => state.receiveRecovery?.state === 'blocked')
      await opener.click()
      await expect(checkbox).not.toBeChecked()
      await dialog.getByRole('button', { name: ja ? '設定に戻る' : 'Back to preferences', exact: true }).click()
      dialog = page.getByRole('dialog')
      await dialog.getByRole('button', { name: ja ? '受信状態を確認' : 'Review receiving', exact: true }).click()
      dialog = page.getByRole('dialog')
      await expect(checkbox).not.toBeChecked()
      for (const width of [1180, 390]) {
        await page.setViewportSize({ width, height: width < 400 ? 844 : 960 })
        await expect.poll(() => dialog.evaluate(element => element.scrollWidth <= element.clientWidth + 1)).toBe(true)
        await app.captureForm(`receive-review-${locale}-${width}`)
      }
      const before = await app.count('receive.recovery.confirm')
      await checkbox.check()
      await dialog.getByRole('button', { name: confirmName, exact: true }).click()
      await expect(dialog).toContainText(ja ? '確認済み状態を保存しました' : 'Review saved.')
      await app.expectState(state => state.receiveRecovery?.state === 'ready' && state.receiveRecovery.code === '' && state.receiveRecovery.reservedBytes === 0 && state.peers.some(peer => peer.id === 'fixture-studio' && peer.autosave?.enabled && !peer.autosave.paused))
      expect(await app.count('receive.recovery.confirm')).toBe(before + 1)
      expect(await app.count('peer.autosave')).toBe(0)
      expect(await app.count('peer.trust')).toBe(0)
      expect(await app.count('settings.update')).toBe(0)
      await expect(checkbox).toHaveCount(0)
      await dialog.getByRole('button', { name: ja ? '再確認' : 'Check again', exact: true }).click()
      await expect(dialog).toContainText(ja ? '受信ストレージは利用可能です' : 'Receive storage is ready')
      await expect(dialog.getByRole('button', { name: confirmName, exact: true })).toHaveCount(0)
      await page.keyboard.press('Escape')
      await expect(page.locator('.conversation .receive-recovery-notice')).toHaveCount(0)
    })
  })

  test.describe(`${locale} damaged receive storage`, () => {
    test.use({ scenario: 'receive-damaged' })
    test('offers storage repair guidance without a destructive reset or receive acknowledgment', async ({ page, app }) => {
      const ja = locale === 'ja'
      await app.appearance(locale, ja ? 'dark' : 'light')
      await app.openPeer('services')
      await expect(page.getByRole('button', { name: ja ? 'サービスに接続' : 'Connect to a service', exact: true })).toBeEnabled()
      await app.expectState(state => state.receiveRecovery?.state === 'blocked' && state.receiveRecovery.code === 'index_unavailable' && state.receiveRecovery.reservedBytes === null)
      await page.locator('.app-header .header-actions button.icon-button').click()
      await page.getByRole('dialog').getByRole('button', { name: ja ? '受信状態を確認' : 'Review receiving', exact: true }).click()
      const dialog = page.getByRole('dialog')
      await expect(dialog).toContainText(ja ? 'soba を再起動してから再確認' : 'restart soba and check again')
      await expect(dialog).toContainText(ja ? '破損した記録を破棄することはできません' : 'cannot discard damaged records')
      await expect(dialog.getByRole('checkbox')).toHaveCount(0)
      await expect(dialog.getByRole('button', { name: ja ? '確認を保存して受信を再開' : 'Confirm review and resume receiving', exact: true })).toHaveCount(0)
      await dialog.getByRole('button', { name: ja ? '再確認' : 'Check again', exact: true }).click()
      await expect(dialog).toContainText(ja ? 'soba を再起動してから再確認' : 'restart soba and check again')
      await app.expectState(state => state.receiveRecovery?.state === 'blocked' && state.receiveRecovery.code === 'index_unavailable' && state.peers.some(peer => peer.id === 'fixture-studio' && peer.autosave?.enabled && !peer.autosave.paused))
      await page.setViewportSize({ width: 390, height: 844 })
      await app.captureForm(`receive-damaged-${locale}-390`)
      await page.keyboard.press('Escape')
      expect(await app.count('peer.autosave')).toBe(0)
      expect(await app.count('settings.update')).toBe(0)
    })
  })
}
