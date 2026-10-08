import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import type { Locale, State } from '../api'
import type { Server } from '../useServer'
import { endpointText } from '../endpoint-i18n'
import { DirectLanEndpoint } from './DirectLanEndpoint'

function fixture(locale: Locale = 'en') {
  const state: State = { csrfToken: 'synthetic', self: { name: 'Sample', status: 'ready' }, peers: [], services: [], shares: [], messages: [], transfers: [], settings: { network: 'direct-lan' }, directLAN: { configured: true, listenerReady: true, endpoint: '127.0.0.1:48444', peers: [{ key: 'synthetic-peer', name: 'Sample peer', endpoint: '127.0.0.3:48444' }] } }
  const run = vi.fn<Server['run']>().mockImplementation(async (name, payload) => {
    if (name === 'direct-lan.endpoint.move.preview') return { ok: true, result: { ...payload, revision: 'exact-review', previousEndpoint: '127.0.0.1:48444', destinations: {} } }
    if (name === 'direct-lan.endpoint.move.apply') return { ok: true, result: { saved: true, active: false, deliveries: [], endpointUpdatesEnabled: true } }
    return { ok: true, result: { state: 'saved_unavailable', endpoint: '127.0.0.1:48444', recoveryRequired: false, endpointUpdatesEnabled: true, peers: [] } }
  })
  const server: Server = { state, auth: 'ready', stale: false, error: null, setError: vi.fn(), busy: new Set(), refresh: vi.fn(), run, updatedAt: null, messageBlock: vi.fn(), messageGuardRevision: 0, handleError: vi.fn() }
  const view = render(<DirectLanEndpoint server={server} state={state} locale={locale} blocked={false} />)
  view.container.querySelector('details')!.open = true
  const e = (key: Parameters<typeof endpointText>[1]) => endpointText(locale, key)
  const reviewMove = async () => {
    fireEvent.change(screen.getByLabelText(e('endpoint')), { target: { value: '127.0.0.2:48444' } })
    fireEvent.click(screen.getByRole('button', { name: e('review') }))
    await screen.findByRole('region', { name: e('review') })
  }
  return { ...view, state, server, run, e, reviewMove }
}
describe('reviewed endpoint controls', () => {
  it.each(['ja', 'en'] as const)('requires separate exact consent and sends no implicit delivery in %s', async locale => {
    const f = fixture(locale)
    expect(f.run).not.toHaveBeenCalled()
    await f.reviewMove()
    expect(f.run).toHaveBeenCalledWith('direct-lan.endpoint.move.preview', { endpoint: '127.0.0.2:48444', deliveries: [] })
    expect(screen.getByRole('button', { name: f.e('apply') })).toBeDisabled()
    fireEvent.click(screen.getByLabelText(f.e('consent')))
    fireEvent.click(screen.getByRole('button', { name: f.e('apply') }))
    await waitFor(() => expect(f.run).toHaveBeenCalledWith('direct-lan.endpoint.move.apply', { endpoint: '127.0.0.2:48444', deliveries: [], expectedRevision: 'exact-review' }))
    expect(f.run.mock.calls.filter(([name]) => name.endsWith('.apply'))).toHaveLength(1)
    await screen.findByText('false')
    expect(screen.queryByRole('button', { name: f.e('apply') })).not.toBeInTheDocument()
  })
  it('discards consent on edits, Back and browser navigation without applying', async () => {
    const f = fixture(); await f.reviewMove()
    fireEvent.click(screen.getByRole('button', { name: f.e('back') }))
    expect(screen.queryByLabelText(f.e('consent'))).not.toBeInTheDocument()
    await f.reviewMove()
    fireEvent.change(screen.getByLabelText(f.e('endpoint')), { target: { value: '127.0.0.4:48444' } })
    expect(screen.queryByLabelText(f.e('consent'))).not.toBeInTheDocument()
    await f.reviewMove()
    act(() => window.dispatchEvent(new PopStateEvent('popstate')))
    expect(screen.queryByLabelText(f.e('consent'))).not.toBeInTheDocument()
    expect(f.run.mock.calls.every(([name]) => name.endsWith('.preview'))).toBe(true)
  })
  it('drops a late preview when another tab changes the displayed binding', async () => {
    const f = fixture()
    let finish!: (result: Awaited<ReturnType<Server['run']>>) => void
    f.run.mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    fireEvent.change(screen.getByLabelText(f.e('endpoint')), { target: { value: '127.0.0.2:48444' } })
    fireEvent.click(screen.getByRole('button', { name: f.e('review') }))
    const state = { ...f.state, directLAN: { ...f.state.directLAN!, endpoint: '127.0.0.4:48444' } }
    f.rerender(<DirectLanEndpoint server={{ ...f.server, state }} state={state} locale="en" blocked={false} />)
    await act(async () => finish({ ok: true, result: { revision: 'old-review', endpoint: '127.0.0.2:48444', deliveries: [], destinations: {} } }))
    expect(screen.queryByLabelText(f.e('consent'))).not.toBeInTheDocument()
  })
  it('keeps the exact apply result when its own move refreshes the binding', async () => {
    const f = fixture(); await f.reviewMove()
    let finish!: (result: Awaited<ReturnType<Server['run']>>) => void
    f.run.mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    fireEvent.click(screen.getByLabelText(f.e('consent')))
    fireEvent.click(screen.getByRole('button', { name: f.e('apply') }))
    const state = { ...f.state, directLAN: { ...f.state.directLAN!, endpoint: '127.0.0.2:48444' } }
    f.rerender(<DirectLanEndpoint server={{ ...f.server, state }} state={state} locale="en" blocked={false} />)
    await act(async () => finish({ ok: true, result: { saved: true, active: false, deliveries: [], endpointUpdatesEnabled: true } }))
    await screen.findByText('false')
    expect(screen.getByText(f.e('result'))).toBeInTheDocument()
  })
  it('does not retry uncertain mutation and requires a fresh status read', async () => {
    const f = fixture(); await f.reviewMove()
    f.run.mockResolvedValueOnce(undefined)
    fireEvent.click(screen.getByLabelText(f.e('consent')))
    fireEvent.click(screen.getByRole('button', { name: f.e('apply') }))
    await screen.findByText(f.e('unknown'))
    expect(screen.getByRole('button', { name: f.e('review') })).toBeDisabled()
    expect(f.run).toHaveBeenCalledTimes(2)
    fireEvent.click(screen.getByRole('button', { name: f.e('refresh') }))
    await waitFor(() => expect(screen.getByRole('button', { name: f.e('review') })).toBeEnabled())
    expect(f.run).toHaveBeenLastCalledWith('direct-lan.endpoint.status', {})
  })
  it.each(['popstate', 'pagehide'])('retains unresolved completion after %s discards an in-flight apply', async eventName => {
    const f = fixture(); await f.reviewMove()
    let finish!: (result: Awaited<ReturnType<Server['run']>>) => void
    f.run.mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    fireEvent.click(screen.getByLabelText(f.e('consent')))
    fireEvent.click(screen.getByRole('button', { name: f.e('apply') }))
    act(() => window.dispatchEvent(new Event(eventName)))
    await act(async () => finish({ ok: true, result: { saved: true, active: true } }))
    await screen.findByText(f.e('unknown'))
    expect(screen.queryByText(f.e('result'))).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: f.e('review') })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: f.e('review') }))
    expect(f.run).toHaveBeenCalledTimes(2)
    fireEvent.click(screen.getByRole('button', { name: f.e('refresh') }))
    await waitFor(() => expect(screen.getByRole('button', { name: f.e('review') })).toBeEnabled())
    expect(f.run).toHaveBeenLastCalledWith('direct-lan.endpoint.status', {})
  })
  it('does not resolve an uncertain mutation from malformed result or status objects', async () => {
    const f = fixture(); await f.reviewMove()
    f.run.mockResolvedValueOnce({ ok: true, result: {} })
    fireEvent.click(screen.getByLabelText(f.e('consent')))
    fireEvent.click(screen.getByRole('button', { name: f.e('apply') }))
    await screen.findByText(f.e('unknown'))
    expect(screen.getByRole('button', { name: f.e('review') })).toBeDisabled()
    f.run.mockResolvedValueOnce({ ok: true, result: {} })
    fireEvent.click(screen.getByRole('button', { name: f.e('refresh') }))
    await screen.findByText(f.e('unknown'))
    expect(screen.getByRole('button', { name: f.e('review') })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: f.e('refresh') }))
    await waitFor(() => expect(screen.getByRole('button', { name: f.e('review') })).toBeEnabled())
  })
  it('allows only explicit status inspection during recovery and does not restart', async () => {
    const f = fixture()
    f.run.mockResolvedValueOnce({ ok: true, result: { stateVersion: 4, endpoint: '127.0.0.1:48444', endpointUpdatesEnabled: false, recoveryRequired: true, pendingTransactionId: 'transaction-1', peers: [] } })
    fireEvent.click(screen.getByRole('button', { name: f.e('refresh') }))
    await waitFor(() => expect(f.run).toHaveBeenCalledTimes(1))
    fireEvent.change(screen.getByLabelText(f.e('action')), { target: { value: 'recover' } })
    f.run.mockResolvedValueOnce({ ok: true, result: { revision: 'recovery-review', transactionId: 'transaction-1', cancel: false, canCancel: false, before: [], after: [] } })
    fireEvent.click(screen.getByRole('button', { name: f.e('review') }))
    await screen.findByRole('region', { name: f.e('review') })
    expect(f.run).toHaveBeenLastCalledWith('direct-lan.endpoint.recovery.inspect', { transactionId: 'transaction-1', cancel: false })
    expect(f.run.mock.calls.some(([name]) => name === 'network.configure' || name === 'application.stop')).toBe(false)
  })
})
