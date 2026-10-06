import { useState } from 'react'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { Locale, State } from '../api'
import { translator } from '../i18n'
import { directLanText } from '../direct-lan-i18n'
import type { Server } from '../useServer'
import { DirectLanSetup, emptyDirectLanDraft, type DirectLanDraft } from './DirectLanSetup'
const publicKey = 'a1'.repeat(32), remoteKey = 'b2'.repeat(32), secret = 'soba-directlan1.synthetic-private-token'
const readyState = (): State => ({ csrfToken: 'synthetic-csrf', self: { name: 'synthetic-device', status: 'ready' }, peers: [], messages: [], transfers: [], services: [], shares: [], settings: { network: 'direct-lan' }, directLAN: { configured: true, listenerReady: true, publicKey, endpoint: '192.168.50.10:48444', prefixes: ['192.168.50.0/24'] } })
function setup({ state = readyState(), draft = emptyDirectLanDraft(), locale = 'en' }: { state?: State; draft?: DirectLanDraft; locale?: Locale } = {}) {
  const user = userEvent.setup(), run = vi.fn<Server['run']>().mockResolvedValue({ ok: true }), setError = vi.fn()
  const server: Server = { state, auth: 'ready', stale: false, error: null, setError, busy: new Set(), refresh: vi.fn(), run, updatedAt: null, messageBlock: vi.fn(), messageGuardRevision: 0, handleError: vi.fn() }
  let currentDraft = draft
  function Harness({ current = state }: { current?: State }) {
    const [value, setDraft] = useState(draft); currentDraft = value
    const [hostname, setHostname] = useState('synthetic-device')
    return <DirectLanSetup server={{ ...server, state: current }} state={current} locale={locale} t={translator(locale)} hostname={hostname} setHostname={setHostname} draft={value} setDraft={setDraft} onViewPeer={vi.fn()} />
  }
  const view = render(<Harness />)
  return { ...view, user, run, setError, draft: () => currentDraft, update: (current: State) => view.rerender(<Harness current={current} />), d: (key: Parameters<typeof directLanText>[1]) => directLanText(locale, key) }
}
describe('Direct LAN setup', () => {
  it.each(['en', 'ja'] as const)('reviews exact scope before configuring in %s', async locale => {
    const state = readyState(); state.settings = { network: 'none' }; delete state.directLAN
    const v = setup({ state, locale })
    expect(v.run).not.toHaveBeenCalled()
    fireEvent.change(screen.getByLabelText(v.d('listen'), { exact: false }), { target: { value: '192.168.50.10:48444' } })
    fireEvent.change(screen.getByLabelText(v.d('prefixes'), { exact: false }), { target: { value: '192.168.50.0/24' } })
    await v.user.click(screen.getByRole('button', { name: v.d('review') }))
    expect(v.run).not.toHaveBeenCalled(); expect(screen.getByRole('region', { name: v.d('reviewTitle') })).toHaveTextContent('192.168.50.10:48444')
    await v.user.click(screen.getByRole('button', { name: v.d('start') }))
    expect(v.run).toHaveBeenCalledWith('network.configure', { mode: 'direct-lan', hostname: 'synthetic-device', directLAN: { listen: '192.168.50.10:48444', prefixes: ['192.168.50.0/24'] } })
  })
  it.each(['public.example.test:48444', '8.8.8.8:48444', '0.0.0.0:48444', '127.0.0.1:443'])('rejects nonprivate/hostname/privileged endpoint %s', async endpoint => {
    const state = readyState(); state.settings = { network: 'none' }; delete state.directLAN
    const v = setup({ state, draft: { ...emptyDirectLanDraft(), listen: endpoint, prefixes: '127.0.0.1/32' } })
    await v.user.click(screen.getByRole('button', { name: v.d('review') })); expect(v.run).not.toHaveBeenCalled(); expect(screen.getByRole('alert')).toBeInTheDocument()
  })
  it('creates only one invitation and cancels exactly that token', async () => {
    const v = setup({ draft: { ...emptyDirectLanDraft(), recipient: remoteKey, name: 'Synthetic recipient' } })
    v.run.mockResolvedValueOnce({ ok: true, result: { invitation: secret, expires: new Date(Date.now() + 300000).toISOString(), recipientPublicKey: remoteKey } })
    await v.user.dblClick(screen.getByRole('button', { name: v.d('inviteAction') }))
    expect(v.run.mock.calls.filter(c => c[0] === 'direct-lan.invite')).toHaveLength(1)
    expect(screen.getByDisplayValue(secret)).toBeInTheDocument()
    await v.user.click(screen.getByRole('button', { name: v.d('cancelInvite') }))
    expect(v.run).toHaveBeenLastCalledWith('direct-lan.cancel', { invitation: secret }); expect(screen.queryByDisplayValue(secret)).not.toBeInTheDocument()
  })
  it('reviews a recipient-bound invitation, invalidates edit and never auto-joins', async () => {
    const v = setup({ draft: { ...emptyDirectLanDraft(), invitation: secret } })
    v.run.mockResolvedValue({ ok: true, result: { hostPublicKey: remoteKey, hostName: 'Synthetic host', endpoint: '192.168.50.11:48444', recipientPublicKey: publicKey, recipientMatches: true, expires: new Date(Date.now() + 300000).toISOString() } })
    await v.user.click(screen.getByRole('button', { name: v.d('inspect') }))
    expect(v.run).toHaveBeenCalledTimes(1); expect(screen.getByRole('region', { name: v.d('joinTitle') })).toHaveTextContent(remoteKey)
    fireEvent.change(screen.getByLabelText(v.d('paste')), { target: { value: secret + 'changed' } })
    expect(screen.queryByRole('button', { name: v.d('pair') })).not.toBeInTheDocument(); expect(v.run).toHaveBeenCalledTimes(1)
  })
  it('rejects a mismatched recipient preview and keeps grants separate after joining', async () => {
    const v = setup({ draft: { ...emptyDirectLanDraft(), invitation: secret } })
    const preview = { hostPublicKey: remoteKey, hostName: 'Synthetic host', endpoint: '192.168.50.11:48444', recipientPublicKey: remoteKey, recipientMatches: true, expires: new Date(Date.now() + 300000).toISOString() }
    v.run.mockResolvedValueOnce({ ok: true, result: preview }); await v.user.click(screen.getByRole('button', { name: v.d('inspect') })); expect(v.setError).toHaveBeenCalledWith({ code: 'invalid_response' })
    v.run.mockResolvedValueOnce({ ok: true, result: { ...preview, recipientPublicKey: publicKey } }); await v.user.click(screen.getByRole('button', { name: v.d('inspect') }))
    v.run.mockResolvedValueOnce({ ok: true, result: { paired: true, trusted: false, peerId: remoteKey } }); await v.user.click(screen.getByRole('button', { name: v.d('pair') }))
    expect(v.run).toHaveBeenLastCalledWith('direct-lan.join', { invitation: secret }); expect(v.run.mock.calls.some(c => c[0] === 'peer.trust')).toBe(false); expect(v.draft().invitation).toBe(''); expect(screen.getByRole('status')).toHaveTextContent(v.d('consumed'))
  })
  it('clears an expired or consumed invitation secret from the DOM', async () => {
    const v = setup({ draft: { ...emptyDirectLanDraft(), active: { value: secret, expires: new Date(Date.now() - 1000).toISOString(), recipient: remoteKey } } })
    await waitFor(() => expect(v.draft().active?.value).toBe('')); expect(screen.queryByDisplayValue(secret)).not.toBeInTheDocument()
  })
  it('rejects a stale review when the local identity or scope changes', async () => {
    const v = setup({ draft: { ...emptyDirectLanDraft(), invitation: secret } })
    v.run.mockResolvedValueOnce({ ok: true, result: { hostPublicKey: remoteKey, hostName: 'Synthetic host', endpoint: '192.168.50.11:48444', recipientPublicKey: publicKey, recipientMatches: true, expires: new Date(Date.now() + 300000).toISOString() } }); await v.user.click(screen.getByRole('button', { name: v.d('inspect') }))
    const changed = readyState(); changed.directLAN!.prefixes = ['192.168.50.10/32']; act(() => v.update(changed)); expect(screen.getByRole('button', { name: v.d('pair') })).toBeDisabled()
  })
})


it('renders requested private QR locally and clears it when the invitation is consumed', async () => {
  const v = setup({ draft: { ...emptyDirectLanDraft(), recipient: remoteKey, name: 'Synthetic recipient' } })
  const qr = Array.from({ length: 21 }, (_, y) => Array.from({ length: 21 }, (_, x) => (x + y) % 2 === 0))
  v.run.mockResolvedValueOnce({ ok: true, result: { invitation: secret, expires: new Date(Date.now() + 300000).toISOString(), recipientPublicKey: remoteKey, qr } })
  await v.user.click(screen.getByLabelText(v.d('qr'))); await v.user.click(screen.getByRole('button', { name: v.d('inviteAction') }))
  expect(v.run).toHaveBeenCalledWith('direct-lan.invite', { recipientPublicKey: remoteKey, name: 'Synthetic recipient', ttlSeconds: 300, qr: true })
  expect(screen.getByRole('img', { name: v.d('qrAlt') })).toBeInTheDocument()
  const next = readyState(); next.peers.push({ id: remoteKey, name: 'Synthetic recipient', networks: ['direct-lan'], online: false, verified: true, trusted: false, bridge: false, path: 'unknown' }); v.update(next)
  await waitFor(() => expect(screen.queryByRole('img', { name: v.d('qrAlt') })).not.toBeInTheDocument()); expect(v.draft().active?.qr).toBeUndefined(); expect(v.draft().active?.value).toBe('')
})


it('offers actual assigned-address metadata without automatically opening a listener', async () => {
  const value=readyState();value.settings={network:'none'};delete value.directLAN
  const v=setup({state:value});v.run.mockResolvedValueOnce({ok:true,result:{addresses:[{interface:'fixture0',address:'192.168.50.10',prefix:'192.168.50.0/24'}]}})
  await v.user.click(screen.getByRole('button',{name:v.d('addresses')}));await v.user.selectOptions(screen.getByLabelText(v.d('chooseAddress')),'0')
  expect(v.draft().listen).toBe('192.168.50.10:48444');expect(v.draft().prefixes).toBe('192.168.50.0/24');expect(v.run.mock.calls.map(c=>c[0])).toEqual(['lan.addresses'])
})
