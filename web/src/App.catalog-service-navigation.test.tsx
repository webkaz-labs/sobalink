import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { App } from './App'
import type { Locale, State } from './api'
import { translator } from './i18n'
import { ResourceCatalogController } from './catalog/controller'
import { ResourceSettingsController } from './resource/controller'
import { readCatalogRequest } from './catalog/decode'
import { appState, remoteSelection, responseFor } from './catalog/fixtures.test-support'
import { catalogText } from './catalog/i18n'
const initial: State = { ...appState, availableServices: [{ id: 'current-advertisement', peerId: remoteSelection.peerKey, name: 'Different current advertisement', network: 'tcp', ports: '9443', lifetime: 'until-revoked', status: 'active', revision: 'current-advertisement-review', checkedAt: '2023-11-14T22:13:20Z' }] }
function setup(locale: Locale = 'en') {
  let state = initial, failed = false
  const commands: { name: string; payload: unknown }[] = []
  localStorage.setItem('sobalink.locale', locale)
  vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
    if (path === '/api/state') { if (failed) throw new TypeError('Synthetic host failure'); return new Response(JSON.stringify(state)) }
    if (path !== '/api/command') throw new Error('Unexpected synthetic route')
    const request = JSON.parse(init!.body as string); commands.push(request)
    if (request.name !== 'resource.catalog.snapshot') throw new Error('Unexpected generic-navigation request')
    return new Response(JSON.stringify({ ok: true, result: responseFor(readCatalogRequest(request.payload)) }))
  }))
  return { commands, setState: (next: State) => { state = next }, failState: () => { failed = true } }
}
async function openSelected(locale: Locale = 'en') { const text = (key: Parameters<typeof catalogText>[1]) => catalogText(locale, key); await userEvent.click(await screen.findByRole('button', { name: text('title') })); await screen.findByRole('dialog', { name: text('title') }); await userEvent.click(screen.getByText(text('remote'))); await userEvent.selectOptions(screen.getByRole('combobox', { name: text('peer') }), remoteSelection.peerKey) }
async function openManual(locale: Locale = 'en') { await userEvent.click(screen.getByRole('button', { name: catalogText(locale, 'openServiceConnections') })); return within(await screen.findByRole('dialog', { name: translator(locale)('connectService') })) }
async function refresh() { await act(async () => { document.dispatchEvent(new Event('visibilitychange')) }) }
function captureOwner() { let owner: ResourceCatalogController | undefined; const open = ResourceCatalogController.prototype.open; vi.spyOn(ResourceCatalogController.prototype, 'open').mockImplementation(function (this: ResourceCatalogController) { owner = this; open.call(this) }); return () => owner! }
describe('generic catalog service-connections navigation', () => {
  it.each(['en', 'ja'] as const)('opens the %s manual form without copying stale row or current advertisement fields', async locale => {
    const h = setup(locale), t = translator(locale), text = (key: Parameters<typeof catalogText>[1]) => catalogText(locale, key); render(<App />); await openSelected(locale)
    await userEvent.click(screen.getByRole('checkbox', { name: text('local_settings') })); await userEvent.click(screen.getByRole('checkbox', { name: text('remoteServices') })); await userEvent.click(screen.getByRole('button', { name: text('refresh') })); const source = await screen.findByRole('region', { name: text('remote_service') }); expect(source).toHaveTextContent('activation-synthetic'); expect(within(source).queryByRole('button')).not.toBeInTheDocument()
    const previousHistory = history.state, dialog = await openManual(locale)
    expect(dialog.getByLabelText(t('availableServices'))).toHaveValue(''); expect(dialog.getByLabelText(t('ports'))).toHaveValue(''); expect(dialog.getByLabelText(t('localStart'))).toHaveValue(''); expect(dialog.queryByRole('option', { name: /activation-synthetic/ })).not.toBeInTheDocument(); expect(dialog.getByRole('option', { name: /Different current advertisement/ })).toBeInTheDocument(); expect(history.state).toEqual(previousHistory)
    expect(h.commands.map(call => call.name)).toEqual(['resource.catalog.snapshot']); await userEvent.click(dialog.getByRole('button', { name: t('cancel') })); expect(screen.queryByRole('dialog')).not.toBeInTheDocument(); expect(h.commands).toHaveLength(1)
  })
  it('requires an explicit current peer and opens without any catalog read', async () => { const h = setup(); render(<App />); await userEvent.click(await screen.findByRole('button', { name: catalogText('en', 'title') })); await userEvent.click(screen.getByText(catalogText('en', 'remote'))); expect(screen.getByRole('button', { name: catalogText('en', 'openServiceConnections') })).toBeDisabled(); await userEvent.selectOptions(screen.getByRole('combobox', { name: catalogText('en', 'peer') }), remoteSelection.peerKey); await openManual(); expect(h.commands).toEqual([]) })
  it('starts blank again rather than reusing a previously edited ordinary draft', async () => { const h = setup(), t = translator('en'); render(<App />); await openSelected(); let dialog = await openManual(); fireEvent.change(dialog.getByLabelText(t('ports')), { target: { value: '12345' } }); fireEvent.change(dialog.getByLabelText(t('localStart')), { target: { value: '18080' } }); await userEvent.click(dialog.getByRole('button', { name: t('cancel') })); await openSelected(); dialog = await openManual(); expect(dialog.getByLabelText(t('ports'))).toHaveValue(''); expect(dialog.getByLabelText(t('localStart'))).toHaveValue(''); expect(dialog.getByLabelText(t('availableServices'))).toHaveValue(''); expect(h.commands).toEqual([]) })
  it('keeps Close and browser Back or Forward inert with no implicit connection', async () => { const h = setup(), t = translator('en'); render(<App />); await openSelected(); let dialog = await openManual(); await userEvent.click(dialog.getAllByRole('button', { name: t('close') })[0]); expect(screen.queryByRole('dialog')).not.toBeInTheDocument(); await openSelected(); dialog = await openManual(); act(() => window.dispatchEvent(new PopStateEvent('popstate'))); expect(screen.queryByRole('dialog')).not.toBeInTheDocument(); act(() => window.dispatchEvent(new PopStateEvent('popstate'))); expect(screen.queryByRole('dialog')).not.toBeInTheDocument(); expect(h.commands).toEqual([]) })
  it('clears the selected-peer navigation after same-PID process turnover', async () => { const h = setup(); render(<App />); await openSelected(); h.setState({ ...initial, resourceCatalogProcessId: '9'.repeat(64) }); await refresh(); await waitFor(() => expect(screen.getByRole('combobox', { name: catalogText('en', 'peer') })).toHaveValue('')); expect(screen.getByRole('button', { name: catalogText('en', 'openServiceConnections') })).toBeDisabled(); expect(h.commands).toEqual([]) })
  it('hides navigation on a stale host and does not infer connection authority from a saved peer', async () => { const h = setup(); render(<App />); await openSelected(); h.failState(); await refresh(); await screen.findByText(catalogText('en', 'contextUnavailable')); expect(screen.queryByRole('button', { name: catalogText('en', 'openServiceConnections') })).not.toBeInTheDocument(); expect(h.commands).toEqual([]) })
  it('refuses a peer that disappeared before navigation', async () => { const h = setup(); render(<App />); await openSelected(); h.setState({ ...initial, peers: [] }); await refresh(); await waitFor(() => expect(screen.getByRole('button', { name: catalogText('en', 'openServiceConnections') })).toBeDisabled()); expect(screen.queryByRole('dialog', { name: translator('en')('connectService') })).not.toBeInTheDocument(); expect(h.commands).toEqual([]) })
  it('does not activate the destination after source-close context invalidation', async () => { const owner = captureOwner(), h = setup(); render(<App />); await openSelected(); let invalidated = false; const unsubscribe = owner().subscribe(() => { if (!invalidated && !owner().getSnapshot().opened) { invalidated = true; owner().updateContext({ ...owner().getSnapshot().context, available: false, revision: 'synthetic-auth-loss' }) } }); await userEvent.click(screen.getByRole('button', { name: catalogText('en', 'openServiceConnections') })); unsubscribe(); expect(screen.queryByRole('dialog', { name: translator('en')('connectService') })).not.toBeInTheDocument(); expect(h.commands).toEqual([]) })
  it('rechecks context after the settings owner closes before showing the manual form', async () => {
    const owner = captureOwner(), h = setup(); render(<App />); await openSelected()
    const original = ResourceSettingsController.prototype.close; let invalidated = false
    vi.spyOn(ResourceSettingsController.prototype, 'close').mockImplementation(function (this: ResourceSettingsController) { original.call(this); if (!invalidated) { invalidated = true; owner().updateContext({ ...owner().getSnapshot().context, available: false, revision: 'synthetic-process-change' }) } })
    await userEvent.click(screen.getByRole('button', { name: catalogText('en', 'openServiceConnections') })); expect(invalidated).toBe(true); expect(screen.queryByRole('dialog', { name: translator('en')('connectService') })).not.toBeInTheDocument(); expect(h.commands).toEqual([])
  })
  it('uses the existing backend readiness gate without configuring another network', async () => {
    const h = setup(); render(<App />); await openSelected(); h.setState({ ...initial, settings: { network: 'lan' } }); await refresh(); await waitFor(() => expect(screen.getByRole('combobox', { name: catalogText('en', 'peer') })).toHaveValue('')); await userEvent.selectOptions(screen.getByRole('combobox', { name: catalogText('en', 'peer') }), remoteSelection.peerKey); await userEvent.click(screen.getByRole('button', { name: catalogText('en', 'openServiceConnections') })); expect(screen.queryByRole('dialog', { name: translator('en')('connectService') })).not.toBeInTheDocument(); expect(screen.getByRole('alert')).toHaveTextContent(catalogText('en', 'unavailable')); expect(h.commands).toEqual([])
  })
  it('does not activate the destination after a newer browser navigation during source close', async () => { const owner = captureOwner(), h = setup(); render(<App />); await openSelected(); let navigated = false; const unsubscribe = owner().subscribe(() => { if (!navigated && !owner().getSnapshot().opened) { navigated = true; window.dispatchEvent(new PopStateEvent('popstate')) } }); await userEvent.click(screen.getByRole('button', { name: catalogText('en', 'openServiceConnections') })); unsubscribe(); expect(screen.queryByRole('dialog')).not.toBeInTheDocument(); expect(h.commands).toEqual([]) })
})
