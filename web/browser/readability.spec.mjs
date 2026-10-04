import { test, expect } from './fixtures.mjs'

// Observe the real rendered UI. No style injection, application-state mutation,
// or duplicate palette: rgba values come from the browser's computed styles.
async function appearance(locator, pseudo = null) {
  return locator.evaluate((element, pseudo) => {
    const canvas = document.createElement('canvas')
    canvas.width = canvas.height = 1
    const context = canvas.getContext('2d')
    const rgba = color => {
      context.clearRect(0, 0, 1, 1)
      context.fillStyle = color
      context.fillRect(0, 0, 1, 1)
      return [...context.getImageData(0, 0, 1, 1).data].map((value, index) => index === 3 ? value / 255 : value)
    }
    const over = (front, back) => front.slice(0, 3).map((value, index) => value * front[3] + back[index] * (1 - front[3]))
    const luminance = color => color.slice(0, 3).map(value => value / 255)
      .map(value => value <= .04045 ? value / 12.92 : ((value + .055) / 1.055) ** 2.4)
      .reduce((sum, value, index) => sum + value * [.2126, .7152, .0722][index], 0)
    const contrast = (a, b) => { const values = [luminance(a), luminance(b)].sort((a, b) => a - b); return (values[1] + .05) / (values[0] + .05) }
    const ancestors = []
    for (let parent = element.parentElement; parent; parent = parent.parentElement) ancestors.unshift(parent)
    const outside = ancestors.reduce((background, ancestor) => over(rgba(getComputedStyle(ancestor).backgroundColor), background), [255, 255, 255])
    const effectiveOpacity = node => {
      let value = 1
      for (let current = node; current; current = current.parentElement) value *= parseFloat(getComputedStyle(current).opacity)
      return value
    }
    const backgroundAt = node => {
      const layers = []
      for (let current = node; current; current = current.parentElement) layers.unshift(current)
      return layers.reduce((background, layer) => over(rgba(getComputedStyle(layer).backgroundColor), background), [255, 255, 255])
    }
    const paintedContrast = (color, node, alpha = 1) => {
      const foreground = rgba(color)
      foreground[3] *= effectiveOpacity(node) * alpha
      const background = backgroundAt(node)
      return contrast(over(foreground, background), background)
    }
    const own = getComputedStyle(element)
    const style = pseudo ? getComputedStyle(element, pseudo) : own
    const elementBackground = over(rgba(own.backgroundColor), outside)
    const inside = pseudo ? over(rgba(style.backgroundColor), elementBackground) : elementBackground
    const opacity = effectiveOpacity(element) * (pseudo ? parseFloat(style.opacity) : 1)
    const foreground = rgba(style.color); foreground[3] *= opacity
    const border = rgba(style.borderTopColor); border[3] *= opacity
    const outline = rgba(style.outlineColor)
    const box = element.getBoundingClientRect()
    const visibleText = []
    const walker = document.createTreeWalker(element, NodeFilter.SHOW_TEXT)
    while (walker.nextNode()) {
      const node = walker.currentNode
      if (!node.textContent.trim()) continue
      const parent = node.parentElement
      const textStyle = getComputedStyle(parent)
      const range = document.createRange()
      range.selectNodeContents(node)
      if (textStyle.visibility === 'visible' && parseFloat(textStyle.fontSize) > 0 &&
          [...range.getClientRects()].some(rect => rect.width > 0 && rect.height > 0)) {
        visibleText.push(paintedContrast(textStyle.color, parent))
      }
    }
    const iconContrasts = [...element.querySelectorAll('svg path, svg rect, svg circle, svg polygon, svg polyline')].flatMap(shape => {
      const shapeStyle = getComputedStyle(shape)
      const bounds = shape.getBoundingClientRect()
      if (shapeStyle.visibility !== 'visible' || bounds.width < 4 || bounds.height < 4) return []
      return ['stroke', 'fill'].flatMap(paint => {
        if (shapeStyle[paint] === 'none' || (paint === 'stroke' && parseFloat(shapeStyle.strokeWidth) <= 0)) return []
        const color = shapeStyle[paint] === 'currentcolor' ? shapeStyle.color : shapeStyle[paint]
        return [paintedContrast(color, shape, parseFloat(shapeStyle[`${paint}Opacity`]))]
      })
    })
    return {
      textContrast: contrast(over(foreground, inside), inside),
      visibleTextCount: visibleText.length, visibleTextContrast: Math.min(...visibleText),
      visibleIconContrast: Math.max(0, ...iconContrasts),
      interactive: element.matches('button, [role="button"], a[href], summary'),
      valueControl: element.matches('input, select, textarea'),
      boundaryContrast: Math.max(contrast(over(border, outside), outside), contrast(inside, outside)),
      fillContrast: contrast(inside, outside),
      innerBoundaryContrast: contrast(over(border, inside), inside),
      outlineContrast: contrast(over(outline, outside), outside),
      borderWidth: parseFloat(style.borderTopWidth), borderStyle: style.borderTopStyle,
      outlineWidth: parseFloat(style.outlineWidth), outlineStyle: style.outlineStyle,
      outlineOffset: parseFloat(style.outlineOffset), outlineColor: style.outlineColor,
      borderColor: style.borderTopColor, borderRadius: style.borderTopLeftRadius, boxShadow: style.boxShadow,
      color: style.color, background: style.backgroundColor, cursor: style.cursor, decoration: style.textDecorationLine,
      fontSize: parseFloat(style.fontSize), width: box.width, height: box.height,
      hasLabel: Boolean(element.labels?.length || element.getAttribute('aria-label') || element.getAttribute('aria-labelledby')),
      readOnly: Boolean(element.readOnly), disabled: element.matches(':disabled'),
    }
  }, pseudo)
}

async function readable(locator, { boundary, input = false, touch = false, pseudo = null } = {}) {
  await expect(locator).toBeVisible()
  // A visible label or recognizable icon can identify a button without a box.
  // Check painted content as well as computed parent color, so opacity:0,
  // zero-size labels, hidden glyphs and hover-only content cannot pass.
  await expect.poll(async () => {
    const metrics = await appearance(locator, pseudo)
    if (metrics.valueControl || pseudo) return metrics.fontSize > 0 && metrics.textContrast >= 4.5
    if (metrics.visibleTextCount > 0) return metrics.visibleTextContrast >= 4.5
    return metrics.visibleIconContrast >= 3
  }, { message: 'At-rest controls retain visible readable text or a recognizable contrasting icon' }).toBe(true)
  const metrics = await appearance(locator, pseudo)
  if (metrics.interactive) await expect(locator).toHaveAccessibleName(/\S/)
  if (boundary ?? input) {
    const outlined = metrics.borderWidth >= 1 && !['none', 'hidden'].includes(metrics.borderStyle) && metrics.boundaryContrast >= 3
    expect(outlined || metrics.fillContrast >= 3, 'Input areas remain identifiable by a purposeful boundary or contrasting fill').toBe(true)
  }
  if (input) {
    expect(metrics.fontSize, 'Form values remain legible without mobile auto-zoom').toBeGreaterThanOrEqual(16)
    expect(metrics.hasLabel, 'Form controls retain an accessible name').toBe(true)
  }
  if (touch) {
    expect(metrics.width, 'Touch hit area is at least 44 CSS px wide').toBeGreaterThanOrEqual(44)
    expect(metrics.height, 'Touch hit area is at least 44 CSS px high').toBeGreaterThanOrEqual(44)
  }
  return metrics
}

async function keyboardFocus(page, locator, contour = locator) {
  await locator.focus()
  // Walk away and back with real keyboard navigation, retaining the same target.
  await page.keyboard.press('Tab')
  await expect(locator).not.toBeFocused()
  await page.keyboard.press('Shift+Tab')
  await expect(locator).toBeFocused()
  await expect.poll(async () => {
    const metrics = await appearance(contour)
    return metrics.outlineWidth >= 2 && metrics.outlineStyle !== 'none' && metrics.outlineContrast >= 3
  }, { message: 'Keyboard focus has a persistent, contrasting visible ring' }).toBe(true)
}

async function fieldFocus(page, locator, contour = locator) {
  await locator.scrollIntoViewIfNeeded()
  const before = await appearance(contour)
  const nativePicker = await locator.evaluate(element => element instanceof HTMLSelectElement && !element.multiple && element.size <= 1)
  const value = nativePicker ? await locator.inputValue() : null
  const check = async () => {
    await expect.poll(async () => {
      const metrics = await appearance(contour)
      return metrics.outlineWidth === 2 && metrics.outlineStyle === 'solid' && metrics.outlineOffset === -1 &&
        metrics.outlineColor === metrics.borderColor && metrics.boxShadow === 'none' && metrics.outlineContrast >= 3
    }, { message: 'Editing has one continuous contour, without a detached ring or second shadow' }).toBe(true)
    const after = await appearance(contour)
    expect([after.width, after.height, after.borderWidth, after.borderRadius], 'Focus must preserve the field geometry').toEqual(
      [before.width, before.height, before.borderWidth, before.borderRadius])
    if (contour !== locator) {
      const inner = await appearance(locator)
      expect(inner.outlineStyle, 'The composite editor must not also frame its inner textarea').toBe('none')
      expect(inner.borderWidth).toBe(0)
      expect(inner.boxShadow).toBe('none')
    }
  }
  await locator.click()
  await expect(locator).toBeFocused()
  await check()
  if (nativePicker) {
    // A pointer click opens Chromium's native picker. Dismiss it before Tab:
    // otherwise Tab only closes the popup and Shift+Tab reaches the prior field.
    await page.keyboard.press('Escape')
    await expect(locator).toBeFocused()
    await expect(locator).toHaveValue(value)
  }
  await keyboardFocus(page, locator, contour)
  await check()
}

const profiles = [
  { name: 'desktop', viewport: { width: 1440, height: 960 }, hasTouch: false },
  { name: 'phone', viewport: { width: 375, height: 844 }, hasTouch: true },
  { name: 'tablet', viewport: { width: 1024, height: 900 }, hasTouch: true },
]

for (const profile of profiles) {
  test.describe(`readability ${profile.name}`, () => {
    test.use({ viewport: profile.viewport, hasTouch: profile.hasTouch })
    for (const theme of ['light', 'dark']) {
      test(`${theme}: controls are recognizable before hover and remain readable in every state`, async ({ page, app }) => {
        test.setTimeout(90_000)
        await app.appearance('en', theme)
        const touch = profile.hasTouch
        expect(await page.evaluate(() => matchMedia('(pointer: coarse)').matches)).toBe(touch)
        expect(await page.evaluate(() => matchMedia('(hover: none)').matches)).toBe(touch)
        await page.mouse.move(0, 0)
        await fieldFocus(page, page.locator('.search-field input'))
        const settings = page.locator('.app-header .header-actions button.icon-button')
        const normal = await readable(settings, { touch })
        await readable(page.locator('.locale-toggle'), { touch })
        for (const button of await page.locator('.primary-nav .button').all()) await readable(button, { boundary: false, touch })
        if (!touch) {
          await settings.hover()
          await expect.poll(async () => {
            const hovered = await appearance(settings)
            return hovered.background !== normal.background || hovered.borderStyle !== normal.borderStyle || hovered.color !== normal.color || hovered.boundaryContrast !== normal.boundaryContrast
          }, { message: 'Hover adds feedback to an already identifiable control' }).toBe(true)
          await readable(settings)
          await page.mouse.move(0, 0)
        }
        await keyboardFocus(page, settings)
        await settings.click()
        const dialog = page.getByRole('dialog')
        await readable(dialog.locator('select').first(), { input: true, touch })
        await fieldFocus(page, dialog.locator('select').first())
        await readable(dialog.locator('input').first(), { input: true, touch })
        for (const button of await dialog.locator('.settings-links > .button').all()) await readable(button, { touch })
        await dialog.getByRole('button', { name: 'Startup and sign-in guide', exact: true }).click()
        const readOnly = dialog.locator('textarea[readonly]').first()
        const readOnlyStyle = await readable(readOnly, { input: true })
        expect(readOnlyStyle.readOnly).toBe(true)
        expect(readOnlyStyle.disabled).toBe(false)
        await fieldFocus(page, readOnly)
        await page.keyboard.press('Escape')

        await page.locator('.network-map-button').click()
        // Device names and artwork remain visible without forcing a boxed
        // nameplate that competes with the graph and obscures its endpoints.
        for (const node of await page.locator('.network-graph-node').all()) {
          await readable(node, { boundary: false, touch })
          await readable(node.locator('.network-graph-device-heading > span:last-child'))
        }
        for (const control of await page.locator('.network-graph-edge, .network-graph-view-toggle').all()) await readable(control, { touch })
        await keyboardFocus(page, page.locator('.network-graph-node').first())
        await page.getByRole('button', { name: 'Saved services', exact: true }).click()
        await dialog.locator('.definition-portable > summary').click()
        const importInput = dialog.locator('input[type=file]')
        await readable(importInput, { input: true, touch })
        await readable(importInput, { pseudo: '::file-selector-button' })
        await page.keyboard.press('Escape')

        await app.openPeer('exchange')
        await page.setViewportSize(profile.viewport)
        const composer = page.locator('.composer textarea')
        await readable(page.locator('.composer'), { boundary: true })
        await readable(composer, { boundary: false, input: true })
        await readable(composer, { boundary: false, pseudo: '::placeholder' })
        await fieldFocus(page, composer, page.locator('.composer'))
        const send = page.locator('.send-button')
        await expect(send).toBeDisabled()
        // Inactive controls are exempt under WCAG; keeping the caption readable
        // is a deliberate product requirement, not a compliance claim.
        const disabled = await readable(send, { touch })
        expect(disabled.disabled).toBe(true)
        await composer.fill('Readability sample')
        await expect(send).toBeEnabled()
        const enabled = await readable(send, { touch })
        expect([disabled.color, disabled.background, disabled.borderStyle]).not.toEqual([enabled.color, enabled.background, enabled.borderStyle])
        await keyboardFocus(page, send)
        expect((await appearance(page.locator('.composer'))).outlineWidth, 'Toolbar focus must not add a second editor contour').toBe(0)
        await composer.fill('')
        await app.openPeer('services')
        await page.setViewportSize(profile.viewport)
        for (const action of await page.locator('.overview-exchange-group .button-ghost').all()) {
          await readable(action, { touch })
        }
        await page.getByRole('button', { name: 'Connect to a service', exact: true }).click()
        const ports = dialog.locator('input[aria-describedby="port-help"]')
        await readable(ports, { input: true, touch })
        await readable(ports, { boundary: false, pseudo: '::placeholder' })
        await fieldFocus(page, ports)
        await ports.fill('0,65536')
        await expect(ports).toHaveAttribute('aria-invalid', 'true')
        await expect.poll(async () => {
          const invalid = await appearance(ports)
          const error = await appearance(dialog.locator('#port-help'))
          return invalid.borderColor === error.color && invalid.outlineColor === error.color
        }, { message: 'The single focus contour retains the visible validation state' }).toBe(true)
        await expect(dialog.locator('button[type=submit]')).toBeDisabled()
        await readable(dialog.locator('button[type=submit]'), { touch })
        await page.emulateMedia({ forcedColors: 'active' })
        await keyboardFocus(page, ports)
        const forced = await appearance(ports)
        expect(forced.outlineOffset).toBe(-1)
        expect(forced.outlineColor).toBe(forced.borderColor)
        await page.emulateMedia({ forcedColors: 'none' })
        await page.keyboard.press('Escape')
        expect(await app.count('message.send'), 'Reviewing states must not send the draft').toBe(0)
        expect(await app.count('service.connect'), 'Reviewing controls must not start services').toBe(0)
        expect(await app.count('startup.save'), 'Reading command examples must not change startup settings').toBe(0)
      })
    }
  })
}
