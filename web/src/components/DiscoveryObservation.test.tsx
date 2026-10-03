import { useState } from 'react'
import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { Peer, State } from '../api'
import { discoveryEnglish, discoveryJapanese, discoveryText } from '../discovery-i18n'
import { translator } from '../i18n'
import { newServiceDraft, type ServiceDraft } from '../service-form'
import type { Server } from '../useServer'
import { DiscoveryObservation } from './DiscoveryObservation'
import { ServiceDialog } from './Dialogs'
const peer: Peer = { id: 'ordinary-peer', name: 'Service host', networks: ['tailnet'], online: true, verified: true, trusted: false, bridge: false, path: 'unknown' }
const state: State = { csrfToken: 'fictional-token', self: { name: 'Notebook', status: 'online' }, settings: { network: 'tailnet' }, peers: [peer], services: [], shares: [], messages: [], transfers: [] }
function server(): Server { return { state, auth: 'ready', stale: false, error: null, setError: vi.fn(), busy: new Set(), refresh: vi.fn(), run: vi.fn().mockResolvedValue({ ok: true }), handleError: vi.fn(), updatedAt: null } }
describe('truthful discovery observations', () => {
  for (const locale of ['en', 'ja'] as const) {
    it.each(['pending', 'confirmed', 'unconfirmed', 'unsupported', 'limited', 'stale'] as const)(`${locale}: explains %s without making installation or reachability claims`, async status => {
      const runtime = server(); const d = (key: string) => discoveryText(locale, key)
      render(<DiscoveryObservation peer={{ ...peer, discovery: { state: status, services: 0, checkedAt: '2026-10-03T05:00:00Z', code: status === 'limited' ? 'discovery_capacity' : undefined } }} locale={locale} t={translator(locale)} server={runtime} />)
      expect(screen.getByRole('region', { name: d('title') })).toHaveTextContent(d(status))
      expect(screen.getByRole('region')).toHaveTextContent(d(status === 'confirmed' ? 'emptyHint' : `${status}Hint`))
      expect(screen.getByText('2026-10-03T05:00:00Z')).toBeVisible()
      expect(runtime.run).not.toHaveBeenCalled()
      await userEvent.click(screen.getByRole('button', { name: d('refresh') }))
      expect(runtime.run).toHaveBeenCalledWith('discovery.refresh', { peerId: peer.id }, `discovery:${peer.id}`)
    })
  }
  it('keeps an ordinary peer selectable for a share and manual connection after unsupported discovery', async () => {
    const runtime = server(); const observed: Peer = { ...peer, discovery: { state: 'unsupported', services: 0 } }
    function Harness({ mode }: { mode: 'connect' | 'share' }) {
      const [draft, setDraft] = useState<ServiceDraft | undefined>()
      return <ServiceDialog server={runtime} peer={observed} state={{ ...state, peers: [observed] }} mode={mode} locale="en" t={translator('en')} onClose={() => {}} draft={draft} onDraft={setDraft} />
    }
    const view = render(<Harness mode="connect" />)
    expect(screen.getByText(discoveryText('en', 'unsupported'))).toBeVisible()
    fireEvent.change(screen.getByLabelText('Ports'), { target: { value: '8080' } })
    expect(screen.getByRole('button', { name: 'Start connection' })).toBeEnabled()
    view.unmount(); render(<Harness mode="share" />)
    expect(screen.getByRole('checkbox', { name: /Service host/ })).toBeChecked()
    fireEvent.change(screen.getByLabelText('Ports'), { target: { value: '8080' } })
    expect(screen.getByRole('button', { name: 'Start sharing' })).toBeEnabled()
    expect(runtime.run).not.toHaveBeenCalled()
  })
  it('does not invent an observation for older snapshots and disables retry on stale management state', () => {
    expect(Object.keys(discoveryJapanese)).toEqual(Object.keys(discoveryEnglish))
    const runtime = server(); const view = render(<DiscoveryObservation peer={peer} locale="en" t={translator('en')} server={runtime} />)
    expect(screen.queryByRole('region')).not.toBeInTheDocument()
    view.rerender(<DiscoveryObservation peer={{ ...peer, discovery: { state: 'stale', services: 7 } }} locale="en" t={translator('en')} server={{ ...runtime, stale: true }} />)
    expect(screen.getByRole('button', { name: 'Check shared services' })).toBeDisabled()
    expect(screen.queryByText('Advertised services: 7')).not.toBeInTheDocument()
  })
})
