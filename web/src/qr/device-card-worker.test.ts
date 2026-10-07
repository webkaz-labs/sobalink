import { afterEach, describe, expect, it, vi } from 'vitest'
import { crc32 } from 'node:zlib'
import { pngPolicy, READER_WASM_BYTES } from './png-policy'
const decoder = vi.hoisted(() => ({ prepare: vi.fn(), read: vi.fn() }))
vi.mock('../../vendor/zxing-wasm-3.1.5/reader/index.js', () => ({ prepareZXingModule: decoder.prepare, readBarcodes: decoder.read }))
const chunk = (type: string, data = new Uint8Array()) => { const output = new Uint8Array(data.length + 12), view = new DataView(output.buffer); view.setUint32(0, data.length); output.set(Array.from(type, c => c.charCodeAt(0)), 4); output.set(data, 8); view.setUint32(data.length + 8, crc32(output.subarray(4, data.length + 8))); return output }
function image() {
  const header = new Uint8Array(13), view = new DataView(header.buffer); view.setUint32(0, 1); view.setUint32(4, 1); header.set([8, 6], 8)
  const parts = [Uint8Array.of(137, 80, 78, 71, 13, 10, 26, 10), chunk('IHDR', header), chunk('iCCP', Uint8Array.of(255)), chunk('IDAT', Uint8Array.of(0)), chunk('IEND')]
  const bytes = new Uint8Array(parts.reduce((sum, part) => sum + part.length, 0)); let at = 0; for (const part of parts) { bytes.set(part, at); at += part.length } return bytes.buffer
}
afterEach(() => { vi.unstubAllGlobals(); vi.clearAllMocks(); vi.resetModules() })
async function setup() {
  vi.resetModules()
  const post = vi.fn(), close = vi.fn(), draw = vi.fn(), fill = vi.fn(), canvas = { width: 1, height: 1, getContext: () => ({ fillStyle: '', fillRect: fill, drawImage: draw, getImageData: () => ({ width: 1, height: 1, data: new Uint8ClampedArray(4) }) }) }
  vi.stubGlobal('postMessage', post)
  vi.stubGlobal('createImageBitmap', vi.fn(async () => ({ width: 1, height: 1, close })))
  vi.stubGlobal('OffscreenCanvas', class { constructor() { return canvas } })
  decoder.prepare.mockResolvedValue(undefined)
  decoder.read.mockResolvedValue([{ isValid: true, format: 'QRCode', sequenceSize: -1, sequenceIndex: -1, sequenceId: '', text: 'AB', bytes: Uint8Array.of(65, 66) }])
  await import('./device-card-worker')
  const receive = globalThis.onmessage!
  const request = { schema: 1, generation: 3, image: image(), wasm: new ArrayBuffer(READER_WASM_BYTES), budget: pngPolicy()!, remainingMs: 10000 }
  const send = async (value: unknown) => { receive.call(window, new MessageEvent('message', { data: value })); await vi.waitFor(() => expect(post).toHaveBeenCalled()) }
  return { post, close, canvas, draw, fill, request, send }
}
describe('real worker protocol with mocked raster and decoder APIs', () => {
  it('accepts the exact parent envelope, composites white, closes native resources and posts only bounded bytes', async () => {
    const t = await setup(); await t.send(t.request)
    expect(t.post.mock.calls[0][0]).toEqual({ schema: 1, generation: 3, status: 'decoded', bytes: Uint8Array.of(65, 66) })
    expect(createImageBitmap).toHaveBeenCalledWith(expect.any(Blob), { imageOrientation: 'from-image', premultiplyAlpha: 'none', colorSpaceConversion: 'none' });
    expect(t.close).toHaveBeenCalledOnce(); expect(t.canvas.width).toBe(0); expect(t.fill).toHaveBeenCalledBefore(t.draw)
    expect(decoder.prepare.mock.calls[0][0].overrides.wasmBinary).toBe(t.request.wasm)
    expect(decoder.read.mock.calls[0][1]).toMatchObject({ formats: ['QRCode'], maxNumberOfSymbols: 2, isPure: false, tryDownscale: false })
    expect(globalThis.onmessage).toBeNull()
  })
  it('rejects a malformed envelope before native raster or WASM', async () => {
    const t = await setup(); await t.send({ ...t.request, extra: true })
    expect(t.post.mock.calls[0][0]).toMatchObject({ status: 'error', error: 'invalidImage' })
    expect(createImageBitmap).not.toHaveBeenCalled(); expect(decoder.prepare).not.toHaveBeenCalled()
  })
  it('rejects dimension mismatches and releases the bitmap', async () => {
    const t = await setup(); vi.mocked(createImageBitmap).mockResolvedValueOnce({ width: 2, height: 1, close: t.close } as ImageBitmap)
    await t.send(t.request)
    expect(t.post.mock.calls[0][0]).toMatchObject({ error: 'invalidImage' }); expect(t.close).toHaveBeenCalledOnce(); expect(decoder.prepare).not.toHaveBeenCalled()
  })
})
