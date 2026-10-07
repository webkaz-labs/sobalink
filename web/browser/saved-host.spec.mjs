import { test, expect } from './fixtures.mjs'

// Real Core status supplies the review and revision. This scenario never starts
// a relay or substitutes an identity, pairing result or authority response.
test.describe('production-backed saved host review', () => {
  test.use({ scenario: 'saved-host' })
  for (const locale of ['en', 'ja']) for (const width of [1440, 390]) {
    test(`${locale}: saved review and cancellation at ${width}px create no network work`, async ({ page, app }) => {
      const ja = locale === 'ja'
      await app.appearance(locale, ja ? 'dark' : 'light')
      await page.setViewportSize({ width, height: width === 390 ? 844 : 960 })
      let expected
      await app.expectState(state => {
        const saved = state.lan?.savedStart
        if (!saved) return false
        expected = { publicKey: saved.publicKey, hostname: saved.hostname, relay: saved.relay, revision: saved.revision }
        return saved.publicKey === state.lan.publicKey && /^[a-f0-9]{64}$/.test(saved.revision) &&
          state.settings.network === 'lan' && saved.hostname === 'saved-notebook' &&
          saved.relay.address === '192.168.50.10:48443' && saved.relay.certificateSHA256 === state.lan.relay.certificateSHA256 &&
          saved.policy.mode === 'allowed-lan-destinations' && saved.policy.prefixes.join(',') === '192.168.50.0/24' &&
          state.lan.relayReady === false && state.lan.pairingReady === false &&
          saved.pairedDevices === 0 && saved.trustedDevices === 0 && saved.automaticReceivers === 0 && saved.pendingStartup.length === 0 && state.peers.length === 0
      }, 'Production Core exposes an offline host with no pair or application grant')
      const commandCount = await page.evaluate(() => window.__sobaQA.commands.length)
      await page.locator('.sidebar-title button').click()
      const reviewName = ja ? '保存済みホストを確認' : 'Review saved host'
      const regionName = ja ? 'この保存済みホストを起動しますか？' : 'Start this saved host?'
      const cancelName = ja ? 'キャンセル' : 'Cancel'
      await expect(page.getByRole('button', { name: reviewName, exact: true })).toBeVisible()
      await expect(page.getByText(ja ? 'リレーは待ち受けていません' : 'Relay listener not running', { exact: true })).toBeVisible()
      await page.getByRole('button', { name: reviewName, exact: true }).click()
      const review = page.getByRole('region', { name: regionName, exact: true })
      await expect(review).toBeFocused()
      await expect(review.locator(':scope > dl > dd')).toHaveText([expected.hostname, expected.publicKey, expected.relay.address, expected.relay.certificateSHA256])
      await expect(review).toContainText('192.168.50.0/24')
      await app.capture(`saved-host-review-${locale}-${width}`)
      await review.locator('details > summary').click()
      await expect(review.locator('details dl dd')).toHaveText(['0', '0', '0'])
      const cancel = review.getByRole('button', { name: cancelName, exact: true })
      await cancel.scrollIntoViewIfNeeded()
      await expect(cancel).toBeVisible()
      await expect.poll(() => cancel.evaluate(button => {
        const r = button.getBoundingClientRect(), hit = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2)
        return r.top >= 0 && r.bottom <= innerHeight && hit !== null && (hit === button || button.contains(hit))
      }), { message: 'Cancel remains visible and unobstructed at the review action area' }).toBe(true)
      await app.capture(`saved-host-review-actions-${locale}-${width}`)
      await cancel.click()
      await expect(review).toHaveCount(0)
      await page.getByRole('button', { name: reviewName, exact: true }).click()
      await page.keyboard.press('Escape')
      await expect(page.getByRole('dialog')).toHaveCount(0)
      await page.locator('.sidebar-title button').click()
      await expect(review).toHaveCount(0)
      await expect(page.getByRole('button', { name: reviewName, exact: true })).toBeVisible()
      expect(await page.evaluate(() => window.__sobaQA.commands.length), 'Review, Cancel, Close and reopen send no commands').toBe(commandCount)
      for (const name of ['network.configure', 'lan.addresses', 'lan.identity', 'lan.invite', 'lan.join', 'peer.trust', 'peer.autosave', 'lan.policy.set', 'lan.routes.apply', 'application.stop']) expect(await app.count(name), `${name} remains unsubmitted`).toBe(0)
      await app.expectState(state => state.lan.savedStart?.revision === expected.revision && state.lan.relayReady === false && state.lan.pairingReady === false && state.peers.length === 0, 'Review and cancellation keep saved state and runtime unchanged')
    })
  }
})
