import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { mkdir, mkdtemp, readFile, rm, stat, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

// Every interaction goes through the pinned official agent-browser CLI. This
// script never imports another browser driver or replaces an application route.
const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const cli = join(root, 'qa-agent-browser/node_modules/agent-browser/bin/agent-browser.js')
const flag = process.argv.indexOf('--session-file')
const sessionFile = flag < 0 ? process.env.SOBA_E2E_SESSION_FILE : process.argv[flag + 1]
const output = resolve(process.env.SOBA_SCREENSHOT_DIR || '/tmp/sobalink-agent-browser-artifacts')
const report = {
  tool: 'agent-browser', version: '0.38.2', fixture: 'actual Go HTTP with fictional in-process peers',
  startedAt: new Date().toISOString(), journeys: [], screenshots: [], snapshots: [], typography: [], geometry: [], edgeStyles: [],
  languageCoverage: { en: 'Complete mutation journeys and responsive layouts.', ja: 'Graph/list and conversation layouts in both themes at every width, keyboard/details navigation, explicit message send and incoming-batch acceptance. Remaining mutation journeys use English.' },
  manualInspection: 'Required: inspect actual PNGs for spacing, clipping, Japanese glyph appearance and contrast.',
  limits: ['Synthetic composition events are not native IME acceptance.', 'In-process peers are not real-device enrollment or delivery.', 'Service listener readiness is not remote application compatibility.', 'OS login and suspend/resume are not exercised.'],
}
let authenticated = false
let launched = false
let currentJourney = 'private session validation'
let session
let temporary
let locale = 'en'
let cliCalls = 0
const categories = new Map()
const browserSession = `soba-journeys-${process.pid}-${Date.now()}`
const studioRequired = [
  'navigation: device list and network graph are directly reachable',
  ...['en', 'ja'].flatMap(language => ['light', 'dark'].map(theme => `graph ${language} ${theme}: explicit diagram/list, resizing and details`)),
  'conversation: explicit send, Enter, composition and validation recovery',
  'conversation: Japanese/English light/dark responsive layouts',
  'incoming batch: explicit acceptance and saved result',
  'file upload: preview, cancel, explicit staging and outgoing cancel',
  'file upload: failed staging and idempotent manual retry',
  'preferences: locale/theme persistence and invalid directory recovery',
  'trust and autosave: revoke, allow, opt-in, pause impact, cancel, resume and revoke',
  'services: invalid range, preview and Escape focus',
  'services: actual start, listener state and stop',
  'services: authoritative copy, retained draft and reviewed restart',
]
const words = {
  en: { settings: 'Preferences', light: 'Light', dark: 'Dark', close: 'Close', details: 'Open device details', closeDetails: 'Close device details', send: 'Send message', accept: 'Accept batch', trust: 'Allow communication', revokeTrust: 'Revoke permission', autosave: 'Enable for this device', pause: 'Pause messages and files', resume: 'Resume messages and files', revoke: 'Turn off', cancel: 'Cancel', stop: 'Stop', setup: 'Set up network', activateRelay: 'Activate selected relay' },
  ja: { settings: '表示設定', light: 'ライト', dark: 'ダーク', close: '閉じる', details: '詳細を表示', closeDetails: '詳細を閉じる', send: 'メッセージを送信', accept: 'まとめて受信', trust: '通信を許可', revokeTrust: '許可を取り消す', autosave: 'このデバイスから自動受信', pause: 'メッセージとファイルを停止', resume: 'メッセージとファイルを再開', revoke: '解除', cancel: 'キャンセル', stop: '停止', setup: 'ネットワークを設定', activateRelay: '選択したリレーを有効にする' },
}
const label = key => words[locale][key]
const cssString = value => JSON.stringify(value)
const visibleJS = selector => `Boolean(document.querySelector(${JSON.stringify(selector)})?.getClientRects().length)`
const unavailable = (name, reason) => report.journeys.push({ name, status: 'unavailable', reason })

function safeText(value) {
  let text = String(value)
  for (const secret of [session?.code, session?.url, session?.receiveDirectory, temporary, sessionFile]) {
    if (secret) text = text.split(secret).join('[redacted]')
  }
  return text.replace(/\b[a-f0-9]{64,}\b/gi, '[identity redacted]')
    .replace(/(?:\/tmp\/|\/home\/|\/Users\/)[^\s"<>]+/g, '[fixture path]')
}

async function browser(args, input) {
  cliCalls++
  categories.set(args[0], (categories.get(args[0]) || 0) + 1)
  // Exclude ambient profiles, auth, proxy, cloud providers and tracing options.
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !/^AGENT_BROWSER_|^(HTTP|HTTPS|ALL|NO)_PROXY$|^(http|https|all|no)_proxy$/.test(key)))
  Object.assign(env, { AGENT_BROWSER_SESSION: browserSession, AGENT_BROWSER_NAMESPACE: browserSession, AGENT_BROWSER_NO_WEBMCP: '1', AGENT_BROWSER_RESTORE_SAVE: 'never', AGENT_BROWSER_AUTOSAVE_INTERVAL_MS: '0', AGENT_BROWSER_DEFAULT_TIMEOUT: '15000' })
  const result = await new Promise((resolveResult, reject) => {
    const child = spawn(process.execPath, [cli, '--json', '--allowed-domains', '127.0.0.1', ...args], { env, stdio: ['pipe', 'pipe', 'pipe'] })
    let stdout = ''
    // Intentionally do not retain stderr, which may include command arguments.
    child.stdout.on('data', chunk => { stdout += chunk; if (stdout.length > 4_000_000) child.kill('SIGTERM') })
    child.stderr.on('data', () => {})
    child.on('error', () => reject(new Error(`CLI unavailable during ${currentJourney}`)))
    const timer = setTimeout(() => child.kill('SIGTERM'), 45000)
    child.on('close', code => { clearTimeout(timer); resolveResult({ code, stdout }) })
    child.stdin.end(input)
  })
  assert.equal(result.code, 0, `agent-browser ${args[0]} failed during ${currentJourney}; raw output withheld`)
  let parsed
  try { parsed = JSON.parse(result.stdout) } catch { throw new Error(`agent-browser ${args[0]} returned invalid JSON; raw output withheld`) }
  assert.notEqual(parsed.success, false, `agent-browser ${args[0]} rejected during ${currentJourney}; raw output withheld`)
  if (Array.isArray(parsed)) for (const row of parsed) assert.notEqual(row.success, false, `agent-browser batch action failed during ${currentJourney}; raw output withheld`)
  return parsed.data ?? parsed
}

async function evaluate(source) {
  const result = await browser(['eval', '--stdin'], source)
  return Object.hasOwn(result, 'result') ? result.result : result
}
const wait = source => browser(['wait', '--fn', source])
const click = selector => browser(['click', selector])
const fill = (selector, value) => browser(['fill', selector, value])
const press = key => browser(['press', key])
const button = (name, scope) => scope
  ? click(`${scope} button[aria-label=${cssString(name)}]`)
  : browser(['find', 'role', 'button', 'click', '--name', name, '--exact'])
const check = async (source, message) => assert.equal(await evaluate(`(async () => Boolean(await (${source})))()`), true, message)
const count = name => evaluate(`(window.__sobaQA?.commands || []).filter(x => x === ${JSON.stringify(name)}).length`)
const noOverflow = () => check('document.documentElement.scrollWidth <= innerWidth + 1', 'page must not overflow horizontally')
const stateCheck = condition => check(`(async () => { const response = await fetch('/api/state'); if (!response.ok) return false; const state = await response.json(); return Boolean(${condition}); })()`, 'real Go state must confirm the requested result')
async function stateWait(condition) {
  // Poll through the normal authenticated read-only endpoint, projecting only a
  // boolean. Cookies, CSRF values, identities, paths and message bodies stay in-page.
  for (let attempt = 0; attempt < 30; attempt++) {
    if (await evaluate(`(async () => { const response = await fetch('/api/state'); if (!response.ok) return false; const state = await response.json(); return Boolean(${condition}); })()`)) return
    await browser(['wait', '250'])
  }
  throw new Error('real Go state did not confirm the requested result')
}

async function auditInstrumentation() {
  await evaluate(`(() => {
    if (window.__sobaQA) return true;
    const qa = {commands: [], errors: 0, cspViolations: 0, uploads: 0, uploadIDs: [], duplicateUploadIdentity: false};
    window.__sobaQA = qa;
    window.addEventListener('error', () => qa.errors++);
    window.addEventListener('unhandledrejection', () => qa.errors++);
    window.addEventListener('securitypolicyviolation', () => qa.cspViolations++);
    const originalFetch = window.fetch;
    window.fetch = function(input, init) {
      const path = new URL(typeof input === 'string' ? input : input.url, location.href).pathname;
      if (path === '/api/command' && init?.body) { try { const command = JSON.parse(init.body); if (typeof command.name === 'string') qa.commands.push(command.name); } catch {} }
      return originalFetch.apply(this, arguments);
    };
    const originalSend = XMLHttpRequest.prototype.send;
    XMLHttpRequest.prototype.send = function(body) {
      if (body instanceof FormData && body.has('manifest')) {
        qa.uploads++;
        const id = body.get('requestId');
        qa.duplicateUploadIdentity = qa.uploadIDs.includes(id);
        qa.uploadIDs.push(id);
      }
      return originalSend.apply(this, arguments);
    };
    return true;
  })()`)
}

async function snapshot(name, selector = '.workspace') {
  assert.ok(authenticated, 'no snapshots before authenticated workspace')
  await check(`!document.querySelector('.login-panel') && !Array.from(document.querySelectorAll('input[type=password], .private-copy')).some(el => el.value)`, 'private controls must be absent or empty before capture')
  const result = await browser(['snapshot', '-c', '-s', selector])
  const text = typeof result === 'string' ? result : result.snapshot ?? result.tree
  assert.equal(typeof text, 'string', 'snapshot must contain an accessibility tree')
  const filename = `${name}.txt`
  await writeFile(join(output, filename), safeText(text), { mode: 0o600 })
  report.snapshots.push(filename)
}
async function capture(name) {
  assert.ok(authenticated, 'no screenshots before authenticated workspace')
  await check(`!document.querySelector('.login-panel') && !Array.from(document.querySelectorAll('input[type=password], .private-copy')).some(el => el.value)`, 'private controls must be absent or empty before capture')
  await noOverflow()
  const filename = `${name}.png`
  await browser(['screenshot', join(output, filename), '--full'])
  report.screenshots.push(filename)
}
async function journey(name, task) {
  currentJourney = name
  const start = Date.now()
  try {
    await task()
    if (authenticated) await check('window.__sobaQA?.errors === 0 && window.__sobaQA?.cspViolations === 0', 'journey must not introduce browser exceptions or CSP violations')
    report.journeys.push({ name, status: 'passed', durationMs: Date.now() - start })
    console.log(`PASS ${name}`)
  } catch (error) {
    report.journeys.push({ name, status: 'failed', assertion: safeText(error.message), durationMs: Date.now() - start })
    throw error
  }
}

async function appearance(nextLocale, theme) {
  await click('.app-header .header-actions button.icon-button')
  await browser(['select', 'dialog select', nextLocale])
  locale = nextLocale
  await button(label(theme))
  await press('Escape')
  await wait(`document.documentElement.lang === ${JSON.stringify(locale)} && document.documentElement.dataset.theme === ${JSON.stringify(theme)} && !document.querySelector('dialog[open]')`)
}
async function closeDetails() {
  if (await evaluate(visibleJS('.details-panel'))) await click('.details-heading button')
}
async function openPeer() {
  await closeDetails()
  await browser(['set', 'viewport', '1440', '960'])
  await browser(['find', 'first', '.device-row', 'click'])
  await wait(visibleJS('.composer textarea'))
}
async function showMap() {
  await closeDetails()
  if (await evaluate(visibleJS('.network-graph'))) return
  if (!(await evaluate(visibleJS('.network-map-button')))) await click('.mobile-back')
  await click('.network-map-button')
  await wait(visibleJS('.network-graph'))
}
async function graphView(view) {
  const current = await evaluate('document.querySelector(".network-graph")?.dataset.view')
  if (current !== view) {
    // Both the original single toggle and the replacement segmented controls
    // expose aria-controls pointing to the selected representation.
    const selector = await evaluate(`(() => {
      const root = document.querySelector('.network-graph');
      const candidate = Array.from(root.querySelectorAll('button[aria-controls]')).find(el => el.getAttribute('aria-controls').endsWith('-${view}'));
      return candidate ? 'button[aria-controls=' + JSON.stringify(candidate.getAttribute('aria-controls')) + ']' : '.network-graph-view-toggle';
    })()`)
    await click(selector)
  }
  await wait(`document.querySelector('.network-graph')?.dataset.view === '${view}'`)
  await check(visibleJS(`.network-graph-${view === 'diagram' ? 'diagram' : 'list'}`), `${view} must actually render after its explicit selection`)
}

async function graphTypography(tag) {
  const metrics = await evaluate(`(async () => {
    await document.fonts.ready;
    const selectors = ['.network-graph-header h2', '.network-graph-node-kind', '.network-graph-node strong', '.network-graph-path', '.network-graph-transfer', '.network-graph-list-peer strong'];
    return selectors.flatMap(selector => Array.from(document.querySelectorAll(selector)).filter(el => el.getClientRects().length).slice(0, 4).map(el => {
      const style = getComputedStyle(el), range = document.createRange();
      const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT); let text;
      while ((text = walker.nextNode()) && !text.textContent.trim()) {}
      if (text) range.selectNodeContents(text);
      const rects = text ? Array.from(range.getClientRects()) : [el.getBoundingClientRect()];
      const measuredGlyphHeight = Math.max(0, ...rects.map(rect => rect.height));
      let transformed = false; for (let parent = el; parent && parent !== document.body; parent = parent.parentElement) { if (getComputedStyle(parent).transform !== 'none') transformed = true; }
      return { selector, fontSize: parseFloat(style.fontSize), lineHeight: parseFloat(style.lineHeight), fontFamily: style.fontFamily, measuredGlyphHeight, svgText: Boolean(el.closest('foreignObject')), transformed };
    }));
  })()`)
  assert.ok(metrics.length >= 3, 'graph must expose measurable labels')
  for (const metric of metrics) {
    assert.ok(metric.fontSize >= (metric.selector.endsWith('strong') ? 14 : 12), 'graph labels must retain their intended readable size')
    assert.equal(metric.svgText, false, 'graph labels must not scale inside SVG foreignObject')
    assert.equal(metric.transformed, false, 'graph labels must keep their declared CSS pixel size')
    assert.ok(metric.measuredGlyphHeight > 0 && metric.measuredGlyphHeight <= metric.lineHeight * 1.3, 'rendered graph glyph height must agree with its text metrics')
  }
  report.typography.push({ tag, metrics })
}

async function graphGeometry(tag) {
  await wait(`(() => {
    const svg = document.querySelector('.network-graph-connectors');
    const paths = Array.from(svg?.querySelectorAll('.network-graph-line') || []);
    if (!svg || !svg.getClientRects().length || !paths.length) return false;
    const width = svg.clientWidth, height = svg.clientHeight;
    return paths.every(path => {
      const d = path.getAttribute('d') || '';
      if (!d || /NaN|Infinity|undefined/.test(d)) return false;
      const box = path.getBBox();
      return path.getTotalLength() > 0 && box.width > 0 && box.height >= 0 && box.x >= -1 && box.y >= -1 && box.x + box.width <= width + 1 && box.y + box.height <= height + 1;
    });
  })()`)
  const paths = await evaluate(`Array.from(document.querySelectorAll('.network-graph-connectors .network-graph-line')).map(path => { const box = path.getBBox(); return { length: path.getTotalLength(), x: box.x, y: box.y, width: box.width, height: box.height }; })`)
  report.geometry.push({ tag, paths })
}

async function edgeAppearance(language, theme) {
  await browser(['set', 'viewport', '1440', '960'])
  await closeDetails()
  await click('.network-map-button')
  await graphView('diagram')
  await browser(['mouse', 'move', '5', '5'])
  const inspect = () => evaluate(`(() => {
    const element = document.querySelector('.network-graph-edge'), style = getComputedStyle(element);
    const canvas = document.createElement('canvas'); canvas.width = canvas.height = 1;
    const context = canvas.getContext('2d');
    const rgba = color => { context.clearRect(0, 0, 1, 1); context.fillStyle = color; context.fillRect(0, 0, 1, 1); return Array.from(context.getImageData(0, 0, 1, 1).data); };
    return { borderWidth: parseFloat(style.borderTopWidth), borderColor: style.borderTopColor, borderRGBA: rgba(style.borderTopColor), backgroundColor: style.backgroundColor, backgroundRGBA: rgba(style.backgroundColor), outlineWidth: parseFloat(style.outlineWidth), outlineStyle: style.outlineStyle, selected: element.classList.contains('is-selected'), focused: element === document.activeElement, focusVisible: element.matches(':focus-visible') };
  })()`)
  const normal = await inspect()
  assert.equal(normal.selected, false, 'normal connection card must be observed before selection')
  assert.ok(normal.borderWidth >= 1 && normal.borderRGBA[3] > 0, 'normal connection card must have a nontransparent border')
  assert.notDeepEqual(normal.borderRGBA, normal.backgroundRGBA, 'normal border must be visually distinct from the card background')
  const luminance = rgba => rgba.slice(0, 3).map(value => value / 255).map(value => value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4).reduce((sum, value, index) => sum + value * [0.2126, 0.7152, 0.0722][index], 0)
  const shades = [luminance(normal.borderRGBA), luminance(normal.backgroundRGBA)].sort((a, b) => a - b)
  normal.borderContrast = (shades[1] + 0.05) / (shades[0] + 0.05)
  assert.ok(normal.borderContrast >= 3, 'normal connection-card outline must reach3:1 contrast against its surface')
  await capture(`edge-${language}-${theme}-normal`)
  await browser(['find', 'first', '.network-graph-edge', 'hover'])
  const hovered = await inspect()
  assert.ok(hovered.borderColor !== normal.borderColor || hovered.backgroundColor !== normal.backgroundColor, 'hover must visibly identify the interactive connection card')
  await capture(`edge-${language}-${theme}-hover`)
  await browser(['mouse', 'move', '5', '5'])
  await browser(['focus', '.network-graph-self'])
  await press('Tab')
  const focused = await inspect()
  assert.ok(focused.focused && focused.focusVisible && focused.outlineWidth >= 2 && focused.outlineStyle !== 'none', 'keyboard focus must have a visible outline on the connection card')
  await capture(`edge-${language}-${theme}-focus`)
  await press('Enter')
  await wait(visibleJS('.details-panel'))
  await closeDetails()
  await browser(['focus', '.network-graph-view-toggle'])
  const selected = await inspect()
  assert.equal(selected.selected, true, 'connection card must retain its selected state after details close')
  assert.ok(selected.borderColor !== normal.borderColor || selected.backgroundColor !== normal.backgroundColor, 'selected connection card must have a distinct appearance')
  await capture(`edge-${language}-${theme}-selected`)
  report.edgeStyles.push({ language, theme, normal, hovered, focused, selected })
}

async function navigationJourney() {
  await journey('navigation: device list and network graph are directly reachable', async () => {
    for (const width of [1180, 390]) {
      await browser(['set', 'viewport', String(width), width === 390 ? '844' : '960'])
      await click('.devices-view-button')
      await wait(`document.querySelector('.network-graph')?.dataset.view === 'list'`)
      await check(visibleJS('.network-graph-list'), 'device list opens directly from global navigation')
      await check(`!document.querySelector('.network-graph-diagram') && document.querySelector('.devices-view-button').getAttribute('aria-current') === 'page'`, 'direct list navigation must not render the graph first')
      await capture(`direct-device-list-${width}`)
      await browser(['find', 'first', '.network-graph-list-peer', 'click'])
      await wait(visibleJS('.details-panel'))
      await press('Escape')
      await wait(`!document.querySelector('.details-panel')`)
      await check(visibleJS('.network-graph-list'), 'details return to the directly selected list')
      await click('.network-map-button')
      await wait(`document.querySelector('.network-graph')?.dataset.view === 'diagram'`)
      await check(visibleJS('.network-graph-diagram'), 'network graph is an equal global destination')
      await check(`document.querySelector('.network-map-button').getAttribute('aria-current') === 'page'`, 'navigation highlights the actual graph view')
    }
    await check('window.__sobaQA.commands.length === 0', 'navigation must not issue a mutation')
  })
}

async function graphMatrix() {
  for (const language of ['en', 'ja']) for (const theme of ['light', 'dark']) {
    await journey(`graph ${language} ${theme}: explicit diagram/list, resizing and details`, async () => {
      await browser(['set', 'viewport', '1440', '960'])
      await appearance(language, theme)
      await showMap()
      await graphView('diagram')
      const diagramHeading = await evaluate('document.querySelector(".network-graph-header h2").textContent.trim()')
      assert.match(diagramHeading, language === 'ja' ? /ネットワーク/ : /Network/, 'diagram heading must describe its representation')
      await graphView('list')
      const listHeading = await evaluate('document.querySelector(".network-graph-header h2").textContent.trim()')
      assert.notEqual(listHeading, diagramHeading, 'list heading must change with the selected representation')
      assert.match(listHeading, language === 'ja' ? /一覧|デバイス/ : /[Ll]ist|Devices/, 'list heading must describe the list')
      await capture(`graph-${language}-${theme}-list-1440`)
      await snapshot(`graph-${language}-${theme}-list`)
      await browser(['back'])
      await wait(`document.querySelector('.network-graph')?.dataset.view === 'diagram'`)
      await browser(['forward'])
      await wait(`document.querySelector('.network-graph')?.dataset.view === 'list'`)
      await graphView('diagram')
      for (const width of [1440, 1180, 960, 390]) {
        await browser(['set', 'viewport', String(width), width === 390 ? '844' : '960'])
        await check(visibleJS('.network-graph-diagram'), 'explicit graph selection must survive resizing')
        await check(`document.querySelector('.network-graph')?.dataset.view === 'diagram'`, 'resizing must not silently replace the chosen graph with a list')
        await graphGeometry(`${language}-${theme}-${width}`)
        await graphTypography(`${language}-${theme}-${width}`)
        await capture(`graph-${language}-${theme}-diagram-${width}`)
        if (width >= 960) {
          await browser(['find', 'first', '.network-graph-diagram .network-graph-node:not(.network-graph-self)', 'click'])
          await wait(visibleJS('.details-panel'))
          await check(visibleJS('.network-graph-diagram'), 'opening details on desktop must preserve the diagram')
          await graphGeometry(`${language}-${theme}-details-${width}`)
          await capture(`graph-${language}-${theme}-details-${width}`)
          await closeDetails()
          await check(visibleJS('.network-graph-diagram'), 'closing details must preserve the diagram')
        }
      }
      await browser(['set', 'viewport', '1440', '960'])
      await browser(['find', 'first', '.network-graph-edge', 'click'])
      await wait(visibleJS('.details-panel'))
      await closeDetails()
      await browser(['focus', '.network-graph-diagram .network-graph-node:not(.network-graph-self)'])
      await check(`document.activeElement.matches('.network-graph-node')`, 'graph node must accept keyboard focus')
      await press('Enter')
      await wait(visibleJS('.details-panel'))
      await press('Escape')
      await wait(`!document.querySelector('.details-panel')`)
      await check(`document.activeElement.matches('.network-graph-node')`, 'details dismissal must restore graph-node focus')
      await edgeAppearance(language, theme)
    })
  }
}

async function conversationJourneys() {
  await journey('conversation: explicit send, Enter, composition and validation recovery', async () => {
    await appearance('en', 'light')
    await openPeer()
    const baseline = await count('message.send')
    await fill('.composer textarea', 'Agent browser draft')
    await press('Enter')
    assert.equal(await count('message.send'), baseline, 'Enter must not send')
    await evaluate(`(() => { const input = document.querySelector('.composer textarea'); input.dispatchEvent(new CompositionEvent('compositionstart', {bubbles: true, data: 'あ'})); input.dispatchEvent(new KeyboardEvent('keydown', {key: 'Enter', code: 'Enter', ctrlKey: true, isComposing: true, bubbles: true})); input.dispatchEvent(new CompositionEvent('compositionend', {bubbles: true, data: 'あ'})); return true; })()`)
    assert.equal(await count('message.send'), baseline, 'composition confirmation must not send')
    await fill('.composer textarea', 'あ'.repeat(5462))
    await click('.send-button')
    await wait(`Array.from(document.querySelectorAll('[role=alert]')).some(el => el.textContent.includes('16 KiB'))`)
    assert.equal(await count('message.send'), baseline, 'oversized UTF-8 text must remain local')
    await check(`document.querySelector('.composer textarea').value.length === 5462`, 'invalid send preserves draft')
    await fill('.composer textarea', 'Agent browser: explicit send')
    await click('.send-button')
    await wait(`document.querySelector('.composer textarea')?.value === ''`)
    assert.equal(await count('message.send'), baseline + 1, 'explicit click must issue exactly one command')
    await stateWait(`state.messages.some(message => message.direction === 'outgoing' && message.text === 'Agent browser: explicit send' && message.status === 'sent')`)
    await fill('.composer textarea', 'Draft survives navigation')
    await showMap()
    await browser(['back'])
    await wait(visibleJS('.conversation-header'))
    await check(`document.querySelector('.composer textarea').value === 'Draft survives navigation'`, 'Back restores the conversation and its unsent draft')
    await browser(['forward'])
    await wait(visibleJS('.network-graph'))
    await openPeer()
    await check(`document.querySelector('.composer textarea').value === 'Draft survives navigation'`, 'reopening preserves the unsent draft')
    await fill('.composer textarea', '')
    await appearance('ja', 'dark')
    await fill('.composer textarea', '表示確認用の資料を共有します。内容を確認してください。')
    await click('.send-button')
    await wait(`document.querySelector('.composer textarea')?.value === ''`)
    await stateWait(`state.messages.some(message => message.direction === 'outgoing' && message.text === '表示確認用の資料を共有します。内容を確認してください。' && message.status === 'sent')`)
    assert.equal(await count('message.send'), baseline + 2, 'Japanese explicit send submits exactly one more message')
    await appearance('en', 'light')
  })
  await journey('incoming batch: explicit acceptance and saved result', async () => {
    await openPeer()
    await appearance('ja', 'dark')
    await wait(visibleJS('.transfer-card.incoming-offer'))
    const before = await count('transfer.accept')
    await click('.transfer-card.incoming-offer .transfer-actions .button-primary')
    await stateWait(`state.transfers.some(item => item.direction === 'incoming' && item.status === 'completed')`)
    assert.equal(await count('transfer.accept'), before + 1, 'receive must submit one explicit acceptance')
    await snapshot('incoming-batch-saved')
    await capture('incoming-batch-saved')
    await appearance('en', 'light')
  })
  await journey('file upload: preview, cancel, explicit staging and outgoing cancel', async () => {
    await openPeer()
    const file = join(temporary, 'qa-notes.txt')
    await writeFile(file, 'Fictional browser acceptance notes.\n', { mode: 0o600 })
    const uploadSelector = '.conversation input[type=file]:not([webkitdirectory])'
    await browser(['upload', uploadSelector, file])
    await wait(visibleJS('.batch-preview'))
    const addition = join(temporary, 'qa-extra.txt')
    await writeFile(addition, 'Second fictional attachment.\n', { mode: 0o600 })
    await browser(['upload', uploadSelector, addition])
    await check(`document.querySelector('.batch-preview').textContent.includes('qa-notes.txt') && document.querySelector('.batch-preview').textContent.includes('qa-extra.txt')`, 'adding files preserves the first selection')
    await button('Remove from batch: qa-extra.txt')
    await check(`document.querySelector('.batch-preview').textContent.includes('qa-notes.txt') && !document.querySelector('.batch-preview').textContent.includes('qa-extra.txt')`, 'removing one item preserves the remaining manifest')
    await check('window.__sobaQA.uploads === 0', 'selection alone must not stage an upload')
    await capture('file-batch-preview')
    await click('.batch-heading button')
    await wait(`!document.querySelector('.batch-preview')`)
    await check('window.__sobaQA.uploads === 0', 'preview cancellation must not upload')
    await browser(['upload', uploadSelector, file])
    await wait(visibleJS('.batch-preview'))
    await click('.batch-footer .button-primary')
    if (session.capabilities?.includes('failed-upload-retry')) {
      await wait(`Boolean(document.querySelector('[role=alert]')) && !document.querySelector('.upload-progress')`)
      await check(visibleJS('.batch-preview'), 'failed staging keeps the reviewable draft')
      await check('window.__sobaQA.uploads === 1', 'staging failure must not silently retry')
      await capture('file-batch-failure')
      await click('.batch-footer .button-primary')
      await wait(`!document.querySelector('.batch-preview')`)
      await check('window.__sobaQA.uploads === 2 && window.__sobaQA.duplicateUploadIdentity', 'manual retry keeps the original upload request identity')
      report.journeys.push({ name: 'file upload: failed staging and idempotent manual retry', status: 'passed' })
    } else {
      unavailable('file upload: failed staging and idempotent manual retry', 'fixture does not advertise failed-upload-retry')
      await wait(`!document.querySelector('.batch-preview')`)
    }
    await stateWait(`state.transfers.some(item => item.direction === 'outgoing' && ['offered','awaiting-acceptance'].includes(item.status))`)
    await browser(['find', 'first', '.timeline-item.outgoing .transfer-actions button', 'click'])
    await stateWait(`state.transfers.some(item => item.direction === 'outgoing' && item.status === 'cancelled')`)
  })
}

async function conversationMatrix() {
  await journey('conversation: Japanese/English light/dark responsive layouts', async () => {
    await openPeer()
    await fill('.composer textarea', 'Responsive draft')
    for (const language of ['en', 'ja']) for (const theme of ['light', 'dark']) {
      await appearance(language, theme)
      for (const width of [1440, 1180, 960, 390]) {
        await browser(['set', 'viewport', String(width), width === 390 ? '844' : '960'])
        await check(visibleJS('.composer textarea'), 'composer stays available at every supported width')
        await check(`parseFloat(getComputedStyle(document.querySelector('.composer textarea')).fontSize) >= 15`, 'message text remains readable')
        await capture(`conversation-${language}-${theme}-${width}`)
      }
      await click('.mobile-back')
      await capture(`devices-${language}-${theme}-390`)
      await browser(['find', 'first', '.device-row', 'click'])
      await check(`document.querySelector('.composer textarea').value === 'Responsive draft'`, 'mobile Back preserves the draft')
      await button(label('details'))
      await capture(`conversation-details-${language}-${theme}-390`)
      await press('Escape')
      await wait(`!document.querySelector('.details-panel')`)
      await check(visibleJS('.composer textarea'), 'Escape returns to the mobile conversation')
      await browser(['set', 'viewport', '1440', '960'])
    }
    await fill('.composer textarea', '')
    await appearance('en', 'light')
  })
}

async function preferencesJourney() {
  await journey('preferences: locale/theme persistence and invalid directory recovery', async () => {
    await appearance('ja', 'dark')
    await browser(['reload'])
    await wait(visibleJS('.workspace'))
    await check(`document.documentElement.lang === 'ja' && document.documentElement.dataset.theme === 'dark'`, 'locale and theme persist after reload')
    await auditInstrumentation()
    await appearance('en', 'light')
    await click('.app-header .header-actions button.icon-button')
    const original = await evaluate('document.querySelector("dialog form input").value')
    assert.ok(original, 'fixture must configure an existing receive directory')
    await fill('dialog form input', 'relative/received')
    await click('dialog button[type=submit]')
    await wait(`Boolean(document.querySelector('dialog [role=alert]'))`)
    await check(`document.querySelector('dialog form input').value === 'relative/received'`, 'invalid directory is retained for correction')
    await fill('dialog form input', original)
    await click('dialog button[type=submit]')
    await wait(`Array.from(document.querySelectorAll('dialog [role=status]')).some(el => el.textContent.includes('Preferences saved'))`)
    await press('Escape')
    await wait(`!document.querySelector('dialog[open]')`)
    await check(`document.activeElement.matches('.app-header .header-actions button.icon-button')`, 'dialog Escape restores trigger focus')
  })
}

async function permissionJourney() {
  await journey('trust and autosave: revoke, allow, opt-in, pause impact, cancel, resume and revoke', async () => {
    await openPeer()
    await button(label('details'))
    await button(label('revokeTrust'))
    await stateWait(`state.peers[0]?.trusted === false`)
    await wait(`!document.querySelector('.composer textarea') || document.querySelector('.composer textarea').disabled`)
    await capture('permission-revoked')
    // Scope avoids the equivalent Allow action in the conversation notice.
    await browser(['find', 'first', '.details-panel .details-section:nth-of-type(2) button', 'click'])
    await stateWait(`state.peers[0]?.trusted === true`)
    await closeDetails()
    await wait(`Boolean(document.querySelector('.composer textarea')) && !document.querySelector('.composer textarea').disabled`)
    await button(label('details'))
    await button(label('autosave'))
    await wait(visibleJS('dialog'))
    await check(`Boolean(document.querySelector('dialog .scope-note'))`, 'autosave must explain its scope')
    await click('dialog button[type=submit]')
    await stateWait(`state.peers[0]?.autosave?.enabled === true && !state.peers[0]?.autosave?.paused`)
    await wait(`!document.querySelector('dialog[open]')`)
    // Keep an actual outgoing offer pending so pause impact is tested, including
    // the cancellation of its staging copy rather than just the dialog copy.
    await closeDetails()
    const pausedFile = join(temporary, 'pause-review.txt')
    await writeFile(pausedFile, 'Fictional batch for pause review.\n', { mode: 0o600 })
    await browser(['upload', '.conversation input[type=file]:not([webkitdirectory])', pausedFile])
    await wait(visibleJS('.batch-preview'))
    await click('.batch-footer .button-primary')
    await wait(`!document.querySelector('.batch-preview')`)
    await stateWait(`state.transfers.some(item => item.direction === 'outgoing' && ['offered','awaiting-acceptance'].includes(item.status))`)
    await button(label('details'))
    await button(label('pause'))
    await wait(visibleJS('dialog'))
    await check(`document.querySelector('dialog').textContent.includes('Original files stay in place')`, 'pause review must explain staging cancellation and original-file preservation')
    await check(visibleJS('.pause-affected'), 'pause review must show the affected outgoing batch')
    await capture('autosave-pause-review')
    await button(label('cancel'))
    await stateCheck(`state.peers[0]?.autosave?.paused !== true`)
    await stateCheck(`state.transfers.some(item => item.direction === 'outgoing' && ['offered','awaiting-acceptance'].includes(item.status))`)
    await button(label('pause'))
    await click('dialog .modal-actions .button-primary')
    await stateWait(`state.peers[0]?.autosave?.paused === true`)
    await stateWait(`!state.transfers.some(item => item.direction === 'outgoing' && ['offered','awaiting-acceptance','queued','transferring'].includes(item.status))`)
    await wait(`!document.querySelector('dialog[open]')`)
    await check(`document.querySelector('.composer textarea').disabled`, 'pause disables new messages')
    await button(label('resume'))
    await stateWait(`state.peers[0]?.autosave?.paused === false`)
    await stateCheck(`!state.transfers.some(item => item.direction === 'outgoing' && ['offered','awaiting-acceptance','queued','transferring'].includes(item.status))`)
    await button(label('revoke'))
    await stateWait(`state.peers[0]?.autosave?.enabled === false`)
    await closeDetails()
  })

}

async function serviceJourney() {
  await journey('services: invalid range, preview and Escape focus', async () => {
    await openPeer()
    await click('.conversation-header .connect-header')
    await fill('dialog input[required][maxLength="64"]', 'browser-qa-service')
    await fill('dialog input[aria-describedby=port-help]', '8000-8064')
    await check(`document.querySelector('dialog button[type=submit]').disabled`, '65 listeners must be rejected')
    await fill('dialog input[aria-describedby=port-help]', '0,65536')
    await check(`document.querySelector('dialog button[type=submit]').disabled`, 'invalid port numbers must be rejected')
    await fill('dialog input[aria-describedby=port-help]', '8080')
    await check(`!document.querySelector('dialog button[type=submit]').disabled`, 'valid scoped listener must be available')
    await check(`document.querySelector('.service-preview').textContent.includes('127.0.0.1:8080')`, 'preview must show the loopback mapping')
    await capture('service-valid-preview')
    await press('Escape')
    await wait(`!document.querySelector('dialog[open]')`)
    await check(`document.activeElement.matches('.connect-header')`, 'service dialog restores trigger focus')
  })
  if (!session.capabilities?.includes('service-lifecycle')) {
    unavailable('services: actual start and stop', 'fixture does not advertise service-lifecycle')
    return
  }
  await journey('services: actual start, listener state and stop', async () => {
    await click('.conversation-header .connect-header')
    await fill('dialog input[required][maxLength="64"]', 'browser-qa-service')
    await fill('dialog input[aria-describedby=port-help]', '8080')
    await fill('dialog input[inputmode=numeric]', String(session.localServicePort || 43919))
    await click('dialog button[type=submit]')
    await stateWait(`state.services.some(service => service.name === 'browser-qa-service' && service.status === 'active')`)
    await wait(`!document.querySelector('dialog[open]')`)
    if (!(await evaluate(visibleJS('.details-panel')))) await button(label('details'))
    await wait(visibleJS('.service-row'))
    await capture('service-active-listener')
    await button(label('stop'))
    await stateWait(`!state.services.some(service => service.name === 'browser-qa-service' && service.status === 'active')`)
    await closeDetails()
  })
  await journey('services: authoritative copy, retained draft and reviewed restart', async () => {
    await button(label('details'))
    const reads = await count('service.config')
    const starts = await count('service.connect')
    await button('Copy settings')
    await wait(`document.querySelector('dialog input[required][maxLength="64"]')?.value === 'browser-qa-service-2'`)
    assert.equal(await count('service.config'), reads + 1, 'copy reads authoritative saved configuration')
    assert.equal(await count('service.connect'), starts, 'reading does not start or renew a listener')
    await fill('dialog input[required][maxLength="64"]', 'reviewed-copy-draft')
    await press('Escape')
    await button('Copy settings')
    await wait(`document.querySelector('dialog input[required][maxLength="64"]')?.value === 'reviewed-copy-draft'`)
    await press('Escape')
    await button('Edit and start')
    await wait(`document.querySelector('dialog input[required][maxLength="64"]')?.value === 'browser-qa-service'`)
    await check(`document.querySelector('dialog input[aria-describedby=port-help]').value === '8080'`, 'edit preserves the exact saved remote port')
    await capture('service-reviewed-restart')
    await button('Apply and start')
    await stateWait(`state.services.some(service => service.name === 'browser-qa-service' && service.status === 'active')`)
    await wait(`!document.querySelector('dialog[open]')`)
    assert.equal(await count('service.connect'), starts + 1, 'reviewed edit restarts once')
    await button(label('stop'))
    await stateWait(`!state.services.some(service => service.name === 'browser-qa-service' && service.status === 'active')`)
    await closeDetails()
  })
}

async function offlineJourney() {
  await journey('offline setup: empty state, LAN validation and no enrollment', async () => {
    await appearance('en', 'light')
    await check(visibleJS('.sidebar-empty'), 'offline fixture must show the empty device state')
    await capture('offline-empty-desktop')
    await showMap()
    await graphView('diagram')
    await check(visibleJS('.network-graph-empty'), 'empty graph must explain that there are no known devices')
    await check(`document.querySelectorAll('.network-graph-edge').length === 0`, 'empty graph must not invent relationships')
    await capture('offline-empty-graph')
    await graphView('list')
    await capture('offline-empty-list')
    await click('.sidebar-title button')
    await wait(visibleJS('dialog'))
    await click('dialog input[value=lan]')
    await wait(visibleJS('.lan-setup'))
    const before = await count('network.configure')
    await check(visibleJS('.relay-host'), 'first LAN setup provides a guided host form')
    await check(`document.querySelector('.relay-host input[type=number]').value === '48443'`, 'host setup suggests a high port')
    await check(`document.querySelector('.relay-host select').value === ''`, 'host setup never selects a listening interface automatically')
    await capture('offline-lan-host-setup')
    await click('.lan-setup details summary')
    await browser(['find', 'label', 'Relay address', 'fill', 'invalid.example:443'])
    await fill('dialog .relay-config input[maxlength="64"]', '0'.repeat(64))
    await button(label('activateRelay'))
    await wait(`Array.from(document.querySelectorAll('[role=alert]')).some(el => el.textContent.includes('numeric IP'))`)
    assert.equal(await count('network.configure'), before, 'invalid relay address must not reach the network')
    await browser(['find', 'label', 'Relay address', 'fill', '127.0.0.1:443'])
    await fill('dialog .relay-config input[maxlength="64"]', 'invalid-pin')
    await button(label('activateRelay'))
    await wait(`Array.from(document.querySelectorAll('[role=alert]')).some(el => el.textContent.includes('64-character'))`)
    assert.equal(await count('network.configure'), before, 'invalid fingerprint must not activate LAN')
    await check(`Array.from(document.querySelectorAll('.lan-setup button[type=submit]')).filter(el => !el.closest('.relay-config')).every(el => el.disabled)`, 'pairing controls remain unavailable before relay readiness')
    await capture('offline-lan-invalid-input')
    await press('Escape')
    await appearance('ja', 'dark')
    await browser(['set', 'viewport', '390', '844'])
    await capture('offline-empty-ja-dark-mobile')
    assert.equal(await count('network.login'), 0, 'no external account sign-in')
    assert.equal(await count('lan.invite'), 0, 'no invitation creation')
    assert.equal(await count('lan.join'), 0, 'no unintended pairing')
  })
}

try {
  assert.ok(sessionFile, 'SOBA_E2E_SESSION_FILE is required; arbitrary sites are not supported')
  const info = await stat(sessionFile)
  assert.equal(info.mode & 0o077, 0, 'session file must be private')
  try { session = JSON.parse(await readFile(sessionFile, 'utf8')) } catch { throw new Error('private session file is not valid JSON; contents withheld') }
  let url
  try { url = new URL(session.url) } catch { throw new Error('fixture URL is invalid; contents withheld') }
  assert.ok(url.protocol === 'http:', 'fixture must use local HTTP')
  assert.ok(url.hostname === '127.0.0.1', 'fixture must bind numeric loopback')
  assert.ok(!(url.username || url.password || url.search || url.hash), 'fixture URL must contain no credentials or query data')
  assert.equal(typeof session.code, 'string')
  assert.ok(session.code.length > 0)
  assert.equal(JSON.parse(await readFile(join(root, 'qa-agent-browser/node_modules/agent-browser/package.json'), 'utf8')).version, '0.38.2', 'use the pinned CLI')
  await mkdir(output, { recursive: true, mode: 0o700 })
  temporary = await mkdtemp(join(tmpdir(), 'soba-agent-qa-'))
  assert.ok(['studio', 'offline'].includes(session.scenario), 'fixture scenario must be exactly studio or offline')
  report.scenario = session.scenario
  const requiredCapabilities = report.scenario === 'offline' ? ['offline-network'] : ['service-lifecycle', 'failed-upload-retry']
  for (const capability of requiredCapabilities) assert.ok(session.capabilities?.includes(capability), `fixture must advertise required capability: ${capability}`)
  report.requiredJourneys = ['authentication: real local code form and workspace', ...(report.scenario === 'offline' ? ['offline setup: empty state, LAN validation and no enrollment'] : studioRequired), 'runtime health: no uncaught exceptions or CSP violations']
  await journey('authentication: real local code form and workspace', async () => {
    await browser(['open', url.href])
    launched = true
    // Disable optional streaming before entering even the test access code.
    await browser(['stream', 'disable'])
    await browser(['set', 'viewport', '1440', '960'])
    await browser(['wait', '.login-panel input[type=password]'])
    await browser(['batch', '--bail'], JSON.stringify([
      ['fill', '.login-panel input[type=password]', session.code],
      ['click', '.login-panel button[type=submit]'],
      ['wait', '.workspace'],
    ]))
    authenticated = true
    await check(`!document.querySelector('.login-panel')`, 'login form must be removed before evidence capture')
    await auditInstrumentation()
  })
  if (report.scenario === 'offline') await offlineJourney()
  else {
    await navigationJourney()
    await graphMatrix()
    await conversationJourneys()
    await conversationMatrix()
    await preferencesJourney()
    await permissionJourney()
    await serviceJourney()
    unavailable('offline setup: empty state and LAN invalid input', 'run a second private server with scenario offline')
    unavailable('failed transfer: retry after remote transfer failure', 'no deterministic failed remote transfer supplied by this fixture')
  }
  await journey('runtime health: no uncaught exceptions or CSP violations', async () => {
    await check('window.__sobaQA.errors === 0 && window.__sobaQA.cspViolations === 0', 'browser runtime and production CSP must stay healthy')
    assert.equal(await count('network.login'), 0, 'no external sign-in command may be submitted')
    assert.equal(await count('lan.invite'), 0, 'no invitation command may be submitted')
    assert.equal(await count('lan.join'), 0, 'no pairing command may be submitted')
  })
  for (const name of report.requiredJourneys) assert.ok(report.journeys.some(item => item.name === name && item.status === 'passed'), `required journey was not exercised: ${name}`)
  report.requiredCoverageComplete = true
} catch (error) {
  process.exitCode = 1
  if (!report.journeys.some(item => item.status === 'failed')) report.journeys.push({ name: currentJourney, status: 'failed', assertion: safeText(error.message) })
  console.error(`FAIL ${currentJourney}: ${safeText(error.message)}`)
  if (authenticated) { try { await capture('failed-journey') } catch { /* Never relax capture privacy checks on failure. */ } }
} finally {
  if (launched) { try { await browser(['close']) } catch { report.limits.push('CLI cleanup did not confirm browser closure.'); process.exitCode = 1 } }
  if (temporary) await rm(temporary, { recursive: true, force: true })
  report.finishedAt = new Date().toISOString()
  report.requiredCoverageComplete ??= false
  for (const name of report.requiredJourneys || []) if (!report.journeys.some(item => item.name === name)) unavailable(name, 'not reached because a required journey failed')
  report.cliCalls = cliCalls
  report.commandCategories = Object.fromEntries(categories)
  report.summary = Object.fromEntries(['passed', 'failed', 'unavailable'].map(status => [status, report.journeys.filter(item => item.status === status).length]))
  await mkdir(output, { recursive: true, mode: 0o700 })
  await writeFile(join(output, 'agent-browser-report.json'), JSON.stringify(report, null, 2) + '\n', { mode: 0o600 })
  console.log(`Agent-browser journeys: ${report.summary.passed} passed, ${report.summary.failed} failed, ${report.summary.unavailable} unavailable. Review sanitized PNGs before visual acceptance.`)
}
