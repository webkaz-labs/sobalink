import { createServer } from 'node:net'
import { test, expect, openDetailsSection } from './fixtures.mjs'

// Real Core commands and native loopback binds with fictional saved definitions.
// This does not establish enrollment, remote application or installed-binary acceptance.
test.use({ scenario: 'studio' })

const viewports = [{ width: 1440, height: 960 }, { width: 375, height: 844 }, { width: 844, height: 390 }]
const candidateLabel = proposal => `127.0.0.1:${proposal.localPort}-${proposal.localEnd}`
const mapping = port => `127.0.0.1:${port} → 8000; 127.0.0.1:${port + 1} → 8002`

function labels(locale) {
  const ja = locale === 'ja'
  return {
    saved: ja ? '保存済みサービス' : 'Saved services',
    create: ja ? '接続定義を保存' : 'Save a connection',
    name: ja ? '接続の名前' : 'Connection name',
    backend: ja ? '保存するネットワーク' : 'Saved network',
    peers: ja ? '正確な端末 ID' : 'Exact device IDs',
    ports: ja ? 'ポート' : 'Ports',
    excluded: ja ? '除外するポート' : 'Exclude ports',
    local: ja ? 'ローカルの開始ポート' : 'Local starting port',
    lifetime: ja ? '有効期間' : 'Lifetime',
    duration: ja ? '有効期間（秒）' : 'Duration in seconds',
    copy: ja ? '保存済み定義をコピー' : 'Copy saved definition',
    open: ja ? '代替ポートを確認' : 'Check alternate ports',
    title: ja ? '代替のローカルポート' : 'Alternate local ports',
    source: ja ? '保存済みの元設定' : 'Saved source',
    check: ja ? 'ループバックポートを今確認' : 'Check loopback ports now',
    count: ja ? '要求する候補数' : 'Requested candidates',
    attempts: ja ? '候補範囲の確認回数' : 'Candidate window budget',
    results: ja ? '確認済みポート候補' : 'Checked port candidates',
    use: ja ? '選んだポートで下書きを編集' : 'Edit draft with selected port',
    chosen: ja ? '選んだ確認済みポート' : 'Selected checked port',
    selectedMapping: ja ? '選んだ対応' : 'Selected mapping',
    reloadSource: ja ? '元の保存済み設定を再読込' : 'Reload saved source',
    reloadDefinition: ja ? '保存済み定義を再読み込み' : 'Reload saved definition',
    review: ja ? '停止状態の定義を確認' : 'Review stopped definition',
    scope: ja ? '保存する範囲を確認' : 'Review saved scope',
    edit: ja ? 'この下書きを編集' : 'Edit this draft',
    save: ja ? '確認した定義を保存' : 'Save reviewed definition',
    conflict: ja ? '保存済みの設定が変更されています。設定を再読み込みして確認してください。' : 'These saved settings have changed. Reload the saved settings and review them again.',
    warning: ja ? '候補は観測結果であり予約ではありません。直後に別のプロセスが使う場合があります。後の明示的な開始時に各待受を再確認し、別途確認した許可の有効期間が始まります。' : 'These are observations, not reservations. Another process can take a port immediately. A later explicit start rechecks every bind and starts the separately reviewed permission lifetime.',
  }
}

const button = (scope, name) => scope.getByRole('button', { name, exact: true })
const commandResponse = (page, name) => page.waitForResponse(response =>
  new URL(response.url()).pathname === '/api/command' && response.request().postDataJSON()?.name === name)

async function successfulResult(response) {
  const value = await response.json()
  expect(response.ok()).toBe(true)
  expect(value.ok).toBe(true)
  return value.result
}

async function listen(port = 0) {
  const server = createServer()
  await new Promise((resolve, reject) => {
    server.once('error', reject)
    server.listen({ host: '127.0.0.1', port, exclusive: true }, () => {
      server.removeListener('error', reject)
      resolve()
    })
  })
  return server
}

async function close(server) {
  if (server?.listening) await new Promise((resolve, reject) => server.close(error => error ? reject(error) : resolve()))
}

async function occupyOriginalPort() {
  // Keep the OS-selected port occupied until the test's finally block. The
  // two-port source mapping must fit entirely in the high-port domain.
  for (let attempt = 0; attempt < 16; attempt++) {
    const server = await listen()
    const port = server.address().port
    if (port > 1024 && port < 65535) return { server, port }
    await close(server)
  }
  throw new Error('No usable ephemeral loopback port was assigned for the synthetic source')
}

async function expectProbeSocketsClosed(proposal) {
  const sockets = []
  try {
    // Hold the entire chosen window at once, proving the returned observation
    // did not retain a reservation or a partial check socket.
    for (let port = proposal.localPort; port <= proposal.localEnd; port++) sockets.push(await listen(port))
    expect(sockets).toHaveLength(2)
  } finally {
    await Promise.all(sockets.map(close))
  }
}

async function sameTabCommand(page, name, payload) {
  // Authentication and CSRF stay inside the authenticated synthetic tab. No
  // token, private fixture session or mocked response leaves this function.
  return page.evaluate(async ({ name, payload }) => {
    const stateResponse = await fetch('/api/state', { credentials: 'same-origin', cache: 'no-store' })
    if (!stateResponse.ok) throw new Error('The synthetic state could not be read')
    const { csrfToken } = await stateResponse.json()
    if (typeof csrfToken !== 'string') throw new Error('The synthetic session is unavailable')
    const response = await fetch('/api/command', {
      method: 'POST', credentials: 'same-origin', cache: 'no-store',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
      body: JSON.stringify({ requestId: crypto.randomUUID(), name, payload }),
    })
    const value = await response.json()
    if (!response.ok || value.ok !== true) throw new Error('The synthetic command did not succeed')
    return value.result
  }, { name, payload })
}

async function keyboardActionAtEverySize(page, app, dialog, previous, target, artifact) {
  for (const viewport of viewports) {
    await page.setViewportSize(viewport)
    // Always move focus elsewhere after resizing, then enter the tested action
    // through a genuine Tab. Focusing the target would hide scroll regressions.
    await previous.focus()
    await expect(previous).toBeFocused()
    await page.keyboard.press('Tab')
    await expect(target).toBeFocused()
    await expect.poll(() => target.evaluate(element => {
      const bounds = element.getBoundingClientRect()
      const top = document.elementFromPoint(bounds.left + bounds.width / 2, bounds.top + bounds.height / 2)
      const body = element.closest('dialog').querySelector('.modal-body')
      return bounds.width > 0 && bounds.height > 0 && bounds.left >= 0 && bounds.right <= innerWidth &&
        bounds.top >= 0 && bounds.bottom <= innerHeight && Boolean(top && element.contains(top)) &&
        body.scrollWidth <= body.clientWidth + 1 && document.documentElement.scrollWidth <= innerWidth + 1
    }), { message: 'Keyboard-entered port action stays inside the viewport and is not covered by another element' }).toBe(true)
    await expect(dialog).toBeVisible()
    await app.capture(`${artifact}-${viewport.width}x${viewport.height}`)
  }
}

async function createStoppedSource(page, app, l, port, name) {
  await button(page, l.saved).click()
  const dialog = page.getByRole('dialog')
  await button(dialog, l.create).click()
  await dialog.getByLabel(l.name, { exact: true }).fill(name)
  // These labels contain explanatory small text, so anchor the accessible name
  // at its exact leading label instead of matching nested option text.
  await dialog.getByRole('combobox', { name: new RegExp(`^${l.backend}`) }).selectOption('tailnet')
  await dialog.getByRole('textbox', { name: new RegExp(`^${l.peers}`) }).fill('fixture-studio')
  await dialog.getByLabel(l.ports, { exact: true }).fill('8000-8002')
  await dialog.getByLabel(l.excluded, { exact: true }).fill('8001')
  await dialog.getByLabel(l.local, { exact: true }).fill(String(port))
  await dialog.getByRole('combobox', { name: l.lifetime, exact: true }).selectOption('finite')
  await dialog.getByLabel(l.duration, { exact: true }).fill('3600')
  await button(dialog, l.review).click()
  const saved = commandResponse(page, 'service.save')
  await button(dialog, l.save).click()
  const source = await successfulResult(await saved)
  expect(source.active).toBe(false)
  expect(source.configuration).toEqual(expect.objectContaining({ name, direction: 'forward', backend: 'tailnet', network: 'tcp', peerId: 'fixture-studio', ports: '8000-8002', excludePorts: '8001', localPort: port, loopbackHost: '127.0.0.1', lifetime: 'finite', ttlSeconds: 3600, purpose: 'generic' }))
  await expect(button(dialog, l.create)).toBeVisible()
  await app.expectState(state => state.services.some(service => service.id === source.configuration.id && service.status === 'saved'), 'The real Core saved the synthetic connection without starting it')
  return source
}

async function openProposals(page, app, l, source, artifact) {
  const dialog = page.getByRole('dialog')
  const entry = dialog.locator('.definition-entry').filter({ has: page.getByText(source.configuration.name, { exact: true }) })
  await openDetailsSection(entry.locator('details.definition-actions'))
  const open = button(entry, l.open)
  if (artifact) await keyboardActionAtEverySize(page, app, dialog, button(entry, l.copy), open, artifact)
  const config = commandResponse(page, 'service.config')
  await open.click()
  expect(await successfulResult(await config)).toEqual(source)
  await expect(page.getByRole('dialog', { name: l.title, exact: true })).toBeVisible()
  const saved = dialog.getByRole('region', { name: l.source, exact: true })
  await expect(saved).toContainText(source.revision)
  await expect(saved).toContainText(mapping(source.configuration.localPort))
  await expect(saved).toContainText('8000, 8002')
  await expect(dialog.getByRole('region', { name: l.results, exact: true })).toHaveCount(0)
}

async function checkProposals(page, l, source) {
  const dialog = page.getByRole('dialog')
  await dialog.getByRole('spinbutton', { name: l.count, exact: true }).fill('2')
  await dialog.getByRole('spinbutton', { name: l.attempts, exact: true }).fill('32')
  const pending = commandResponse(page, 'service.ports')
  await button(dialog, l.check).click()
  const response = await pending
  const request = response.request().postDataJSON()
  expect(request.payload).toEqual({ id: source.configuration.id, expectedRevision: source.revision, fromPort: 49152, count: 2, attempts: 32 })
  const result = await successfulResult(response)
  expect(result.configuration).toEqual(source.configuration)
  expect(result.revision).toBe(source.revision)
  expect(result.reservation).toBe(false)
  expect(result.code).toBe('listener_conflict')
  expect(result.conflictPort).toBe(source.configuration.localPort)
  expect(result.effectivePorts).toBe('8000,8002')
  expect(result.proposals).toHaveLength(2)
  expect(result.stopReason).toBe('requested_count')
  const results = dialog.getByRole('region', { name: l.results, exact: true })
  await expect(results.getByText(l.warning, { exact: true })).toBeVisible()
  await expect(results.getByRole('radio')).toHaveCount(2)
  for (const proposal of result.proposals) {
    expect(proposal.localEnd).toBe(proposal.localPort + 1)
    await expect(results.getByRole('radio', { name: candidateLabel(proposal), exact: true })).not.toBeChecked()
  }
  await expect(button(results, l.use)).toBeDisabled()
  return { result, requestId: request.requestId }
}

async function expectNoRuntimeActions(app, id) {
  await app.expectState(state => state.services.some(service => service.id === id && service.status === 'saved') &&
    [...state.services, ...state.shares].every(service => !['active', 'reconnecting'].includes(service.status)), 'Checking and saving preserve stopped local services')
  for (const command of ['service.connect', 'service.share', 'services.start', 'services.stop', 'service.stop', 'network.configure', 'startup.save', 'proxy.start']) expect(await app.count(command)).toBe(0)
}

for (const locale of ['en', 'ja']) {
  test(`${locale}: real alternate ports keep the source fixed until a reviewed stopped save`, async ({ page, app }) => {
    const l = labels(locale)
    await app.appearance(locale, locale === 'ja' ? 'dark' : 'light')
    const occupied = await occupyOriginalPort()
    try {
      const source = await createStoppedSource(page, app, l, occupied.port, 'fixture-port-choice')
      const dialog = page.getByRole('dialog')
      const id = source.configuration.id
      await openProposals(page, app, l, source, `port-proposals-entry-${locale}`)
      expect(await app.count('service.ports')).toBe(0)
      expect(await app.count('service.save')).toBe(1)
      await keyboardActionAtEverySize(page, app, dialog, dialog.getByRole('spinbutton', { name: l.attempts, exact: true }), button(dialog, l.check), `port-proposals-check-${locale}`)
      const first = await checkProposals(page, l, source)
      const candidate = first.result.proposals[0]
      await expectProbeSocketsClosed(candidate)
      expect(await sameTabCommand(page, 'service.config', { id })).toEqual(source)
      const radio = dialog.getByRole('radio', { name: candidateLabel(candidate), exact: true })
      await radio.check()
      await expect(dialog.getByRole('region', { name: l.results, exact: true }).getByLabel(l.selectedMapping, { exact: true })).toContainText(mapping(candidate.localPort))
      await keyboardActionAtEverySize(page, app, dialog, radio, button(dialog, l.use), `port-proposals-choice-${locale}`)
      const reread = commandResponse(page, 'service.config')
      await button(dialog, l.use).click()
      expect(await successfulResult(await reread)).toEqual(source)
      await expect(dialog.getByLabel(l.local, { exact: true })).toHaveValue(String(candidate.localPort))
      const chosen = dialog.getByRole('region', { name: l.chosen, exact: true })
      await expect(chosen).toContainText(source.revision)
      await expect(chosen).toContainText(mapping(occupied.port))
      expect(await sameTabCommand(page, 'service.config', { id })).toEqual(source)
      expect(await app.count('service.save')).toBe(1)
      // Dismissing the seeded draft must preserve the original fixed mapping.
      await page.keyboard.press('Escape')
      await expect(button(dialog, l.create)).toBeVisible()
      expect(await sameTabCommand(page, 'service.config', { id })).toEqual(source)
      await openProposals(page, app, l, source)
      const second = await checkProposals(page, l, source)
      expect(second.requestId).not.toBe(first.requestId)
      expect(await app.count('service.ports')).toBe(2)
      const selected = second.result.proposals[1]
      await dialog.getByRole('radio', { name: candidateLabel(selected), exact: true }).check()
      await expect(dialog.getByRole('region', { name: l.results, exact: true }).getByLabel(l.selectedMapping, { exact: true })).toContainText(mapping(selected.localPort))
      const secondRead = commandResponse(page, 'service.config')
      await button(dialog, l.use).click()
      expect(await successfulResult(await secondRead)).toEqual(source)
      await expect(dialog.getByLabel(l.local, { exact: true })).toHaveValue(String(selected.localPort))
      await expect(dialog.getByLabel(l.ports, { exact: true })).toHaveValue('8000-8002')
      await expect(dialog.getByLabel(l.excluded, { exact: true })).toHaveValue('8001')
      await expect(dialog.getByLabel(l.duration, { exact: true })).toHaveValue('3600')
      await button(dialog, l.review).click()
      const scope = dialog.getByRole('region', { name: l.scope, exact: true })
      for (const value of ['fixture-port-choice', 'tailnet · TCP', 'fixture-studio', '8000-8002', '8001', `127.0.0.1:${selected.localPort}-${selected.localEnd} → 8000, 8002`, '3600', 'generic']) await expect(scope).toContainText(value)
      expect(await sameTabCommand(page, 'service.config', { id })).toEqual(source)
      expect(await app.count('service.save')).toBe(1)
      await keyboardActionAtEverySize(page, app, dialog, button(scope, l.edit), button(scope, l.save), `port-proposals-review-${locale}`)
      const saved = commandResponse(page, 'service.save')
      await button(scope, l.save).click()
      const savedResponse = await saved
      expect(savedResponse.request().postDataJSON().payload.expectedRevision).toBe(source.revision)
      const updated = await successfulResult(savedResponse)
      expect(updated.active).toBe(false)
      expect(updated.revision).not.toBe(source.revision)
      expect(updated.configuration).toEqual({ ...source.configuration, localPort: selected.localPort })
      await expect(button(dialog, l.create)).toBeVisible()
      expect(await sameTabCommand(page, 'service.config', { id })).toEqual(updated)
      expect(await app.count('service.save')).toBe(2)
      await expectProbeSocketsClosed(selected)
      await expectNoRuntimeActions(app, id)
    } finally {
      await close(occupied.server)
    }
  })

  test(`${locale}: real revision changes invalidate both port checking and candidate draft reuse`, async ({ page, app }) => {
    const l = labels(locale)
    await app.appearance(locale, locale === 'ja' ? 'dark' : 'light')
    const occupied = await occupyOriginalPort()
    try {
      const source = await createStoppedSource(page, app, l, occupied.port, 'fixture-port-revision')
      const id = source.configuration.id
      const dialog = page.getByRole('dialog')
      await openProposals(page, app, l, source)
      // A real same-tab Core save simulates another editor changing the saved
      // lifetime. It does not intercept or fulfill any browser request.
      const changed = await sameTabCommand(page, 'service.save', { configuration: { ...source.configuration, ttlSeconds: 1800 }, expectedRevision: source.revision })
      expect(changed.active).toBe(false)
      expect(changed.revision).not.toBe(source.revision)
      const rejected = commandResponse(page, 'service.ports')
      await button(dialog, l.check).click()
      const rejectedResponse = await rejected
      expect(rejectedResponse.ok()).toBe(false)
      expect(rejectedResponse.request().postDataJSON().payload.expectedRevision).toBe(source.revision)
      await expect(dialog.getByRole('alert')).toContainText(l.conflict)
      await expect(button(dialog, l.check)).toBeDisabled()
      await expect(dialog.getByRole('radio')).toHaveCount(0)
      expect(await sameTabCommand(page, 'service.config', { id })).toEqual(changed)
      const reload = commandResponse(page, 'service.config')
      await button(dialog, l.reloadSource).click()
      expect(await successfulResult(await reload)).toEqual(changed)
      await expect(dialog.getByRole('alert')).toHaveCount(0)
      const checked = await checkProposals(page, l, changed)
      const selected = checked.result.proposals[0]
      await dialog.getByRole('radio', { name: candidateLabel(selected), exact: true }).check()
      await expect(dialog.getByRole('region', { name: l.results, exact: true }).getByLabel(l.selectedMapping, { exact: true })).toContainText(mapping(selected.localPort))
      const newer = await sameTabCommand(page, 'service.save', { configuration: { ...changed.configuration, ttlSeconds: 900 }, expectedRevision: changed.revision })
      expect(newer.active).toBe(false)
      expect(newer.revision).not.toBe(changed.revision)
      const reread = commandResponse(page, 'service.config')
      await button(dialog, l.use).click()
      expect(await successfulResult(await reread)).toEqual(newer)
      await expect(dialog.getByRole('alert')).toContainText(l.conflict)
      await expect(button(dialog, l.review)).toHaveCount(0)
      await expect(dialog.getByLabel(l.local, { exact: true })).toHaveCount(0)
      expect(await app.count('service.save')).toBe(3)
      const fresh = commandResponse(page, 'service.config')
      await button(dialog, l.reloadDefinition).click()
      expect(await successfulResult(await fresh)).toEqual(newer)
      await expect(dialog.getByRole('region', { name: l.chosen, exact: true })).toHaveCount(0)
      await expect(dialog.getByLabel(l.local, { exact: true })).toHaveValue(String(occupied.port))
      await expect(dialog.getByLabel(l.duration, { exact: true })).toHaveValue('900')
      await expect(button(dialog, l.review)).toBeEnabled()
      await page.keyboard.press('Escape')
      await expect(button(dialog, l.create)).toBeVisible()
      expect(await sameTabCommand(page, 'service.config', { id })).toEqual(newer)
      expect(await app.count('service.ports')).toBe(2)
      expect(await app.count('service.save')).toBe(3)
      await expectNoRuntimeActions(app, id)
    } finally {
      await close(occupied.server)
    }
  })
}
