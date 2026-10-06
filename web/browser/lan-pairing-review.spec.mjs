import { test, expect } from './fixtures.mjs'

// Browser UI regression only: authenticated Go serves the production page, but
// LAN state and inspection/join responses are synthetic. This does not establish
// enrollment, remote availability, or real-device pairing acceptance. No usable
// private invitation is created, read, logged, or captured by these cases.
const input = 'synthetic-ui-review-only-not-a-private-invitation'
const publicKey = 'a1'.repeat(32), hostKey = 'b2'.repeat(32), pin = 'c3'.repeat(32)
const relay = { kind: 'relay', address: '192.0.2.10:443', certificateSHA256: pin }
const labels = locale => locale === 'ja' ? {
  joinTab: '招待に参加', input: '招待', inspect: '招待の内容を確認', review: '招待の内容を確認',
  confirm: 'このデバイスとペアリング', cancel: 'キャンセル', paired: 'デバイスをペアリングしました',
  scope: '通信の信頼、自動受信、サービス、起動時の許可は別途必要です。',
} : {
  joinTab: 'Join an invitation', input: 'Invitation', inspect: 'Review invitation', review: 'Review this invitation',
  confirm: 'Pair with this device', cancel: 'Cancel', paired: 'Device paired',
  scope: 'Communication trust, automatic receiving, services and startup permissions need separate approval.',
}
async function installReadyFixture(page, { failFirstJoin = false } = {}) {
  const commands = []
  let firstJoinID, exactPayloads = true, stableRetryID = true
  await page.route('**/api/state', async route => {
    const response = await route.fetch(), state = await response.json()
    await route.fulfill({ response, json: { ...state, settings: { ...state.settings, network: 'lan' }, lan: {
      configured: true, pairingReady: true, publicKey, relay, path: 'unknown',
      policy: { mode: 'trusted-relay', prefixes: [], editable: false, restartRequired: false },
    } } })
  })
  await page.route('**/api/command', async route => {
    const request = route.request().postDataJSON()
    if (!['lan.inspect', 'lan.join'].includes(request.name)) {
      // The pairing UI must never make any additional mutating command here.
      commands.push(request.name)
      await route.fulfill({ status: 400, json: { ok: false, error: { code: 'invalid_request', message: 'Synthetic UI fixture rejects additional commands' } } })
      return
    }
    commands.push(request.name)
    exactPayloads &&= Object.keys(request.payload).length === 1 && request.payload.invitation === input
    if (request.name === 'lan.inspect') {
      await route.fulfill({ json: { ok: true, result: { recipientPublicKey: publicKey, recipientMatches: true, hostPublicKey: hostKey, hostName: 'Synthetic inviting device', expires: new Date(Date.now() + 300_000).toISOString(), relay } } })
      return
    }
    if (firstJoinID === undefined) {
      firstJoinID = request.requestId
      if (failFirstJoin) { await route.abort('failed'); return }
    } else stableRetryID &&= firstJoinID === request.requestId
    await route.fulfill({ json: { ok: true, result: { paired: true, trusted: false, peerId: hostKey } } })
  })
  await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')))
  return { commands, exactPayloads: () => exactPayloads, stableRetryID: () => stableRetryID }
}
async function openJoin(page, text) {
  await page.locator('.sidebar-title button').click()
  await page.locator('dialog input[value="lan"]').check()
  await expect(page.locator('dialog .lan-setup .relay-summary')).toBeVisible()
  await page.getByRole('button', { name: text.joinTab, exact: true }).click()
}
async function inspect(page, text) {
  await page.getByLabel(text.input, { exact: false }).fill(input)
  await page.getByRole('button', { name: text.inspect, exact: true }).click()
  const review = page.getByRole('region', { name: text.review, exact: true })
  await expect(review).toBeVisible()
  await expect(review).toBeFocused()
  await expect(review).toContainText('Synthetic inviting device')
  await expect(review).toContainText(hostKey)
  await expect(review).toContainText(relay.address)
  await expect(review).toContainText(pin)
  await expect(review).toContainText(text.scope)
  await expect(review.locator('time')).toHaveAttribute('datetime', /.+/)
  await expect(page.locator('dialog input[type="password"]')).toHaveCount(0)
  return review
}

test.describe('configured LAN pairing review with synthetic responses', () => {
  test.use({ scenario: 'offline' })
  for (const locale of ['en', 'ja']) {
    test(`${locale}: review is required, fits narrow layout and is destroyed by cancel, close and Back`, async ({ page, app }) => {
      await app.appearance(locale, locale === 'ja' ? 'dark' : 'light')
      const fixture = await installReadyFixture(page), text = labels(locale)
      await openJoin(page, text)
      let review = await inspect(page, text)
      expect(fixture.commands).toEqual(['lan.inspect'])
      await review.scrollIntoViewIfNeeded()
      await app.capture(`lan-pairing-review-${locale}-desktop`)
      await page.setViewportSize({ width: 390, height: 844 })
      await review.scrollIntoViewIfNeeded()
      await app.capture(`lan-pairing-review-${locale}-390`)
      expect(await review.evaluate(element => element.scrollWidth <= element.clientWidth + 1)).toBe(true)
      await review.getByRole('button', { name: text.cancel, exact: true }).click()
      await expect(page.getByRole('region', { name: text.review, exact: true })).toHaveCount(0)
      review = await inspect(page, text)
      await page.keyboard.press('Escape')
      await expect(page.locator('dialog[open]')).toHaveCount(0)
      await openJoin(page, text)
      await expect(page.getByRole('region', { name: text.review, exact: true })).toHaveCount(0)
      await inspect(page, text)
      // A same-page history entry exercises the app's actual popstate cleanup.
      await page.evaluate(() => history.pushState({ ...history.state, pairingReviewFixture: true }, ''))
      await page.goBack()
      await expect(page.locator('dialog[open]')).toHaveCount(0)
      await openJoin(page, text)
      await expect(page.getByRole('region', { name: text.review, exact: true })).toHaveCount(0)
      expect(fixture.commands).toEqual(['lan.inspect', 'lan.inspect', 'lan.inspect'])
      expect(fixture.exactPayloads()).toBe(true)
    })

    test(`${locale}: explicit pairing preserves the existing retry request identity`, async ({ page, app }) => {
      await app.appearance(locale, 'light')
      const fixture = await installReadyFixture(page, { failFirstJoin: true }), text = labels(locale)
      await openJoin(page, text)
      const review = await inspect(page, text)
      expect(fixture.commands).toEqual(['lan.inspect'])
      await review.getByRole('button', { name: text.confirm, exact: true }).click()
      await expect(page.locator('dialog [role="alert"]')).toBeVisible()
      await expect(page.getByText(text.paired, { exact: true })).toHaveCount(0)
      await review.getByRole('button', { name: text.confirm, exact: true }).click()
      await expect(page.getByText(text.paired, { exact: true })).toBeVisible()
      expect(fixture.commands).toEqual(['lan.inspect', 'lan.join', 'lan.join'])
      expect(fixture.exactPayloads()).toBe(true)
      expect(fixture.stableRetryID()).toBe(true)
    })
  }
})
