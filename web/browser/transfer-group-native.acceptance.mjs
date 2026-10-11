import assert from 'node:assert/strict'
import { test, expect } from './transfer-native-fixtures.mjs'

test('transfer-native-selected-failed-only-en-ja', async ({ page, app }) => {
  const { session } = app
  if (await page.locator('html').getAttribute('lang') !== 'en') await page.locator('.locale-toggle').click()
  await page.locator('.device-row').filter({ hasText: session.peerName }).click()
  await page.locator('.device-sections').getByRole('button', { name: 'Files & messages', exact: true }).click()
  const group = page.getByRole('region', { name: 'Transfers with this device', exact: true })
  await expect(group).toContainText('Failed: 2')
  await expect(group).toContainText('Succeeded: 0')
  await expect(group).toContainText('Only failed files in selected transfers are retried.')
  const selected = group.getByRole('checkbox')
  const selectedLabel = `Select for retry: ${session.selected.name} (${session.selected.direction === 'incoming' ? '↓' : '↑'})`
  await group.getByRole('checkbox', { name: selectedLabel, exact: true }).check()
  await expect(selected).toHaveCount(2)
  await page.locator('.locale-toggle').click()
  await page.setViewportSize({ width: 390, height: 844 })
  const japanese = page.getByRole('region', { name: 'この相手との転送', exact: true })
  await expect(japanese).toContainText('失敗: 2')
  await expect(japanese.getByRole('button', { name: '選択した失敗転送を再試行 (1)', exact: true })).toBeEnabled()
  const response = page.waitForResponse(response => {
    if (new URL(response.url()).origin !== new URL(session.viewer.url).origin || new URL(response.url()).pathname !== '/api/command') return false
    try { return JSON.parse(response.request().postData()).name === 'transfer.retry' } catch { return false }
  })
  await japanese.getByRole('button', { name: '選択した失敗転送を再試行 (1)', exact: true }).click()
  assert.equal((await response).status(), 200)
  await expect(japanese).toContainText('再試行を要求しました。完了は転送状況で確認してください。')
  await expect(page.locator(`[data-transfer-id="${session.selected.id}"][data-transfer-direction="${session.selected.direction}"]`).getByRole('article')).toHaveAttribute('aria-label', `${session.selected.name} · 保存済み`)
  await expect(page.locator(`[data-transfer-id="${session.untouched.id}"][data-transfer-direction="${session.untouched.direction}"]`).getByRole('article')).toHaveAttribute('aria-label', `${session.untouched.name} · 失敗`)
  await expect(japanese).toContainText('成功: 1')
  await expect(japanese).toContainText('失敗: 1')
  assert.deepEqual(app.counts(), { retries: 1, errors: 0 })
})
