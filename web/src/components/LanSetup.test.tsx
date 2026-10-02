import { useState } from 'react'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { CommandResult, Peer, State } from '../api'
import { translator } from '../i18n'
import type { Server } from '../useServer'
import { emptyLanDraft, LanSetup, type ActiveInvitation, type LanDraft } from './LanSetup'

const t = translator('en')
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
function setup({ state = readyState(), draft = emptyLanDraft(), stale = false }: { state?: State; draft?: LanDraft; stale?: boolean } = {}) {
  const user = userEvent.setup()
  const run = vi.fn<Server['run']>().mockResolvedValue({ ok: true })
  const setError = vi.fn()
  const onViewPeer = vi.fn()
  let currentDraft = draft
  const server: Server = {
    state, auth: 'ready', stale, error: null, setError, busy: new Set(),
    refresh: vi.fn().mockResolvedValue(state), run, updatedAt: null, handleError: vi.fn(),
  }
  function Harness({ nextState, visible = true }: { nextState: State; visible?: boolean }) {
    const [value, setDraft] = useState(draft)
    const [hostname, setHostname] = useState('test-device')
    currentDraft = value
    return visible ? <LanSetup server={{ ...server, state: nextState }} state={nextState} t={t} locale="en" hostname={hostname} setHostname={setHostname} draft={value} setDraft={setDraft} onViewPeer={onViewPeer} /> : null
  }
  const view = render(<Harness nextState={state} />)
  return { ...view, user, run, setError, onViewPeer, draft: () => currentDraft, update: (nextState = state, visible = true) => view.rerender(<Harness nextState={nextState} visible={visible} />) }
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

  it.each(['tailnet selected', 'relay unconfigured', 'relay not ready', 'stale state'] as const)('blocks joining when %s even with an invitation present', async reason => {
    const state = readyState()
    if (reason === 'tailnet selected') state.settings!.network = 'tailnet'
    if (reason === 'relay unconfigured') state.lan!.configured = false
    if (reason === 'relay not ready') state.lan!.pairingReady = false
    const view = setup({ state, stale: reason === 'stale state', draft: { ...emptyLanDraft(), joinInvitation: opaqueInvitation } })
    const button = screen.getByRole('button', { name: t('join') })
    expect(button).toBeDisabled()
    expect(screen.getByText(t('relayRequired'))).toBeInTheDocument()
    // The handler must enforce the same boundary even for a direct form submit.
    fireEvent.submit(button.closest('form')!)
    expect(view.run).not.toHaveBeenCalled()
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
