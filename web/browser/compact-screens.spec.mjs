import { test, expect } from './fixtures.mjs'

const viewports = [{ width: 375, height: 844 }, { width: 390, height: 844 }, { width: 844, height: 390 }]

async function assertDialogLayout(page, readiness = 'form') {
  const dialog = page.getByRole('dialog')
  await expect(dialog).toBeVisible()
  if (readiness === 'catalog') await expect(dialog.locator('.definition-toolbar')).toBeVisible()
  else await expect(dialog.locator('input:not([type=checkbox]):not([type=radio]):visible, select:visible, textarea:visible').first()).toBeVisible()
  for (const size of viewports) {
    await page.setViewportSize(size)
    await expect.poll(() => dialog.evaluate(element => {
      const bounds = element.getBoundingClientRect()
      const heading = element.querySelector('.modal-heading').getBoundingClientRect()
      const body = element.querySelector('.modal-body')
      const visible = field => field.getClientRects().length > 0
      const fields = [...element.querySelectorAll('input:not([type=checkbox]):not([type=radio]), select, textarea')].filter(visible)
      return bounds.left >= -1 && bounds.right <= innerWidth + 1 && bounds.top >= -1 && bounds.bottom <= innerHeight + 1 &&
        heading.top >= bounds.top - 1 && heading.bottom <= innerHeight + 1 &&
        body.scrollWidth <= body.clientWidth + 1 && document.documentElement.scrollWidth <= innerWidth + 1 &&
        fields.every(field => parseFloat(getComputedStyle(field).fontSize) >= 16)
    }), { message: 'Compact forms keep readable inputs, the heading and all content within the viewport' }).toBe(true)
    const completeValues = await dialog.locator('.service-mapping-row select, .service-mapping-row input').evaluateAll(fields => fields.every(field => {
      const style = getComputedStyle(field)
      const canvas = document.createElement('canvas')
      const context = canvas.getContext('2d')
      context.font = style.font
      const text = field instanceof HTMLSelectElement ? field.selectedOptions[0]?.textContent || '' : field.value || field.placeholder
      const usableWidth = field.clientWidth - parseFloat(style.paddingLeft) - parseFloat(style.paddingRight) - (field instanceof HTMLSelectElement ? 24 : 0)
      return context.measureText(text).width <= usableWidth
    }))
    expect(completeValues, 'Local mapping and lifetime remain fully readable, including Japanese selected values').toBe(true)
    const controls = await dialog.evaluate(element => {
      const enabled = field => field.tabIndex >= 0 && !field.disabled && field.getClientRects().length > 0
      return {
        count: [...element.querySelectorAll('button, input, select, textarea, a[href], summary, [tabindex]')].filter(enabled).length,
        footerCount: [...element.querySelectorAll('.modal-actions button')].filter(enabled).length,
      }
    })
    expect(controls.count, 'The reviewed compact form has a bounded complete focus cycle').toBeLessThanOrEqual(200)
    const close = dialog.locator('.modal-heading button')
    await close.focus()
    await page.keyboard.press('Shift+Tab')
    expect(await dialog.evaluate(element => element.contains(document.activeElement)), 'Reverse Tab from close stays inside the modal').toBe(true)
    if (controls.count > 1) await expect(close, 'Reverse Tab reaches the last available control').not.toBeFocused()
    await page.keyboard.press('Tab')
    await expect(close, 'Forward Tab from the last control returns directly to close').toBeFocused()
    // Walk the complete focus cycle in both directions. In particular,
    // exercise the last controls beneath the sticky action row, not just the
    // first inputs at the top of a long service or capacity form.
    for (const direction of ['Tab', 'Shift+Tab']) {
      await close.focus()
      const visitedFooter = new Set()
      let returned = false
      for (let index = 0; index < controls.count + 2; index++) {
        await page.keyboard.press(direction)
        const result = await dialog.evaluate(element => {
          const focused = document.activeElement
          if (!element.contains(focused)) return { contained: false, visible: false, returned: false, footer: -1 }
          const bounds = focused.getBoundingClientRect()
          const x = Math.min(innerWidth - 1, Math.max(0, bounds.left + bounds.width / 2))
          const y = Math.min(innerHeight - 1, Math.max(0, bounds.top + bounds.height / 2))
          const hit = document.elementFromPoint(x, y)
          return {
            contained: true,
            visible: bounds.width > 0 && bounds.height > 0 && bounds.top >= -1 && bounds.bottom <= innerHeight + 1 &&
              Boolean(hit && (focused === hit || focused.contains(hit))),
            returned: focused === element.querySelector('.modal-heading button'),
            footer: [...element.querySelectorAll('.modal-actions button')].indexOf(focused),
          }
        })
        expect(result.contained, 'Tab stays inside the modal throughout the full keyboard cycle').toBe(true)
        expect(result.visible, 'Keyboard focus remains visible rather than hidden beneath a sticky heading or footer').toBe(true)
        if (result.footer >= 0) visitedFooter.add(result.footer)
        if (result.returned) { returned = true; break }
      }
      expect(returned, 'Focus returns to the close control without escaping the modal').toBe(true)
      expect(visitedFooter.size, 'Every enabled final action is visible and reached by keyboard').toBe(controls.footerCount)
    }
  }
}

for (const locale of ['en', 'ja']) {
  test(`compact ${locale}: service, saved, network and settings forms retain focus and cancel safely`, async ({ page, app }) => {
    test.setTimeout(120_000)
    const ja = locale === 'ja'
    await app.appearance(locale, ja ? 'dark' : 'light')
    await app.openPeer('services')
    await page.setViewportSize({ width: 375, height: 844 })
    const tabLines = await page.locator('.device-sections button').evaluateAll(buttons => buttons.map(button => {
      const text = [...button.childNodes].find(node => node.nodeType === Node.TEXT_NODE && node.textContent.trim())
      const range = document.createRange()
      range.selectNodeContents(text)
      return range.getClientRects().length
    }))
    expect(tabLines, 'Compact navigation allocates space to full labels rather than orphaning the last character').toEqual([1, 1])
    for (const name of [ja ? 'サービスに接続' : 'Connect to a service', ja ? 'サービスを共有' : 'Share a service']) {
      const trigger = page.getByRole('button', { name, exact: true })
      await trigger.click()
      await page.locator('dialog .advanced > summary').click()
      await assertDialogLayout(page)
      await page.keyboard.press('Escape')
      await expect(page.getByRole('dialog')).toHaveCount(0)
      await expect(trigger).toBeFocused()
    }

    const saved = page.getByRole('button', { name: ja ? '保存済みサービス' : 'Saved services', exact: true })
    await saved.click()
    await assertDialogLayout(page, 'catalog')
    await page.keyboard.press('Escape')
    await expect(saved).toBeFocused()

    for (const name of [ja ? '容量と履歴' : 'Capacity and history', ja ? '起動とサインイン時の設定' : 'Startup and sign-in guide', ja ? '高度な接続' : 'Advanced connections']) {
      await page.locator('.app-header .header-actions button.icon-button').click()
      await page.getByRole('button', { name, exact: true }).click()
      await assertDialogLayout(page)
      await page.keyboard.press('Escape')
      await expect(page.getByRole('dialog')).toHaveCount(0)
    }

    await page.setViewportSize({ width: 1180, height: 960 })
    await page.locator('.sidebar-title button').click()
    await assertDialogLayout(page)
    await page.keyboard.press('Escape')
    for (const command of ['service.connect', 'service.share', 'service.save', 'services.start', 'network.configure', 'network.logout', 'application.stop', 'policy.apply', 'startup.save', 'proxy.start']) {
      expect(await app.count(command), 'Reading and cancelling compact forms must not change runtime configuration').toBe(0)
    }

    await page.setViewportSize({ width: 375, height: 844 })
    await page.locator('.devices-view-button').click()
    const targets = await page.locator('.primary-nav button, .app-header .icon-button, .locale-toggle').evaluateAll(elements => elements.map(element => {
      const bounds = element.getBoundingClientRect()
      return bounds.width >= 44 && bounds.height >= 44 && element.scrollWidth <= element.clientWidth + 1
    }))
    expect(targets.every(Boolean), 'Dense navigation retains full touch targets and visible labels').toBe(true)
    await app.capture(`compact-device-list-${locale}-375`)
  })
}
