import { useState } from 'react'
import { act, fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { Locale, State } from '../api'
import { cardDigest, type DeviceCardMode } from '../device-cards'
import { deviceCardText } from '../device-card-i18n'
import { directLanText } from '../direct-lan-i18n'
import { translator } from '../i18n'
import type { Server } from '../useServer'
import { DirectLanSetup, emptyDirectLanDraft, type DirectLanDraft } from './DirectLanSetup'
import { LanSetup, emptyLanDraft, type LanDraft } from './LanSetup'
const publicKey = 'a1'.repeat(32), remoteKey = 'b2'.repeat(32), certificateSHA256 = 'c3'.repeat(32)
function state(mode: DeviceCardMode): State {
  return { csrfToken: 'synthetic-csrf', self: { name: 'Synthetic local device', status: 'online' }, peers: [], messages: [], transfers: [], services: [], shares: [], settings: { network: mode },
    lan: { configured: true, pairingReady: true, publicKey, path: 'relay', relay: { kind: 'relay', address: '192.0.2.1:443', certificateSHA256 } },
    directLAN: { configured: true, listenerReady: true, publicKey, endpoint: '192.168.50.1:48444', prefixes: ['192.168.50.0/24'] } }
}
function setup(mode: DeviceCardMode, locale: Locale = 'en') {
  const user = userEvent.setup(), run = vi.fn<Server['run']>(), original = mode === 'lan'
    ? { ...emptyLanDraft(), relayAddress: '192.0.2.1:443', relayPin: certificateSHA256, policyMode: 'trusted-relay' as const, policyPrefixes: '' }
    : { ...emptyDirectLanDraft(), listen: '192.168.50.1:48444', prefixes: '192.168.50.0/24' }
  const currentState = state(mode)
  const server = { state: currentState, auth: 'ready', stale: false, run, busy: new Set(), setError: vi.fn(), refresh: vi.fn() } as unknown as Server
  let current: LanDraft | DirectLanDraft = original
  function Harness({ next = currentState }: { next?: State }) {
    const [lanDraft, setLanDraft] = useState(original as LanDraft), [directDraft, setDirectDraft] = useState(original as DirectLanDraft), [hostname, setHostname] = useState('Synthetic local device')
    current = mode === 'lan' ? lanDraft : directDraft
    return mode === 'lan' ? <LanSetup server={server} state={next} t={translator(locale)} locale={locale} hostname={hostname} setHostname={setHostname} draft={lanDraft} setDraft={setLanDraft} onViewPeer={vi.fn()} />
      : <DirectLanSetup server={server} state={next} t={translator(locale)} locale={locale} hostname={hostname} setHostname={setHostname} draft={directDraft} setDraft={setDirectDraft} onViewPeer={vi.fn()} />
  }
  const view = render(<Harness />), c = (key: Parameters<typeof deviceCardText>[1]) => deviceCardText(locale, key)
  const remote = { version: 1, mode, publicKey: remoteKey, name: 'Synthetic recipient', ...(mode === 'lan' ? { relay: { address: '192.0.2.99:444', certificateSHA256: 'd4'.repeat(32) } } : { endpoint: '192.168.99.3:48446' }) }
  const text = 'soba-card1.' + btoa(JSON.stringify(remote)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
  const inspect = async () => {
    fireEvent.click(screen.getByRole('button', { name: c('open') })); fireEvent.change(screen.getByLabelText(c('input')), { target: { value: text } })
    run.mockResolvedValueOnce({ ok: true, result: { ...remote, verification: 'unverified', freshness: 'unknown', contentDigest: await cardDigest(text) } })
    await user.click(screen.getByRole('button', { name: c('inspect') })); await screen.findByRole('region', { name: c('review') })
  }
  return { ...view, user, run, original, c, inspect, draft: () => current, update: (next: State) => view.rerender(<Harness next={next} />) }
}
describe('device cards inside existing network setup', () => {
  it.each([['lan', 'en'], ['lan', 'ja'], ['direct-lan', 'en'], ['direct-lan', 'ja']] as const)('fills only the inert %s draft in %s and keeps invitation creation separate', async (mode, locale) => {
    const v = setup(mode, locale); await v.inspect(); expect(v.draft()).toEqual(v.original)
    await v.user.click(screen.getByRole('button', { name: v.c('use') }))
    expect(v.draft()).toEqual({ ...v.original, ...(mode === 'lan' ? { recipientPublicKey: remoteKey, recipientName: 'Synthetic recipient' } : { recipient: remoteKey, name: 'Synthetic recipient' }) })
    expect(v.run.mock.calls.map(call => call[0])).toEqual(['device-card.inspect']); expect(screen.getByRole('status')).toHaveTextContent(v.c('used'))
    const createLabel = mode === 'lan' ? translator(locale)('createInvitation') : directLanText(locale, 'inviteAction'); expect(screen.getByRole('button', { name: createLabel })).toBeEnabled()
  })
  it.each(['lan', 'direct-lan'] as const)('discards %s review on invitation-tab navigation', async mode => {
    const v = setup(mode); await v.inspect(); const join = mode === 'lan' ? translator('en')('joinDevice') : directLanText('en', 'join')
    await v.user.click(screen.getByRole('button', { name: join })); expect(screen.queryByRole('button', { name: v.c('use') })).not.toBeInTheDocument(); expect(v.draft()).toMatchObject(v.original); expect(v.run.mock.calls.map(call => call[0])).toEqual(['device-card.inspect'])
  })
  it.each(['lan', 'direct-lan'] as const)('discards %s review when saved endpoints or permissions change', async mode => {
    const v = setup(mode); await v.inspect(); const next = state(mode)
    if (mode === 'lan') next.lan!.relay!.certificateSHA256 = 'd4'.repeat(32)
    else next.directLAN!.prefixes = ['192.168.50.1/32']
    act(() => v.update(next)); expect(screen.queryByRole('button', { name: v.c('use') })).not.toBeInTheDocument(); expect(v.draft()).toEqual(v.original)
  })
})
