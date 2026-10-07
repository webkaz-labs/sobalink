/**
 * Static PNG structural admission, not an image decoder or a browser-memory sandbox.
 * Rules: https://www.w3.org/TR/2025/REC-png-3-20250624/
 *
 * IDAT and ancillary contents (including compressed metadata and orientation) are
 * not interpreted. Passing this check does not establish that rasterization is
 * safe or will succeed. A later worker adapter must handle metadata, check actual
 * raster dimensions, enforce a parent-owned deadline by terminating the worker,
 * and release native resources. This module performs no allocation from IHDR.
 */
export interface PngPreflightBudget {
  maxEncodedBytes: number
  maxPixels: number
  maxWorkingBytes: number
  deadlineMs: number
  /** Measured fixed decoder/module allowance, supplied by the future adapter. */
  fixedDecoderBytes: number
  /** Measured scratch/margin, including any additional retained copies. */
  decoderScratchBytes: number
}

export type PngPreflightError = 'invalidBudget' | 'invalidInput' | 'invalidClock'
  | 'deadlineExceeded' | 'encodedBudget' | 'pixelBudget' | 'workingBudget'
  | 'arithmeticOverflow' | 'invalidSignature' | 'invalidStructure' | 'invalidHeader'
  | 'invalidCRC' | 'unsupportedCriticalChunk' | 'animatedPNG' | 'invalidPalette'

export interface PngPreflightInfo {
  width: number
  height: number
  bitDepth: number
  colorType: number
  interlace: 0 | 1
  frameCount: 1
  encodedBytes: number
  encodedStorageBytes: number
  pixels: number
  /** Packed, non-interlaced row size, without its filter byte. */
  rowBytes: number
  /** Exact IHDR-derived scanline size, including Adam7 row/filter overhead. */
  filteredScanlineBytes: number
  rgbaBytes: number
  accountedWorkingBytes: number
}

export type PngPreflightResult = { ok: true; info: PngPreflightInfo }
  | { ok: false; error: PngPreflightError }

const signature = [137, 80, 78, 71, 13, 10, 26, 10]
const pngIntegerMax = 0x7fffffff
const adam7 = [[0, 0, 8, 8], [4, 0, 8, 8], [0, 4, 4, 8], [2, 0, 4, 4], [0, 2, 2, 4], [1, 0, 2, 2], [0, 1, 1, 2]]
const budgetKeys = ['maxEncodedBytes', 'maxPixels', 'maxWorkingBytes', 'deadlineMs', 'fixedDecoderBytes', 'decoderScratchBytes'] as const
const positiveInteger = (value: unknown): value is number => typeof value === 'number' && Number.isSafeInteger(value) && value > 0

class Failure extends Error {
  constructor(readonly code: PngPreflightError) { super(code) }
}
function fail(code: PngPreflightError): never { throw new Failure(code) }
function checked(value: number): number {
  if (!Number.isSafeInteger(value) || value < 0) fail('arithmeticOverflow')
  return value
}
const add = (a: number, b: number) => checked(a + b)
const multiply = (a: number, b: number) => checked(a * b)

function headerInfo(view: DataView, offset: number, encodedBytes: number, encodedStorageBytes: number, budget: PngPreflightBudget): PngPreflightInfo {
  const width = view.getUint32(offset), height = view.getUint32(offset + 4)
  const bitDepth = view.getUint8(offset + 8), colorType = view.getUint8(offset + 9), interlace = view.getUint8(offset + 12)
  const depths: Record<number, readonly number[]> = { 0: [1, 2, 4, 8, 16], 2: [8, 16], 3: [1, 2, 4, 8], 4: [8, 16], 6: [8, 16] }
  if (!width || !height || width > pngIntegerMax || height > pngIntegerMax || !depths[colorType]?.includes(bitDepth)
    || view.getUint8(offset + 10) !== 0 || view.getUint8(offset + 11) !== 0 || (interlace !== 0 && interlace !== 1)) fail('invalidHeader')
  const pixels = multiply(width, height)
  if (pixels > budget.maxPixels) fail('pixelBudget')
  const channels = colorType === 2 ? 3 : colorType === 4 ? 2 : colorType === 6 ? 4 : 1
  const bitsPerPixel = channels * bitDepth
  const packedRow = (columns: number) => Math.ceil(multiply(columns, bitsPerPixel) / 8)
  const rowBytes = packedRow(width), rgbaBytes = multiply(pixels, 4)
  let filteredScanlineBytes = 0
  for (const [x, y, dx, dy] of interlace ? adam7 : [[0, 0, 1, 1]]) {
    const columns = Math.max(0, Math.ceil((width - x) / dx)), rows = Math.max(0, Math.ceil((height - y) / dy))
    if (columns && rows) filteredScanlineBytes = add(filteredScanlineBytes, multiply(add(packedRow(columns), 1), rows))
  }
  // Charge the entire input backing buffer (not just a subarray), packed scanlines,
  // bitmap + canvas + RGBA (3 * 4 bytes/pixel), wrapper luminance + WASM input
  // (2 * 1 byte/pixel), and caller-supplied fixed module/scratch allowances.
  // This estimate neither constrains WASM memory growth nor native/browser heaps.
  const accountedWorkingBytes = [encodedStorageBytes, filteredScanlineBytes, multiply(rgbaBytes, 3), multiply(pixels, 2), budget.fixedDecoderBytes, budget.decoderScratchBytes].reduce(add, 0)
  if (accountedWorkingBytes > budget.maxWorkingBytes) fail('workingBudget')
  return { width, height, bitDepth, colorType, interlace, frameCount: 1, encodedBytes, encodedStorageBytes, pixels, rowBytes, filteredScanlineBytes, rgbaBytes, accountedWorkingBytes }
}

/**
 * All budgets/allowances must be supplied as positive finite safe integers; there
 * are no product defaults here. elapsedMs is the trusted monotonic elapsed time
 * since job admission, not a fresh clock starting at preflight. It is checked at
 * chunk boundaries and at most every 4096 CRC bytes (a checkpoint cadence, not an
 * image limit). Synchronous work only cooperates with cancellation/deadlines;
 * the future parent/worker adapter must provide actual termination and reject
 * late results. Callers must keep the input unchanged until this function returns.
 * Required budget properties are read once, including inherited/non-enumerable
 * properties. A throwing getter returns invalidBudget; extra keys are not read.
 */
export function preflightStaticPng(bytes: Uint8Array, budget: PngPreflightBudget, elapsedMs: () => number): PngPreflightResult {
  try {
    if (!budget) fail('invalidBudget')
    // Validate the exact snapshot used for admission, not an earlier property read.
    let limits: PngPreflightBudget
    try {
      limits = {
        maxEncodedBytes: budget.maxEncodedBytes,
        maxPixels: budget.maxPixels,
        maxWorkingBytes: budget.maxWorkingBytes,
        deadlineMs: budget.deadlineMs,
        fixedDecoderBytes: budget.fixedDecoderBytes,
        decoderScratchBytes: budget.decoderScratchBytes,
      }
    } catch { fail('invalidBudget') }
    if (!budgetKeys.every(key => positiveInteger(limits[key]))) fail('invalidBudget')
    if (!(bytes instanceof Uint8Array) || !(bytes.buffer instanceof ArrayBuffer)) fail('invalidInput')
    if (bytes.byteLength > limits.maxEncodedBytes) fail('encodedBudget')
    if (bytes.buffer.byteLength > limits.maxWorkingBytes) fail('workingBudget')
    let lastElapsed = 0
    const checkpoint = () => {
      let elapsed: number
      try { elapsed = elapsedMs() } catch { fail('invalidClock') }
      if (!Number.isFinite(elapsed) || elapsed < lastElapsed) fail('invalidClock')
      if (elapsed >= limits.deadlineMs) fail('deadlineExceeded')
      lastElapsed = elapsed
    }
    checkpoint()
    if (bytes.byteLength < signature.length || !signature.every((byte, i) => bytes[i] === byte)) fail('invalidSignature')
    const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength)
    let offset = signature.length, info: PngPreflightInfo | undefined
    let palette = false, imageData = false, imageDataEnded = false, imageDataBytes = 0
    while (offset < bytes.byteLength) {
      checkpoint()
      const remaining = bytes.byteLength - offset
      if (remaining < 12) fail('invalidStructure')
      const length = view.getUint32(offset)
      // Subtraction bounds length before computing chunk/data/CRC end offsets.
      if (length > pngIntegerMax || length > remaining - 12) fail('invalidStructure')
      const typeStart = offset + 4, dataStart = offset + 8, crcStart = dataStart + length, end = crcStart + 4
      for (let i = typeStart; i < dataStart; i++) {
        const byte = bytes[i]
        if (!(byte >= 65 && byte <= 90 || byte >= 97 && byte <= 122)) fail('invalidStructure')
      }
      if (bytes[typeStart + 2] & 32) fail('invalidStructure')
      const type = String.fromCharCode(bytes[typeStart], bytes[typeStart + 1], bytes[typeStart + 2], bytes[typeStart + 3])
      if (!info && type !== 'IHDR') fail('invalidStructure')
      if (type === 'IHDR' && (info || length !== 13)) fail('invalidStructure')
      let crc = 0xffffffff
      for (let start = typeStart; start < crcStart; start += 4096) {
        checkpoint()
        const blockEnd = Math.min(crcStart, start + 4096)
        for (let i = start; i < blockEnd; i++) {
          crc ^= bytes[i]
          for (let bit = 0; bit < 8; bit++) crc = crc >>> 1 ^ (crc & 1 ? 0xedb88320 : 0)
        }
      }
      if ((crc ^ 0xffffffff) >>> 0 !== view.getUint32(crcStart)) fail('invalidCRC')
      if (type === 'acTL' || type === 'fcTL' || type === 'fdAT') fail('animatedPNG')
      if (imageData && type !== 'IDAT') imageDataEnded = true
      if (type === 'IHDR') {
        info = headerInfo(view, dataStart, bytes.byteLength, bytes.buffer.byteLength, limits)
      } else if (type === 'PLTE') {
        if (!info || palette || imageData || info.colorType === 0 || info.colorType === 4
          || !length || length % 3 !== 0 || length > 768 || info.colorType === 3 && length / 3 > 2 ** info.bitDepth) fail('invalidPalette')
        palette = true
      } else if (type === 'IDAT') {
        if (!info || imageDataEnded) fail('invalidStructure')
        if (info.colorType === 3 && !palette) fail('invalidPalette')
        imageData = true
        imageDataBytes = add(imageDataBytes, length)
      } else if (type === 'IEND') {
        if (!info || length || !imageData || !imageDataBytes || end !== bytes.byteLength) fail('invalidStructure')
        checkpoint()
        return { ok: true, info }
      } else if (!(bytes[typeStart] & 32)) {
        fail('unsupportedCriticalChunk')
      }
      offset = end
    }
    fail('invalidStructure')
  } catch (error) {
    if (error instanceof Failure) return { ok: false, error: error.code }
    throw error
  }
}
