import { crc32 } from 'node:zlib'
import { describe, expect, it } from 'vitest'
import { pngRasterInput } from './png-raster-input'
import { preflightStaticPng } from './png-preflight'
import { pngPolicy, preflightBudget } from './png-policy'
const join = (...parts: Uint8Array[]) => { const result = new Uint8Array(parts.reduce((sum, p) => sum + p.length, 0)); let at = 0; for (const part of parts) { result.set(part, at); at += part.length } return result }
const chunk = (type: string, data = new Uint8Array()) => { const result = new Uint8Array(data.length + 12), view = new DataView(result.buffer); view.setUint32(0, data.length); result.set(Array.from(type, c => c.charCodeAt(0)), 4); result.set(data, 8); view.setUint32(data.length + 8, crc32(result.subarray(4, data.length + 8))); return result }
const header = (color = 6) => { const bytes = new Uint8Array(13), view = new DataView(bytes.buffer); view.setUint32(0, 1); view.setUint32(4, 1); bytes.set([8, color, 0, 0, 0], 8); return chunk('IHDR', bytes) }
const signature = Uint8Array.of(137, 80, 78, 71, 13, 10, 26, 10)
const png = (...chunks: Uint8Array[]) => join(signature, ...chunks, chunk('IEND'))
const idat = chunk('IDAT', Uint8Array.of(0))
const admitted = (bytes: Uint8Array) => { expect(preflightStaticPng(bytes, preflightBudget(pngPolicy()!, bytes.length), () => 0).ok).toBe(true); return pngRasterInput(bytes) }
describe('native PNG raster input metadata isolation', () => {
  it('strips compressed profile/text, EXIF orientation and unknown ancillary bytes without inflating them', () => {
    const bytes = png(header(), ...['iCCP', 'zTXt', 'iTXt', 'eXIf', 'gAMA', 'vpAg'].map(type => chunk(type, Uint8Array.of(255))), idat)
    expect(admitted(bytes)).toEqual(png(header(), idat))
  })
  it('keeps indexed transparency and palette as pixel data', () => {
    const bytes = png(header(3), chunk('PLTE', Uint8Array.of(0, 0, 0, 255, 255, 255)), chunk('tRNS', Uint8Array.of(0, 255)), idat)
    expect(admitted(bytes)).toEqual(bytes)
  })
  it.each([
    [header(), chunk('tRNS', Uint8Array.of(0)), idat],
    [header(0), chunk('tRNS', Uint8Array.of(1, 0)), idat],
    [header(2), chunk('tRNS', new Uint8Array(2)), idat],
    [header(2), chunk('tRNS', new Uint8Array(6)), chunk('PLTE', Uint8Array.of(0, 0, 0)), idat],
    [header(0), idat, chunk('tRNS', Uint8Array.of(0, 0))],
    [header(0), chunk('tRNS', Uint8Array.of(0, 0)), chunk('tRNS', Uint8Array.of(0, 0)), idat],
  ])('rejects invalid transparency representation', (...chunks) => expect(admitted(png(...chunks))).toBeUndefined())
  it('handles many zero-sized IDAT chunks without a retained chunk-object collection', () => {
    const bytes = png(header(), ...Array.from({ length: 10000 }, () => chunk('IDAT')), idat)
    expect(admitted(bytes)).toEqual(bytes)
  })
})
