import { afterEach, describe, expect, it, vi } from 'vitest'
import { bootstrapLocalAccess } from './ui-bootstrap'
const code = 'A'.repeat(32)
function browser(hash = `#handoff=${code}`, extra: Partial<Record<"hostname" | "protocol" | "search" | "pathname" | "port", string>> = {}) {
  const location = { hash, pathname: '/', search: '', protocol: 'http:', hostname: '127.0.0.1', port: '12345', ...extra }
  const replaceState = vi.fn(() => { location.hash = '' })
  vi.stubGlobal('window', { location, history: { state: null, replaceState } })
  const fetcher = vi.fn().mockResolvedValue({ ok: true })
  vi.stubGlobal('fetch', fetcher)
  return { location, replaceState, fetcher }
}
afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers() })
describe('local UI bootstrap', () => {
  it('erases the fragment before the only exchange and awaits it', async () => {
    const { location, replaceState, fetcher } = browser()
    let complete!: (value: unknown) => void
    fetcher.mockImplementation(() => {
      expect(location.hash).toBe('')
      return new Promise(resolve => { complete = resolve })
    })
    let settled = false
    const operation = bootstrapLocalAccess().then(() => { settled = true })
    await bootstrapLocalAccess()
    expect(fetcher).toHaveBeenCalledTimes(1)
    expect(settled).toBe(false)
    expect(replaceState).toHaveBeenCalledWith(null, '', '/')
    expect(fetcher).toHaveBeenCalledWith('/api/session', expect.objectContaining({
      method: 'POST', credentials: 'same-origin', redirect: 'error', cache: 'no-store', body: JSON.stringify({ code }),
    }))
    complete({ ok: true }); await operation
    expect(settled).toBe(true)
  })
  it.each(['', '#handoff=', '#handoff=bad', `#handoff=${code}&handoff=${code}`, `#handoff=${code}&next=https://example.invalid`, '#unrelated', `#handoff=${code}%0A`])('rejects malformed or absent fragments', async hash => {
    const { fetcher, location } = browser(hash)
    await bootstrapLocalAccess()
    expect(fetcher).not.toHaveBeenCalled()
    expect(location.hash).toBe('')
  })
  it.each([{ hostname: 'localhost' }, { hostname: '192.0.2.1' }, { protocol: 'https:' }, { search: '?code=bad' }, { pathname: '/other' }, { port: '' }])('rejects unexpected address', async extra => {
    const { fetcher, location } = browser(undefined, extra)
    await bootstrapLocalAccess()
    expect(fetcher).not.toHaveBeenCalled()
    expect(location.hash).toBe('')
  })
  it('does not exchange if history cleanup fails', async () => {
    const { replaceState, fetcher } = browser()
    replaceState.mockImplementation(() => { throw new Error('history disabled') })
    await bootstrapLocalAccess()
    expect(fetcher).not.toHaveBeenCalled()
  })
  it.each([401, 409, 429, 500])('does not replay HTTP %s', async status => {
    const { fetcher } = browser()
    fetcher.mockResolvedValue({ ok: false, status })
    await bootstrapLocalAccess(); await bootstrapLocalAccess()
    expect(fetcher).toHaveBeenCalledTimes(1)
  })
  it('does not log or retry uncertain failures', async () => {
    const { fetcher } = browser()
    fetcher.mockRejectedValue(new Error(`private ${code}`))
    const log = vi.spyOn(console, 'error')
    await bootstrapLocalAccess(); await bootstrapLocalAccess()
    expect(fetcher).toHaveBeenCalledTimes(1)
    expect(log).not.toHaveBeenCalled()
  })
  it('aborts a stalled exchange', async () => {
    vi.useFakeTimers()
    const { fetcher } = browser()
    fetcher.mockImplementation((_url, options) => new Promise((_resolve, reject) => {
      options.signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')))
    }))
    const operation = bootstrapLocalAccess()
    await vi.advanceTimersByTimeAsync(10_000); await operation
    expect(fetcher.mock.calls[0][1].signal.aborted).toBe(true)
  })
})
