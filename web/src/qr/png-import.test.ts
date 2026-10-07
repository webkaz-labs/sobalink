import { readFileSync } from 'node:fs'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { PngCardImporter } from './png-import'
import { pngPolicy, READER_WASM_BYTES } from './png-policy'
const wasm = Uint8Array.from(readFileSync('vendor/zxing-wasm-3.1.5/reader/zxing_reader.wasm'))
const workers: FakeWorker[] = []
class FakeWorker {
  onmessage: ((event: { data: unknown }) => void) | null = null
  onerror: (() => void) | null = null
  constructor(readonly url: URL, readonly options: unknown) { workers.push(this) }
  terminate = vi.fn()
  postMessage = vi.fn()
}
function response(chunks: Uint8Array[], length: string | null = String(READER_WASM_BYTES), mime = 'application/wasm') {
  const cancel = vi.fn(async () => {}), releaseLock = vi.fn(), list = chunks.slice()
  return { cancel, response: { ok: true, headers: { get: (key: string) => key === 'content-type' ? mime : length }, body: { getReader: () => ({ read: async () => list.length ? { value: list.shift()!, done: false } : { done: true }, cancel, releaseLock }) } } }
}
afterEach(() => { vi.unstubAllGlobals(); workers.length = 0 })
describe('PNG parent verifies bounded same-origin reader asset before worker creation', () => {
  it('checks the full pinned binary then transfers data only to a local module worker', async () => {
    vi.stubGlobal('Worker', FakeWorker)
    const stream = response([wasm.subarray(0, 100), wasm.subarray(100)])
    const fetch = vi.fn(async (_input: unknown, _init?: RequestInit) => stream.response); vi.stubGlobal('fetch', fetch)
    const importer = new PngCardImporter(), result = importer.start(new File(['synthetic PNG'], 'card.png'), 'lan', pngPolicy()!)
    await vi.waitFor(() => expect(workers).toHaveLength(1))
    expect(new URL(workers[0].url).origin).toBe(location.origin)
    expect(workers[0].options).toEqual({ type: 'module', name: 'device-card-png' })
    expect(fetch.mock.calls[0][1]).toMatchObject({ credentials: 'omit', redirect: 'error', cache: 'no-store' })
    const request = workers[0].postMessage.mock.calls[0][0]
    expect(request.wasm.byteLength).toBe(READER_WASM_BYTES)
    expect(request.budget).toEqual(pngPolicy())
    importer.cancel(); expect(await result).toEqual({ ok: false, error: 'cancelled' }); expect(workers[0].terminate).toHaveBeenCalledOnce(); expect(stream.cancel).toHaveBeenCalled()
  })
  it.each(['length', 'overflow', 'truncated', 'hash', 'mime'] as const)('rejects %s failure without creating a decoder', async failure => {
    vi.stubGlobal('Worker', FakeWorker)
    const changed = wasm.slice(); changed[0] ^= 1
    const stream = failure === 'length' ? response([wasm], '1') : failure === 'overflow' ? response([wasm, Uint8Array.of(0)], null)
      : failure === 'truncated' ? response([wasm.subarray(0, 100)], null) : failure === 'hash' ? response([changed]) : response([wasm], String(wasm.length), 'text/plain')
    vi.stubGlobal('fetch', vi.fn(async () => stream.response))
    const importer = new PngCardImporter()
    expect((await importer.start(new File(['synthetic PNG'], 'card.png'), 'lan', pngPolicy()!)).ok).toBe(false)
    expect(workers).toHaveLength(0)
    importer.dispose()
  })
  it('rejects a working budget below known preparation cost before file/asset I/O', async () => {
    const fetch = vi.fn(); vi.stubGlobal('fetch', fetch)
    const read = vi.spyOn(FileReader.prototype, 'readAsArrayBuffer')
    const importer = new PngCardImporter()
    expect(await importer.start(new File(['synthetic PNG'], 'card.png'), 'lan', { ...pngPolicy()!, maxWorkingBytes: 1 })).toEqual({ ok: false, error: 'imageBudget' })
    expect(fetch).not.toHaveBeenCalled(); expect(read).not.toHaveBeenCalled()
  })
  it('enforces File.size before reading the file or fetching assets', async () => {
    const fetch = vi.fn(); vi.stubGlobal('fetch', fetch)
    const importer = new PngCardImporter()
    expect(await importer.start(new File(['xx'], 'card.png'), 'lan', { ...pngPolicy()!, maxEncodedBytes: 1 })).toEqual({ ok: false, error: 'imageBudget' })
    expect(fetch).not.toHaveBeenCalled()
  })
})
