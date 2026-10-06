import { test, expect, openDetailsSection } from './fixtures.mjs'

// The existing routes fixture supplies an offline Core and a selected pinned
// loopback relay. WAN reads/writes use production commands, not browser mocks.
// Documentation endpoints are reviewed but never probed; this suite does not
// start a network engine or establish IPv6 reachability or NAT traversal.
test.describe('explicit WAN candidate settings', () => {
  test.use({ scenario: 'routes' })
  for (const locale of ['en', 'ja']) {
    for (const width of [1440, 390]) {
      test(`${locale}: ${width}px validation, cancelled review, IPv6-only save, reload and disable`, async ({ page, app }) => {
        const ja = locale === 'ja'
        const text = {
          title: ja ? '詳細設定：WAN直接経路候補' : 'Advanced: WAN direct candidates',
          read: ja ? 'WAN設定を確認' : 'Review WAN settings',
          addresses: ja ? '正確なSTUN IP:port接続先' : 'Exact STUN IP:port endpoints',
          budget: ja ? '1回の探索の最大送信先数' : 'Maximum probes per discovery pass',
          ipv6: ja ? '利用可能なグローバルIPv6アドレスを通知する' : 'Advertise eligible global IPv6 addresses',
          review: ja ? '経路候補の変更を確認' : 'Review candidate changes',
          save: ja ? '明示したWAN探索を保存' : 'Save explicit WAN discovery',
          cancel: ja ? 'キャンセル' : 'Cancel',
          disable: ja ? 'WAN探索を無効化' : 'Disable WAN discovery',
          enabled: ja ? '明示した探索が有効' : 'Explicit discovery enabled',
          disabled: ja ? '探索は無効' : 'Discovery disabled',
          saved: ja ? 'WAN設定を保存しました。再起動すると適用されます。' : 'WAN configuration saved. Restart to apply.',
          invalid: ja ? '重複しない数値IP:port接続先' : 'distinct numeric IP:port endpoints',
          impact: ja ? 'LAN外へUDPを送信する場合があります' : 'This may send UDP outside the LAN',
          restart: ja ? 'ここでの保存では探索を開始しません' : 'No discovery is started by saving here',
          ipv6Hint: ja ? 'IPv6候補だけを使う場合、接続先は空欄にできます' : 'Leave endpoints empty for IPv6-only candidates',
        }
        await app.appearance(locale, ja ? 'dark' : 'light')
        const wan = page.locator('dialog details.form-section').filter({ has: page.getByText(text.title, { exact: true }) })
        const endpoints = wan.getByRole('textbox', { name: text.addresses })
        const budget = wan.getByRole('spinbutton', { name: text.budget, exact: true })
        const ipv6 = wan.getByRole('checkbox', { name: text.ipv6, exact: true })
        const review = wan.locator('.invitation-card')
        const button = name => wan.getByRole('button', { name, exact: true })
        const openWAN = async () => {
          await page.locator('.sidebar-title button').click()
          await page.locator('dialog input[value="lan"]').check()
          await expect(wan).not.toHaveAttribute('open')
          await openDetailsSection(wan)
        }
        const assertNoActivation = async () => {
          for (const name of ['network.configure', 'network.login', 'mixed.bind', 'lan.invite', 'lan.join']) {
            expect(await app.count(name), 'WAN settings must not activate or enroll a network').toBe(0)
          }
          await app.expectState(state => state.self.status === 'idle' && state.settings?.network === 'lan' && state.lan?.relay?.address === '127.0.0.1:54446' && !state.lan?.pairingReady && state.peers.length === 1 && state.peers.every(peer => !peer.online && !peer.trusted) && state.services.length === 0 && state.shares.length === 0, 'WAN configuration preserves the offline fixture and unrelated trust')
        }
        const captureReview = async name => {
          // A dialog scrolls independently of the page. Capture both the exact
          // settings and the review actions using the existing privacy gate.
          await endpoints.scrollIntoViewIfNeeded()
          await app.capture(`${name}-settings`)
          await button(text.save).scrollIntoViewIfNeeded()
          await app.capture(`${name}-actions`)
        }

        await openWAN()
        await page.setViewportSize({ width, height: width === 390 ? 844 : 960 })
        expect(await app.count('wan.candidates.get'), 'opening the disclosure is not a read or probe request').toBe(0)
        await expect(endpoints).toHaveCount(0)
        await button(text.read).click()
        await expect(wan.getByRole('status')).toHaveText(text.disabled)
        await expect(endpoints).toHaveValue('')
        await expect(ipv6).not.toBeChecked()
        await expect(budget).toHaveValue('4')
        await expect(wan).toContainText(text.ipv6Hint)
        expect(await app.count('wan.candidates.get')).toBe(1)

        // An empty discovery request is invalid unless IPv6 was explicitly
        // selected. Validation must not issue a mutation or expose Save.
        await button(text.review).click()
        await expect(wan.getByRole('alert')).toContainText(text.invalid)
        await expect(button(text.save)).toHaveCount(0)
        expect(await app.count('wan.candidates.set')).toBe(0)
        await wan.getByRole('alert').scrollIntoViewIfNeeded()
        await app.capture(`wan-empty-denied-${locale}-${width}`)
        for (const invalid of ['0.0.0.0:3478', '192.0.2.20:3478, 192.0.2.20:3478']) {
          await endpoints.fill(invalid)
          await button(text.review).click()
          await expect(wan.getByRole('alert')).toContainText(text.invalid)
          await expect(button(text.save)).toHaveCount(0)
        }
        await endpoints.fill('192.0.2.20:3478, [2001:db8::20]:3478')
        for (const invalid of ['0', '1.5', '65536']) {
          await budget.fill(invalid)
          await button(text.review).click()
          await expect(wan.getByRole('alert')).toContainText(text.invalid)
          await expect(button(text.save)).toHaveCount(0)
        }
        await budget.fill('2')
        await button(text.review).click()
        await expect(wan.getByRole('alert')).toHaveCount(0)
        await expect(review.locator('.code-value')).toHaveText('192.0.2.20:3478, [2001:db8::20]:3478')
        await expect(review).toContainText(text.impact)
        await expect(review).toContainText(text.restart)
        for (const field of [endpoints, budget, ipv6]) await expect(field).toBeDisabled()
        expect(await app.count('wan.candidates.set')).toBe(0)
        await captureReview(`wan-endpoint-review-${locale}-${width}`)
        await button(text.cancel).click()
        await expect(review).toHaveCount(0)
        for (const field of [endpoints, budget, ipv6]) await expect(field).toBeEnabled()
        await expect(endpoints).toHaveValue('192.0.2.20:3478, [2001:db8::20]:3478')
        await expect(budget).toHaveValue('2')

        await endpoints.fill('')
        await ipv6.check()
        await budget.fill('3')
        await button(text.review).click()
        await expect(review.locator('.code-value')).toHaveText('')
        await expect(ipv6).toBeChecked()
        await expect(ipv6).toBeDisabled()
        expect(await app.count('wan.candidates.set')).toBe(0)
        // Dismissing an uncommitted review must not save it. Reopening and
        // explicitly reading Core restores the saved disabled configuration.
        await page.keyboard.press('Escape')
        await expect(page.getByRole('dialog')).toHaveCount(0)
        await openWAN()
        await button(text.read).click()
        await expect(wan.getByRole('status')).toHaveText(text.disabled)
        await expect(endpoints).toHaveValue('')
        await expect(ipv6).not.toBeChecked()
        await expect(budget).toHaveValue('4')
        expect(await app.count('wan.candidates.set')).toBe(0)

        await ipv6.check()
        await budget.fill('3')
        await button(text.review).click()
        await expect(review).toContainText(text.restart)
        await expect(endpoints).toHaveValue('')
        await expect(ipv6).toBeChecked()
        await expect(budget).toHaveValue('3')
        for (const field of [endpoints, budget, ipv6]) await expect(field).toBeDisabled()
        await captureReview(`wan-ipv6-review-${locale}-${width}`)
        await assertNoActivation()
        expect(await app.count('wan.candidates.set')).toBe(0)
        await button(text.save).click()
        await expect(wan.getByText(text.saved, { exact: true })).toBeVisible()
        await expect(wan.getByText(text.enabled, { exact: true })).toBeVisible()
        await expect(review).toHaveCount(0)
        expect(await app.count('wan.candidates.set')).toBe(1)
        await assertNoActivation()

        // A fresh page performs a real WAN read; the React form's optimistic
        // saved state is insufficient evidence of persisted configuration.
        await page.reload()
        await expect(page.locator('.workspace')).toBeVisible()
        await expect(page.locator('html')).toHaveAttribute('lang', locale)
        await openWAN()
        expect(await app.count('wan.candidates.get')).toBe(0)
        await button(text.read).click()
        await expect(wan.getByRole('status')).toHaveText(text.enabled)
        await expect(endpoints).toHaveValue('')
        await expect(ipv6).toBeChecked()
        await expect(budget).toHaveValue('3')
        expect(await app.count('wan.candidates.set')).toBe(0)
        await button(text.disable).scrollIntoViewIfNeeded()
        await app.capture(`wan-ipv6-reloaded-${locale}-${width}`)
        await button(text.disable).click()
        await expect(wan.getByText(text.saved, { exact: true })).toBeVisible()
        await expect(wan.getByText(text.disabled, { exact: true })).toBeVisible()
        expect(await app.count('wan.candidates.set')).toBe(1)
        await button(text.read).click()
        await expect(wan.getByRole('status')).toHaveText(text.disabled)
        await expect(endpoints).toHaveValue('')
        await expect(ipv6).not.toBeChecked()
        await expect(budget).toHaveValue('4')
        await expect(button(text.disable)).toHaveCount(0)
        await assertNoActivation()
        await page.keyboard.press('Escape')
        await expect(page.getByRole('dialog')).toHaveCount(0)
      })
    }
  }
})
