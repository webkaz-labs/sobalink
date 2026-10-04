import { test, expect } from './fixtures.mjs'

// UI → HTTP boundary regression: authentication, state and unrelated commands
// use the real isolated Go fixture. Only the first message.send response is a
// synthetic storage error. This does not establish a real Core save failure.
for (const [locale, code] of [['en', 'message_history_unavailable'], ['ja', 'message_peer_storage_unavailable']]) {
  test(`uncertain message draft blocks duplicate HTTP sends in ${locale} (${code})`, async ({ page, app }) => {
    await app.openPeer()
    if (locale === 'ja') await app.appearance('ja', 'light')
    let requests = 0
    await page.route('**/api/command', async route => {
      if (route.request().postDataJSON()?.name !== 'message.send') return route.continue()
      requests++
      if (requests === 1) return route.fulfill({ status: 507, contentType: 'application/json', body: JSON.stringify({ code }) })
      return route.continue()
    })
    const composer = page.locator('.composer textarea')
    const send = page.getByRole('button', { name: locale === 'ja' ? 'メッセージを送信' : 'Send message', exact: true })
    const draft = 'Fictional uncertain message for review'
    const baseline = await app.count('message.send')
    await composer.fill(draft)
    await send.click()
    await expect(send).toBeDisabled()
    await expect(composer).toBeEnabled()
    await expect(composer).toHaveValue(draft)
    await expect(page.locator('.composer-area [role="alert"]')).toContainText(locale === 'ja' ? '届いている可能性' : 'may already')
    await expect(page.locator('.composer-area [role="alert"]')).toContainText(locale === 'ja' ? '再読み込み' : 'page reloads')
    await expect(page.locator('.timeline .message-bubble').filter({ hasText: draft })).toHaveCount(0)

    await page.locator('.main-alerts [role="alert"] button').click()
    await composer.press('Control+Enter')
    await composer.press('Control+Enter')
    await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')))
    await page.getByRole('button', { name: locale === 'ja' ? 'ネットワーク図' : 'Network graph', exact: true }).click()
    await page.goBack()
    await expect(composer).toHaveValue(draft)
    await expect(send).toBeDisabled()
    expect(requests, 'the blocked draft never creates a second HTTP message request').toBe(1)
    expect(await app.count('message.send')).toBe(baseline + 1)

    await composer.fill('Fictional different intended message')
    await expect(send).toBeEnabled()
    await send.click()
    await expect(composer).toHaveValue('')
    await app.expectState(state => state.messages.some(message => message.text === 'Fictional different intended message' && message.direction === 'outgoing' && message.status === 'sent'))
    await composer.fill(draft)
    await expect(send).toBeDisabled()
    await composer.press('Control+Enter')
    expect(requests, 'successful different text does not remove the earlier draft guard').toBe(2)
    expect(await app.count('message.send')).toBe(baseline + 2)
  })
}
