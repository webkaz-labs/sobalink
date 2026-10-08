import { test, expect } from './product-activation-fixtures.mjs'
import { productActivationCases } from './product-activation-contract.mjs'

function commandResponse(page, name) {
  return page.waitForResponse(response => {
    try {
      const request = response.request()
      return new URL(response.url()).origin === new URL(page.url()).origin && new URL(response.url()).pathname === '/api/command' && request.method() === 'POST' && request.postDataJSON()?.name === name
    } catch { return false }
  })
}

async function openUpgradePanel(page) {
  // Enter through the full production App's network dialog. Merely choosing a
  // panel does not configure networking or replace any persisted endpoint.
  await page.locator('.sidebar-title').getByRole('button', { name: 'Set up network', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'Choose how to connect', exact: true })
  await expect(dialog).toBeVisible()
  await dialog.locator('input[type="radio"][value="direct-lan"]').check()
  const panel = dialog.getByRole('region', { name: 'Upgrade saved Direct LAN pair', exact: true })
  await expect(panel).toBeVisible()
  return panel
}

async function verifyOrdinaryActivation(page, activation, originalDeadline) {
  const panel = await openUpgradePanel(page)
  // The UI intentionally gives connected and network-started the same cautious
  // wording. Observe the real response caused by the actual status control so
  // helper readiness, local-confirmed or connected cannot substitute for the
  // genuine native coordinator's network-started state.
  await expect.poll(async () => {
    const [response] = await Promise.all([
      commandResponse(page, 'direct-lan.upgrade.status'),
      panel.getByRole('button', { name: 'Check upgrade status', exact: true }).click(),
    ])
    const body = await response.json()
    const progress = body.result
    return response.ok() && body.ok === true && progress?.state === 'network-started' && progress.peerId === activation.peerId && typeof progress.deadline === 'string' && Number.isFinite(Date.parse(progress.deadline)) && (!originalDeadline || progress.deadline === originalDeadline) && !progress.errorCode
  }, { timeout: 40000, intervals: [500, 1000] }).toBe(true)
  const progress = panel.getByRole('status', { name: 'Upgrade progress', exact: true })
  await expect(progress).toContainText('Ordinary backend started. Network and application readiness require separate checks.')
  await expect(progress).toContainText(activation.peerId)
  // Native proof reads both real persisted pair/context records and genuine
  // ordinary activation. It is mandatory, rather than an optional marker.
  await expect.poll(() => activation.nativeActivationConfirmed(), { timeout: 15000 }).toBe(true)
}

test(productActivationCases[0], async ({ page, activation }) => {
  try {
    const panel = await openUpgradePanel(page)
    const savedPeer = panel.getByRole('combobox')
    await expect(savedPeer).toHaveCount(1)
    await savedPeer.selectOption(activation.peerId)
    await expect(savedPeer).toHaveValue(activation.peerId)
    const [response] = await Promise.all([
      commandResponse(page, 'direct-lan.upgrade.review'),
      panel.getByRole('button', { name: 'Review upgrade', exact: true }).click(),
    ])
    const body = await response.json(), review = body.result
    expect(response.ok() && body.ok === true && review?.peerId === activation.peerId && review.restartRequired === true && typeof review.revision === 'string' && review.revision.length > 0 && typeof review.deadline === 'string' && Date.parse(review.deadline) > Date.now()).toBe(true)
    const shown = panel.getByRole('region', { name: 'Review upgrade', exact: true })
    await expect(shown).toBeVisible()
    await expect(shown.locator('dd').nth(0)).toHaveText(review.peerId)
    await expect(shown.locator('dd').nth(1)).toHaveText(review.localEndpoint)
    await expect(shown.locator('dd').nth(2)).toHaveText(review.peerEndpoint)
    await expect(shown.locator('dd').nth(3)).toContainText(review.scope.family)
    await expect(shown.locator('li')).toHaveText(review.scope.prefixes)
    await expect(shown.locator('time').first()).toHaveAttribute('datetime', review.deadline)
    await expect(shown.locator('dd').nth(5)).toHaveText(review.revision)
    const [popup] = await Promise.all([
      page.waitForEvent('popup'),
      shown.getByRole('button', { name: 'Apply reviewed upgrade', exact: true }).click(),
    ])
    await expect(popup.locator('#continue')).toBeVisible()
    const claimed = JSON.parse(await popup.locator('#review').textContent())
    expect(claimed.peerId === review.peerId && claimed.localEndpoint === review.localEndpoint && claimed.peerEndpoint === review.peerEndpoint && claimed.revision === review.revision && claimed.deadline === review.deadline && JSON.stringify(claimed.scope) === JSON.stringify(review.scope)).toBe(true)
    expect(await activation.oldExited()).toBe(false)
    expect(await activation.successorStarted()).toBe(false)
    // Claim has already severed the helper's opener dependency. Retire the old
    // App tab before confirmation so its normal background polling cannot hit
    // an origin after the native supervisor has revoked ownership on exit.
    await page.close()
    await popup.getByRole('button', { name: 'Confirm restart and continue', exact: true }).click()
    await expect.poll(() => activation.oldExited()).toBe(true)
    await expect.poll(() => activation.successorStarted()).toBe(true)
    await expect(popup.locator('#management')).toBeVisible()
    // This is the production verified launcher gate with only the OS opening
    // function stubbed. It does not establish real OS browser-launch acceptance.
    expect(await activation.opened()).toBe(false)
    await popup.getByRole('button', { name: 'Open fresh local management', exact: true }).click()
    await expect.poll(() => activation.opened()).toBe(true)
    await expect(popup.locator('#open')).toBeDisabled()
    const fresh = await activation.normalSuccessorLogin(popup)
    await verifyOrdinaryActivation(fresh, activation, review.deadline)
  } catch { throw new Error('Product Web composition assertion failed; private details withheld') }
})

test.describe('production CLI entry', () => {
  test.use({ activationCase: 'product-core-cli' })
  test(productActivationCases[1], async ({ page, activation }) => {
    try {
      // Setup has authenticated the old full App. The private marker starts the
      // fixture-owned real terminal driver, which reviews and applies through
      // the production CLI with the exact frozen arguments. No Web command is
      // substituted for that CLI entry point.
      await page.close()
      await activation.startCLI()
      await expect.poll(() => activation.oldExited()).toBe(true)
      await expect.poll(() => activation.successorStarted()).toBe(true)
      const fresh = await activation.normalCLISuccessorLogin()
      await verifyOrdinaryActivation(fresh, activation)
    } catch { throw new Error('Product CLI composition assertion failed; private details withheld') }
  })
})
