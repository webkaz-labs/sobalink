import { prepareZXingModule, readBarcodes } from '../../vendor/zxing-wasm-3.1.5/reader/index.js'
import { preflightStaticPng } from './png-preflight'
import { preflightBudget, READER_WASM_BYTES, validPngPolicy } from './png-policy'
import { pngRasterInput } from './png-raster-input'
import { readerCardBytes } from './reader-result'

type Scope = { onmessage: ((event: MessageEvent<unknown>) => void) | null; postMessage: (message: unknown, transfer?: Transferable[]) => void }
const scope = globalThis as unknown as Scope
let admitted = false
scope.onmessage = event => {
  if (admitted) return
  admitted = true
  scope.onmessage = null
  void decode(event.data)
}
async function decode(data: unknown) {
  if (!data || typeof data !== 'object' || Array.isArray(data)) return
  const request = data as Record<string, unknown>
  const { schema, generation, image, wasm, budget, remainingMs } = request
  if (schema !== 1 || !Number.isSafeInteger(generation) || (generation as number) <= 0) return
  const fail = (error: string) => scope.postMessage({ schema: 1, generation, status: 'error', error })
  if (Object.keys(request).length !== 6 || !(image instanceof ArrayBuffer) || !(wasm instanceof ArrayBuffer) || image === wasm || !validPngPolicy(budget)
    || image.byteLength === 0 || image.byteLength > budget.maxEncodedBytes || wasm.byteLength !== READER_WASM_BYTES
    || typeof remainingMs !== 'number' || !Number.isFinite(remainingMs) || remainingMs <= 0 || remainingMs > budget.deadlineMs) return fail('invalidImage')
  const started = performance.now()
  const elapsed = () => performance.now() - started + budget.deadlineMs - remainingMs
  let bitmap: ImageBitmap | undefined, canvas: OffscreenCanvas | undefined
  try {
    const bytes = new Uint8Array(image)
    const checked = preflightStaticPng(bytes, preflightBudget(budget, bytes.byteLength), elapsed)
    if (!checked.ok) return fail(checked.error === 'animatedPNG' ? 'animatedPNG' : checked.error === 'deadlineExceeded' ? 'deadlineExceeded' : ['encodedBudget', 'pixelBudget', 'workingBudget'].includes(checked.error) ? 'imageBudget' : 'invalidImage')
    if (typeof createImageBitmap !== 'function' || typeof OffscreenCanvas !== 'function') return fail('unavailable')
    const raster = pngRasterInput(bytes)
    if (!raster) return fail('invalidImage')
    bitmap = await createImageBitmap(new Blob([raster as Uint8Array<ArrayBuffer>], { type: 'image/png' }), { imageOrientation: 'from-image', premultiplyAlpha: 'none', colorSpaceConversion: 'none' })
    if (elapsed() >= budget.deadlineMs) return fail('deadlineExceeded')
    if (bitmap.width !== checked.info.width || bitmap.height !== checked.info.height) return fail('invalidImage')
    canvas = new OffscreenCanvas(bitmap.width, bitmap.height)
    const context = canvas.getContext('2d', { alpha: false, willReadFrequently: true })
    if (!context) return fail('unavailable')
    context.fillStyle = '#fff'; context.fillRect(0, 0, bitmap.width, bitmap.height)
    context.drawImage(bitmap, 0, 0)
    bitmap.close(); bitmap = undefined
    const pixels = context.getImageData(0, 0, canvas.width, canvas.height)
    canvas.width = 0; canvas.height = 0; canvas = undefined
    await prepareZXingModule({ overrides: { wasmBinary: wasm, locateFile: name => {
      if (name !== 'zxing_reader.wasm') throw new Error('unexpectedAsset')
      return 'unavailable:local-reader'
    }, print: () => {}, printErr: () => {} }, fireImmediately: true })
    if (elapsed() >= budget.deadlineMs) return fail('deadlineExceeded')
    const result = readerCardBytes(await readBarcodes(pixels, { formats: ['QRCode'], tryHarder: true, tryRotate: true, tryInvert: true, tryDownscale: false, tryDenoise: false, isPure: false, maxNumberOfSymbols: 2, textMode: 'Plain', returnErrors: false }))
    if (elapsed() >= budget.deadlineMs) return fail('deadlineExceeded')
    if (typeof result === 'string') return fail(result)
    scope.postMessage({ schema: 1, generation, status: 'decoded', bytes: result }, [result.buffer])
  } catch { fail('decodeFailed') }
  finally { bitmap?.close(); if (canvas) { canvas.width = 0; canvas.height = 0 } }
}
