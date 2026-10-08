import { afterEach, describe, expect, it, vi } from 'vitest'
import { createUpgradeHandoff } from './api'
import { openUpgradeHandoff, validHandoffDescriptor } from './upgrade-handoff'
import type { UpgradeReview } from './direct-lan-upgrade'
vi.mock('./api', () => ({ createUpgradeHandoff: vi.fn() }))
afterEach(() => { vi.restoreAllMocks(); vi.clearAllMocks(); vi.useRealTimers() })
const review = (): UpgradeReview => ({ peerId: 'synthetic-peer', revision: 'synthetic-revision', deadline: new Date(Date.now() + 60000).toISOString(), localEndpoint: '192.0.2.1:1234', peerEndpoint: '192.0.2.2:1234', scope: { family: 'ipv4', prefixes: ['192.0.2.0/24'] }, restartRequired: true, resumePreparation: false })
const descriptor = (r: UpgradeReview) => ({ url: 'http://127.0.0.1:54321', token: 'a'.repeat(64), deadline: r.deadline })
function popupMock() {
  const popup = { closed: false, close: vi.fn(), postMessage: vi.fn(), location: { replace: vi.fn() } }
  vi.spyOn(window, 'open').mockReturnValue(popup as unknown as Window)
  return popup
}
async function drain() { await Promise.resolve(); await Promise.resolve() }
describe('private Web restart handoff', () => {
  it('accepts only a bare exact numeric loopback origin, token and original deadline', () => {
    const r = review(), d = descriptor(r)
    expect(validHandoffDescriptor(d, r.deadline)).toBe(true)
    for (const url of ['https://127.0.0.1:54321', 'http://localhost:54321', 'http://127.0.0.1:54321/?code=secret', 'http://127.0.0.1:54321/#secret', 'http://127.0.0.1:54321/path', 'http://user@127.0.0.1:54321', 'http://192.0.2.1:54321']) expect(validHandoffDescriptor({ ...d, url }, r.deadline)).toBe(false)
    expect(validHandoffDescriptor({ ...d, token: 'short' }, r.deadline)).toBe(false)
    expect(validHandoffDescriptor({ ...d, deadline: 'different' }, r.deadline)).toBe(false)
  })
  it('does not request a helper when the popup is blocked', () => {
    vi.spyOn(window, 'open').mockReturnValue(null)
    const failed = vi.fn(); openUpgradeHandoff(review(), 'en', failed)
    expect(failed).toHaveBeenCalledOnce(); expect(createUpgradeHandoff).not.toHaveBeenCalled()
  })
  it('transfers once only to the exact popup source and origin without URL credentials', async () => {
    const popup = popupMock(), r = review(), d = descriptor(r)
    vi.mocked(createUpgradeHandoff).mockResolvedValue(d)
    const cleanup = openUpgradeHandoff(r, 'ja', vi.fn()); await drain()
    expect(createUpgradeHandoff).toHaveBeenCalledWith({ peerId: r.peerId, deadline: r.deadline, expectedRevision: r.revision }, 'ja', expect.any(AbortSignal))
    expect(popup.location.replace).toHaveBeenCalledWith(d.url)
    const message = (source: unknown, origin: string) => window.dispatchEvent(new MessageEvent('message', { source: source as Window, origin, data: { kind: 'sobalink-upgrade-ready' } }))
    message(window, d.url); message(popup, 'https://example.invalid'); expect(popup.postMessage).not.toHaveBeenCalled()
    message(popup, d.url); message(popup, d.url)
    expect(popup.postMessage).toHaveBeenCalledExactlyOnceWith({ kind: 'sobalink-upgrade-transfer', token: d.token }, d.url)
    cleanup(); expect(popup.close).not.toHaveBeenCalled()
  })

  it('recovers after the transferred popup declines without reusing its secret or retrying', async () => {
    const popup = popupMock(), r = review(), d = descriptor(r)
    vi.mocked(createUpgradeHandoff).mockResolvedValue(d)
    const failed = vi.fn(), cleanup = openUpgradeHandoff(r, 'en', failed); await drain()
    window.dispatchEvent(new MessageEvent('message', { source: popup as unknown as Window, origin: d.url, data: { kind: 'sobalink-upgrade-ready' } }))
    window.dispatchEvent(new MessageEvent('message', { source: window, origin: d.url, data: { kind: 'sobalink-upgrade-ended' } }))
    expect(failed).not.toHaveBeenCalled()
    window.dispatchEvent(new MessageEvent('message', { source: popup as unknown as Window, origin: d.url, data: { kind: 'sobalink-upgrade-ended' } }))
    expect(failed).toHaveBeenCalledOnce(); expect(createUpgradeHandoff).toHaveBeenCalledOnce(); expect(popup.postMessage).toHaveBeenCalledOnce()
    cleanup()
  })
  it('dismissal invalidates a delayed response and closes the unclaimed window', async () => {
    const popup = popupMock(), r = review(); let resolve!: (value: ReturnType<typeof descriptor>) => void
    vi.mocked(createUpgradeHandoff).mockImplementation(() => new Promise(done => { resolve = done }))
    const failed = vi.fn(), cleanup = openUpgradeHandoff(r, 'en', failed)
    cleanup(); resolve(descriptor(r)); await drain()
    expect(popup.close).toHaveBeenCalledOnce(); expect(popup.location.replace).not.toHaveBeenCalled(); expect(popup.postMessage).not.toHaveBeenCalled(); expect(failed).not.toHaveBeenCalled()
  })
  it('expired or closed pending windows never receive a capability', async () => {
    vi.useFakeTimers(); const popup = popupMock(), r = review(); vi.mocked(createUpgradeHandoff).mockResolvedValue(descriptor(r))
    const failed = vi.fn(); openUpgradeHandoff(r, 'en', failed); await drain(); popup.closed = true; vi.advanceTimersByTime(500)
    expect(popup.close).toHaveBeenCalledOnce(); expect(failed).toHaveBeenCalledOnce(); expect(popup.postMessage).not.toHaveBeenCalled()
  })
})
