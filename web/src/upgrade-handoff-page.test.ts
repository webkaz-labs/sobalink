import { afterEach, describe, expect, it, vi } from 'vitest'

// Execute the actual embedded page script against inert objects and fake fetch.
// No jsdom navigation, browser, server, process, sign-in or sockets are used.
const fileSystemModule = 'node:fs'
const { readFileSync } = await import(fileSystemModule) as { readFileSync(path: string, encoding: 'utf8'): string }
const goSource = readFileSync('../cmd/soba/upgrade_handoff_page.go', 'utf8')
const script = goSource.split('const handoffJS = `')[1]?.split('`')[0]
if (!script) throw new Error('actual handoff script missing')
afterEach(() => { vi.clearAllTimers(); vi.useRealTimers() })
function fixture(locale: 'en' | 'ja' = 'en') {
  vi.useFakeTimers()
  const parent = { postMessage: vi.fn() }, listeners = new Map<string, (event: unknown) => unknown>()
  const elements = new Map<string, { hidden: boolean; disabled: boolean; textContent: string; value: string; onclick?: () => unknown; select: () => void }>()
  for (const id of ['title', 'message', 'review', 'continue', 'cancel', 'management', 'open', 'code']) elements.set(id, { hidden: true, disabled: false, textContent: '', value: '', select: vi.fn() })
  const document = { getElementById: (id: string) => elements.get(id), documentElement: { lang: '' } }
  const window = { opener: parent as unknown, addEventListener: (name: string, callback: (event: unknown) => unknown) => listeners.set(name, callback) }
  const calls: { path: string; options: { headers: Record<string, string> } }[] = []
  const review = { peerId: 'synthetic-peer', revision: 'synthetic-review', deadline: new Date(Date.now() + 60000).toISOString(), localEndpoint: '192.0.2.1:1234', peerEndpoint: '192.0.2.2:1234', scope: { family: 'ipv4', prefixes: ['192.0.2.0/24'] } }
  const results: Record<string, unknown> = { '/claim': { token: 'b'.repeat(64), review, locale }, '/commit': { state: 'acknowledgement' }, '/ack': { state: 'restarting' }, '/status': { state: 'ready', url: 'http://127.0.0.1:54321', code: 'synthetic-private-code' }, '/pulse': { state: 'ready' }, '/open': { state: 'open-requested' }, '/cancel': { state: 'cancelled' } }
  const fetch = vi.fn(async (path: string, options: { headers: Record<string, string> }) => { calls.push({ path, options }); return { ok: results[path] !== false, json: async () => results[path] } })
  new Function('window', 'document', 'fetch', 'setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'URL', 'AbortSignal', 'previousOrigin', script)(window, document, fetch, setTimeout, clearTimeout, setInterval, clearInterval, URL, { timeout: () => ({}) }, 'http://127.0.0.1:12345')
  const transfer = (source: unknown = parent, origin = 'http://127.0.0.1:12345') => listeners.get('message')?.({ source, origin, data: { kind: 'sobalink-upgrade-transfer', token: 'a'.repeat(64) } })
  const click = (id: string) => elements.get(id)?.onclick?.()
  return { parent, listeners, elements, document, window, calls, review, results, fetch, transfer, click }
}
async function drain() { for (let i = 0; i < 8; i++) await Promise.resolve() }
describe('actual temporary restart page script', () => {
  it.each(['en', 'ja'] as const)('requires exact sender and explicit confirmation, keeps the full review in %s', async locale => {
    const f = fixture(locale)
    await f.transfer({}, 'http://127.0.0.1:12345'); await f.transfer(f.parent, 'https://example.invalid')
    expect(f.fetch).not.toHaveBeenCalled()
    await f.transfer(); await f.transfer()
    expect(f.calls.map(call => call.path)).toEqual(['/claim'])
    expect(f.document.documentElement.lang).toBe(locale)
    expect(f.elements.get('review')?.textContent).toContain(f.review.peerEndpoint)
    expect(f.elements.get('review')?.textContent).toContain(f.review.deadline)
    expect(f.elements.get('continue')?.hidden).toBe(false)
    const first = f.click('continue'); await f.click('continue'); await first; await drain()
    expect(f.calls.filter(call => call.path === '/commit')).toHaveLength(1)
    expect(f.calls.filter(call => call.path === '/ack')).toHaveLength(1)
    expect(f.calls.find(call => call.path === '/ack')?.options.headers['X-Handoff-Token']).toBe('b'.repeat(64))
    expect(f.elements.get('code')?.textContent).toContain('synthetic-private-code')
    expect(f.elements.get('management')?.value).toBe('http://127.0.0.1:54321')
    expect(f.calls.filter(call => call.path === '/open')).toHaveLength(0)
  })
  it('does not acknowledge or start after an uncertain confirmation reply', async () => {
    const f = fixture(); await f.transfer(); f.results['/commit'] = false; await f.click('continue'); await drain()
    expect(f.calls.some(call => call.path === '/ack')).toBe(false)
    expect(f.calls.some(call => call.path === '/cancel')).toBe(true)
    expect(f.elements.get('continue')?.hidden).toBe(true)
  })
  it('page dismissal cancels, clears code and never resumes on Back/Forward', async () => {
    const f = fixture(); await f.transfer(); await f.click('continue'); await drain()
    f.listeners.get('pagehide')?.({}); f.listeners.get('pageshow')?.({ persisted: true }); await drain()
    expect(f.elements.get('code')?.textContent).toBe('')
    expect(f.elements.get('management')?.value).toBe('')
    expect(f.elements.get('continue')?.hidden).toBe(true)
    const count = f.calls.length; await vi.advanceTimersByTimeAsync(2000)
    expect(f.calls).toHaveLength(count)
  })
  it('uses the original deadline and cancels after a lost page lease response', async () => {
    const f = fixture(); await f.transfer(); f.results['/pulse'] = false
    await vi.advanceTimersByTimeAsync(1000)
    expect(f.calls.some(call => call.path === '/cancel')).toBe(true)
    expect(f.calls.some(call => call.path === '/commit')).toBe(false)
    expect(f.elements.get('continue')?.hidden).toBe(true)
  })
  it('opens via an explicit one-use local action, preserving address-bar fallback', async () => {
    const f = fixture(); await f.transfer(); await f.click('continue'); await drain()
    f.results['/open'] = { state: 'manual-open-required' }
    await f.click('open'); await f.click('open')
    expect(f.calls.filter(call => call.path === '/open')).toHaveLength(1)
    expect(f.elements.get('message')?.textContent).toContain('address bar')
    expect(f.elements.get('management')?.value).toBe('http://127.0.0.1:54321')
    expect(f.elements.get('code')?.textContent).toContain('synthetic-private-code')
    expect(goSource).not.toContain('<a id="management"')
  })
  it('expiry removes the private result without automatically starting again', async () => {
    const f = fixture(); await f.transfer(); await vi.advanceTimersByTimeAsync(60000)
    expect(f.calls.some(call => call.path === '/commit')).toBe(false)
    expect(f.elements.get('code')?.textContent).toBe('')
    expect(f.elements.get('continue')?.hidden).toBe(true)
    expect(f.calls.some(call => call.path === '/cancel')).toBe(true)
  })
})
