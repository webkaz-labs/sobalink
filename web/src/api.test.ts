import { describe, expect, it, vi } from 'vitest'
import { ApiError, canExchange, command, getState, safeAuthURL, setCSRFToken, type Peer } from './api'
import { detectLocale, en, ja, errorText, errorDetail, translator } from './i18n'

describe('local API boundary', () => {
  it('sends cookie-bound, CSRF-protected commands with an explicit request identity', async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200 }))
    vi.stubGlobal('fetch', fetch)
    setCSRFToken('test-csrf')
    await command('message.send', { peerId: 'a', text: 'hello' }, 'request-one')
    expect(fetch).toHaveBeenCalledWith('/api/command', expect.objectContaining({ credentials: 'same-origin', redirect: 'error', cache: 'no-store', headers: expect.objectContaining({ 'X-CSRF-Token': 'test-csrf' }), body: JSON.stringify({ requestId: 'request-one', name: 'message.send', payload: { peerId: 'a', text: 'hello' } }) }))
  })
  it('preserves stable error codes and rejects malformed success', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ code: 'unauthenticated' }), { status: 401 })))
    await expect(getState()).rejects.toMatchObject({ code: 'unauthenticated' })
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('{}', { status: 200 })))
    await expect(getState()).rejects.toMatchObject({ code: 'invalid_response' })
  })
  it('does not mistake an unacknowledged mutation for success', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('{"ok":false}', { status: 200 })))
    await expect(command('peer.reconnect', { peerId: 'a' })).rejects.toBeInstanceOf(ApiError)
  })
  it('requires live verified locally trusted app support', () => {
    const peer: Peer = { id: 'a', name: 'A', networks: ['tailnet'], online: true, verified: true, trusted: true, bridge: true, path: 'direct' }
    expect(canExchange(peer)).toBe(true)
    for (const key of ['online', 'verified', 'trusted', 'bridge']) expect(canExchange({ ...peer, [key]: false })).toBe(false)
    expect(canExchange({ ...peer, autosave: { enabled: true, paused: true, directory: '/tmp/received' } })).toBe(false)
  })
  it('renders only bounded official authorization links', () => {
    expect(safeAuthURL('https://login.tailscale.com/a/test-token_1')).toBe('https://login.tailscale.com/a/test-token_1')
    for (const url of ['http://login.tailscale.com/a/test', 'https://login.tailscale.com:444/a/test', 'https://login.tailscale.com/a/test?redirect=other', 'https://login.tailscale.com/a/test#fragment', 'https://login.tailscale.com/a/test%20token', 'https://example.com/a/test', 'javascript:alert(1)', 'https://login.tailscale.com/a/']) expect(safeAuthURL(url)).toBeNull()
  })
})
describe('locale coverage', () => {
  it('uses Japanese locale variants and safe English fallback', () => {
    expect(detectLocale(['ja-JP'])).toBe('ja')
    expect(detectLocale(['ja_JP'])).toBe('ja')
    expect(detectLocale(['fr-FR'])).toBe('en')
    expect(detectLocale(['en-US', 'ja-JP'])).toBe('en')
    expect(detectLocale(['fr-FR', 'ja-JP'])).toBe('ja')
    expect(detectLocale([])).toBe('en')
  })
  it('has matching human-interface keys', () => { expect(Object.keys(ja).sort()).toEqual(Object.keys(en).sort()) })
})


describe('localized recovery', () => {
  it.each(['lan_environment_proxy', 'lan_environment_override', 'lan_relay_mismatch', 'lan_certificate_expired', 'network_restart_required', 'lan_cancel_invite_first', 'lan_pair_reply_uncertain', 'lan_remote_paired_local_save', 'lan_revoke_not_persisted'])('gives a Japanese next step for %s while retaining technical details separately', code => {
    const error = new ApiError(code, 'Synthetic backend diagnostic')
    expect(errorText(error, translator('ja'))).toMatch(/[ぁ-んァ-ヶ一-龠]/)
    expect(errorText(error, translator('ja'))).not.toContain(error.message)
    expect(errorDetail(error, translator('ja'))).toBe(error.message)
    expect(errorText(error, translator('en'))).not.toContain('Synthetic')
  })
  it('keeps unknown backend text secondary to localized failure guidance', () => {
    const error = new ApiError('future_failure', 'Synthetic detail')
    expect(errorText(error, translator('ja'))).toBe(ja.request_failed)
    expect(errorDetail(error, translator('ja'))).toBe(error.message)
  })
})

describe('effective exchange budgets', () => {
  it('honors selected limits above prior browser defaults and retains finite resource bounds', async () => {
    const { exchangeBudgets } = await import('./api')
    const state = { limits: { effective: { logical: { messageBytes: { mode: 'unlimited' }, batchEntries: { mode: 'unlimited' }, batchBytes: { mode: 'unlimited' }, fileBytes: { mode: 'limited', value: 2 ** 31 } }, resources: { messageTextBytes: { mode: 'limited', value: 65536 }, transferManifestBytes: { mode: 'limited', value: 1024 * 1024 }, transferSpoolBytes: { mode: 'limited', value: 2 ** 32 } } } } } as unknown as import('./api').State
    expect(exchangeBudgets(state)).toEqual({ pathDepth: 16, pathBytes: 4096, messageBytes: 65536, batchEntries: 2048, batchBytes: 2 ** 32, fileBytes: 2 ** 31 })
  })
})

it('derives unlimited path choices from the finite manifest budget', async () => {
  const { exchangeBudgets } = await import('./api')
  const state = { limits: { effective: { logical: { pathBytes: { mode: 'unlimited' }, pathDepth: { mode: 'unlimited' } }, resources: { transferManifestBytes: { mode: 'limited', value: 1024 } } } } } as unknown as import('./api').State
  expect(exchangeBudgets(state)).toMatchObject({ pathBytes: 1024, pathDepth: 512 })
  state.limits!.effective.logical!.pathDepth = { mode: 'limited', value: 3 }
  expect(exchangeBudgets(state)).toMatchObject({ pathBytes: 1024, pathDepth: 3 })
})
