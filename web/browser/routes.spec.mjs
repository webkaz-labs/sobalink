import { test, expect, openDetailsSection } from './fixtures.mjs'

// Authenticated commands run through the real Core on an isolated offline pair.
// This covers authorization/persistence only, not relay failover or app behavior.
test.describe('paired route recovery', () => {
  test.use({ scenario: 'routes' })
  for (const locale of ['en', 'ja']) {
    const ja = locale === 'ja'
    const text = {
      title: ja ? '接続経路の復旧' : 'Route recovery', receive: ja ? '受け取った更新情報を確認' : 'Review received update',
      inspect: ja ? '更新情報を検証' : 'Inspect update', review: ja ? '経路の承認内容を確認' : 'Review route approval',
      approve: ja ? '選択した経路を承認' : 'Approve selected routes', cancel: ja ? 'キャンセル' : 'Cancel',
      saved: ja ? '経路の承認を保存しました。接続できるかは未確認です。' : 'Route approval saved. Reachability is not yet verified.',
      revoke: ja ? '経路の承認を取り消す' : 'Revoke route approvals', confirmRevoke: ja ? 'すべての承認を取り消す' : 'Revoke all approvals',
      revoked: ja ? '経路の承認を取り消しました。' : 'Route approvals revoked.', savedReview: ja ? '保存済みの更新情報を確認' : 'Review saved update',
      export: ja ? '非公開の更新情報を作成' : 'Create private update', withdrawOption: ja ? 'すべての経路候補を撤回' : 'Withdraw all advertised routes', exportWithdrawal: ja ? '撤回の更新情報を作成' : 'Create withdrawal update', hide: ja ? '更新情報を隠して消去' : 'Hide and clear update',
      prepared: ja ? '詳細: 準備済みリレー候補' : 'Advanced: prepared relay candidates',
      add: ja ? '候補を追加' : 'Add candidate', address: ja ? 'リレーのアドレス' : 'Relay address',
      pin: ja ? '証明書のSHA-256' : 'Certificate SHA-256', scope: ja ? 'リレーの範囲' : 'Relay scope',
      candidateReview: ja ? 'リレー候補の確認' : 'Review relay candidate', saveCandidate: ja ? '候補を保存' : 'Save candidate',
      remove: ja ? '候補を削除' : 'Remove candidate', confirmRemove: ja ? 'この候補を削除' : 'Remove this candidate',
    }
    test(`${locale}: inspect, cancel, finite explicit approval, reload, reapproval and revoke`, async ({ page, app }) => {
      await app.appearance(locale, ja ? 'dark' : 'light')
      await app.openPeer('services'); await app.openDetails()
      const disclosure = page.locator('.details-panel .route-disclosure')
      await expect(disclosure).not.toHaveAttribute('open')
      expect(await app.count('lan.routes.list')).toBe(0)
      await disclosure.locator(':scope > summary').focus()
      await page.keyboard.press('Enter')
      await expect(disclosure).toHaveAttribute('open', '')
      const panel = disclosure.locator('.route-panel')
      await panel.getByRole('button', { name: text.receive, exact: true }).click()
      await app.fillRouteUpdate(panel.locator('input[data-private=route-update]'))
      await panel.getByRole('button', { name: text.inspect, exact: true }).click()
      await expect(panel.getByRole('heading', { name: text.review, exact: true })).toBeFocused()
      await expect(panel.locator('input[data-private=route-update]')).toHaveCount(0)
      await expect(panel.getByRole('checkbox')).toHaveCount(2)
      for (const checkbox of await panel.getByRole('checkbox').all()) await expect(checkbox).not.toBeChecked()
      await expect(panel.getByRole('button', { name: text.approve, exact: true })).toBeDisabled()
      await expect(panel).toContainText('192.0.2.20:443')
      await expect(panel).toContainText('b'.repeat(64))
      await app.capture(`route-review-${locale}-desktop`)
      await panel.getByRole('button', { name: text.cancel, exact: true }).click()
      expect(await app.count('lan.routes.apply')).toBe(0)
      await panel.getByRole('button', { name: text.receive, exact: true }).click()
      await app.fillRouteUpdate(panel.locator('input[data-private=route-update]'))
      await panel.getByRole('button', { name: text.inspect, exact: true }).click()
      await panel.getByRole('checkbox', { name: /192\.0\.2\.20:443/ }).check()
      await page.setViewportSize({ width: 390, height: 844 })
      await panel.getByRole('button', { name: text.approve, exact: true }).scrollIntoViewIfNeeded()
      await app.capture(`route-review-${locale}-390`)
      await panel.getByRole('button', { name: text.approve, exact: true }).click()
      await expect(panel.getByText(text.saved, { exact: true })).toBeVisible()
      expect(await app.count('lan.routes.apply')).toBe(1)
      await app.expectState(state => state.peers.length === 1 && state.peers[0].trusted === false && !state.peers[0].autosave?.enabled && state.services.length === 0 && state.shares.length === 0)
      await page.reload(); await expect(page.locator('.workspace')).toBeVisible()
      await app.openPeer('services'); await app.openDetails(); await openDetailsSection(page.locator('.details-panel .route-disclosure'))
      await expect(panel).toContainText('192.0.2.20:443')
      await panel.getByRole('button', { name: text.savedReview, exact: true }).click()
      await expect(panel.getByRole('checkbox')).toHaveCount(2)
      for (const checkbox of await panel.getByRole('checkbox').all()) await expect(checkbox).not.toBeChecked()
      await panel.getByRole('checkbox', { name: /192\.0\.2\.20:443/ }).check()
      await panel.getByRole('button', { name: text.approve, exact: true }).click()
      await expect(panel.getByText(text.saved, { exact: true })).toBeVisible()
      expect(await app.count('lan.routes.approve')).toBe(1)
      await panel.getByRole('button', { name: text.revoke, exact: true }).click()
      await panel.getByRole('button', { name: text.cancel, exact: true }).click()
      expect(await app.count('lan.routes.revoke')).toBe(0)
      await panel.getByRole('button', { name: text.revoke, exact: true }).click()
      await panel.getByRole('button', { name: text.confirmRevoke, exact: true }).click()
      await expect(panel.getByText(text.revoked, { exact: true })).toBeVisible()
      expect(await app.count('lan.routes.revoke')).toBe(1)
      expect(await app.count('peer.trust')).toBe(0)
      expect(await app.count('peer.autosave')).toBe(0)
      expect(await app.count('network.configure')).toBe(0)
      await app.capture(`route-revoked-${locale}-desktop`)
      await openDetailsSection(panel.locator('.route-more'))
      await panel.getByRole('button', { name: text.export, exact: true }).click()
      await expect(panel.getByRole('checkbox', { name: text.withdrawOption, exact: true })).not.toBeChecked()
      await panel.getByRole('checkbox', { name: text.withdrawOption, exact: true }).check()
      await panel.getByRole('button', { name: text.exportWithdrawal, exact: true }).click()
      await expect.poll(() => panel.locator('textarea[data-private=route-update]').evaluate(element => element.value.length > 0), { message: 'A private withdrawal update should be available without exposing its contents' }).toBe(true)
      expect(await app.count('lan.routes.export')).toBe(1)
      // Do not capture or serialize the private update, even on failure.
      await panel.getByRole('button', { name: text.hide, exact: true }).click()
      await expect(panel.locator('[data-private=route-update]')).toHaveCount(0)
      expect(await app.count('lan.routes.revoke')).toBe(1)

    })
    test(`${locale}: offline candidate review, cancel, save and remove preserve original relay`, async ({ page, app }) => {
      await app.appearance(locale, ja ? 'dark' : 'light')
      await page.locator('.sidebar-title button').click()
      const disclosure = page.locator('dialog .route-disclosure')
      await expect(disclosure.locator('summary')).toHaveText(text.prepared)
      await openDetailsSection(disclosure)
      const panel = disclosure.locator('.route-panel')
      await expect(panel.getByRole('button', { name: text.remove, exact: true })).toHaveCount(0)
      await panel.getByRole('button', { name: text.add, exact: true }).click()
      await expect(panel.getByLabel(text.address, { exact: true })).toBeFocused()
      await panel.getByLabel(text.address, { exact: true }).fill('192.0.2.30:443')
      await panel.getByLabel(text.pin, { exact: true }).fill('c'.repeat(64))
      await panel.getByLabel(text.scope, { exact: true }).selectOption('external')
      await panel.getByRole('button', { name: text.candidateReview, exact: true }).click()
      await expect(panel.getByRole('heading', { name: text.candidateReview, exact: true })).toBeFocused()
      await panel.getByRole('button', { name: text.cancel, exact: true }).click()
      expect(await app.count('lan.routes.add')).toBe(0)
      await panel.getByRole('button', { name: text.add, exact: true }).click()
      await panel.getByRole('button', { name: text.candidateReview, exact: true }).click()
      await page.setViewportSize({ width: 390, height: 844 })
      await panel.getByRole('button', { name: text.saveCandidate, exact: true }).scrollIntoViewIfNeeded()
      await app.capture(`route-candidate-${locale}-390`)
      await panel.getByRole('button', { name: text.saveCandidate, exact: true }).click()
      await expect(panel.locator('.route-list > li')).toHaveCount(2)
      expect(await app.count('lan.routes.add')).toBe(1)
      await panel.getByRole('button', { name: text.remove, exact: true }).click()
      await panel.getByRole('button', { name: text.confirmRemove, exact: true }).click()
      await expect(panel.locator('.route-list > li')).toHaveCount(1)
      await expect(panel).toContainText('127.0.0.1:54446')
      expect(await app.count('network.configure')).toBe(0)
      await app.expectState(state => state.peers.length === 1 && state.peers[0].trusted === false)
    })
  }
})
