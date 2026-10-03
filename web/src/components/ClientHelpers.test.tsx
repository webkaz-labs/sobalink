import { useState } from 'react'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { ClientSettingsView, CommandResult, Locale, RustDeskSetup, RustDeskSetupReview, State } from '../api'
import { clientEnglish, clientJapanese, clientText } from '../client-i18n'
import { readClientSettings, readRustDeskReview, validRustDeskSetup } from '../client-helpers'
import { translator } from '../i18n'
import { loopbackEndpoint } from '../service-form'
import type { Server } from '../useServer'
import { ClientSettingsDialog, CopyValue, RustDeskSetupDialog } from './ClientHelpers'
const publicKey = 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA='
const state: State = { csrfToken: 'fictional-local-control', self: { name: 'Notebook', status: 'online' }, settings: { network: 'tailnet' }, peers: [{ id: 'fixture-id', name: 'ID device', networks: ['tailnet'], online: true, trusted: true, verified: true, bridge: true, path: 'direct' }, { id: 'fixture-relay', name: 'Relay device', networks: ['tailnet'], online: false, trusted: true, verified: true, bridge: true, path: 'unknown' }], services: [], shares: [], messages: [], transfers: [] }
const setup: RustDeskSetup = { name: 'rustdesk', backend: 'tailnet', idPeerId: 'fixture-id', relayPeerId: 'fixture-relay', publicKey, idPort: 21116, relayPort: 21117, localIdPort: 32116, localRelayPort: 32117, loopbackHost: '127.0.0.1', lifetime: 'until-stopped', ttlSeconds: 0 }
function review(configuration = setup, saved = false): RustDeskSetupReview {
  const rows = [ ['nat', 'tcp', configuration.idPeerId, configuration.idPort - 1, configuration.localIdPort - 1], ['id', 'tcp', configuration.idPeerId, configuration.idPort, configuration.localIdPort], ['heartbeat', 'udp', configuration.idPeerId, configuration.idPort, configuration.localIdPort], ['relay', 'tcp', configuration.relayPeerId, configuration.relayPort, configuration.localRelayPort] ] as const
  const services = rows.map(([role, network, peerId, remote, local]) => ({ id: `fixture-${role}`, name: `${configuration.name}-${role}`, direction: 'forward' as const, backend: configuration.backend, network, peerId, ports: String(remote), localPort: local, loopbackHost: configuration.loopbackHost, lifetime: configuration.lifetime, ttlSeconds: configuration.ttlSeconds, purpose: 'rustdesk', discoverable: false }))
  return { configuration, group: { name: configuration.name, serviceIds: services.map(service => service.id), rustdesk: { publicKey: configuration.publicKey, natServiceId: services[0].id, idServiceId: services[1].id, heartbeatServiceId: services[2].id, relayServiceId: services[3].id } }, services, saved, applied: saved, revision: 'a'.repeat(64), clientSettings: { group: configuration.name, publicKey: configuration.publicKey, idServer: loopbackEndpoint(configuration.loopbackHost, String(configuration.localIdPort)), relayServer: loopbackEndpoint(configuration.loopbackHost, String(configuration.localRelayPort)), proxy: '', udpEnabled: true, remoteIdSuffix: '/r', application: 'unverified', roles: rows.map(([role, network, peerId, remote, local], index) => ({ role, network, peerId, remotePort: remote, localEndpoint: loopbackEndpoint(configuration.loopbackHost, String(local)), serviceId: services[index].id, lifetime: configuration.lifetime, ttlSeconds: configuration.ttlSeconds, status: saved ? 'saved' : 'planned', listenerReady: false })), notices: [{ code: 'application_unverified', message: 'RustDesk application compatibility is unverified.', messageJa: 'RustDesk アプリの互換性は未確認です。' }, { code: 'rustdesk_same_relay', message: 'Use the same local relay address and port at every endpoint.', messageJa: 'すべての端末で同じローカルリレーアドレスとポートを使ってください。' }] } }
}
function renderSetup(locale: Locale = 'en', handler?: (name: string, payload: unknown) => Promise<CommandResult | undefined>) {
  const onClose = vi.fn(); const onSaved = vi.fn(); const run = vi.fn<Server['run']>()
  function Harness() {
    const [error, setError] = useState<unknown>(null)
    run.mockImplementation(async (name, payload) => { setError(null); if (handler) { try { return await handler(name, payload) } catch (value) { setError(value); return } }; return { ok: true, result: review((payload as { configuration: RustDeskSetup }).configuration, name === 'rustdesk.save') as unknown as CommandResult['result'] } })
    const server: Server = { state, error, setError, run, auth: 'ready', stale: false, busy: new Set(), refresh: vi.fn(), handleError: vi.fn(), updatedAt: null }
    return <RustDeskSetupDialog server={server} locale={locale} t={translator(locale)} onClose={onClose} onSaved={onSaved} />
  }
  return { ...render(<Harness />), run, onClose, onSaved }
}
async function fill(locale: Locale = 'en', offline = false) {
  const c = (key: string) => clientText(locale, key)
  const id = within(screen.getByRole('group', { name: c('idPeer') })); const relay = within(screen.getByRole('group', { name: c('relayPeer') }))
  if (offline) { fireEvent.change(id.getByLabelText(c('exactID')), { target: { value: 'saved-id-peer' } }); fireEvent.change(relay.getByLabelText(c('exactID')), { target: { value: 'saved-relay-peer' } }) }
  else { await userEvent.selectOptions(id.getByRole('combobox'), 'fixture-id'); await userEvent.selectOptions(relay.getByRole('combobox'), 'fixture-relay') }
  fireEvent.change(screen.getByLabelText(c('publicKey')), { target: { value: publicKey } })
}
describe('reviewed RustDesk helper', () => {
  it.each(['en', 'ja'] as const)('reviews exact four flows and saves without starting (%s)', async locale => {
    const c = (key: string) => clientText(locale, key); const view = renderSetup(locale)
    await fill(locale)
    await userEvent.click(screen.getByRole('button', { name: c('review') }))
    const region = await screen.findByRole('region', { name: c('reviewTitle') })
    expect(region).toHaveTextContent('127.0.0.1:32115 → 21115'); expect(region).toHaveTextContent('127.0.0.1:32117 → 21117')
    expect(region.querySelectorAll('.definition-scope')).toHaveLength(4)
    expect(region).toHaveTextContent(c('planned')); expect(region).toHaveTextContent(c('unverified'))
    expect(within(region).getByLabelText(c('publicKey'))).toHaveValue(publicKey)
    expect(view.run.mock.calls.map(call => call[0])).toEqual(['rustdesk.preview'])
    await userEvent.click(within(region).getByRole('button', { name: c('save') }))
    await screen.findByText(c('saved'))
    expect(view.run).toHaveBeenLastCalledWith('rustdesk.save', { configuration: setup, expectedRevision: 'a'.repeat(64) })
    expect(view.onSaved).not.toHaveBeenCalled()
    await userEvent.click(screen.getByRole('button', { name: c('manage') }))
    expect(view.onSaved).toHaveBeenCalledWith('rustdesk')
    expect(view.run.mock.calls.some(call => ['services.start', 'service.connect', 'network.configure'].includes(call[0]))).toBe(false)
  })
  it('accepts explicit offline identities and IPv6 with independent server ports', async () => {
    const view = renderSetup(); await fill('en', true)
    fireEvent.change(screen.getByLabelText('Remote ID port (TCP and UDP)'), { target: { value: '22016' } })
    fireEvent.change(screen.getByLabelText('Remote relay port (TCP)'), { target: { value: '23017' } })
    await userEvent.selectOptions(screen.getByLabelText('Loopback address'), '::1')
    await userEvent.click(screen.getByRole('button', { name: 'Review four flows' }))
    const region = await screen.findByRole('region', { name: 'Review RustDesk group' })
    expect(region).toHaveTextContent('[::1]:32115 → 22015'); expect(region).toHaveTextContent('[::1]:32117 → 23017'); expect(region).toHaveTextContent('saved-relay-peer')
    expect(view.run).toHaveBeenCalledOnce()
  })
  it('back invalidates the revision; cancelling never saves', async () => {
    const view = renderSetup(); await fill(); await userEvent.click(screen.getByRole('button', { name: 'Review four flows' }))
    await userEvent.click(await screen.findByRole('button', { name: 'Back to settings' }))
    fireEvent.change(screen.getByLabelText('Group name'), { target: { value: 'another-group' } })
    expect(screen.queryByRole('button', { name: 'Save reviewed group' })).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' })); expect(view.onClose).toHaveBeenCalledOnce()
    expect(view.run.mock.calls.map(call => call[0])).toEqual(['rustdesk.preview'])
  })
  it.each(['rustdesk_revision_conflict', 'rustdesk_group_conflict', 'network_error'])('explains %s and requires a new preview before another save', async code => {
    const view = renderSetup('en', async (name, payload) => { if (name === 'rustdesk.save') throw { code }; return { ok: true, result: review((payload as { configuration: RustDeskSetup }).configuration) as unknown as CommandResult['result'] } })
    await fill(); await userEvent.click(screen.getByRole('button', { name: 'Review four flows' })); await userEvent.click(await screen.findByRole('button', { name: 'Save reviewed group' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(clientText('en', code) || 'interrupted')
    expect(screen.queryByRole('button', { name: 'Save reviewed group' })).not.toBeInTheDocument()
    expect(screen.getByLabelText('Group name')).toHaveValue('rustdesk')
    expect(view.run.mock.calls.map(call => call[0])).toEqual(['rustdesk.preview', 'rustdesk.save'])
  })
  it('does not revive a dismissed preview or save after a late response', async () => {
    let resolve!: (value: CommandResult) => void
    const pending = new Promise<CommandResult>(value => { resolve = value })
    const view = renderSetup('en', async () => pending); await fill(); await userEvent.click(screen.getByRole('button', { name: 'Review four flows' }))
    expect(screen.getByRole('button', { name: 'Loading…' })).toBeDisabled()
    view.unmount(); resolve({ ok: true, result: review() as unknown as CommandResult['result'] })
    await pending; expect(view.onSaved).not.toHaveBeenCalled(); expect(view.run).toHaveBeenCalledOnce()
  })
  it('rejects malformed or altered preview scope before a save action exists', async () => {
    const changed = review(); changed.services[3].ports = '23017'
    renderSetup('en', async () => ({ ok: true, result: changed as unknown as CommandResult['result'] })); await fill(); await userEvent.click(screen.getByRole('button', { name: 'Review four flows' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(clientText('en', 'invalid_response'))
    expect(screen.queryByRole('button', { name: 'Save reviewed group' })).not.toBeInTheDocument()
    expect(() => readRustDeskReview({ ...review(), services: [] }, setup)).toThrow()
  })
  it('validates public-key, TCP overlap and lifetime without a network request', () => {
    expect(validRustDeskSetup(setup)).toBe(true)
    for (const patch of [{ publicKey: 'fictional-invalid' }, { relayPort: setup.idPort, relayPeerId: setup.idPeerId }, { localRelayPort: setup.localIdPort - 1 }, { idPort: 1024 }, { lifetime: 'finite' as const, ttlSeconds: 0 }]) expect(validRustDeskSetup({ ...setup, ...patch })).toBe(false)
    expect(Object.keys(clientJapanese)).toEqual(Object.keys(clientEnglish))
  })
})
function clientSettings(): ClientSettingsView {
  return { application: 'unverified', rustdesk: [], notices: [{ code: 'application_unverified', message: 'Keep TLS certificate and SSH host-key verification enabled.', messageJa: 'TLS 証明書・SSH ホスト鍵の検証は有効のままにしてください。' }], services: [{ id: 'saved-ssh', name: 'ssh-fixture', backend: 'tailnet', direction: 'forward', purpose: 'ssh', network: 'tcp', peerId: 'fixture-id', localHost: '127.0.0.1', localEndpoint: '127.0.0.1:2222', remoteEndpoints: [], mappings: [{ localFirst: 2222, localLast: 2222, remoteFirst: 22, remoteLast: 22 }], lifetime: 'until-stopped', ttlSeconds: 0, status: 'saved', listenerReady: false, application: 'unverified', ssh: { hostKeyAlias: 'sobalink-tailnet-fixture-id-22', args: ['ssh'], command: 'ssh -o HostKeyAlias=sobalink-tailnet-fixture-id-22 -p 2222 -l USER 127.0.0.1' }, httpCandidate: 'http://127.0.0.1:2222/', notices: [{ code: 'http_candidate_tls', message: 'HTTP is only a candidate; preserve the original TLS hostname, SNI and certificate validation.', messageJa: 'HTTP は候補です。元の TLS ホスト名・SNI・証明書検証を維持してください。' }] }] }
}
describe('read-only client hints', () => {
  it.each(['en', 'ja'] as const)('shows exact endpoints and application caveats without launch or mutation (%s)', async locale => {
    const value = clientSettings(); const run = vi.fn().mockResolvedValue({ ok: true, result: value }); const server = { state, error: null, setError: vi.fn(), run, stale: false } as unknown as Server; const c = (key: string) => clientText(locale, key)
    render(<ClientSettingsDialog target={{ ids: ['saved-ssh'] }} server={server} locale={locale} t={translator(locale)} onClose={vi.fn()} />)
    expect(await screen.findByLabelText(c('hostKey'))).toHaveValue('sobalink-tailnet-fixture-id-22')
    expect(screen.getByLabelText(c('ssh'))).toHaveValue(value.services[0].ssh!.command)
    expect(screen.getByLabelText(c('http'))).toHaveValue('http://127.0.0.1:2222/')
    expect(screen.getByText(locale === 'ja' ? value.services[0].notices[0].messageJa : value.services[0].notices[0].message)).toBeVisible()
    expect(screen.queryByRole('link')).not.toBeInTheDocument()
    expect(run.mock.calls.map(call => call[0])).toEqual(['client.settings'])
  })

  it.each(['en', 'ja'] as const)('shows legacy omitted lifetime as finite and rejects invalid lifetime data (%s)', async locale => {
    const value = clientSettings(); Object.assign(value.services[0], { lifetime: '', ttlSeconds: 3600, direction: 'share', ssh: undefined, httpCandidate: undefined })
    const run = vi.fn().mockResolvedValue({ ok: true, result: value }); const server = { state, error: null, setError: vi.fn(), run, stale: false } as unknown as Server
    render(<ClientSettingsDialog target={{ ids: ['saved-ssh'] }} server={server} locale={locale} t={translator(locale)} onClose={vi.fn()} />)
    await screen.findByText('ssh-fixture')
    expect(screen.getByText(/3,600/)).toHaveTextContent(locale === 'ja' ? '3,600 秒' : '3,600 seconds')
    expect(screen.queryByText(/Until I stop/)).not.toBeInTheDocument()
    expect(() => readClientSettings({ ...value, services: [{ ...value.services[0], ttlSeconds: 0 }] })).toThrow('invalid_response')
  })
  it('ignores an older selection response and displays only the current selection', async () => {
    let resolve!: (value: unknown) => void; const pending = new Promise(value => { resolve = value })
    const run = vi.fn().mockReturnValueOnce(pending).mockResolvedValue({ ok: true, result: { ...clientSettings(), services: [] } }); const server = { state, error: null, setError: vi.fn(), run, stale: false } as unknown as Server
    const view = render(<ClientSettingsDialog target={{ ids: ['saved-ssh'] }} server={server} locale="en" t={translator('en')} onClose={vi.fn()} />)
    await waitFor(() => expect(run).toHaveBeenCalledOnce()); view.rerender(<ClientSettingsDialog target={{ ids: [] }} server={server} locale="en" t={translator('en')} onClose={vi.fn()} />)
    await screen.findByText(clientText('en', 'empty')); resolve({ ok: true, result: clientSettings() }); await pending
    expect(screen.queryByLabelText('SSH command')).not.toBeInTheDocument()
  })
  it('keeps copy failure recoverable without opening an application', async () => {
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: vi.fn().mockRejectedValue(new Error('fixture clipboard blocked')) } })
    render(<CopyValue label="SSH command" value="ssh fictional-host" locale="en" />)
    await userEvent.click(screen.getByRole('button', { name: 'Copy value: SSH command' }))
    expect(await screen.findByRole('status')).toHaveTextContent('Select and copy the text manually')
    expect(screen.getByLabelText('SSH command')).toHaveValue('ssh fictional-host')
  })
})
