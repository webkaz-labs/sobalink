import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import type { Locale, State } from '../api'
import type { Server } from '../useServer'
import { upgradeText } from '../upgrade-i18n'
import { DirectLanUpgrade } from './DirectLanUpgrade'
const peer = { id: 'synthetic-peer', name: 'Sample peer', networks: ['direct-lan'] as const, online: false, verified: true, trusted: true, bridge: false, path: 'unknown' as const }
function setup(locale: Locale = 'en', restartRequired = false) {
  const state: State = { csrfToken: 'synthetic', self: { name: 'Sample local', status: 'ready' }, peers: [{ ...peer, networks: ['direct-lan'] }], services: [], shares: [], messages: [], transfers: [], settings: { network: 'direct-lan' } }
  const run = vi.fn<Server['run']>().mockImplementation(async (name, payload) => {
    if (name === 'direct-lan.upgrade.review') return { ok: true, result: { ...payload, revision: 'synthetic-revision', localEndpoint: '192.168.50.10:48444', peerEndpoint: '192.168.50.20:48444', scope: { family: 'ipv4', prefixes: ['192.168.50.0/24'] }, restartRequired, resumePreparation: true, previousDeadline: '2026-01-01T00:00:00Z' } }
    if (name === 'direct-lan.upgrade.run') return { ok: true, result: { ...payload, state: 'network-started' } }
    return { ok: true, result: { state: 'idle' } }
  })
  const server: Server = { state, auth: 'ready', stale: false, error: null, setError: vi.fn(), busy: new Set(), refresh: vi.fn(), run, updatedAt: null, messageBlock: vi.fn(), messageGuardRevision: 0, handleError: vi.fn() }
  const view = render(<DirectLanUpgrade server={server} state={state} locale={locale} blocked={false} />)
  const u = (key: Parameters<typeof upgradeText>[1]) => upgradeText(locale, key)
  const inspect = async () => {
    fireEvent.change(screen.getByLabelText(u('peer')), { target: { value: peer.id } })
    fireEvent.click(screen.getByRole('button', { name: u('review') }))
    await screen.findByRole('region', { name: u('review') })
  }
  return { ...view, run, server, state, u, inspect }
}
describe('Direct LAN upgrade user consent', () => {
  it.each(['en', 'ja'] as const)('shows exact scope and resume lifetime before a separate apply in %s', async locale => {
    const v = setup(locale)
    expect(v.run).not.toHaveBeenCalled()
    await v.inspect()
    const region = screen.getByRole('region', { name: v.u('review') })
    for (const value of [peer.id, '192.168.50.10:48444', '192.168.50.20:48444', '192.168.50.0/24', '2026-01-01T00:00:00Z']) expect(region).toHaveTextContent(value)
    expect(v.run).toHaveBeenCalledTimes(1)
    const request = v.run.mock.calls[0][1] as { peerId: string; deadline: string }
    fireEvent.click(screen.getByRole('button', { name: v.u('apply') }))
    await waitFor(() => expect(v.run).toHaveBeenCalledWith('direct-lan.upgrade.run', { ...request, expectedRevision: 'synthetic-revision' }))
    await screen.findByText(v.u('network-started'))
    expect(JSON.stringify(v.run.mock.calls)).not.toMatch(/transcript|confirmed|receipt/)
  })
  it('does not offer a browser restart when the safe lifecycle helper is absent', async () => {
    const v = setup('en', true); await v.inspect()
    expect(screen.getByRole('button', { name: v.u('apply') })).toBeDisabled()
    expect(screen.getByText(v.u('restart'))).toBeInTheDocument()
    expect(v.run).toHaveBeenCalledTimes(1)
  })
  it('discards reviewed consent after selected inputs change', async () => {
    const v = setup(); await v.inspect()
    fireEvent.change(screen.getByLabelText(v.u('peer')), { target: { value: '' } })
    expect(screen.queryByRole('button', { name: v.u('apply') })).not.toBeInTheDocument()
    expect(v.run).toHaveBeenCalledTimes(1)
  })
  it('allows status and cancellation without supplying wire evidence', async () => {
    const v = setup()
    v.run.mockResolvedValueOnce({ ok: true, result: { state: 'preparing', peerId: peer.id, deadline: new Date(Date.now() + 240000).toISOString() } })
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: v.u('refresh') })) })
    expect(v.run).toHaveBeenCalledWith('direct-lan.upgrade.status', {})
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: v.u('cancel') })) })
    expect(v.run).toHaveBeenCalledWith('direct-lan.upgrade.cancel', {})
  })
  it('uses raw saved identities when mixed mode hides direct peers', async () => {
    const v = setup()
    const state: State = { ...v.state, peers: [], settings: { network: 'mixed' }, directLAN: { configured: true, listenerReady: false, peers: [{ key: peer.id, name: peer.name, endpoint: '192.168.50.20:48444' }] } }
    v.rerender(<DirectLanUpgrade server={{ ...v.server, state }} state={state} locale="en" blocked={false} />)
    await v.inspect()
    expect(v.run.mock.calls[0][1]).toMatchObject({ peerId: peer.id })
  })
  it('rejects a review returned for a different peer', async () => {
    const v = setup()
    v.run.mockResolvedValueOnce({ ok: true, result: { peerId: 'wrong-peer' } })
    fireEvent.change(screen.getByLabelText(v.u('peer')), { target: { value: peer.id } })
    fireEvent.click(screen.getByRole('button', { name: v.u('review') }))
    await screen.findByText(v.u('invalid'))
    expect(screen.queryByRole('button', { name: v.u('apply') })).not.toBeInTheDocument()
  })
})
