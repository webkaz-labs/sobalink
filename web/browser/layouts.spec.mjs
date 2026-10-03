import { test, expect } from './fixtures.mjs'

const widths = [1440, 1180, 960, 390]
const viewport = width => ({ width, height: width === 390 ? 844 : 960 })

async function graphGeometry(page) {
  await expect.poll(() => page.locator('.network-graph-connectors').evaluate(svg => {
    const paths = [...svg.querySelectorAll('.network-graph-line')]
    return paths.length > 0 && paths.every(path => {
      const box = path.getBBox()
      return !/NaN|Infinity|undefined/.test(path.getAttribute('d') || '') && path.getTotalLength() > 0 && box.width > 0 && box.x >= -1 && box.y >= -1 && box.x + box.width <= svg.clientWidth + 1 && box.y + box.height <= svg.clientHeight + 1
    })
  }), { message: 'Graph connectors must have valid visible geometry within their canvas' }).toBe(true)
  await expect(page.locator('.network-graph foreignObject')).toHaveCount(0)
  const text = await page.locator('.network-graph-node strong, .network-graph-edge').evaluateAll(elements => elements.every(element => {
    for (let parent = element; parent && parent !== document.body; parent = parent.parentElement) if (getComputedStyle(parent).transform !== 'none') return false
    return parseFloat(getComputedStyle(element).fontSize) >= 14
  }))
  expect(text, 'Graph labels keep their declared readable CSS size').toBe(true)
}

async function typography(page, app, name) {
  await page.evaluate(() => document.fonts.ready)
  const selectors = ['.sidebar-title h1', '.conversation-header h2', '.accept-hint', '.composer textarea', '.composer-hints', '.input-help summary', '.network-graph-header h2', '.network-graph-node strong', '.network-graph-path']
  const metrics = await page.evaluate(selectors => selectors.flatMap(selector => {
    const element = document.querySelector(selector)
    if (!element?.getClientRects().length) return []
    const style = getComputedStyle(element)
    return [{ selector, fontFamily: style.fontFamily, fontSize: parseFloat(style.fontSize), lineHeight: parseFloat(style.lineHeight), fontWeight: style.fontWeight }]
  }), selectors)
  const cdp = await page.context().newCDPSession(page)
  try {
    await cdp.send('DOM.enable')
    await cdp.send('CSS.enable')
    const { root } = await cdp.send('DOM.getDocument', { depth: 0 })
    for (const metric of metrics) {
      const { nodeId } = await cdp.send('DOM.querySelector', { nodeId: root.nodeId, selector: metric.selector })
      const { fonts } = await cdp.send('CSS.getPlatformFontsForNode', { nodeId })
      metric.renderedFonts = fonts.map(font => ({ family: font.familyName, postScriptName: font.postScriptName, glyphCount: font.glyphCount, custom: font.isCustomFont }))
      expect(metric.fontSize, `${metric.selector} remains readable`).toBeGreaterThanOrEqual(13)
      expect(metric.lineHeight / metric.fontSize, `${metric.selector} retains line spacing`).toBeGreaterThanOrEqual(1.39)
    }
    expect(metrics.some(metric => metric.renderedFonts.some(font => /NotoSansCJKjp|Noto Sans CJK JP|Hiragino|YuGothic|Yu Gothic|Meiryo/i.test(`${font.family} ${font.postScriptName}`))), 'Japanese UI uses a real intended Japanese font').toBe(true)
    await app.writeMetrics(name, metrics)
  } finally { await cdp.detach() }
}

async function edgeStyles(page, app, tag) {
  await page.setViewportSize(viewport(1440))
  await page.locator('.network-map-button').click()
  await page.mouse.move(5, 5)
  const edge = page.locator('.network-graph-edge').first()
  const read = () => edge.evaluate(element => {
    const style = getComputedStyle(element)
    const canvas = document.createElement('canvas')
    canvas.width = canvas.height = 1
    const context = canvas.getContext('2d')
    const rgba = color => { context.clearRect(0, 0, 1, 1); context.fillStyle = color; context.fillRect(0, 0, 1, 1); return [...context.getImageData(0, 0, 1, 1).data] }
    return { borderWidth: parseFloat(style.borderTopWidth), borderColor: style.borderTopColor, border: rgba(style.borderTopColor), backgroundColor: style.backgroundColor, background: rgba(style.backgroundColor), outlineWidth: parseFloat(style.outlineWidth), outlineStyle: style.outlineStyle }
  })
  await expect(edge).not.toHaveClass(/is-selected/)
  const normal = await read()
  const luminance = rgba => rgba.slice(0, 3).map(value => value / 255).map(value => value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4).reduce((sum, value, index) => sum + value * [0.2126, 0.7152, 0.0722][index], 0)
  const shades = [luminance(normal.border), luminance(normal.background)].sort((a, b) => a - b)
  expect(normal.borderWidth).toBeGreaterThanOrEqual(1)
  expect((shades[1] + 0.05) / (shades[0] + 0.05), 'Connection-card outline contrasts with its surface').toBeGreaterThanOrEqual(3)
  await app.capture(`edge-${tag}-normal`)
  await edge.hover()
  const hovered = await read()
  expect(hovered.borderColor !== normal.borderColor || hovered.backgroundColor !== normal.backgroundColor, 'Hover visibly identifies the connection card').toBe(true)
  await app.capture(`edge-${tag}-hover`)
  await page.mouse.move(5, 5)
  await page.locator('.network-graph-self').focus()
  await page.keyboard.press('Tab')
  await expect(edge).toBeFocused()
  const focused = await read()
  expect(focused.outlineWidth).toBeGreaterThanOrEqual(2)
  expect(focused.outlineStyle).not.toBe('none')
  await app.capture(`edge-${tag}-focus`)
  await page.keyboard.press('Enter')
  await expect(page.locator('.details-panel')).toBeVisible()
  await app.closeDetails()
  await page.locator('.network-graph-view-toggle').focus()
  await expect(edge).toHaveClass(/is-selected/)
  await app.capture(`edge-${tag}-selected`)
}

for (const locale of ['en', 'ja']) for (const theme of ['light', 'dark']) {
  test(`graph ${locale} ${theme}: direct list, history, responsive details and keyboard states`, async ({ page, app }) => {
    await app.appearance(locale, theme)
    await page.locator('.devices-view-button').click()
    await expect(page.locator('.network-graph')).toHaveAttribute('data-view', 'list')
    await expect(page.locator('.network-graph-diagram')).toHaveCount(0)
    await expect(page.locator('.devices-view-button')).toHaveAttribute('aria-current', 'page')
    await app.capture(`graph-${locale}-${theme}-list-1440`)
    const row = page.locator('.network-graph-list-peer').first()
    await row.click()
    await expect(page.locator('.details-panel')).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(row).toBeFocused()
    await page.locator('.network-map-button').click()
    await expect(page.locator('.network-graph-header h2')).toHaveText(locale === 'ja' ? 'ネットワーク図' : 'Network graph')
    await page.locator('.network-graph-view-toggle').click()
    await expect(page.locator('.network-graph-header h2')).toHaveText(locale === 'ja' ? 'デバイス一覧' : 'Device list')
    await page.goBack()
    await expect(page.locator('.network-graph')).toHaveAttribute('data-view', 'diagram')
    await page.goForward()
    await expect(page.locator('.network-graph')).toHaveAttribute('data-view', 'list')
    await page.locator('.network-graph-view-toggle').click()
    for (const width of widths) {
      await page.setViewportSize(viewport(width))
      await expect(page.locator('.network-graph')).toHaveAttribute('data-view', 'diagram')
      await graphGeometry(page)
      await app.capture(`graph-${locale}-${theme}-diagram-${width}`)
      if (width >= 960) {
        const node = page.locator('.network-graph-node:not(.network-graph-self)').first()
        await node.click()
        await expect(page.locator('.details-panel')).toBeVisible()
        await graphGeometry(page)
        await app.capture(`graph-${locale}-${theme}-details-${width}`)
        await page.keyboard.press('Escape')
        await expect(node).toBeFocused()
      }
    }
    if (locale === 'ja') await typography(page, app, `typography-graph-ja-${theme}-390`)
    await edgeStyles(page, app, `${locale}-${theme}`)
    expect(await page.evaluate(() => window.__sobaQA.commands.length), 'Graph inspection does not mutate the application').toBe(0)
  })

  test(`conversation ${locale} ${theme}: responsive composer, sidebar and complete details`, async ({ page, app }) => {
    await app.appearance(locale, theme)
    await app.openPeer()
    const composer = page.locator('.composer textarea')
    await composer.fill(locale === 'ja' ? 'この資料を確認して、気づいたところをあとで共有します。少しお待ちください。' : 'I will review the notes and share feedback after the files arrive.')
    await page.locator('.send-button').click()
    await expect(composer).toHaveValue('')
    await composer.fill(locale === 'ja' ? 'まだ送信していない下書きです。' : 'Responsive unsent draft')
    for (const width of widths) {
      await page.setViewportSize(viewport(width))
      await expect(composer).toBeVisible()
      expect(await composer.evaluate(element => parseFloat(getComputedStyle(element).fontSize))).toBeGreaterThanOrEqual(15)
      await app.capture(`conversation-${locale}-${theme}-${width}`)
    }
    if (locale === 'ja') await typography(page, app, `typography-conversation-ja-${theme}-390`)
    await page.locator('.mobile-back').click()
    await app.capture(`devices-${locale}-${theme}-390`)
    await page.locator('.device-row').first().click()
    await expect(composer).toHaveValue(locale === 'ja' ? 'まだ送信していない下書きです。' : 'Responsive unsent draft')
    await app.openDetails()
    await app.capture(`conversation-details-${locale}-${theme}-390`)
    await page.locator('.details-panel .service-buttons').scrollIntoViewIfNeeded()
    await app.capture(`conversation-details-${locale}-${theme}-390-actions`)
    await page.keyboard.press('Escape')
    await expect(page.locator('.details-panel')).toHaveCount(0)
    await expect(composer).toBeVisible()
  })
}
