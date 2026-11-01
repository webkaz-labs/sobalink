import { test, expect } from './fixtures.mjs'

// Real production UI/HTTP with the existing fictional offline fixture.
// Navigation must not enroll, activate a network or start saved services.
test.use({ scenario: 'offline' })
for (const locale of ['en', 'ja']) {
  test(`${locale}: setup guidance stays reachable and navigation never starts connections`, async ({ page, app }) => {
    const ja = locale === 'ja'
    const title = ja ? '3つの手順で接続' : 'Connect in three steps'
    const savedName = ja ? '保存済みの接続を選ぶ' : 'Choose saved connections'
    const networkName = ja ? 'ネットワークを設定' : 'Set up network'
    const tailnetName = ja ? 'Tailscale の設定・ログイン' : 'Tailscale setup & sign-in'
    await app.appearance(locale, ja ? 'dark' : 'light')
    const home = page.locator('.main-empty .setup-guide')
    await expect(home).toBeVisible()
    await expect(home.locator('li')).toHaveCount(3)
    await home.scrollIntoViewIfNeeded()
    await app.capture(`setup-guide-${locale}-desktop`)
    for (const viewport of [{ width: 375, height: 844 }, { width: 844, height: 390 }]) {
      await page.setViewportSize(viewport)
      const opener = page.locator('.sidebar-footer').getByRole('button', { name: title, exact: true })
      await opener.click()
      const dialog = page.getByRole('dialog', { name: title, exact: true })
      await expect(dialog).toBeVisible()
      await expect(dialog.locator('li')).toHaveCount(3)
      await expect(dialog.getByRole('button', { name: ja ? '相手を選ぶ' : 'Choose a device', exact: true })).toBeDisabled()
      const layout = await dialog.evaluate(element => {
        const bounds = element.getBoundingClientRect()
        const body = element.querySelector('.modal-body')
        return bounds.left >= -1 && bounds.right <= innerWidth + 1 && bounds.top >= -1 && bounds.bottom <= innerHeight + 1 && body.scrollWidth <= body.clientWidth + 1
      })
      expect(layout, 'The entire guidance dialog fits; long content scrolls without horizontal clipping').toBe(true)
      const controls = dialog.locator('button:enabled')
      for (let index = 0; index < await controls.count(); index++) {
        const control = controls.nth(index)
        await control.focus()
        expect(await control.evaluate(element => {
          const bounds = element.getBoundingClientRect()
          const hit = document.elementFromPoint(bounds.left + bounds.width / 2, bounds.top + bounds.height / 2)
          return bounds.top >= 0 && bounds.bottom <= innerHeight && bounds.width >= 32 && bounds.height >= 32 && Boolean(hit && (hit === element || element.contains(hit)))
        }), 'Every enabled guidance action has visible keyboard focus and an unobscured target').toBe(true)
      }
      await dialog.getByRole('button', { name: savedName, exact: true }).focus()
      await expect(dialog.getByRole('button', { name: savedName, exact: true })).toBeInViewport()
      await app.capture(`setup-guide-${locale}-${viewport.width}x${viewport.height}`)
      await page.keyboard.press('Escape')
      await expect(page.getByRole('dialog')).toHaveCount(0)
      await expect(opener).toBeFocused()
      // Reopen and move to existing saved definitions without starting them.
      await opener.click()
      await dialog.getByRole('button', { name: savedName, exact: true }).click()
      const filter = page.getByRole('combobox', { name: ja ? '相手で絞り込む' : 'Filter by device', exact: true })
      await expect(filter).toBeVisible()
      await expect(filter).toHaveValue('')
      await expect(filter.locator('option')).toHaveCount(1)
      await app.capture(`setup-saved-${locale}-${viewport.width}x${viewport.height}`)
      await page.keyboard.press('Escape')
      await opener.click()
      await dialog.getByRole('button', { name: networkName, exact: true }).click()
      await page.getByRole('button', { name: tailnetName, exact: true }).click()
      await expect(page.locator('dialog input[value="tailnet"]')).toBeChecked()
      await expect(page.locator('.tailnet-login button').first()).toBeDisabled()
      await app.capture(`setup-tailnet-${locale}-${viewport.width}x${viewport.height}`)
      await page.getByRole('button', { name: ja ? '接続方法の選択へ戻る' : 'Back to connection options', exact: true }).click()
      await page.getByRole('button', { name: tailnetName, exact: true }).click()
      await expect(page.locator('dialog input[value="tailnet"]')).toBeChecked()
      await page.keyboard.press('Escape')
      await expect(page.getByRole('dialog')).toHaveCount(0)
    }
    for (const command of ['network.configure', 'network.resume', 'network.login', 'service.connect', 'services.start', 'startup.save']) expect(await app.count(command), `${command} must remain an explicit operation`).toBe(0)
  })
}
