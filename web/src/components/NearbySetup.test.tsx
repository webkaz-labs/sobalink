import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { createRef } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Locale } from '../api'
import type { Server } from '../useServer'
import { nearbyText, nearbyNextAction } from '../nearby-i18n'
import { contactText, idleSetup, liveSetup, reviewSetup, setupScopes } from '../nearby-fixture'
import type { SetupSnapshot } from '../nearby-setup'
import type { DecodeResult } from '../qr/decode-job'
import { NearbySetup, type NearbySetupHandle } from './NearbySetup'
const png = vi.hoisted(() => ({ start: vi.fn(), cancel: vi.fn(), dispose: vi.fn() }))
vi.mock('../qr/png-import', () => ({ PngCardImporter: class { start = png.start; cancel = png.cancel; dispose = png.dispose } }))
const deferred = <T,>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done }); return { promise, resolve } }
function setup(initial: SetupSnapshot = idleSetup(), locale: Locale = 'en') {
  let current = initial
  const mutations = vi.fn<Server['run']>().mockImplementation(async () => ({ ok: true, result: current as any }))
  const run = vi.fn<Server['run']>().mockImplementation(async (name, payload, key) => name === 'setup.scopes' ? { ok: true, result: setupScopes } : name === 'setup.status' ? { ok: true, result: current as any } : mutations(name, payload, key))
  const ref = createRef<NearbySetupHandle>(), server = { auth: 'ready', stale: false, state: null, run, busy: new Set() } as unknown as Server
  const view = render(<NearbySetup server={server} locale={locale} ref={ref} />)
  const c = (key: Parameters<typeof nearbyText>[1]) => nearbyText(locale, key)
  const ready = () => waitFor(() => expect(screen.queryByText(c('loading'))).not.toBeInTheDocument())
  const fill = () => { fireEvent.change(screen.getByLabelText(c('alias'), { exact: false }), { target: { value: 'Peer A' } }); fireEvent.change(screen.getByLabelText(c('scope')), { target: { value: setupScopes.scopes[0].scopeId } }); fireEvent.click(screen.getByLabelText(c('otherHost'))); fireEvent.click(screen.getByLabelText(c('consent'))) }
  return { ...view, ref, server, run, mutations, c, ready, fill, set: (next: SetupSnapshot) => { current = next }, refresh: async () => { fireEvent.click(screen.getAllByRole('button', { name: c('checkStatus') })[0]); await act(async () => {}) } }
}
beforeEach(() => { vi.clearAllMocks(); png.start.mockReset() })
describe('nearby setup explicit consent and recovery', () => {
  it.each(['en', 'ja'] as const)('requires an entered alias and network consent in %s', async locale => {
    const v = setup(idleSetup(), locale); await v.ready()
    expect(v.mutations).not.toHaveBeenCalled(); expect(screen.getByRole('button', { name: v.c('start') })).toBeDisabled()
    expect(screen.getByLabelText(v.c('alias'), { exact: false })).toHaveValue('')
    v.fill(); expect(screen.getByRole('button', { name: v.c('start') })).toBeEnabled()
    fireEvent.click(screen.getByLabelText(v.c('allowHost')))
    expect(screen.getByLabelText(v.c('consent'))).not.toBeChecked()
    fireEvent.click(screen.getByLabelText(v.c('consent')))
    v.mutations.mockResolvedValueOnce({ ok: true, result: liveSetup() as any })
    fireEvent.click(screen.getByRole('button', { name: v.c('start') })); fireEvent.click(screen.getByRole('button', { name: v.c('start') }))
    await waitFor(() => expect(v.mutations).toHaveBeenCalledTimes(1))
    expect(v.mutations).toHaveBeenCalledWith('setup.start', { alias: 'Peer A', scopeId: setupScopes.scopes[0].scopeId, bootstrapPort: 48445, allowHost: true, relayPort: 48443, consent: true }, 'setup.mutation')
  })
  it('recovers a lost start reply using status without replaying start', async () => {
    const v = setup(); await v.ready(); v.fill(); v.mutations.mockResolvedValueOnce(undefined)
    fireEvent.click(screen.getByRole('button', { name: v.c('start') }))
    expect(await screen.findByRole('alert')).toHaveTextContent(v.c('uncertain'))
    expect(screen.getByRole('button', { name: v.c('start') })).toBeDisabled()
    v.set(liveSetup()); await v.refresh()
    expect(v.mutations).toHaveBeenCalledTimes(1)
    expect(v.run).toHaveBeenLastCalledWith('setup.status', {}, 'setup.status')
    expect(screen.getByText('Peer B')).toBeInTheDocument()
  })
  it('uses exact-session status after reload and guards cancel double clicks', async () => {
    const live = liveSetup(), v = setup(live); await v.ready()
    expect(v.run).toHaveBeenCalledWith('setup.status', {}, 'setup.status'); expect(v.mutations).not.toHaveBeenCalled()
    await v.refresh(); expect(v.run).toHaveBeenLastCalledWith('setup.status', { sessionId: live.sessionId }, 'setup.status')
    const pending = deferred<any>(); v.mutations.mockReturnValueOnce(pending.promise)
    let first!: Promise<boolean>, second!: Promise<boolean>
    act(() => { first = v.ref.current!.close(); second = v.ref.current!.close() })
    expect(await second).toBe(false); expect(v.mutations).toHaveBeenCalledTimes(1)
    await act(async () => { pending.resolve({ ok: true, result: { ...live, state: 'cancelled', discovery: { state: 'stopped' } } }); expect(await first).toBe(true) })
    expect(v.mutations).toHaveBeenCalledWith('setup.cancel', { sessionId: live.sessionId }, 'setup.mutation')
  })
  it('waits through physical cleanup before resolving a Close request', async () => {
    const live = liveSetup(), v = setup(live); await v.ready()
    v.mutations.mockResolvedValueOnce({ ok: true, result: { ...live, state: 'cancelling', nextAction: 'wait_for_cleanup' } as any })
    let closed = false, closing!: Promise<boolean>
    await act(async () => { closing = v.ref.current!.close().then(result => { closed = result; return result }) })
    expect(closed).toBe(false); expect(screen.getByText(v.c('cancelling'))).toBeInTheDocument()
    expect(screen.queryByText(v.c('cancelled'))).not.toBeInTheDocument()
    v.set({ ...live, state: 'cancelled', discovery: { state: 'stopped' } }); await v.refresh()
    expect(await closing).toBe(true); expect(v.mutations).toHaveBeenCalledTimes(1)
  })
  it('allows a fresh failed-session retry only with new consent after confirmed cleanup', async () => {
    const failed = { ...liveSetup(), state: 'failed' as const, nextAction: 'try_again', discovery: { state: 'stopped' as const } }
    const v = setup(failed); await v.ready()
    expect(screen.queryByRole('button', { name: v.c('start') })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: v.c('startAgain') }))
    expect(screen.getByRole('button', { name: v.c('start') })).toBeDisabled()
    v.fill(); v.mutations.mockResolvedValueOnce({ ok: true, result: liveSetup() as any })
    fireEvent.click(screen.getByRole('button', { name: v.c('start') }))
    await waitFor(() => expect(v.mutations).toHaveBeenCalledWith('setup.start', expect.objectContaining({ allowHost: false, relayPort: 0, consent: true }), 'setup.mutation'))
  })
  it('rejects a different session binding after a lost start response', async () => {
    const v = setup(); await v.ready(); v.fill(); v.mutations.mockResolvedValueOnce(undefined)
    fireEvent.click(screen.getByRole('button', { name: v.c('start') })); await screen.findByRole('alert')
    const other = liveSetup(); other.contact = { ...other.contact!, alias: 'Other synthetic peer' }; other.contactText = contactText(other.contact)
    v.set(other); await v.refresh()
    expect(screen.getByRole('alert')).toHaveTextContent(v.c('invalidResponse'))
    expect(v.mutations).toHaveBeenCalledTimes(1)
  })
  it('does not leave when cancellation response is uncertain', async () => {
    const v = setup(liveSetup()); await v.ready(); v.mutations.mockResolvedValueOnce(undefined)
    await act(async () => { expect(await v.ref.current!.close()).toBe(false) })
    expect(screen.getByRole('alert')).toHaveTextContent(v.c('uncertain'))
  })
  it('keeps QR/manual flow available when discovery is unavailable without implicitly trusting', async () => {
    const live = liveSetup(); live.discovery = { state: 'unavailable', reasonCode: 'unavailable' }
    const v = setup(live); await v.ready()
    expect(screen.getByText(v.c('discoveryUnavailable'))).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText(v.c('peerExchange')), { target: { value: contactText(live.candidates[0].contact) } })
    fireEvent.click(screen.getByRole('button', { name: v.c('importPeer') }))
    await waitFor(() => expect(v.mutations).toHaveBeenCalledWith('setup.manual', { sessionId: live.sessionId, contactText: contactText(live.candidates[0].contact) }, 'setup.mutation'))
    expect(v.run.mock.calls.some(([name]) => name === 'peer.trust')).toBe(false)
  })
  it('does not convert lifecycle blockers or saved pairing to connected', async () => {
    const s = reviewSetup(); s.process.state = 'needs_restart'; s.paired = true
    const v = setup(s); await v.ready()
    expect(screen.getByText(v.c('needsRestart'))).toBeInTheDocument(); expect(screen.getByText(v.c('paired'))).toBeInTheDocument()
    expect(screen.queryByText(v.c('connected'))).not.toBeInTheDocument(); expect(screen.getByRole('button', { name: v.c('approve') })).toBeDisabled()
  })
})
describe('full transcript comparison and stale UI boundaries', () => {
  it('requires all fingerprint digits and explicit comparison before one exact approval', async () => {
    const s = reviewSetup(), v = setup(s); await v.ready()
    const field = screen.getByLabelText(v.c('peerFingerprint')), confirm = screen.getByLabelText(v.c('verified'))
    expect(confirm).toBeDisabled(); fireEvent.change(field, { target: { value: 'eeeeee' } }); expect(confirm).toBeDisabled()
    fireEvent.change(field, { target: { value: s.plan!.verificationText } }); expect(confirm).toBeEnabled()
    expect(screen.getByRole('button', { name: v.c('approve') })).toBeDisabled(); fireEvent.click(confirm)
    expect(screen.getByRole('button', { name: v.c('approve') })).toBeDisabled()
    fireEvent.click(screen.getByLabelText(v.c('saveConsent')))
    const pending = deferred<any>(); v.mutations.mockReturnValueOnce(pending.promise)
    fireEvent.click(screen.getByRole('button', { name: v.c('approve') })); fireEvent.click(screen.getByRole('button', { name: v.c('approve') }))
    expect(v.mutations).toHaveBeenCalledTimes(1)
    expect(v.mutations).toHaveBeenCalledWith('setup.approve', { sessionId: s.sessionId, revision: 1, fingerprint: s.plan!.fingerprint, verified: true }, 'setup.mutation')
    await act(async () => pending.resolve({ ok: true, result: { ...s, localApproved: true } }))
    expect(screen.getByRole('button', { name: v.c('approve') })).toBeDisabled()
  })
  it('clears comparison after a new plan revision', async () => {
    const s = reviewSetup(), v = setup(s); await v.ready()
    fireEvent.change(screen.getByLabelText(v.c('peerFingerprint')), { target: { value: s.plan!.fingerprint } }); fireEvent.click(screen.getByLabelText(v.c('verified')))
    v.set({ ...s, plan: { ...s.plan!, revision: 2, fingerprint: 'f'.repeat(64), verificationText: 'sobalink-setup-verify-v1:' + 'f'.repeat(64) } }); await v.refresh()
    expect(screen.getByLabelText(v.c('peerFingerprint'))).toHaveValue(''); expect(screen.getByLabelText(v.c('saveConsent'))).not.toBeChecked(); expect(screen.getByRole('button', { name: v.c('approve') })).toBeDisabled()
  })
  it.each(['edit', 'cancel', 'unmount', 'stale'] as const)('discards late PNG decoding after %s', async boundary => {
    const s = reviewSetup(), v = setup(s); await v.ready(); const pending = deferred<DecodeResult>(); png.start.mockReturnValueOnce(pending.promise)
    fireEvent.change(screen.getByLabelText(v.c('fingerprintPNG')), { target: { files: [new File(['synthetic'], 'fixture.png')] } })
    if (boundary === 'edit') fireEvent.change(screen.getByLabelText(v.c('peerFingerprint')), { target: { value: 'new draft' } })
    if (boundary === 'cancel') fireEvent.click(await screen.findByRole('button', { name: v.c('pngCancel') }))
    if (boundary === 'unmount') v.unmount()
    if (boundary === 'stale') v.rerender(<NearbySetup server={{ ...v.server, stale: true }} locale="en" ref={v.ref} />)
    await act(async () => pending.resolve({ ok: true, text: s.plan!.verificationText }))
    expect(v.mutations).not.toHaveBeenCalled()
    if (boundary !== 'unmount') expect(screen.getByLabelText(v.c('peerFingerprint'))).toHaveValue(boundary === 'edit' ? 'new draft' : '')
    expect(png.cancel.mock.calls.length + png.dispose.mock.calls.length).toBeGreaterThan(0)
  })
})

describe('repeat setup preserves the completed connection', () => {
  const connected = (joined = true): SetupSnapshot => ({ ...reviewSetup(), state: 'connected', connected: true, paired: true, localApproved: true, remoteApproved: true, nextAction: joined ? 'start_new_setup' : 'done', discovery: { state: 'stopped' } })
  const freshConsent = (v: ReturnType<typeof setup>) => {
    fireEvent.change(screen.getByLabelText(v.c('alias'), { exact: false }), { target: { value: 'Peer A' } })
    fireEvent.change(screen.getByLabelText(v.c('scope')), { target: { value: setupScopes.scopes[0].scopeId } })
    fireEvent.click(screen.getByLabelText(v.c('otherHost')))
    fireEvent.click(screen.getByLabelText(v.c('consent')))
  }
  const newer = (): SetupSnapshot => {
    const next = liveSetup(); next.sessionId = 'f'.repeat(64); next.contact = { ...next.contact!, sessionId: next.sessionId }; next.contactText = contactText(next.contact)
    return next
  }
  it.each(['en', 'ja'] as const)('opens an unconsented new form only after explicit joined-session reuse in %s', async locale => {
    const old = connected(), v = setup(old, locale); await v.ready()
    expect(screen.queryByRole('button', { name: v.c('start') })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: v.c('connectAnother') }))
    expect(await screen.findByText(v.c('freshForm'))).toBeInTheDocument()
    expect(v.mutations).not.toHaveBeenCalled()
    expect(screen.getByLabelText(v.c('alias'), { exact: false })).toHaveValue('')
    expect(screen.getByLabelText(v.c('scope'))).toHaveValue('')
    expect(screen.getByLabelText(v.c('allowHost'))).not.toBeChecked()
    expect(screen.getByLabelText(v.c('consent'))).not.toBeChecked()
    expect(screen.getByRole('button', { name: v.c('start') })).toBeDisabled()
    expect(screen.queryByRole('region', { name: v.c('review') })).not.toBeInTheDocument()
    freshConsent(v); v.mutations.mockResolvedValueOnce({ ok: true, result: newer() as any })
    fireEvent.click(screen.getByRole('button', { name: v.c('start') }))
    await waitFor(() => expect(screen.getByText('Peer B')).toBeInTheDocument())
    expect(v.mutations).toHaveBeenCalledTimes(1)
    expect(v.mutations).toHaveBeenCalledWith('setup.start', expect.objectContaining({ scopeId: setupScopes.scopes[0].scopeId, allowHost: false, relayPort: 0, consent: true }), 'setup.mutation')
    expect(screen.queryByText(v.c('connected'))).not.toBeInTheDocument()
  })
  it('cancels only the original receipt owner and waits for joined cleanup before the fresh form', async () => {
    const old = connected(false), v = setup(old); await v.ready()
    const closing = { ...old, state: 'cancelling' as const, connected: false, nextAction: 'wait_for_cleanup' }
    v.mutations.mockResolvedValueOnce({ ok: true, result: closing as any })
    fireEvent.click(screen.getByRole('button', { name: v.c('connectAnother') })); fireEvent.click(screen.getByRole('button', { name: v.c('finishPreviousAction') }))
    expect(await screen.findByText(v.c('cancelling'))).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: v.c('start') })).not.toBeInTheDocument()
    expect(screen.queryByText(v.c('cancelled'))).not.toBeInTheDocument()
    expect(v.mutations).toHaveBeenCalledTimes(1)
    expect(v.mutations).toHaveBeenCalledWith('setup.cancel', { sessionId: old.sessionId }, 'setup.mutation')
    v.set({ ...old, nextAction: 'start_new_setup' }); await v.refresh()
    expect(v.run).toHaveBeenLastCalledWith('setup.status', { sessionId: old.sessionId }, 'setup.status')
    expect(screen.getByText(v.c('freshForm'))).toBeInTheDocument()
    expect(screen.getByLabelText(v.c('consent'))).not.toBeChecked()
  })
  it('keeps Close pending until the old connected owner finishes cleanup', async () => {
    const old = connected(false), v = setup(old); await v.ready()
    v.mutations.mockResolvedValueOnce({ ok: true, result: { ...old, state: 'cancelling', connected: false, nextAction: 'wait_for_cleanup' } as any })
    fireEvent.click(screen.getByRole('button', { name: v.c('connectAnother') })); await screen.findByText(v.c('cancelling'))
    let closed = false, closing!: Promise<boolean>
    await act(async () => { closing = v.ref.current!.close().then(result => { closed = result; return result }) })
    expect(closed).toBe(false)
    v.set({ ...old, nextAction: 'start_new_setup' }); await v.refresh()
    expect(await closing).toBe(true); expect(v.mutations).toHaveBeenCalledTimes(1)
  })
  it('recovers a lost receipt cancellation using only the original session', async () => {
    const old = connected(false), v = setup(old); await v.ready(); v.mutations.mockResolvedValueOnce(undefined)
    fireEvent.click(screen.getByRole('button', { name: v.c('connectAnother') }))
    expect(await screen.findByRole('alert')).toHaveTextContent(v.c('uncertain'))
    expect(screen.queryByRole('button', { name: v.c('start') })).not.toBeInTheDocument()
    v.set({ ...old, nextAction: 'start_new_setup' }); await v.refresh()
    expect(v.run).toHaveBeenLastCalledWith('setup.status', { sessionId: old.sessionId }, 'setup.status')
    expect(screen.getByRole('button', { name: v.c('start') })).toBeDisabled()
    expect(v.mutations).toHaveBeenCalledTimes(1)
  })
  it('never treats an old connected receipt as a lost new-start success', async () => {
    const old = connected(), v = setup(old); await v.ready()
    fireEvent.click(screen.getByRole('button', { name: v.c('connectAnother') })); await screen.findByText(v.c('freshForm'))
    freshConsent(v); v.mutations.mockResolvedValueOnce(undefined)
    fireEvent.click(screen.getByRole('button', { name: v.c('start') }))
    expect(await screen.findByRole('alert')).toHaveTextContent(v.c('uncertain'))
    expect(screen.getByText(v.c('newStartPending'))).toBeInTheDocument()
    await v.refresh()
    expect(v.run).toHaveBeenLastCalledWith('setup.status', {}, 'setup.status')
    expect(screen.getByRole('alert')).toHaveTextContent(v.c('uncertain'))
    expect(screen.queryByRole('button', { name: v.c('start') })).not.toBeInTheDocument()
    expect(screen.queryByText(v.c('connected'))).not.toBeInTheDocument()
    expect(screen.queryByRole('region', { name: v.c('review') })).not.toBeInTheDocument()
    v.set(newer()); await v.refresh()
    expect(screen.getByText('Peer B')).toBeInTheDocument()
    expect(screen.queryByText(v.c('newStartPending'))).not.toBeInTheDocument()
    expect(v.mutations).toHaveBeenCalledTimes(1)
  })
  it('clears every local approval input when new-start recovery already contains a fresh plan', async () => {
    const v = setup(connected()); await v.ready()
    fireEvent.click(screen.getByRole('button', { name: v.c('connectAnother') })); await screen.findByText(v.c('freshForm'))
    freshConsent(v); v.mutations.mockResolvedValueOnce(undefined)
    fireEvent.click(screen.getByRole('button', { name: v.c('start') })); await screen.findByRole('alert')
    const next = reviewSetup(); next.sessionId = 'f'.repeat(64); next.contact = { ...next.contact!, sessionId: next.sessionId }; next.contactText = contactText(next.contact)
    next.plan!.participants[0].session_id = next.sessionId; next.remoteApproved = true
    v.set(next); await v.refresh()
    expect(screen.getByRole('region', { name: v.c('review') })).toBeInTheDocument()
    expect(screen.getByLabelText(v.c('peerFingerprint'))).toHaveValue('')
    expect(screen.getByLabelText(v.c('verified'))).not.toBeChecked()
    expect(screen.getByLabelText(v.c('saveConsent'))).not.toBeChecked()
    expect(screen.getByRole('button', { name: v.c('approve') })).toBeDisabled()
    expect(v.mutations).toHaveBeenCalledTimes(1)
  })
  it('rejects the old session even if returned directly by new setup.start', async () => {
    const old = connected(), v = setup(old); await v.ready()
    fireEvent.click(screen.getByRole('button', { name: v.c('connectAnother') })); await screen.findByText(v.c('freshForm'))
    freshConsent(v); fireEvent.click(screen.getByRole('button', { name: v.c('start') }))
    expect(await screen.findByRole('alert')).toHaveTextContent(v.c('invalidResponse'))
    expect(screen.queryByRole('button', { name: v.c('start') })).not.toBeInTheDocument()
    expect(screen.getByText(v.c('newStartPending'))).toBeInTheDocument()
  })
})


describe('plain-language role choice and bounded corrections', () => {
  it.each(['en', 'ja'] as const)('requires an explicit host choice without granting networking in %s', async locale => {
    const v = setup(idleSetup(), locale); await v.ready()
    expect(screen.getByRole('radio', { name: v.c('allowHost') })).not.toBeChecked()
    expect(screen.getByRole('radio', { name: v.c('otherHost') })).not.toBeChecked()
    expect(screen.getByLabelText(v.c('consent'))).toBeDisabled()
    expect(screen.getByText(v.c('hostHint'))).toBeInTheDocument()
    expect(screen.getByText(v.c('stopImpact'))).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText(v.c('alias'), { exact: false }), { target: { value: 'Peer A' } })
    fireEvent.click(screen.getByRole('radio', { name: v.c('otherHost') }))
    expect(screen.getByText(v.c('otherHostHint'))).toBeInTheDocument()
    expect(screen.getByRole('button', { name: v.c('start') })).toBeDisabled()
    expect(v.mutations).not.toHaveBeenCalled()
    fireEvent.click(screen.getByLabelText(v.c('consent')))
    fireEvent.click(screen.getByRole('button', { name: v.c('start') }))
    expect(v.mutations).toHaveBeenCalledWith('setup.start', expect.objectContaining({ allowHost: false, relayPort: 0, consent: true }), 'setup.mutation')
  })
  it.each(['en', 'ja'] as const)('offers a clean no-host retry with every choice reset in %s', async locale => {
    const old = liveSetup(), v = setup(idleSetup(), locale); await v.ready(); v.fill()
    fireEvent.click(screen.getByLabelText(v.c('allowHost'))); fireEvent.click(screen.getByLabelText(v.c('consent')))
    v.mutations.mockResolvedValueOnce({ ok: true, result: old as any })
    fireEvent.click(screen.getByRole('button', { name: v.c('start') })); await screen.findByText('Peer B')
    v.set({ ...old, state: 'cancelling', reasonCode: 'setup_no_permitted_host', nextAction: 'wait_for_cleanup' }); await v.refresh()
    expect(screen.getByText(v.c('noHost'))).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: v.c('reviewHost') })).not.toBeInTheDocument()
    v.set({ ...old, state: 'failed', reasonCode: 'setup_no_permitted_host', nextAction: 'try_again', discovery: { state: 'stopped' } }); await v.refresh()
    fireEvent.click(screen.getByRole('button', { name: v.c('reviewHost') }))
    expect(screen.getByLabelText(v.c('alias'), { exact: false })).toHaveValue('')
    expect(screen.getByLabelText(v.c('scope'))).toHaveValue('')
    expect(screen.getByLabelText(v.c('allowHost'))).not.toBeChecked()
    expect(screen.getByLabelText(v.c('otherHost'))).not.toBeChecked()
    expect(screen.getByLabelText(v.c('consent'))).not.toBeChecked()
    expect(v.mutations).toHaveBeenCalledTimes(1)
    v.fill(); fireEvent.click(screen.getByLabelText(v.c('allowHost'))); fireEvent.click(screen.getByLabelText(v.c('consent')))
    v.mutations.mockResolvedValueOnce(undefined)
    fireEvent.click(screen.getByRole('button', { name: v.c('start') }))
    expect(await screen.findByRole('alert')).toHaveTextContent(v.c('uncertain'))
    expect(screen.getByRole('button', { name: v.c('start') })).toBeDisabled()
    v.set(old); await v.refresh()
    expect(screen.getByRole('alert')).toHaveTextContent(v.c('uncertain'))
    expect(v.mutations).toHaveBeenCalledTimes(2)
  })
  it.each([
    { state: 'failed', reasonCode: 'setup_no_permitted_host', nextAction: 'review_existing_network' },
    { state: 'failed', reasonCode: 'setup_existing_anchor_conflict', nextAction: 'review_existing_network' },
    { state: 'recovery_required', reasonCode: 'setup_cleanup_unconfirmed', nextAction: 'review_recovery' },
    { state: 'failed', nextAction: 'try_again', paired: true },
    { state: 'failed', nextAction: 'try_again', process: { state: 'needs_restart', profilePreserved: true, approvalsSurviveRestart: false } },
  ] as const)('does not reopen an unsafe terminal result: %j', async changes => {
    const base = 'paired' in changes && changes.paired ? reviewSetup() : liveSetup()
    const v = setup({ ...base, discovery: { state: 'stopped' }, ...changes }); await v.ready()
    expect(screen.queryByText(v.c('invalidResponse'))).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: v.c('reviewHost') })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: v.c('startAgain') })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: v.c('start') })).not.toBeInTheDocument()
    expect(v.mutations).not.toHaveBeenCalled()
  })
  it.each(['en', 'ja'] as const)('re-reviews corrected ports and removes raw next-action tokens in %s', async locale => {
    const v = setup({ ...idleSetup(), nextAction: 'choose_scope' }, locale); await v.ready(); v.fill()
    expect(screen.getByText(nearbyNextAction(locale, 'choose_scope'))).toBeInTheDocument()
    expect(screen.queryByText('choose_scope')).not.toBeInTheDocument()
    fireEvent.change(screen.getByLabelText(v.c('bootstrapPort')), { target: { value: '54544' } })
    expect(screen.getByLabelText(v.c('consent'))).not.toBeChecked()
    fireEvent.click(screen.getByLabelText(v.c('consent'))); fireEvent.click(screen.getByRole('button', { name: v.c('start') }))
    expect(screen.getByRole('alert')).toHaveTextContent(v.c('invalidInput')); expect(v.mutations).not.toHaveBeenCalled()
    fireEvent.change(screen.getByLabelText(v.c('bootstrapPort')), { target: { value: '55000' } })
    fireEvent.click(screen.getByLabelText(v.c('consent'))); fireEvent.click(screen.getByRole('button', { name: v.c('start') }))
    expect(v.mutations).toHaveBeenCalledWith('setup.start', expect.objectContaining({ bootstrapPort: 55000, allowHost: false, relayPort: 0 }), 'setup.mutation')
    for (const action of ['wait_for_cleanup', 'select_peer', 'select_on_both_devices', 'compare_full_fingerprint', 'wait_for_peer', 'review_recovery', 'restart_offline', 'review_existing_network', 'try_manual', 'try_again', 'done', 'start_new_setup', 'future_unknown']) {
      expect(nearbyNextAction(locale, action)).not.toContain(action)
      expect(nearbyNextAction(locale, action)).not.toBe(nearbyNextAction(locale === 'ja' ? 'en' : 'ja', action))
    }
  })
})


it('hides the previous failed plan when deliberately opening a fresh review', async () => {
  const failed = { ...reviewSetup(), state: 'failed' as const, nextAction: 'try_again', discovery: { state: 'stopped' as const } }
  const v = setup(failed); await v.ready()
  expect(screen.getByRole('region', { name: v.c('review') })).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: v.c('startAgain') }))
  expect(screen.queryByRole('region', { name: v.c('review') })).not.toBeInTheDocument()
  expect(screen.queryByText(v.c('failed'))).not.toBeInTheDocument()
  expect(v.mutations).not.toHaveBeenCalled()
})
