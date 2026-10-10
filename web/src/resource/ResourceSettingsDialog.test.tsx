import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { ApiError, type Locale } from '../api'
import { translator } from '../i18n'
import { ResourceSettingsDialog } from './ResourceSettingsDialog'
import { resourceEnglish as en, resourceJapanese as ja } from './i18n'
import { changedSettings, context, deferred, descriptor, harness, localOperation, remotePreview, remoteReply, remoteSelection, response, type Call } from './fixtures.test-support'
import type { ResourceSettingsController } from './controller'

function show(controller: ResourceSettingsController, locale: Locale = 'en', close = vi.fn()) {
  return { ...render(<ResourceSettingsDialog controller={controller} locale={locale} t={translator(locale)} onClose={close} />), close }
}
async function openLocal(locale: Locale = 'en') {
  const text = locale === 'ja' ? ja : en
  await userEvent.click(screen.getByRole('button', { name: text.loadLocal }))
  await userEvent.click(await screen.findByRole('button', { name: text.selectLocal }))
  await waitFor(() => expect(screen.getByRole('button', { name: text.preview })).toBeEnabled())
}
async function openRemote(expectedReview: 'enabled' | 'blocked' = 'enabled') {
  await userEvent.click(screen.getByRole('button', { name: en.remote }))
  await userEvent.selectOptions(screen.getByRole('combobox', { name: en.peer }), remoteSelection.peerKey)
  await userEvent.selectOptions(screen.getByRole('combobox', { name: en.protocol }), '2')
  await userEvent.type(screen.getByRole('textbox', { name: en.resourceId }), remoteSelection.selector.target.resourceId)
  await userEvent.type(screen.getByRole('textbox', { name: en.grantId }), remoteSelection.selector.grantId)
  await userEvent.type(screen.getByRole('textbox', { name: en.grantRevision }), String(remoteSelection.selector.grantRevision))
  await userEvent.click(screen.getByRole('button', { name: en.selectRemote }))
  await waitFor(() => expect(screen.getByRole('button', { name: en.refresh })).toBeEnabled())
  const review = screen.getByRole('button', { name: en.preview })
  if (expectedReview === 'blocked') expect(review).toBeDisabled()
  else expect(review).toBeEnabled()
}
describe('single-resource settings dialog', () => {
  it.each(['en', 'ja'] as const)('keeps the %s flow explicit, bilingual, and unmodified in machine IDs', async locale => {
    const text = locale === 'ja' ? ja : en, { controller, calls } = harness()
    show(controller, locale)
    expect(calls).toHaveLength(0)
    expect(screen.getByText(text.admission)).toBeVisible()
    await openLocal(locale)
    expect(screen.getAllByText(descriptor.resourceId).length).toBeGreaterThan(0)
    expect(screen.queryByRole('option', { name: /unlimited|無制限/i })).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: text.preview }))
    const review = await screen.findByRole('region', { name: text.review })
    expect(within(review).getByRole('heading', { name: text.review })).toHaveFocus()
    expect(within(review).getByRole('button', { name: text.apply })).toBeDisabled()
    expect(calls.some(call => call.name.endsWith('.apply'))).toBe(false)
    await userEvent.click(within(review).getByRole('checkbox', { name: text.confirmation }))
    await userEvent.click(within(review).getByRole('button', { name: text.apply }))
    expect(await screen.findByText(text.applied)).toBeVisible()
    expect(calls.filter(call => call.name === 'resource.apply')).toHaveLength(1)
    expect(screen.getByText(text.currentSeparate)).toBeVisible()
  })
  it('rejects invalid numeric choices locally and preserves a changed draft through Back to edit', async () => {
    const { controller, calls } = harness(); show(controller); await openLocal()
    await userEvent.selectOptions(screen.getByRole('combobox', { name: en.files }), 'limited')
    const input = screen.getByRole('textbox', { name: `${en.files}: ${en.limitValue}` })
    for (const value of ['0', '-1', '1.5', '1e3', '9007199254740992']) {
      fireEvent.change(input, { target: { value } })
      expect(screen.getByRole('button', { name: en.preview })).toBeDisabled()
    }
    fireEvent.change(input, { target: { value: '7' } })
    await userEvent.click(screen.getByRole('button', { name: en.preview }))
    await userEvent.click(await screen.findByRole('button', { name: en.back }))
    expect(screen.getByRole('textbox', { name: `${en.files}: ${en.limitValue}` })).toHaveValue('7')
    expect(screen.getByRole('combobox', { name: en.files })).toHaveFocus()
    expect(calls.some(call => call.name.endsWith('.apply'))).toBe(false)
    await userEvent.click(screen.getByRole('button', { name: en.preview }))
    expect(await screen.findByRole('button', { name: en.apply })).toBeDisabled()
  })
  it('cancel and Escape before dispatch send no mutation and restore the opener focus', async () => {
    const { controller, calls } = harness()
    const opener = document.createElement('button'); opener.textContent = 'Synthetic opener'; document.body.append(opener); opener.focus()
    const view = show(controller); await openLocal()
    await userEvent.click(screen.getByRole('button', { name: en.preview }))
    await userEvent.click(await screen.findByRole('button', { name: en.cancelReview }))
    expect(screen.queryByRole('checkbox', { name: en.confirmation })).not.toBeInTheDocument()
    fireEvent(screen.getByRole('dialog'), new Event('cancel', { cancelable: true }))
    expect(view.close).toHaveBeenCalledOnce()
    view.unmount()
    expect(opener).toHaveFocus()
    opener.remove()
    expect(calls.some(call => call.name.endsWith('.apply'))).toBe(false)
  })
  it('requires explicit remote selectors, displays migration scope, and invalidates review on every selector edit', async () => {
    const { controller, calls } = harness(); show(controller)
    await userEvent.click(screen.getByRole('button', { name: en.remote }))
    expect(screen.getByRole('button', { name: en.selectRemote })).toBeDisabled()
    expect(calls).toHaveLength(0)
    await openRemote()
    await userEvent.click(screen.getByRole('button', { name: en.preview }))
    expect(await screen.findByText(en.migration)).toBeVisible()
    expect(screen.getByText(en.managementScope)).toBeVisible()
    expect(screen.getByText(remotePreview.operationId)).toBeVisible()
    fireEvent.change(screen.getByRole('textbox', { name: en.grantRevision }), { target: { value: '2' } })
    expect(screen.queryByRole('button', { name: en.apply })).not.toBeInTheDocument()
    expect(controller.getSnapshot().selection).toBeNull()
    expect(calls.some(call => call.name.endsWith('.apply'))).toBe(false)
  })
  it('keeps Stop waiting separate from cancellation and status-only recovery after reopening', async () => {
    const wait = deferred()
    const { controller, calls } = harness(call => call.name === 'resource.remote.management.apply' ? wait.promise : response(call))
    const view = show(controller); await openRemote()
    await userEvent.click(screen.getByRole('button', { name: en.preview }))
    await userEvent.click(await screen.findByRole('checkbox', { name: en.confirmation }))
    await userEvent.click(screen.getByRole('button', { name: en.apply }))
    expect(screen.getByText(en.stopHint)).toBeVisible()
    await userEvent.click(screen.getByRole('button', { name: en.stopWaiting }))
    expect(screen.getByText(en.unknown)).toBeVisible()
    expect(screen.getByRole('button', { name: en.preview })).toBeDisabled()
    view.unmount(); show(controller); await openRemote('blocked')
    expect(screen.getByText(remotePreview.operationId)).toBeVisible()
    await act(async () => { wait.resolve(remoteReply('apply')); await wait.promise })
    expect(screen.queryByText(en.applied)).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: en.checkStatus }))
    expect(await screen.findByText(en.applied)).toBeVisible()
    expect(calls.filter(call => call.name === 'resource.remote.management.apply')).toHaveLength(1)
  })
  it('renders historical outcome independently from a different current local descriptor', async () => {
    const { controller } = harness(call => call.name === 'resource.apply' ? { ...localOperation, current: { ...descriptor, requested: changedSettings } } : response(call))
    show(controller); await openLocal()
    await userEvent.click(screen.getByRole('button', { name: en.preview }))
    await userEvent.click(await screen.findByRole('checkbox', { name: en.confirmation }))
    await userEvent.click(screen.getByRole('button', { name: en.apply }))
    expect(await screen.findByText(en.applied)).toBeVisible()
    expect(within(screen.getByRole('region', { name: en.lastObserved })).getByText('7')).toBeVisible()
    expect(screen.getByText(en.currentSeparate)).toBeVisible()
  })
  it('clears visible selector data and controls on authentication loss without replay', async () => {
    const handler = (call: Call) => { if (call.name === 'resource.remote.management.apply') throw new ApiError('unauthenticated', ''); return response(call) }
    const { controller, calls } = harness(handler); show(controller); await openRemote()
    await userEvent.click(screen.getByRole('button', { name: en.preview }))
    await userEvent.click(await screen.findByRole('checkbox', { name: en.confirmation }))
    await userEvent.click(screen.getByRole('button', { name: en.apply }))
    expect(await screen.findByText(en.contextUnavailable)).toBeVisible()
    expect(screen.queryByText(remoteSelection.peerKey)).not.toBeInTheDocument()
    expect(screen.queryByText(remotePreview.operationId)).not.toBeInTheDocument()
    act(() => { controller.updateContext({ ...context, authenticated: false }); controller.updateContext(context) })
    expect(screen.getByRole('textbox', { name: en.grantId })).toHaveValue('')
    expect(calls.filter(call => call.name === 'resource.remote.management.apply')).toHaveLength(1)
  })
})
