import { act, fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { CommandResult, LANStartReview, Locale, State } from '../api'
import { translator } from '../i18n'
import { lanTranslator } from '../lan-i18n'
import type { Server } from '../useServer'
import { SavedLanHost } from './SavedLanHost'

function saved(): LANStartReview {
  return { publicKey: 'e'.repeat(64), revision: 'a'.repeat(64), hostname: 'fixture-host', relay: { kind: 'host', address: '192.168.50.10:48443', certificateSHA256: 'b'.repeat(64) }, policy: { mode: 'allowed-lan-destinations', prefixes: ['192.168.50.0/24'] }, pairedDevices: 2, trustedDevices: 1, automaticReceivers: 1, preparedRelays: [{ kind: 'relay', address: '192.168.50.11:48443', certificateSHA256: 'c'.repeat(64) }], pendingStartup: ['fixture-startup'] }
}
function state(): State {
  return { csrfToken: 'fixture-session', self: { name: 'fixture-host', status: 'idle' }, peers: [], messages: [], transfers: [], services: [], shares: [], settings: { network: 'lan' }, lan: { configured: true, pairingReady: false, path: 'unknown', relay: saved().relay, savedStart: saved() } }
}
function setup(locale: Locale = 'en') {
  const run = vi.fn<Server['run']>().mockResolvedValue({ ok: true })
  const user = userEvent.setup()
  const server = { run, auth: 'ready', stale: false, busy: new Set(), setError: vi.fn() } as unknown as Server
  const initial = state()
  const view = render(<SavedLanHost server={server} state={initial} locale={locale} t={translator(locale)} blocked={false} />)
  return { ...view, run, user, initial, update: (next = initial, visible = true, blocked = false, patch: Partial<Server> = {}) => view.rerender(visible ? <SavedLanHost server={{ ...server, ...patch }} state={next} locale={locale} t={translator(locale)} blocked={blocked} /> : <></>) }
}
function deferred() { let resolve!: (value: CommandResult | undefined) => void; const promise = new Promise<CommandResult | undefined>(done => { resolve = done }); return { promise, resolve } }

describe('exact saved LAN host start', () => {
  it.each(['en', 'ja'] as const)('reviews exact saved endpoint, pin, policy and current authority in %s without re-entry or discovery', async locale => {
    const view = setup(locale), d = lanTranslator(locale)
    expect(view.run).not.toHaveBeenCalled()
    await view.user.click(screen.getByRole('button', { name: d('reviewSavedHost') }))
    const review = screen.getByRole('region', { name: d('savedHostReview') })
    for (const value of ['fixture-host', '192.168.50.10:48443', 'b'.repeat(64), '192.168.50.0/24']) expect(review).toHaveTextContent(value)
    expect(review).toHaveTextContent(d('savedHostImpact'))
    await view.user.click(screen.getByText(d('existingScope')))
    for (const value of ['192.168.50.11:48443', 'c'.repeat(64), 'fixture-startup']) expect(review).toHaveTextContent(value)
    expect(view.run).not.toHaveBeenCalled()
    await view.user.click(screen.getByRole('button', { name: translator(locale)('cancel') }))
    expect(screen.queryByRole('region')).not.toBeInTheDocument()
    expect(view.run).not.toHaveBeenCalled()
    await view.user.click(screen.getByRole('button', { name: d('reviewSavedHost') }))
    await view.user.click(screen.getByRole('button', { name: d('startSavedHost') }))
    expect(view.run.mock.calls).toEqual([['network.configure', { mode: 'lan', expectedLANStartRevision: 'a'.repeat(64) }]])
    expect(screen.getByRole('status')).toHaveTextContent(translator(locale)('actionAccepted'))
    expect(screen.getByRole('button', { name: d('startSavedHost') })).toBeDisabled()
  })

  it.each(['revision', 'pin', 'endpoint', 'policy', 'scope', 'session', 'stale'] as const)('invalidates a review after %s changes and does not revive it when values return', async field => {
    const view = setup(), d = lanTranslator('en')
    await view.user.click(screen.getByRole('button', { name: d('reviewSavedHost') }))
    const next = structuredClone(view.initial)
    if (field === 'revision') next.lan!.savedStart!.revision = 'd'.repeat(64)
    if (field === 'pin') next.lan!.savedStart!.relay.certificateSHA256 = 'd'.repeat(64)
    if (field === 'endpoint') next.lan!.savedStart!.relay.address = '192.168.50.10:48444'
    if (field === 'policy') next.lan!.savedStart!.policy = { mode: 'trusted-relay', prefixes: [] }
    if (field === 'scope') next.lan!.savedStart!.automaticReceivers++
    if (field === 'session') next.csrfToken = 'changed-session'
    view.update(next, true, field === 'stale')
    view.update(view.initial)
    expect(screen.queryByRole('region')).not.toBeInTheDocument()
    expect(view.run).not.toHaveBeenCalled()
  })

  it.each(['close', 'newer-state'] as const)('submits once and ignores an acknowledgement after %s', async change => {
    const view = setup(), d = lanTranslator('en'), pending = deferred()
    view.run.mockReturnValueOnce(pending.promise)
    await view.user.click(screen.getByRole('button', { name: d('reviewSavedHost') }))
    const button = screen.getByRole('button', { name: d('startSavedHost') })
    fireEvent.click(button); fireEvent.click(button)
    expect(view.run).toHaveBeenCalledTimes(1)
    expect(screen.getByRole('button', { name: translator('en')('cancel') })).toBeDisabled()
    const next = structuredClone(view.initial); next.lan!.savedStart!.revision = 'd'.repeat(64)
    view.update(next, change !== 'close')
    await act(async () => { pending.resolve({ ok: true }); await pending.promise })
    if (change === 'close') view.update(next)
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
    expect(screen.queryByRole('region')).not.toBeInTheDocument()
    expect(view.run).toHaveBeenCalledTimes(1)
  })

  it('offers no saved-start action for a server state without an authoritative review', () => {
    const view = setup(), next = state(); delete next.lan!.savedStart
    view.update(next)
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
    expect(view.run).not.toHaveBeenCalled()
  })
})
