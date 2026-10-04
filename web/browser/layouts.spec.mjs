import { test, expect } from './fixtures.mjs'

const widths = [1440, 1180, 960, 390, 375]
const viewport = width => ({ width, height: width <= 390 ? 844 : 960 })

async function graphGeometry(page) {
  await expect.poll(() => page.locator('.network-graph-connectors').evaluate(svg => {
    const paths = [...svg.querySelectorAll('.network-graph-line')]
    return paths.length > 0 && paths.every(path => {
      const box = path.getBBox()
      const d = path.getAttribute('d') || ''
      const canvas = svg.getBoundingClientRect()
      const peerId = path.closest('[data-peer-id]').getAttribute('data-peer-id')
      const branch = [...svg.parentElement.querySelectorAll('.network-graph-branch')].find(item => item.getAttribute('data-peer-id') === peerId)
      const local = svg.parentElement.querySelector('.network-graph-self .network-graph-device-glyph').getBoundingClientRect()
      const remote = branch.querySelector('.network-graph-device-glyph').getBoundingClientRect()
      const start = path.getPointAtLength(0)
      const end = path.getPointAtLength(path.getTotalLength())
      const atLocalPort = Math.min(Math.abs(start.x - (local.left + 2 - canvas.left)), Math.abs(start.x - (local.right - 2 - canvas.left))) < 1 && Math.abs(start.y - (local.top + local.height / 2 - canvas.top)) < 1
      const atRemotePort = Math.abs(end.x - (remote.left + 2 - canvas.left)) < 1 && Math.abs(end.y - (remote.top + remote.height / 2 - canvas.top)) < 1
      return d.match(/M /g)?.length === 1 && atLocalPort && atRemotePort && !/NaN|Infinity|undefined/.test(d) && path.getTotalLength() > 0 && box.width > 0 && box.x >= -1 && box.y >= -1 && box.x + box.width <= svg.clientWidth + 1 && box.y + box.height <= svg.clientHeight + 1
    })
  }), { message: 'Graph connectors must have valid visible geometry within their canvas' }).toBe(true)
  await expect(page.locator('.network-graph foreignObject')).toHaveCount(0)
  const text = await page.locator('.network-graph-node strong, .network-graph-edge').evaluateAll(elements => elements.every(element => {
    for (let parent = element; parent && parent !== document.body; parent = parent.parentElement) if (getComputedStyle(parent).transform !== 'none') return false
    return parseFloat(getComputedStyle(element).fontSize) >= 14
  }))
  expect(text, 'Graph labels keep their declared readable CSS size').toBe(true)
}

async function graphLayout(page, count) {
  await expect(page.locator('.network-graph-branch')).toHaveCount(count)
  await graphGeometry(page)
  await expect.poll(() => page.locator('.network-graph').evaluate(graph => {
    const canvas = graph.querySelector('.network-graph-canvas')
    const bounds = canvas.getBoundingClientRect()
    const branches = [...canvas.querySelectorAll('.network-graph-branch')]
    const withinCanvas = element => {
      const box = element.getBoundingClientRect()
      return box.width > 0 && box.height > 0 && box.left >= bounds.left - 1 && box.right <= bounds.right + 1 && box.top >= bounds.top - 1 && box.bottom <= bounds.bottom + 1
    }
    const disjoint = (a, b) => a.right <= b.left + 1 || b.right <= a.left + 1 || a.bottom <= b.top + 1 || b.bottom <= a.top + 1
    return document.documentElement.scrollWidth <= innerWidth + 1 && graph.scrollWidth <= graph.clientWidth + 1 && canvas.scrollWidth <= canvas.clientWidth + 1 && branches.every((branch, index) => {
      const contents = [...branch.children]
      return withinCanvas(branch) && (!index || branches[index - 1].getBoundingClientRect().bottom <= branch.getBoundingClientRect().top + 1) && contents.every((element, index) => withinCanvas(element) && contents.slice(index + 1).every(other => disjoint(element.getBoundingClientRect(), other.getBoundingClientRect())))
    }) && [...graph.querySelectorAll('.network-graph-node strong, .network-graph-edge, .network-graph-selected-facts')].every(element => element.scrollWidth <= element.clientWidth + 1)
  }), { message: 'Graph rows, labels and selected facts must remain inside the canvas without overlapping or horizontal overflow' }).toBe(true)
}

// Only these layout cases replace state reads with fictional peers. Existing
// workflow cases continue to exercise the real isolated Go state and commands.
for (const locale of ['en', 'ja']) for (const count of [3, 6, 12]) {
  test(`graph selection ${locale}: ${count} peers retain layout through switching, closing and narrow views`, async ({ page, app }) => {
    await app.appearance(locale, 'light')
    const peers = Array.from({ length: count }, (_, index) => ({
      id: `layout-device-${String(index + 1).padStart(2, '0')}`,
      name: locale === 'ja' ? `制作スタジオの共有デバイス・長い表示名 ${String(index + 1).padStart(2, '0')}` : `Shared studio device with a longer display name ${String(index + 1).padStart(2, '0')}`,
      networks: ['tailnet'], online: true, verified: true, trusted: true, bridge: true,
      path: ['direct', 'relay', 'unknown'][index % 3],
    }))
    await page.route('**/api/state', async route => {
      const response = await route.fetch()
      const state = await response.json()
      await route.fulfill({ response, json: { ...state, peers, services: [], shares: [], availableServices: [], messages: [], transfers: [] } })
    })
    await page.reload()
    await expect(page.locator('.workspace')).toBeVisible()
    await page.locator('.network-map-button').click()
    const node = index => page.locator(`.network-graph-branch[data-peer-id="${peers[index].id}"] > .network-graph-node`)
    const selectedName = index => expect(page.locator('.details-identity h3')).toHaveText(peers[index].name)
    for (const width of [1180, 1100, 960, 800, 390, 375]) {
      await page.setViewportSize(viewport(width))
      await page.locator('.network-map-button').click()
      await graphLayout(page, count)
      await expect(page.locator('.network-graph-node[aria-pressed="true"]')).toHaveCount(0)
      await node(0).click()
      await selectedName(0)
      await expect(page.locator('.details-panel')).toBeVisible()
      if (width > 800) {
        await graphLayout(page, count)
        const height = await page.locator('.network-graph-canvas').evaluate(element => element.getBoundingClientRect().height)
        // Equal-height detail blocks move intervening rows without resizing
        // the canvas. This is the stale-connector regression, not a resize test.
        for (const index of [1, count - 1, 0, count - 1]) {
          await node(index).click()
          await selectedName(index)
          await graphLayout(page, count)
          expect(await page.locator('.network-graph-canvas').evaluate(element => element.getBoundingClientRect().height)).toBeCloseTo(height, 0)
        }
      } else {
        await expect(page.locator('.network-graph')).toBeHidden()
        expect(await page.locator('.details-panel').evaluate(element => element.scrollWidth <= element.clientWidth + 1)).toBe(true)
      }
      await page.keyboard.press('Escape')
      await expect(page.locator('.details-panel')).toHaveCount(0)
      await graphLayout(page, count)
      const last = width > 800 ? count - 1 : 0
      await expect(node(last)).toBeFocused()
      await page.keyboard.press('Enter')
      await selectedName(last)
      await app.closeDetails()
      await expect(node(last)).toBeFocused()
      await graphLayout(page, count)
      // Changing from diagram to list keeps selection and the same details.
      await page.locator('.network-graph-view-toggle').click()
      const rows = page.locator('.network-graph-list-peer')
      await expect(rows.nth(last)).toHaveAttribute('aria-pressed', 'true')
      await rows.nth(1).focus()
      await page.keyboard.press('Space')
      await selectedName(1)
      await app.closeDetails()
      await expect(rows.nth(1)).toBeFocused()
      await page.locator('.network-graph-view-toggle').click()
      await graphLayout(page, count)
      await page.locator('.network-map-button').click()
      await expect(page.locator('.network-graph-node[aria-pressed="true"], .network-graph-selected-facts')).toHaveCount(0)
      await graphLayout(page, count)
    }
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await page.setViewportSize(viewport(1180))
    await node(0).click()
    await node(1).click()
    await graphLayout(page, count)
    await page.goBack()
    await selectedName(0)
    await graphLayout(page, count)
    await page.goForward()
    await selectedName(1)
    await graphLayout(page, count)
    await expect(page.locator('.network-graph [data-direction], .network-graph .has-transfer, .network-graph animate, .network-graph animateMotion')).toHaveCount(0)
    expect(await page.locator('.network-graph button').evaluateAll(elements => elements.every(element => getComputedStyle(element).transitionDuration.split(',').every(value => parseFloat(value) === 0)))).toBe(true)
    await app.closeDetails()
    await expect(node(1)).toBeFocused()
    await graphLayout(page, count)
    await app.capture(`graph-selection-${locale}-${count}-peers`)
    expect(await page.evaluate(() => window.__sobaQA.commands.length), 'Layout inspection does not issue application commands').toBe(0)
  })
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
  await page.locator('.network-graph-view-toggle').focus()
  await page.mouse.move(5, 5)
  const edge = page.locator('.network-graph-edge').first()
  const read = () => edge.evaluate(async element => {
    await Promise.allSettled(element.getAnimations().map(animation => animation.finished))
    const style = getComputedStyle(element)
    const canvas = document.createElement('canvas')
    canvas.width = canvas.height = 1
    const context = canvas.getContext('2d')
    const rgba = color => { context.clearRect(0, 0, 1, 1); context.fillStyle = color; context.fillRect(0, 0, 1, 1); return [...context.getImageData(0, 0, 1, 1).data].map((value, index) => index === 3 ? value / 255 : value) }
    const over = (front, back) => {
      const alpha = front[3] + back[3] * (1 - front[3])
      return [...front.slice(0, 3).map((value, index) => alpha ? (value * front[3] + back[index] * back[3] * (1 - front[3])) / alpha : 0), alpha]
    }
    // Composite each complete layer before applying its opacity, including
    // transparent label/SVG backgrounds and opacity on any ancestor.
    const composite = (node, paint) => {
      for (let current = node; current; current = current.parentElement) {
        const currentStyle = getComputedStyle(current)
        paint = over(paint, rgba(currentStyle.backgroundColor))
        paint[3] *= parseFloat(currentStyle.opacity)
      }
      return over(paint, [255, 255, 255, 1])
    }
    const luminance = color => color.slice(0, 3).map(value => value / 255).map(value => value <= .04045 ? value / 12.92 : ((value + .055) / 1.055) ** 2.4).reduce((sum, value, index) => sum + value * [.2126, .7152, .0722][index], 0)
    const contrast = (a, b) => { const shades = [luminance(a), luminance(b)].sort((a, b) => a - b); return (shades[1] + .05) / (shades[0] + .05) }
    const paintedContrast = (color, node, opacity = 1, underlay = [0, 0, 0, 0]) => {
      const foreground = rgba(color)
      foreground[3] *= opacity
      return contrast(composite(node, over(foreground, underlay)), composite(node, underlay))
    }
    const box = element.getBoundingClientRect()
    const label = element.querySelector('.network-graph-path')
    const labelStyle = getComputedStyle(label)
    const labelRange = document.createRange()
    labelRange.selectNodeContents(label)
    const labelVisible = Boolean(label.textContent.trim()) && labelStyle.visibility === 'visible' && parseFloat(labelStyle.fontSize) >= 14 && [...labelRange.getClientRects()].some(rect => rect.width > 0 && rect.height > 0 && rect.left >= box.left && rect.right <= box.right && rect.top >= box.top && rect.bottom <= box.bottom)
    const glyph = element.querySelector('.network-graph-route-glyph')
    const iconContrasts = [...element.querySelectorAll('.network-graph-route-glyph path, .network-graph-route-glyph circle, .network-graph-route-glyph rect')].flatMap(shape => {
      const shapeStyle = getComputedStyle(shape)
      const bounds = shape.getBoundingClientRect()
      if (shapeStyle.visibility !== 'visible' || !(bounds.width > 0 || bounds.height > 0) || shapeStyle.stroke === 'none' || parseFloat(shapeStyle.strokeWidth) <= 0) return []
      return [paintedContrast(shapeStyle.stroke, shape, parseFloat(shapeStyle.strokeOpacity))]
    })
    const peerId = element.closest('[data-peer-id]').getAttribute('data-peer-id')
    const group = [...element.closest('.network-graph').querySelectorAll('.network-graph-connectors [data-peer-id]')].find(group => group.getAttribute('data-peer-id') === peerId)
    const line = group.querySelector('.network-graph-line')
    const lineStyle = getComputedStyle(line)
    const trackStyle = getComputedStyle(group.querySelector('.network-graph-line-track'))
    const underlay = rgba(trackStyle.stroke)
    underlay[3] *= parseFloat(trackStyle.strokeOpacity) * parseFloat(trackStyle.opacity)
    return {
      borderColor: style.borderTopColor, backgroundColor: style.backgroundColor,
      outlineWidth: parseFloat(style.outlineWidth), outlineStyle: style.outlineStyle,
      width: box.width, height: box.height, labelVisible,
      labelContrast: paintedContrast(labelStyle.color, label),
      hasIcon: Boolean(glyph), iconContrast: Math.max(0, ...iconContrasts),
      connectorVisible: lineStyle.visibility === 'visible' && lineStyle.stroke !== 'none' && parseFloat(lineStyle.strokeWidth) > 0 && line.getTotalLength() > 0,
      lineContrast: paintedContrast(lineStyle.stroke, group, parseFloat(lineStyle.strokeOpacity) * parseFloat(lineStyle.opacity), underlay),
    }
  })
  const readableRoute = async () => {
    await expect(edge).toBeVisible()
    await expect(edge).toHaveAccessibleName(/\S/)
    await expect.poll(async () => {
      const metrics = await read()
      return metrics.labelVisible && metrics.labelContrast >= 4.5 && (!metrics.hasIcon || metrics.iconContrast >= 3)
    }, { message: 'The route button has an actually painted readable label and contrasting route icon when present' }).toBe(true)
    const metrics = await read()
    expect(metrics.width, 'The route button retains a 44 CSS px wide hit area').toBeGreaterThanOrEqual(44)
    expect(metrics.height, 'The route button retains a 44 CSS px high hit area').toBeGreaterThanOrEqual(44)
    expect(metrics.connectorVisible, 'The meaningful connector remains painted').toBe(true)
    expect(metrics.lineContrast, 'Meaningful connector lines contrast with their effective surface').toBeGreaterThanOrEqual(3)
    return metrics
  }
  await expect(edge).not.toHaveClass(/is-selected/)
  await expect(edge).toHaveAttribute('aria-pressed', 'false')
  const normal = await readableRoute()
  await app.capture(`edge-${tag}-normal`)
  await edge.hover()
  await expect.poll(async () => {
    const hovered = await read()
    return hovered.borderColor !== normal.borderColor || hovered.backgroundColor !== normal.backgroundColor
  }, { message: 'Hover visibly identifies the route badge' }).toBe(true)
  await readableRoute()
  await app.capture(`edge-${tag}-hover`)
  await page.mouse.move(5, 5)
  await page.locator('.network-graph-self').focus()
  await page.keyboard.press('Tab')
  await expect(edge).toBeFocused()
  const focused = await readableRoute()
  expect(focused.outlineWidth).toBeGreaterThanOrEqual(2)
  expect(focused.outlineStyle).not.toBe('none')
  await app.capture(`edge-${tag}-focus`)
  await page.keyboard.press('Enter')
  await expect(page.locator('.details-panel')).toBeVisible()
  await app.closeDetails()
  await page.locator('.network-graph-view-toggle').focus()
  await expect(edge).toHaveClass(/is-selected/)
  await expect(edge).toHaveAttribute('aria-pressed', 'true')
  const selected = await readableRoute()
  expect(selected.borderColor !== normal.borderColor || selected.backgroundColor !== normal.backgroundColor, 'Selection visibly identifies the route badge').toBe(true)
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
      if (width <= 390) {
        const sizes = await page.locator('.primary-nav button, .network-graph-view-toggle').evaluateAll(elements => elements.map(element => ({ width: element.getBoundingClientRect().width, height: element.getBoundingClientRect().height, clipped: element.scrollWidth > element.clientWidth + 1 })))
        expect(sizes.every(size => size.width >= 44 && size.height >= 44 && !size.clipped), 'Narrow navigation remains readable and operable').toBe(true)
      }
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
