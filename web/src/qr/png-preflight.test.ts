import { crc32 } from 'node:zlib'
import { describe, expect, it } from 'vitest'
import { preflightStaticPng, type PngPreflightBudget, type PngPreflightError } from './png-preflight'

const signature = Uint8Array.of(137, 80, 78, 71, 13, 10, 26, 10)
// Synthetic test allowances, not proposed product defaults.
const budget: PngPreflightBudget = { maxEncodedBytes: 100_000, maxPixels: 10_000, maxWorkingBytes: 1_000_000, deadlineMs: 100, fixedDecoderBytes: 1000, decoderScratchBytes: 500 }
const join = (...parts: Uint8Array[]) => {
  const result = new Uint8Array(parts.reduce((sum, part) => sum + part.length, 0))
  let offset = 0
  for (const part of parts) { result.set(part, offset); offset += part.length }
  return result
}
function chunk(type: string, data = new Uint8Array()) {
  const result = new Uint8Array(data.length + 12), view = new DataView(result.buffer)
  view.setUint32(0, data.length)
  result.set(Array.from(type, character => character.charCodeAt(0)), 4)
  result.set(data, 8)
  // An independent platform CRC implementation builds the fixtures.
  view.setUint32(data.length + 8, crc32(result.subarray(4, data.length + 8)))
  return result
}
function header(width = 3, height = 2, bitDepth = 8, colorType = 6, interlace = 0, compression = 0, filter = 0) {
  const data = new Uint8Array(13), view = new DataView(data.buffer)
  view.setUint32(0, width); view.setUint32(4, height)
  data.set([bitDepth, colorType, compression, filter, interlace], 8)
  return chunk('IHDR', data)
}
const idat = (data = Uint8Array.of(0)) => chunk('IDAT', data)
const iend = () => chunk('IEND')
const png = (...chunks: Uint8Array[]) => join(signature, ...chunks)
const sample = () => png(header(), idat(), iend())
const read = (bytes: Uint8Array, limits: Partial<PngPreflightBudget> = {}) => preflightStaticPng(bytes, { ...budget, ...limits }, () => 0)
const reject = (bytes: Uint8Array, error: PngPreflightError) => expect(read(bytes)).toEqual({ ok: false, error })

describe('static PNG structural preflight', () => {
  it('checks a known IEND CRC and reports explicit, accounted structural facts', () => {
    expect(Array.from(iend().slice(-4))).toEqual([0xae, 0x42, 0x60, 0x82])
    const bytes = sample(), result = read(bytes)
    expect(result).toEqual({ ok: true, info: {
      width: 3, height: 2, bitDepth: 8, colorType: 6, interlace: 0, frameCount: 1,
      encodedBytes: bytes.length, encodedStorageBytes: bytes.length, pixels: 6,
      rowBytes: 12, filteredScanlineBytes: 26, rgbaBytes: 24,
      accountedWorkingBytes: bytes.length + 26 + 3 * 24 + 2 * 6 + 1000 + 500,
    } })
  })

  it('does not confuse framed bytes with valid zlib, metadata, rasterization or QR decoding', () => {
    // None of these content bytes form a valid corresponding compressed stream.
    // The boundary deliberately does not interpret or allocate from their data.
    const bytes = png(header(), chunk('iCCP', Uint8Array.of(255)), chunk('zTXt', Uint8Array.of(255)), chunk('eXIf', Uint8Array.of(255)), idat(), iend())
    expect(read(bytes).ok).toBe(true)
  })

  it('accepts all legal color/depth combinations without a QR-version or dimension preset', () => {
    for (const [colorType, depths] of [[0, [1, 2, 4, 8, 16]], [2, [8, 16]], [3, [1, 2, 4, 8]], [4, [8, 16]], [6, [8, 16]]] as const) {
      for (const bitDepth of depths) for (const interlace of [0, 1]) {
        const chunks = [header(7, 9, bitDepth, colorType, interlace)]
        if (colorType === 3) chunks.push(chunk('PLTE', Uint8Array.of(0, 0, 0)))
        expect(read(png(...chunks, idat(), iend())).ok).toBe(true)
      }
    }
    const largeWidth = png(header(100_001, 1), idat(), iend())
    expect(read(largeWidth, { maxPixels: 100_001, maxWorkingBytes: 10_000_000 }).ok).toBe(true)
  })

  it('rejects every truncation and every one-bit corruption of a small complete frame', () => {
    const bytes = sample()
    for (let length = 0; length < bytes.length; length++) expect(read(bytes.slice(0, length)).ok).toBe(false)
    for (let i = 0; i < bytes.length; i++) for (let bit = 0; bit < 8; bit++) {
      const mutated = bytes.slice(); mutated[i] ^= 1 << bit
      expect(read(mutated).ok).toBe(false)
    }
  })

  it('rejects out-of-range or truncated chunk lengths without allocating the claimed bytes', () => {
    for (const length of [1, 13, 0x7fffffff, 0x80000000, 0xffffffff]) {
      const frame = chunk('IHDR'); new DataView(frame.buffer).setUint32(0, length)
      reject(png(frame), 'invalidStructure')
    }
    reject(join(sample(), Uint8Array.of(0)), 'invalidStructure')
    reject(png(header(), idat(), iend(), iend()), 'invalidStructure')
  })

  it.each([
    [0, 1, 8, 6, 0, 0, 0], [1, 0, 8, 6, 0, 0, 0],
    [0x80000000, 1, 8, 6, 0, 0, 0], [1, 0xffffffff, 8, 6, 0, 0, 0],
    [1, 1, 1, 2, 0, 0, 0], [1, 1, 16, 3, 0, 0, 0],
    [1, 1, 8, 1, 0, 0, 0], [1, 1, 8, 5, 0, 0, 0],
    [1, 1, 0, 0, 0, 0, 0], [1, 1, 3, 0, 0, 0, 0],
    [1, 1, 8, 6, 2, 0, 0], [1, 1, 8, 6, 0, 1, 0], [1, 1, 8, 6, 0, 0, 1],
  ])('rejects invalid IHDR fields %j', (width, height, depth, color, interlace, compression, filter) => {
    reject(png(header(width, height, depth, color, interlace, compression, filter), idat(), iend()), 'invalidHeader')
  })

  it('requires first/unique IHDR, contiguous IDAT, and a final empty IEND', () => {
    for (const bytes of [
      png(idat(), header(), iend()), png(chunk('tEXt'), header(), idat(), iend()),
      png(header(), header(), idat(), iend()), png(chunk('IHDR', new Uint8Array(12)), idat(), iend()),
      png(header(), iend()), png(header(), idat(new Uint8Array()), iend()),
      png(header(), idat(), chunk('tEXt'), idat(), iend()), png(header(), idat()),
      png(header(), idat(), chunk('IEND', Uint8Array.of(0))),
    ]) reject(bytes, 'invalidStructure')
    expect(read(png(header(), idat(new Uint8Array()), idat(), idat(new Uint8Array()), chunk('tEXt'), iend())).ok).toBe(true)
  })

  it('bounds chunk names, rejects unknown critical chunks, and checks every ancillary CRC', () => {
    for (const type of ['abct', 'ab1D', 'A!CD', 'ab\u0080D']) reject(png(header(), chunk(type), idat(), iend()), 'invalidStructure')
    reject(png(header(), chunk('ABCD'), idat(), iend()), 'unsupportedCriticalChunk')
    const unknown = chunk('abCD', Uint8Array.of(1, 2, 3))
    expect(read(png(header(), unknown, idat(), iend())).ok).toBe(true)
    unknown[unknown.length - 1] ^= 1
    reject(png(header(), unknown, idat(), iend()), 'invalidCRC')
  })

  it.each(['acTL', 'fcTL', 'fdAT'])('rejects %s rather than scanning an APNG frame', type => {
    for (const content of [new Uint8Array(), Uint8Array.of(0, 0, 0, 1, 0, 0, 0, 0)]) {
      reject(png(header(), chunk(type, content), idat(), iend()), 'animatedPNG')
      reject(png(header(), idat(), chunk(type, content), iend()), 'animatedPNG')
    }
  })

  it('enforces required, forbidden, unique, ordered and depth-bounded palettes', () => {
    const palette = chunk('PLTE', new Uint8Array(6))
    for (const bytes of [
      png(header(1, 1, 1, 3), idat(), iend()),
      png(header(1, 1, 8, 0), palette, idat(), iend()),
      png(header(1, 1, 8, 4), palette, idat(), iend()),
      png(header(), palette, palette, idat(), iend()), png(header(), idat(), palette, iend()),
      png(header(1, 1, 1, 3), chunk('PLTE', new Uint8Array(9)), idat(), iend()),
    ]) reject(bytes, 'invalidPalette')
    for (const size of [0, 1, 2, 4, 769, 771]) reject(png(header(), chunk('PLTE', new Uint8Array(size)), idat(), iend()), 'invalidPalette')
    for (const colorType of [2, 3, 6]) expect(read(png(header(1, 1, 8, colorType), chunk('PLTE', new Uint8Array(768)), idat(), iend())).ok).toBe(true)
    expect(read(png(header(1, 1, 1, 3), palette, idat(), iend())).ok).toBe(true)
  })

  it('differentially verifies CRC framing against Node for deterministic ancillary contents', () => {
    let state = 0x137acdf
    const next = () => (state = (Math.imul(state, 1664525) + 1013904223) >>> 0)
    for (let n = 0; n < 100; n++) {
      const data = Uint8Array.from({ length: 1 + next() % 256 }, () => next() & 255)
      const ancillary = chunk('teST', data)
      expect(read(png(header(), ancillary, idat(), iend())).ok).toBe(true)
      ancillary[8 + next() % data.length] ^= 1
      reject(png(header(), ancillary, idat(), iend()), 'invalidCRC')
    }
  })

  it('traverses many small chunks without collecting or copying their contents', () => {
    const bytes = png(header(), ...Array.from({ length: 1000 }, () => chunk('teST')), idat(), iend())
    const before = bytes.slice()
    expect(read(bytes).ok).toBe(true)
    expect(bytes).toEqual(before)
  })
})

describe('finite admission and cooperative time boundary', () => {
  describe.each(['inherited', 'nonEnumerable', 'getter'] as const)('%s budget properties', form => {
    it.each([
      ['maxEncodedBytes', 'encodedBudget'], ['maxPixels', 'pixelBudget'],
      ['maxWorkingBytes', 'workingBudget'], ['deadlineMs', 'deadlineExceeded'],
    ] as const)('enforces the supplied %s cap', (key, error) => {
      const supplied = { ...budget }
      let reads = 0
      if (form === 'inherited') {
        Reflect.deleteProperty(supplied, key)
        Object.setPrototypeOf(supplied, { [key]: 1 })
      } else if (form === 'nonEnumerable') {
        Object.defineProperty(supplied, key, { enumerable: false, value: 1 })
      } else {
        Object.defineProperty(supplied, key, { enumerable: true, get: () => ++reads === 1 ? 1 : NaN })
      }
      const elapsed = () => key === 'deadlineMs' ? 2 : 0
      expect(preflightStaticPng(sample(), supplied, elapsed)).toEqual({ ok: false, error })
      expect(preflightStaticPng(sample(), { ...budget, [key]: 1 }, elapsed)).toEqual({ ok: false, error })
      if (form === 'getter') expect(reads).toBe(1)
    })
  })

  it('reads exactly the six required values once and ignores arbitrary extra keys', () => {
    const supplied = { ...budget }, reads = { ...budget }
    for (const key of Object.keys(budget) as (keyof PngPreflightBudget)[]) {
      reads[key] = 0
      Object.defineProperty(supplied, key, { get: () => ++reads[key] === 1 ? budget[key] : NaN })
    }
    Object.defineProperty(supplied, 'extra', { enumerable: true, get: () => { throw new Error('must not read extra keys') } })
    expect(preflightStaticPng(sample(), supplied, () => 0).ok).toBe(true)
    expect(Object.values(reads)).toEqual([1, 1, 1, 1, 1, 1])
  })

  it.each(Object.keys(budget) as (keyof PngPreflightBudget)[])('returns invalidBudget when the %s getter throws', key => {
    const supplied = { ...budget }
    Object.defineProperty(supplied, key, { get: () => { throw new Error('private budget context') } })
    expect(preflightStaticPng(sample(), supplied, () => 0)).toEqual({ ok: false, error: 'invalidBudget' })
  })

  it('requires every budget/allowance to be a positive finite safe integer', () => {
    for (const key of Object.keys(budget)) for (const value of [undefined, null, 0, -1, 0.5, Infinity, NaN, Number.MAX_SAFE_INTEGER + 1, '100']) {
      expect(read(sample(), { [key]: value } as Partial<PngPreflightBudget>)).toEqual({ ok: false, error: 'invalidBudget' })
    }
    expect(preflightStaticPng(sample(), null as unknown as PngPreflightBudget, () => 0)).toEqual({ ok: false, error: 'invalidBudget' })
  })

  it('accepts exact encoded/pixel/working limits and rejects a one-byte/pixel shortfall', () => {
    const bytes = sample(), result = read(bytes)
    if (!result.ok) throw new Error('fixture failed')
    const exact = { maxEncodedBytes: bytes.length, maxPixels: 6, maxWorkingBytes: result.info.accountedWorkingBytes }
    expect(read(bytes, exact).ok).toBe(true)
    expect(read(bytes, { ...exact, maxEncodedBytes: bytes.length - 1 })).toEqual({ ok: false, error: 'encodedBudget' })
    expect(read(bytes, { ...exact, maxPixels: 5 })).toEqual({ ok: false, error: 'pixelBudget' })
    expect(read(bytes, { ...exact, maxWorkingBytes: exact.maxWorkingBytes - 1 })).toEqual({ ok: false, error: 'workingBudget' })
  })

  it('charges the full retained backing buffer while parsing only the supplied view', () => {
    const bytes = sample(), backing = new Uint8Array(bytes.length + 100)
    backing.set(bytes, 20)
    const result = read(backing.subarray(20, 20 + bytes.length))
    expect(result).toMatchObject({ ok: true, info: { encodedBytes: bytes.length, encodedStorageBytes: backing.length } })
    const plain = read(bytes)
    if (!plain.ok || !result.ok) throw new Error('fixture failed')
    expect(result.info.accountedWorkingBytes - plain.info.accountedWorkingBytes).toBe(100)
    expect(read(backing.subarray(20, 20 + bytes.length), { maxWorkingBytes: backing.length - 1 })).toEqual({ ok: false, error: 'workingBudget' })
  })

  it('rejects concurrently mutable shared input and invalid byte containers', () => {
    for (const bytes of [new Uint8Array(new SharedArrayBuffer(100)), null, [], new Uint8ClampedArray(100)]) {
      expect(preflightStaticPng(bytes as Uint8Array, budget, () => 0)).toEqual({ ok: false, error: 'invalidInput' })
    }
  })

  it('uses checked arithmetic for dimensions, RGBA and the sum of allowances', () => {
    const high = { maxPixels: Number.MAX_SAFE_INTEGER, maxWorkingBytes: Number.MAX_SAFE_INTEGER }
    expect(read(png(header(0x7fffffff, 0x7fffffff), idat(), iend()), high)).toEqual({ ok: false, error: 'arithmeticOverflow' })
    expect(read(png(header(0x7fffffff, 2_000_000), idat(), iend()), high)).toEqual({ ok: false, error: 'arithmeticOverflow' })
    expect(read(sample(), { ...high, fixedDecoderBytes: Number.MAX_SAFE_INTEGER })).toEqual({ ok: false, error: 'arithmeticOverflow' })
    // A PNG-protocol maximum width alone is legal; no actual raster is allocated.
    expect(read(png(header(0x7fffffff, 1, 1, 0), idat(), iend()), high).ok).toBe(true)
  })

  it('matches independently enumerated Adam7 rows and BigInt arithmetic', () => {
    const passes = [
      [1, 6, 4, 6, 2, 6, 4, 6], [7, 7, 7, 7, 7, 7, 7, 7],
      [5, 6, 5, 6, 5, 6, 5, 6], [7, 7, 7, 7, 7, 7, 7, 7],
      [3, 6, 4, 6, 3, 6, 4, 6], [7, 7, 7, 7, 7, 7, 7, 7],
      [5, 6, 5, 6, 5, 6, 5, 6], [7, 7, 7, 7, 7, 7, 7, 7],
    ]
    for (const depth of [1, 2, 4, 8, 16]) for (let width = 1; width <= 9; width++) for (let height = 1; height <= 9; height++) {
      let scanlines = 0n
      for (let pass = 1; pass <= 7; pass++) for (let y = 0; y < height; y++) {
        let columns = 0n
        for (let x = 0; x < width; x++) if (passes[y % 8][x % 8] === pass) columns++
        if (columns) scanlines += 1n + (columns * BigInt(depth) + 7n) / 8n
      }
      const bytes = png(header(width, height, depth, 0, 1), idat(), iend()), result = read(bytes)
      const pixels = BigInt(width) * BigInt(height)
      expect(result).toMatchObject({ ok: true, info: {
        filteredScanlineBytes: Number(scanlines), rgbaBytes: Number(pixels * 4n),
        accountedWorkingBytes: Number(BigInt(bytes.length + 1500) + scanlines + 14n * pixels),
      } })
    }
  })

  it('uses the existing elapsed job deadline, with a strict result-time recheck', () => {
    expect(preflightStaticPng(sample(), budget, () => 99.9).ok).toBe(true)
    expect(preflightStaticPng(sample(), budget, () => 100)).toEqual({ ok: false, error: 'deadlineExceeded' })
    let count = 0
    expect(preflightStaticPng(sample(), budget, () => { count++; return 0 }).ok).toBe(true)
    let call = 0
    expect(preflightStaticPng(sample(), budget, () => ++call === count ? 100 : 0)).toEqual({ ok: false, error: 'deadlineExceeded' })
  })

  it('checks inside a large CRC and across many small chunks', () => {
    for (const bytes of [
      png(header(), chunk('teST', new Uint8Array(20_000)), idat(), iend()),
      png(header(), ...Array.from({ length: 100 }, () => chunk('teST')), idat(), iend()),
    ]) {
      let calls = 0
      expect(preflightStaticPng(bytes, budget, () => ++calls < 6 ? 0 : 100)).toEqual({ ok: false, error: 'deadlineExceeded' })
      expect(calls).toBe(6)
    }
  })

  it('fails closed on unusable/backwards clocks and snapshots the admitted policy', () => {
    for (const elapsed of [NaN, Infinity, -1]) expect(preflightStaticPng(sample(), budget, () => elapsed)).toEqual({ ok: false, error: 'invalidClock' })
    expect(preflightStaticPng(sample(), budget, () => { throw new Error('private clock context') })).toEqual({ ok: false, error: 'invalidClock' })
    let clock = 2
    expect(preflightStaticPng(sample(), budget, () => clock--)).toEqual({ ok: false, error: 'invalidClock' })
    const changing = { ...budget }
    expect(preflightStaticPng(sample(), changing, () => { changing.maxPixels = 1; return 0 }).ok).toBe(true)
  })
})
