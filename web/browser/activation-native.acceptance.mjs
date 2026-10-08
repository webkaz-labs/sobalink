import { test as rawTest, expect } from './activation-fixtures.mjs'

function test(title, body) { rawTest(title, async ({ page, activation }, testInfo) => { try { await body({ page, activation }, testInfo); activation.bodyPassed() } catch { activation.bodyFailed(); throw new Error('Synthetic acceptance assertion failed; private details withheld') } }) }
test.describe = rawTest.describe; test.use = rawTest.use

for (const locale of ['en', 'ja']) {
  test(`production helper and normal sign-in with synthetic owners (${locale})`, async ({ activation }) => {
    const popup = await activation.popup(locale)
    await expect(popup.locator('#review')).toContainText('synthetic-web-peer')
    await expect(popup.locator('#review')).toContainText('192.0.2.0/24')
    activation.phase('restart-confirm'); await popup.locator('#continue').click()
    await expect(popup.locator('#management')).toBeVisible()
    await expect.poll(() => activation.oldExited()).toBe(true)
    await expect.poll(() => activation.successorStarted()).toBe(true)
    // OS-open is stubbed at the launcher function, after real identity/URL checks.
    expect(await activation.opened()).toBe(false)
    await popup.locator('#open').click()
    await expect.poll(() => activation.opened()).toBe(true)
    await expect(popup.locator('#open')).toBeDisabled()
    await activation.normalSuccessorLogin(popup)
    // Normal code cannot be retrieved a second time through helper status.
    const extra = await popup.evaluate(() => window.__activationProbe('/status'))
    expect(extra.status).toBe(200); expect(extra.containsCode).toBe(false)
    const replayAck = await popup.evaluate(() => window.__activationProbe('/ack'))
    expect(replayAck.status).toBe(409)
    const replayOpen = await popup.evaluate(() => window.__activationProbe('/open'))
    expect(replayOpen.status).toBe(409)

  })
}

test('decline before confirmation keeps the old owner and permits explicit review', async ({ page, activation }) => {
  activation.expectNoSuccessor()
  const popup = await activation.popup()
  await popup.locator('#cancel').click()
  await expect(page.locator('#status')).toContainText('review again')
  expect(await activation.oldExited()).toBe(false); expect(await activation.successorStarted()).toBe(false)
  await page.locator('#review').click(); await expect(page.locator('#apply')).toBeEnabled()
})

test('closed popup before confirmation never starts a successor', async ({ page, activation }) => {
  activation.expectNoSuccessor()
  const popup = await activation.popup(); await popup.close()
  await expect(page.locator('#status')).toContainText('review again')
  expect(await activation.oldExited()).toBe(false); expect(await activation.successorStarted()).toBe(false)
})

test('logout of originating session before confirmation denies stop admission', async ({ page, activation }) => {
  activation.expectNoSuccessor()
  const popup = await activation.popup()
  await page.locator('#logout').click(); await expect(page.locator('#status')).toHaveText('Signed out')
  activation.phase('restart-confirm'); await popup.locator('#continue').click()
  await expect(popup.locator('#message')).toContainText('uncertain or unavailable')
  expect(await activation.oldExited()).toBe(false); expect(await activation.successorStarted()).toBe(false)
})


test.describe('session-bound stop admission', () => {
  test.use({ activationCase: 'pause-before-stop' })
  test('logout after old run but before stop consumption denies shutdown', async ({ page, activation }) => {
    activation.expectNoSuccessor()
    const popup = await activation.popup(); activation.phase('restart-confirm'); await popup.locator('#continue').click()
    await expect.poll(() => activation.waitingAtStop()).toBe(true)
    await page.locator('#logout').click(); await expect(page.locator('#status')).toHaveText('Signed out')
    await activation.permitStop()
    await expect.poll(() => activation.sessionStopDenied()).toBe(true)
    expect(await activation.oldExited()).toBe(false); expect(await activation.successorStarted()).toBe(false)
    await popup.locator('#cancel').click()
  })
})

test.describe('failed shutdown acknowledgement', () => {
  test.use({ activationCase: 'lost-ack' })
  test('old exit without final acknowledgement never launches a successor', async ({ activation }) => {
    activation.expectNoSuccessor()
    const popup = await activation.popup(); activation.phase('restart-confirm'); await popup.locator('#continue').click()
    await expect.poll(() => activation.oldExited()).toBe(true)
    expect(await activation.successorStarted()).toBe(false)
    await expect(popup.locator('#management')).toBeHidden()
    await popup.locator('#cancel').click()
    expect(await activation.successorStarted()).toBe(false)
  })
})
