import { useState } from 'react'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { CommandResult, LanInvitationPreview, Locale, Peer, State } from '../api'
import { lanTranslator } from '../lan-i18n'
import { translator } from '../i18n'
import type { Server } from '../useServer'
import { emptyLanDraft, LanSetup, type ActiveInvitation, type LanDraft } from './LanSetup'

const t = translator('en')
const lt = lanTranslator('en')
const publicKey = 'a1'.repeat(32)
const recipientPublicKey = 'b2'.repeat(32)
const certificateSHA256 = 'c3'.repeat(32)
const opaqueInvitation = 'opaque-invitation:AbC_123+/==?recipient=exact&nonce=DoNotNormalize'

function readyState(): State {
  return {
    csrfToken: 'fixture-csrf', self: { name: 'test-device', status: 'online' },
    peers: [], messages: [], transfers: [], services: [], shares: [],
    settings: { network: 'lan' },
    lan: { configured: true, pairingReady: true, publicKey, path: 'relay', relay: { kind: 'relay', address: '192.0.2.10:443', certificateSHA256 } },
  }
}
function invitation(): ActiveInvitation {
  return { value: opaqueInvitation, expires: new Date(Date.now() + 300_000).toISOString(), recipientPublicKey, recipientName: 'Recipient', relayAddress: '192.0.2.10:443', certificateSHA256 }
}
function pairedPeer(): Peer {
  return { id: recipientPublicKey, name: 'Recipient', networks: ['lan'], online: true, verified: true, trusted: false, bridge: true, path: 'relay', fingerprint: recipientPublicKey }
}
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}
function setup({ state = readyState(), draft = emptyLanDraft(), stale = false, locale = 'en', addressOptions = [] }: { state?: State; draft?: LanDraft; stale?: boolean; locale?: Locale; addressOptions?: unknown[] } = {}) {
  const user = userEvent.setup()
  // Keep read-only address discovery separate from configuration/identity/pair actions.
  const run = vi.fn<Server['run']>().mockResolvedValue({ ok: true })
  const readAddresses = vi.fn<Server['run']>().mockResolvedValue({ ok: true, result: { addresses: addressOptions } })
  const execute: Server['run'] = (...args) => args[0] === 'lan.addresses' ? readAddresses(...args) : run(...args)
  const setError = vi.fn()
  const onViewPeer = vi.fn()
  let currentDraft = draft
  const server: Server = {
    state, auth: 'ready', stale, error: null, setError, busy: new Set(),
    refresh: vi.fn().mockResolvedValue(state), run: execute, updatedAt: null, messageBlock: vi.fn().mockResolvedValue(null), messageGuardRevision: 0, handleError: vi.fn(),
  }
  function Harness({ nextState, visible = true }: { nextState: State; visible?: boolean }) {
    const [value, setDraft] = useState(draft)
    const [hostname, setHostname] = useState('test-device')
    currentDraft = value
    return visible ? <LanSetup server={{ ...server, state: nextState }} state={nextState} t={translator(locale)} locale={locale} hostname={hostname} setHostname={setHostname} draft={value} setDraft={setDraft} onViewPeer={onViewPeer} /> : null
  }
  const view = render(<Harness nextState={state} />)
  return { ...view, user, run, readAddresses, refresh: server.refresh as ReturnType<typeof vi.fn<Server['refresh']>>, setError, onViewPeer, draft: () => currentDraft, update: (nextState = state, visible = true) => view.rerender(<Harness nextState={nextState} visible={visible} />) }
}
function unconfiguredState(): State {
  return { ...readyState(), settings: { network: 'none' }, lan: { configured: false, pairingReady: false, publicKey, path: 'unknown' } }
}
function fillRelay(address: string, pin = certificateSHA256) {
  fireEvent.change(screen.getByLabelText(t('relayAddress'), { exact: false }), { target: { value: address } })
  fireEvent.change(screen.getByLabelText(t('certificatePin'), { exact: false }), { target: { value: pin } })
}
function expectSecretAbsent(container: HTMLElement, secret: string) {
  expect(container.textContent).not.toContain(secret)
  expect(screen.queryByDisplayValue(secret)).not.toBeInTheDocument()
  for (const element of container.querySelectorAll('*')) {
    for (const attribute of element.attributes) expect(attribute.value).not.toContain(secret)
  }
}

describe('LAN setup commands and relay scope', () => {
  it('creates identity only after an explicit action and retains the acknowledged public ID', async () => {
    const state = unconfiguredState()
    delete state.lan!.publicKey
    const view = setup({ state })
    expect(view.run).not.toHaveBeenCalled()
    view.run.mockResolvedValueOnce({ ok: true, result: { publicKey } })
    await view.user.click(screen.getByRole('button', { name: t('createIdentity') }))
    await waitFor(() => expect(view.draft().publicKey).toBe(publicKey))
    expect(view.run.mock.calls).toEqual([['lan.identity', {}]])
    expect(screen.getByText(publicKey)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: t('createIdentity') })).not.toBeInTheDocument()
  })

  it.each(['relay.example.test:443', '192.0.2.10:0', '192.0.2.10:65536', '256.0.2.10:443', '192.0.2.10:443/path', '2001:db8::10:443'])('rejects relay address %s without configuring a network', async address => {
    const view = setup({ state: unconfiguredState() })
    fillRelay(address)
    await view.user.click(screen.getByRole('button', { name: t('activateRelay') }))
    expect(await screen.findByRole('alert')).toHaveTextContent(t('relayInvalid'))
    expect(view.run).not.toHaveBeenCalled()
  })

  it('requires a complete hexadecimal certificate pin before activating the selected relay', async () => {
    const view = setup({ state: unconfiguredState() })
    fillRelay('192.0.2.10:443', 'z'.repeat(64))
    await view.user.click(screen.getByRole('button', { name: t('activateRelay') }))
    expect(await screen.findByRole('alert')).toHaveTextContent(t('pinInvalid'))
    expect(view.run).not.toHaveBeenCalled()
  })

  it.each(['192.0.2.10:443', '[2001:db8::10]:443'])('activates only the explicitly supplied relay %s and its normalized pin', async address => {
    const view = setup({ state: unconfiguredState() })
    fillRelay(address, certificateSHA256.toUpperCase())
    expect(view.run).not.toHaveBeenCalled()
    await view.user.click(screen.getByRole('button', { name: t('activateRelay') }))
    expect(view.run.mock.calls).toEqual([['network.configure', { mode: 'lan', hostname: 'test-device', lan: { kind: 'relay', address, certificateSHA256 } }]])
  })

  it.each(['tailnet selected', 'relay unconfigured', 'relay not ready', 'stale state'] as const)('never joins directly when %s', async reason => {
    const state = readyState()
    if (reason === 'tailnet selected') state.settings!.network = 'tailnet'
    if (reason === 'relay unconfigured') state.lan!.configured = false
    if (reason === 'relay not ready') state.lan!.pairingReady = false
    const view = setup({ state, stale: reason === 'stale state', draft: { ...emptyLanDraft(), joinInvitation: opaqueInvitation } })
    const button = screen.getByRole('button', { name: lt('reviewInvitation') })
    if (reason === 'stale state') expect(button).toBeDisabled()
    fireEvent.submit(button.closest('form')!)
    await waitFor(() => expect(view.run).not.toHaveBeenCalledWith('lan.join', expect.anything()))
    if (reason === 'stale state') expect(view.run).not.toHaveBeenCalled()
    else expect(view.run).toHaveBeenCalledWith('lan.inspect', { invitation: opaqueInvitation })
  })

  it('joins only after submission and leaves communication trust and automatic receiving untouched', async () => {
    const view = setup({ draft: { ...emptyLanDraft(), joinInvitation: ` ${opaqueInvitation} ` } })
    view.run.mockResolvedValueOnce({ ok: true, result: { paired: true, trusted: false, peerId: pairedPeer().id } })
    expect(view.run).not.toHaveBeenCalled()
    expect(screen.getByLabelText(t('invitation'), { exact: false })).toHaveAttribute('type', 'password')
    await view.user.click(screen.getByRole('button', { name: t('join') }))
    await waitFor(() => expect(view.draft().joinedPeerId).toBe(pairedPeer().id))
    expect(view.run.mock.calls).toEqual([['lan.join', { invitation: opaqueInvitation }]])
    expect(view.draft().joinInvitation).toBe('')
    expect(screen.getByText(t('paired'))).toBeInTheDocument()
    expect(view.onViewPeer).not.toHaveBeenCalled()
  })

  it('rejects an unexpected join acknowledgement that claims communication was automatically trusted', async () => {
    const view = setup({ draft: { ...emptyLanDraft(), joinInvitation: opaqueInvitation } })
    view.run.mockResolvedValueOnce({ ok: true, result: { paired: true, trusted: true, peerId: pairedPeer().id } })
    await view.user.click(screen.getByRole('button', { name: t('join') }))
    expect(view.setError).toHaveBeenCalledWith({ code: 'invalid_response' })
    expect(view.draft().joinedPeerId).toBeUndefined()
    expect(view.draft().joinInvitation).toBe(opaqueInvitation)
    expect(view.onViewPeer).not.toHaveBeenCalled()
  })
})

describe('LAN invitation secrets and interrupted responses', () => {
  it('creates a recipient-bound invitation without putting its secret in the DOM or browser storage', async () => {
    const active = invitation()
    const view = setup({ draft: { ...emptyLanDraft(), recipientPublicKey: recipientPublicKey.toUpperCase(), recipientName: ' Recipient ' } })
    const writeText = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue()
    view.run.mockResolvedValueOnce({ ok: true, result: { invitation: active.value, expires: active.expires, recipientPublicKey } })
    expect(view.run).not.toHaveBeenCalled()
    await view.user.click(screen.getByRole('button', { name: t('createInvitation') }))
    await waitFor(() => expect(view.draft().activeInvitation?.value).toBe(active.value))
    expect(view.run.mock.calls).toEqual([['lan.invite', { recipientPublicKey, name: 'Recipient', ttlSeconds: 300 }]])
    expectSecretAbsent(view.container, active.value)
    expect(writeText).not.toHaveBeenCalled()
    expect(localStorage.length).toBe(0)
    expect(sessionStorage.length).toBe(0)
  })

  it('copies the exact opaque invitation only after the copy action', async () => {
    const active = invitation()
    const view = setup({ draft: { ...emptyLanDraft(), activeInvitation: active } })
    const writeText = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue()
    expectSecretAbsent(view.container, active.value)
    expect(writeText).not.toHaveBeenCalled()
    await view.user.click(screen.getByRole('button', { name: t('copyInvitation') }))
    expect(writeText.mock.calls).toEqual([[active.value]])
    expect(screen.getByRole('button', { name: t('copied') })).toBeInTheDocument()
    expectSecretAbsent(view.container, active.value)
    expect(view.run).not.toHaveBeenCalled()
  })

  it('reveals a selectable manual copy field only when an explicit clipboard attempt fails', async () => {
    const active = invitation()
    const view = setup({ draft: { ...emptyLanDraft(), activeInvitation: active } })
    vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValue(new Error('Clipboard unavailable'))
    expect(screen.queryByRole('textbox', { name: t('copyInvitation') })).not.toBeInTheDocument()
    await view.user.click(screen.getByRole('button', { name: t('copyInvitation') }))
    const field = await screen.findByRole('textbox', { name: t('copyInvitation') })
    expect(field).toHaveValue(active.value)
    expect(field).toHaveAttribute('readonly')
    expect(field).toHaveFocus()
    expect((field as HTMLTextAreaElement).selectionEnd).toBe(active.value.length)
    expect(view.run).not.toHaveBeenCalled()
  })

  it('cancels using the exact acknowledged opaque value and clears it only after acknowledgement', async () => {
    const active = invitation()
    const pending = deferred<CommandResult>()
    const view = setup({ draft: { ...emptyLanDraft(), activeInvitation: active } })
    view.run.mockReturnValueOnce(pending.promise)
    await view.user.click(screen.getByRole('button', { name: t('cancelInvitation') }))
    expect(view.run.mock.calls).toEqual([['lan.cancel', { invitation: active.value }]])
    expect(view.draft().activeInvitation).toEqual(active)
    await act(async () => { pending.resolve({ ok: true }); await pending.promise })
    expect(view.draft().activeInvitation).toBeUndefined()
    expect(screen.queryByRole('button', { name: t('copyInvitation') })).not.toBeInTheDocument()
  })

  it('retains an invitation when cancellation is not acknowledged', async () => {
    const active = invitation()
    const view = setup({ draft: { ...emptyLanDraft(), activeInvitation: active } })
    view.run.mockResolvedValueOnce(undefined)
    await view.user.click(screen.getByRole('button', { name: t('cancelInvitation') }))
    expect(view.draft().activeInvitation).toEqual(active)
    expect(screen.getByRole('button', { name: t('cancelInvitation') })).toBeInTheDocument()
  })

  it('retains a late invitation acknowledgement after closing so reopening can cancel it', async () => {
    const active = invitation()
    const pending = deferred<CommandResult>()
    const view = setup({ draft: { ...emptyLanDraft(), recipientPublicKey, recipientName: 'Recipient' } })
    view.run.mockReturnValueOnce(pending.promise)
    await view.user.click(screen.getByRole('button', { name: t('createInvitation') }))
    view.update(readyState(), false)
    await act(async () => { pending.resolve({ ok: true, result: { invitation: active.value, expires: active.expires, recipientPublicKey } }); await pending.promise })
    expect(view.draft().activeInvitation?.value).toBe(active.value)
    expect(view.onViewPeer).not.toHaveBeenCalled()
    view.update()
    expect(screen.getByRole('button', { name: t('cancelInvitation') })).toBeInTheDocument()
    expectSecretAbsent(view.container, active.value)
    expect(view.run).toHaveBeenCalledTimes(1)
  })

  it('retains late pairing success without navigating away from a screen opened after setup closed', async () => {
    const state = { ...readyState(), peers: [pairedPeer()] }
    const pending = deferred<CommandResult>()
    const view = setup({ state, draft: { ...emptyLanDraft(), joinInvitation: opaqueInvitation } })
    view.run.mockReturnValueOnce(pending.promise)
    await view.user.click(screen.getByRole('button', { name: t('join') }))
    view.update(state, false)
    await act(async () => { pending.resolve({ ok: true, result: { paired: true, trusted: false, peerId: pairedPeer().id } }); await pending.promise })
    expect(view.draft().joinedPeerId).toBe(pairedPeer().id)
    expect(view.draft().joinInvitation).toBe('')
    expect(view.onViewPeer).not.toHaveBeenCalled()
    view.update(state)
    await view.user.click(screen.getByRole('button', { name: t('viewDevice') }))
    expect(view.onViewPeer).toHaveBeenCalledExactlyOnceWith(pairedPeer().id)
    expect(view.run.mock.calls).toEqual([['lan.join', { invitation: opaqueInvitation }]])
  })

  it('shows an issued invitation as paired after the exact recipient appears and stops offering copy or cancellation', async () => {
    const active = invitation()
    const view = setup({ draft: { ...emptyLanDraft(), activeInvitation: active } })
    expect(screen.getByRole('button', { name: t('copyInvitation') })).toBeInTheDocument()
    view.update({ ...readyState(), peers: [pairedPeer()] })
    expect((await screen.findAllByText(t('paired'))).length).toBeGreaterThan(0)
    expect(screen.queryByRole('button', { name: t('copyInvitation') })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: t('cancelInvitation') })).not.toBeInTheDocument()
    await view.user.click(screen.getByRole('button', { name: t('viewDevice') }))
    expect(view.onViewPeer).toHaveBeenCalledExactlyOnceWith(pairedPeer().id)
    expect(view.run).not.toHaveBeenCalled()
  })

  it('rejects recipient names over 80 UTF-8 bytes before creating an invitation', async () => {
    const view = setup({ draft: { ...emptyLanDraft(), recipientPublicKey, recipientName: 'あ'.repeat(27) } })
    await view.user.click(screen.getByRole('button', { name: t('createInvitation') }))
    expect(await screen.findByRole('alert')).toBeInTheDocument()
    expect(view.run).not.toHaveBeenCalled()
    expect(view.draft().recipientName).toBe('あ'.repeat(27))
  })

  it('accepts a recipient name at the 80-byte UTF-8 boundary without rewriting it', async () => {
    const name = `${'あ'.repeat(26)}ab`
    const view = setup({ draft: { ...emptyLanDraft(), recipientPublicKey, recipientName: name } })
    const active = invitation()
    view.run.mockResolvedValueOnce({ ok: true, result: { invitation: active.value, expires: active.expires, recipientPublicKey } })
    await view.user.click(screen.getByRole('button', { name: t('createInvitation') }))
    expect(view.run.mock.calls).toEqual([['lan.invite', { recipientPublicKey, name, ttlSeconds: 300 }]])
    expect(view.draft().activeInvitation?.recipientName).toBe(name)
  })
})

function previewInvitation(): LanInvitationPreview {
  return { recipientPublicKey: publicKey, recipientMatches: true, hostPublicKey: recipientPublicKey, hostName: 'Host device', expires: new Date(Date.now() + 300_000).toISOString(), relay: { kind: 'relay', address: '192.0.2.10:443', certificateSHA256 } }
}
const hostAddresses = [{ interface: 'ethernet-fixture', address: '192.168.20.5' }, { interface: 'wifi-fixture', address: 'fd00::5' }]
async function selectHost(view: ReturnType<typeof setup>, index = 0) {
  view.readAddresses.mockResolvedValueOnce({ ok: true, result: { addresses: hostAddresses } })
  await view.user.click(screen.getByRole('button', { name: lt('refreshAddresses') }))
  await view.user.selectOptions(screen.getByRole('combobox', { name: lt('addressChoice') }), `${hostAddresses[index].interface}\n${hostAddresses[index].address}`)
}

describe('Embedded relay review and lifecycle', () => {
  it('loads eligible addresses read-only while keeping selection and listener start explicit', async () => {
    const view = setup({ state: unconfiguredState(), addressOptions: hostAddresses })
    await screen.findByRole('option', { name: /192.168.20.5/ })
    expect(view.readAddresses.mock.calls).toEqual([['lan.addresses', {}]])
    expect(view.run).not.toHaveBeenCalled()
    expect(screen.getByRole('combobox', { name: lt('addressChoice') })).toHaveValue('')
    expect(screen.getByRole('button', { name: lt('reviewHost') })).toBeDisabled()
  })

  it('retains non-secret host address and port through section changes and closing', async () => {
    const state = unconfiguredState()
    const view = setup({ state, addressOptions: hostAddresses })
    await screen.findByRole('option', { name: /192.168.20.5/ })
    await view.user.selectOptions(screen.getByRole('combobox', { name: lt('addressChoice') }), 'ethernet-fixture\n192.168.20.5')
    fireEvent.change(screen.getByRole('spinbutton', { name: new RegExp(lt('hostPort')) }), { target: { value: '49001' } })
    await view.user.click(screen.getByRole('button', { name: t('joinDevice') }))
    await view.user.click(screen.getByRole('button', { name: lt('hostRelay') }))
    await waitFor(() => expect(screen.getByRole('combobox', { name: lt('addressChoice') })).toHaveValue('ethernet-fixture\n192.168.20.5'))
    expect(screen.getByRole('spinbutton', { name: new RegExp(lt('hostPort')) })).toHaveValue(49001)
    view.update(state, false)
    view.update(state, true)
    await waitFor(() => expect(screen.getByRole('combobox', { name: lt('addressChoice') })).toHaveValue('ethernet-fixture\n192.168.20.5'))
    expect(screen.getByRole('spinbutton', { name: new RegExp(lt('hostPort')) })).toHaveValue(49001)
    expect(view.run).not.toHaveBeenCalled()
  })

  it('keeps a cleared host port editable instead of silently restoring the default', async () => {
    setup({ state: unconfiguredState() })
    const input = screen.getByRole('spinbutton', { name: new RegExp(lt('hostPort')) })
    fireEvent.change(input, { target: { value: '' } })
    expect(input).toHaveValue(null)
  })

  it('offers saved stopped host settings for reviewed recovery without CLI re-entry', async () => {
    const state: State = { ...readyState(), self: { name: 'test-device', status: 'idle' }, lan: { ...readyState().lan!, relay: { kind: 'host', address: '192.168.20.5:50123', certificateSHA256 }, pairingReady: false, listenerReady: false, relayReady: false } }
    const view = setup({ state, addressOptions: hostAddresses })
    await screen.findByRole('option', { name: /192.168.20.5/ })
    expect(screen.getByRole('spinbutton', { name: new RegExp(lt('hostPort')) })).toHaveValue(50123)
    expect(screen.getByText(lt('relayStopped'))).toBeInTheDocument()
    expect(view.run).not.toHaveBeenCalled()
  })

  it('keeps advanced options collapsed and selects no LAN address until explicitly chosen', async () => {
    const view = setup({ state: unconfiguredState() })
    expect(view.run).not.toHaveBeenCalled()
    expect(screen.getByText(lt('manualRelay')).closest('details')).not.toHaveAttribute('open')
    expect(screen.getByRole('combobox', { name: lt('addressChoice') })).toHaveValue('')
    expect(screen.getByRole('button', { name: lt('reviewHost') })).toBeDisabled()
    view.readAddresses.mockResolvedValueOnce({ ok: true, result: { addresses: hostAddresses } })
    await view.user.click(screen.getByRole('button', { name: lt('refreshAddresses') }))
    expect(screen.getByRole('combobox', { name: lt('addressChoice') })).toHaveValue('')
    expect(view.run.mock.calls).toEqual([])
  })

  it('reviews an exact interface, address, high port and listener impact; Cancel performs no mutation', async () => {
    const view = setup({ state: unconfiguredState() })
    await selectHost(view)
    await view.user.click(screen.getByRole('button', { name: lt('reviewHost') }))
    const review = screen.getByRole('region', { name: lt('hostReview') })
    expect(review).toHaveTextContent('192.168.20.5:48443')
    expect(review).toHaveTextContent('ethernet-fixture')
    expect(review).toHaveTextContent(lt('hostImpact'))
    expect(view.run.mock.calls).toEqual([])
    await view.user.click(screen.getByRole('button', { name: t('cancel') }))
    expect(screen.queryByRole('region', { name: lt('hostReview') })).not.toBeInTheDocument()
    expect(screen.getByRole('combobox', { name: lt('addressChoice') })).toHaveValue('ethernet-fixture\n192.168.20.5')
    expect(view.run.mock.calls).toEqual([])
  })

  it.each([0, 1])('starts only the reviewed numeric listener, address choice %i', async index => {
    const view = setup({ state: unconfiguredState() })
    await selectHost(view, index)
    await view.user.click(screen.getByRole('button', { name: lt('reviewHost') }))
    await view.user.click(screen.getByRole('button', { name: lt('startHost') }))
    expect(view.run.mock.calls).toEqual([['network.configure', { mode: 'lan', hostname: 'test-device', lan: { kind: 'host', address: index ? '[fd00::5]:48443' : '192.168.20.5:48443' } }]])
    expect(screen.queryByText(lt('relayRunning'))).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: lt('startHost') })).toBeDisabled()
  })

  it.each(['0.0.0.0', '::', '127.0.0.1', '8.8.8.8'])('rejects an ineligible address response %s', async address => {
    const view = setup({ state: unconfiguredState() })
    view.readAddresses.mockResolvedValueOnce({ ok: true, result: { addresses: [{ interface: 'fixture', address }] } })
    await view.user.click(screen.getByRole('button', { name: lt('refreshAddresses') }))
    expect(view.setError).toHaveBeenCalledWith({ code: 'invalid_response' })
    expect(screen.getByRole('button', { name: lt('reviewHost') })).toBeDisabled()
    expect(view.run.mock.calls).toEqual([])
  })

  it('offers a truthful empty address state without a wildcard fallback', async () => {
    const view = setup({ state: unconfiguredState() })
    view.readAddresses.mockResolvedValueOnce({ ok: true, result: { addresses: [] } })
    await view.user.click(screen.getByRole('button', { name: lt('refreshAddresses') }))
    expect(screen.getByRole('status')).toHaveTextContent(lt('noAddresses'))
    expect(screen.getByRole('button', { name: lt('reviewHost') })).toBeDisabled()
    expect(screen.getAllByRole('option')).toHaveLength(1)
  })

  it.each(['1023', '65536', '54544', '49000'])('rejects low, invalid or reserved port %s even on direct submit', async port => {
    const state = { ...unconfiguredState(), reservedPorts: [49000] }
    const view = setup({ state })
    await selectHost(view)
    fireEvent.change(screen.getByRole('spinbutton', { name: new RegExp(lt('hostPort')) }), { target: { value: port } })
    fireEvent.submit(screen.getByRole('button', { name: lt('reviewHost') }).closest('form')!)
    expect(screen.getByRole('alert')).toHaveTextContent(lt('hostPortInvalid'))
    expect(screen.queryByRole('button', { name: lt('startHost') })).not.toBeInTheDocument()
    expect(view.run.mock.calls).toEqual([])
  })

  it('distinguishes saved configuration, running listener and remote reachability', () => {
    const state = readyState()
    state.lan = { ...state.lan!, pairingReady: false, listenerReady: false, relayReady: false, path: 'unknown', relay: { kind: 'host', address: '192.168.20.5:48443', certificateSHA256 } }
    const view = setup({ state })
    expect(screen.getByText(lt('savedRelay'))).toBeInTheDocument()
    expect(screen.getByText(lt('relayStopped'))).toBeInTheDocument()
    expect(screen.getByText(t('relayNotReady'))).toBeInTheDocument()
    expect(screen.queryByText(lt('relayRunning'))).not.toBeInTheDocument()
    view.update({ ...state, lan: { ...state.lan, relayReady: true, pairingReady: true, listenerReady: true } })
    expect(screen.getByText(lt('relayRunning'))).toBeInTheDocument()
    expect(screen.getByText(lt('pairingListenerReady'))).toBeInTheDocument()
    expect(screen.getByText(lt('reachabilityUnknown'))).toBeInTheDocument()
    expect(screen.getByText(t('unknown'))).toBeInTheDocument()
  })

  it('warns that Stop ends the whole app and active connections, and Cancel does not stop it', async () => {
    const state = readyState()
    state.services = [{ id: 'active-service', name: 'Fixture service', peerId: recipientPublicKey, network: 'tcp', status: 'active' }]
    const view = setup({ state })
    await view.user.click(screen.getByRole('button', { name: lt('stopApplication') }))
    const review = screen.getByRole('region', { name: lt('stopReview') })
    expect(review).toHaveTextContent(lt('stopImpact'))
    expect(review).toHaveTextContent(lt('activeServices'))
    expect(review).toHaveTextContent('1')
    await view.user.click(screen.getByRole('button', { name: t('cancel') }))
    expect(view.run).not.toHaveBeenCalled()
    await view.user.click(screen.getByRole('button', { name: lt('stopApplication') }))
    view.run.mockResolvedValueOnce({ ok: true, result: { state: 'stopping' } })
    await view.user.click(screen.getByRole('button', { name: lt('confirmStop') }))
    expect(view.run.mock.calls).toEqual([['application.stop', {}]])
    expect(screen.getByRole('status')).toHaveTextContent(lt('stopping'))
    expect(screen.getByRole('button', { name: lt('confirmStop') })).toBeDisabled()
  })
})

describe('Initial invitation setup without preconfigured LAN', () => {
  it('requires an explicit identity and does not inspect or configure before review', async () => {
    const state = unconfiguredState()
    delete state.lan!.publicKey
    const view = setup({ state, draft: { ...emptyLanDraft(), joinInvitation: opaqueInvitation } })
    expect(screen.getByRole('button', { name: lt('reviewInvitation') })).toBeDisabled()
    expect(screen.getByText(lt('joinNeedsIdentity'))).toBeInTheDocument()
    expect(view.run).not.toHaveBeenCalled()
    view.run.mockResolvedValueOnce({ ok: true, result: { publicKey } })
    await view.user.click(screen.getByRole('button', { name: t('createIdentity') }))
    expect(screen.getByRole('button', { name: lt('reviewInvitation') })).toBeEnabled()
    expect(view.run.mock.calls).toEqual([['lan.identity', {}]])
  })

  it('shows only public preview fields, and cancelling neither configures nor joins', async () => {
    const view = setup({ state: unconfiguredState(), draft: { ...emptyLanDraft(), joinInvitation: opaqueInvitation } })
    view.run.mockResolvedValueOnce({ ok: true, result: { ...previewInvitation() } })
    await view.user.click(screen.getByRole('button', { name: lt('reviewInvitation') }))
    const review = screen.getByRole('region', { name: lt('joinReview') })
    expect(review).toHaveTextContent('Host device')
    expect(review).toHaveTextContent('192.0.2.10:443')
    expect(review).toHaveTextContent(certificateSHA256)
    expect(review).toHaveTextContent(lt('joinRelayImpact'))
    expectSecretAbsent(view.container, opaqueInvitation)
    await view.user.click(screen.getByRole('button', { name: t('cancel') }))
    expect(view.run.mock.calls).toEqual([['lan.inspect', { invitation: opaqueInvitation }]])
    expect(view.draft().joinInvitation).toBe(opaqueInvitation)
    expect(localStorage.length).toBe(0)
    expect(sessionStorage.length).toBe(0)
  })

  it('connects the exact inspected relay then joins only after readiness, without granting trust', async () => {
    const view = setup({ state: unconfiguredState(), draft: { ...emptyLanDraft(), joinInvitation: opaqueInvitation } })
    view.run.mockResolvedValueOnce({ ok: true, result: { ...previewInvitation() } }).mockResolvedValueOnce({ ok: true }).mockResolvedValueOnce({ ok: true, result: { paired: true, trusted: false, peerId: recipientPublicKey } })
    view.refresh.mockResolvedValueOnce(readyState())
    await view.user.click(screen.getByRole('button', { name: lt('reviewInvitation') }))
    await view.user.click(screen.getByRole('button', { name: lt('activateAndJoin') }))
    await waitFor(() => expect(view.draft().joinInvitation).toBe(''))
    expect(view.run.mock.calls).toEqual([['lan.inspect', { invitation: opaqueInvitation }], ['network.configure', { mode: 'lan', hostname: 'test-device', lan: previewInvitation().relay }], ['lan.join', { invitation: opaqueInvitation }]])
    expect(view.draft().joinedPeerId).toBe(recipientPublicKey)
  })

  it('preserves configured progress and the invitation when the pairing listener is not ready', async () => {
    const view = setup({ state: unconfiguredState(), draft: { ...emptyLanDraft(), joinInvitation: opaqueInvitation } })
    view.run.mockResolvedValueOnce({ ok: true, result: { ...previewInvitation() } }).mockResolvedValueOnce({ ok: true })
    view.refresh.mockResolvedValueOnce({ ...readyState(), lan: { ...readyState().lan!, pairingReady: false } })
    await view.user.click(screen.getByRole('button', { name: lt('reviewInvitation') }))
    await view.user.click(screen.getByRole('button', { name: lt('activateAndJoin') }))
    expect(screen.getByRole('alert')).toHaveTextContent(lt('joinConfiguredHint'))
    expect(view.run).not.toHaveBeenCalledWith('lan.join', expect.anything())
    expect(view.draft().joinInvitation).toBe(opaqueInvitation)
    expect(screen.queryByText(t('paired'))).not.toBeInTheDocument()
  })

  it('does not join after the setup screen closes during configuration', async () => {
    const view = setup({ state: unconfiguredState(), draft: { ...emptyLanDraft(), joinInvitation: opaqueInvitation } })
    const pending = deferred<CommandResult>()
    view.run.mockResolvedValueOnce({ ok: true, result: { ...previewInvitation() } }).mockReturnValueOnce(pending.promise)
    await view.user.click(screen.getByRole('button', { name: lt('reviewInvitation') }))
    await view.user.click(screen.getByRole('button', { name: lt('activateAndJoin') }))
    view.update(unconfiguredState(), false)
    await act(async () => { pending.resolve({ ok: true }); await pending.promise })
    expect(view.run).not.toHaveBeenCalledWith('lan.join', expect.anything())
    expect(view.refresh).not.toHaveBeenCalled()
    expect(view.draft().joinInvitation).toBe(opaqueInvitation)
  })

  it('discards an inspection that returns after the invitation was edited', async () => {
    const view = setup({ state: unconfiguredState(), draft: { ...emptyLanDraft(), joinInvitation: opaqueInvitation } })
    const pending = deferred<CommandResult>()
    view.run.mockReturnValueOnce(pending.promise)
    await view.user.click(screen.getByRole('button', { name: lt('reviewInvitation') }))
    fireEvent.change(screen.getByLabelText(t('invitation'), { exact: false }), { target: { value: 'new invitation' } })
    await act(async () => { pending.resolve({ ok: true, result: { ...previewInvitation() } }); await pending.promise })
    expect(screen.queryByRole('region', { name: lt('joinReview') })).not.toBeInTheDocument()
    expect(view.draft().joinInvitation).toBe('new invitation')
  })

  it.each(['expired', 'different recipient'] as const)('rejects a %s preview before configuring anything', async reason => {
    const view = setup({ state: unconfiguredState(), draft: { ...emptyLanDraft(), joinInvitation: opaqueInvitation } })
    const preview = previewInvitation()
    if (reason === 'expired') preview.expires = new Date(Date.now() - 1000).toISOString()
    else preview.recipientPublicKey = recipientPublicKey
    view.run.mockResolvedValueOnce({ ok: true, result: { ...preview } })
    await view.user.click(screen.getByRole('button', { name: lt('reviewInvitation') }))
    expect(screen.queryByRole('button', { name: lt('activateAndJoin') })).not.toBeInTheDocument()
    expect(view.run.mock.calls).toEqual([['lan.inspect', { invitation: opaqueInvitation }]])
    if (reason === 'expired') expect(screen.getByRole('alert')).toHaveTextContent(lt('invitationExpired'))
    else expect(view.setError).toHaveBeenCalledWith({ code: 'invalid_response' })
  })

  it('renders initial setup controls and safety explanations in Japanese', async () => {
    const view = setup({ state: unconfiguredState(), locale: 'ja' })
    const ja = lanTranslator('ja')
    expect(screen.getByRole('button', { name: ja('hostRelay') })).toBeInTheDocument()
    view.readAddresses.mockResolvedValueOnce({ ok: true, result: { addresses: hostAddresses } })
    await view.user.click(screen.getByRole('button', { name: ja('refreshAddresses') }))
    await view.user.selectOptions(screen.getByRole('combobox', { name: ja('addressChoice') }), 'ethernet-fixture\n192.168.20.5')
    await view.user.click(screen.getByRole('button', { name: ja('reviewHost') }))
    expect(screen.getByRole('region', { name: ja('hostReview') })).toHaveTextContent(ja('hostImpact'))
    expect(screen.getByRole('button', { name: ja('startHost') })).toBeInTheDocument()
  })
})

describe('LAN setup failure and interruption boundaries', () => {
  it('clears a consumed invitation capability while keeping only its public summary', async () => {
    const view = setup({ draft: { ...emptyLanDraft(), activeInvitation: invitation() } })
    view.update({ ...readyState(), peers: [pairedPeer()] })
    await waitFor(() => expect(view.draft().activeInvitation?.value).toBe(''))
    expect(view.draft().activeInvitation?.consumed).toBe(true)
    expectSecretAbsent(view.container, opaqueInvitation)
  })

  it('does not offer an expired invitation for copying and keeps explicit cancellation available', async () => {
    const active = { ...invitation(), expires: new Date(Date.now() - 1000).toISOString() }
    const view = setup({ draft: { ...emptyLanDraft(), activeInvitation: active } })
    expect(screen.getByText(t('expired'))).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: t('copyInvitation') })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: t('cancelInvitation') })).toBeEnabled()
    expectSecretAbsent(view.container, opaqueInvitation)
  })

  it('does not join or claim success when relay configuration is unacknowledged', async () => {
    const view = setup({ state: unconfiguredState(), draft: { ...emptyLanDraft(), joinInvitation: opaqueInvitation } })
    view.run.mockResolvedValueOnce({ ok: true, result: { ...previewInvitation() } }).mockResolvedValueOnce(undefined)
    await view.user.click(screen.getByRole('button', { name: lt('reviewInvitation') }))
    await view.user.click(screen.getByRole('button', { name: lt('activateAndJoin') }))
    expect(view.run.mock.calls.map(call => call[0])).toEqual(['lan.inspect', 'network.configure'])
    expect(view.refresh).not.toHaveBeenCalled()
    expect(view.draft().joinedPeerId).toBeUndefined()
    expect(view.draft().joinInvitation).toBe(opaqueInvitation)
  })

  it('does not overwrite a different running relay after inspection', async () => {
    const state = readyState()
    state.lan = { ...state.lan!, pairingReady: false, relay: { kind: 'relay', address: '192.0.2.11:443', certificateSHA256 } }
    const view = setup({ state, draft: { ...emptyLanDraft(), joinInvitation: opaqueInvitation } })
    view.run.mockResolvedValueOnce({ ok: true, result: { ...previewInvitation() } })
    await view.user.click(screen.getByRole('button', { name: lt('reviewInvitation') }))
    await view.user.click(screen.getByRole('button', { name: lt('activateAndJoin') }))
    expect(screen.getByRole('alert')).toHaveTextContent(lt('joinDifferentRelay'))
    expect(view.run.mock.calls.map(call => call[0])).toEqual(['lan.inspect'])
  })

  it('retries only pairing after configured readiness appears and an earlier join was unacknowledged', async () => {
    const view = setup({ state: unconfiguredState(), draft: { ...emptyLanDraft(), joinInvitation: opaqueInvitation } })
    view.run.mockResolvedValueOnce({ ok: true, result: { ...previewInvitation() } }).mockResolvedValueOnce({ ok: true }).mockResolvedValueOnce(undefined)
    view.refresh.mockResolvedValueOnce(readyState())
    await view.user.click(screen.getByRole('button', { name: lt('reviewInvitation') }))
    await view.user.click(screen.getByRole('button', { name: lt('activateAndJoin') }))
    expect(view.draft().joinInvitation).toBe(opaqueInvitation)
    view.update(readyState())
    view.run.mockResolvedValueOnce({ ok: true, result: { paired: true, trusted: false, peerId: recipientPublicKey } })
    await view.user.click(screen.getByRole('button', { name: lt('activateAndJoin') }))
    expect(view.run.mock.calls.map(call => call[0])).toEqual(['lan.inspect', 'network.configure', 'lan.join', 'lan.join'])
    expect(view.draft().joinInvitation).toBe('')
  })

  it('waits for the real status when Stop is unacknowledged', async () => {
    const view = setup()
    view.run.mockResolvedValueOnce(undefined)
    await view.user.click(screen.getByRole('button', { name: lt('stopApplication') }))
    await view.user.click(screen.getByRole('button', { name: lt('confirmStop') }))
    expect(screen.queryByText(lt('stopping'))).not.toBeInTheDocument()
    expect(screen.getByText(lt('savedRelay'))).toBeInTheDocument()
    expect(screen.getByRole('button', { name: lt('confirmStop') })).toBeEnabled()
  })
})
