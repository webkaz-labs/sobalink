import { test, expect } from './fixtures.mjs'

const viewports = [
  { width: 1440, height: 960 },
  { width: 844, height: 390 },
  { width: 1440, height: 390 },
  { width: 667, height: 375 },
  { width: 375, height: 844 },
]

async function expectUncovered(control, app, artifact) {
  let geometry
  try {
    await expect.poll(async () => {
      geometry = await control.evaluate(element => {
        const bounds = element.getBoundingClientRect()
        const frame = element.closest('.device-sidebar')
        const points = [[.5, .5], [.1, .1], [.9, .1], [.1, .9], [.9, .9]]
        // Only numeric geometry and hit counts may enter a diagnostic artifact.
        return {
          width: bounds.width, height: bounds.height, left: bounds.left, right: bounds.right, top: bounds.top, bottom: bounds.bottom,
          viewportWidth: innerWidth, viewportHeight: innerHeight,
          frameTop: frame?.getBoundingClientRect().top ?? 0, frameBottom: frame?.getBoundingClientRect().bottom ?? 0,
          frameScrollTop: frame?.scrollTop ?? 0, frameScrollHeight: frame?.scrollHeight ?? 0, frameClientHeight: frame?.clientHeight ?? 0,
          uncoveredPoints: points.filter(([x, y]) => {
            const hit = document.elementFromPoint(bounds.left + bounds.width * x, bounds.top + bounds.height * y)
            return Boolean(hit && (hit === element || element.contains(hit)))
          }).length,
        }
      })
      return geometry.width > 0 && geometry.height > 0 && geometry.left >= 0 && geometry.right <= geometry.viewportWidth &&
        geometry.top >= 0 && geometry.bottom <= geometry.viewportHeight && geometry.uncoveredPoints === 5
    }, { message: 'The entire control remains reachable without decoration or clipped content intercepting it' }).toBe(true)
  } catch (error) {
    // Retain the original assertion if the existing privacy guard blocks capture.
    if (app && artifact && geometry) await app.writeMetrics(artifact, geometry).catch(() => {})
    throw error
  }
}

async function expectFrame(page) {
  await expect.poll(() => page.evaluate(() => {
    const header = document.querySelector('.app-header').getBoundingClientRect()
    const workspace = document.querySelector('.workspace').getBoundingClientRect()
    const shell = document.querySelector('.app-shell')
    return header.top >= 0 && header.bottom <= innerHeight && workspace.top >= header.bottom - 1 && workspace.bottom <= innerHeight + 1 &&
      shell.scrollHeight <= innerHeight + 1 && document.documentElement.scrollHeight <= innerHeight + 1 && document.documentElement.scrollWidth <= innerWidth + 1
  }), { message: 'Empty-home content stays in its own scroll frames without overflowing the page or header' }).toBe(true)
  const controls = page.locator('.app-header button:visible')
  for (let index = 0; index < await controls.count(); index++) await expectUncovered(controls.nth(index))
}

for (const locale of ['en', 'ja']) {
  test(`${locale}: empty home preserves header hit targets and full short-height keyboard access`, async ({ page, app }) => {
    const ja = locale === 'ja'
    await app.appearance(locale, ja ? 'dark' : 'light')
    const saved = page.getByRole('button', { name: ja ? '保存済みサービス' : 'Saved services', exact: true })
    const catalog = page.getByRole('button', { name: ja ? 'リソース' : 'Resources', exact: true })
    const group = page.getByRole('button', { name: ja ? 'グループの転送設定' : 'Group transfer settings', exact: true })
    const sidebar = page.locator('.device-sidebar')
    const home = page.locator('.main-empty')
    for (const viewport of viewports) {
      await page.setViewportSize(viewport)
      await expectFrame(page)
      // Exercise the pointer path at the failing size without a forced click.
      await saved.click()
      await expect(page.locator('dialog .definition-toolbar')).toBeVisible()
      await page.keyboard.press('Escape')
      await expect(page.getByRole('dialog')).toHaveCount(0)
      await expect(saved).toBeFocused()
      if (viewport.width > 800) {
        await expect(home).toBeVisible()
        await home.evaluate(element => { element.scrollTop = 0 })
        await expectUncovered(home.locator('.empty-network'))
      } else await expect(home).not.toBeVisible()
      await app.capture(`empty-home-${locale}-${viewport.width}x${viewport.height}`)

      // Every sidebar control, including rows and the final network action,
      // remains reachable even when the controls are taller than the frame.
      const sidebarControls = sidebar.locator('button:visible:enabled, input:visible:enabled')
      const count = await sidebarControls.count()
      expect(count).toBeGreaterThan(0)
      await sidebarControls.first().focus()
      for (let index = 0; index < count; index++) {
        await expect(sidebarControls.nth(index)).toBeFocused()
        await expectUncovered(sidebarControls.nth(index), app, `empty-home-geometry-${locale}-${viewport.width}x${viewport.height}-${index}`)
        if (index < count - 1) await page.keyboard.press('Tab')
      }
      await expect(sidebar.locator('.sidebar-footer button')).toBeFocused()
      await expectFrame(page)
      await app.capture(`empty-home-sidebar-${locale}-${viewport.width}x${viewport.height}`)
      if (viewport.width > 800) {
        // Native Tab scrolls the final setup action into view; no content is
        // shrunk, hidden or cut off to protect the header.
        await page.keyboard.press('Tab')
        const setup = home.getByRole('button', { name: ja ? 'ネットワークを設定' : 'Set up network', exact: true })
        await expect(setup).toBeFocused()
        await expectUncovered(setup)
        await expectFrame(page)
        await app.capture(`empty-home-actions-${locale}-${viewport.width}x${viewport.height}`)
      }
      await page.locator('.network-map-button').focus()
      // Traverse each intervening header action with native Tab before opening
      // Saved services, checking both keyboard order and the full hit target.
      for (const control of [catalog, group, saved]) {
        await page.keyboard.press('Tab')
        await expect(control).toBeFocused()
        await expectUncovered(control)
      }
      await page.keyboard.press('Enter')
      await expect(page.locator('dialog .definition-toolbar')).toBeVisible()
      await page.keyboard.press('Escape')
      await expect(page.getByRole('dialog')).toHaveCount(0)
      await expect(saved).toBeFocused()
      await expectFrame(page)
    }
    for (const command of ['network.configure', 'service.connect', 'service.share', 'services.start', 'services.stop', 'service.save']) expect(await app.count(command)).toBe(0)
  })
}
