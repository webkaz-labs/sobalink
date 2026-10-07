import workerURL from './device-card-worker.ts?worker&url'
import wasmURL from '../../vendor/zxing-wasm-3.1.5/reader/zxing_reader.wasm?url'
import type { DeviceCardMode } from '../device-cards'
import { DecodeJobController, type DecodeResult, type DecodeWorker } from './decode-job'
import { preflightBudget, validPngPolicy, READER_WASM_BYTES, READER_WASM_SHA256, type PngPolicy } from './png-policy'

function readFile(file: File, signal: AbortSignal): Promise<ArrayBuffer> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    const abort = () => { reader.abort(); finish(); reject(new Error('cancelled')) }
    const finish = () => { signal.removeEventListener('abort', abort); reader.onload = null; reader.onerror = null; reader.onabort = null }
    reader.onload = () => { const result = reader.result; finish(); if (result instanceof ArrayBuffer) resolve(result); else reject(new Error('invalidImage')) }
    reader.onerror = reader.onabort = () => { finish(); reject(new Error('invalidImage')) }
    if (signal.aborted) return abort()
    signal.addEventListener('abort', abort, { once: true })
    try { reader.readAsArrayBuffer(file) } catch { finish(); reject(new Error('invalidImage')) }
  })
}
async function readerAsset(signal: AbortSignal): Promise<ArrayBuffer> {
  const url = new URL(wasmURL, location.href)
  if (url.origin !== location.origin) throw new Error('unavailable')
  const response = await fetch(url, { signal, credentials: 'omit', redirect: 'error', cache: 'no-store' })
  if (!response.ok || !response.body || response.headers.get('content-type')?.split(';')[0] !== 'application/wasm') throw new Error('unavailable')
  const length = response.headers.get('content-length')
  if (length !== null && Number(length) !== READER_WASM_BYTES) throw new Error('unavailable')
  // Bound the response before assembly; do not trust Content-Length alone.
  const buffer = new Uint8Array(READER_WASM_BYTES), reader = response.body.getReader()
  let offset = 0
  try {
    for (;;) {
      const { value, done } = await reader.read()
      if (done) break
      if (signal.aborted || value.length > buffer.length - offset) throw new Error('unavailable')
      buffer.set(value, offset); offset += value.length
    }
  } finally { await reader.cancel().catch(() => {}); reader.releaseLock() }
  if (offset !== buffer.length || signal.aborted) throw new Error('unavailable')
  const digest = Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256', buffer)), byte => byte.toString(16).padStart(2, '0')).join('')
  if (digest !== READER_WASM_SHA256 || signal.aborted) throw new Error('unavailable')
  return buffer.buffer
}
export class PngCardImporter {
  private readonly jobs = new DecodeJobController(() => {
    const url = new URL(workerURL, location.href)
    if (url.origin !== location.origin) throw new Error('unavailable')
    return new Worker(url, { type: 'module', name: 'device-card-png' }) as unknown as DecodeWorker
  })
  cancel() { this.jobs.cancel() }
  dispose() { this.jobs.dispose() }
  start(file: File, mode: DeviceCardMode, budget: PngPolicy): Promise<DecodeResult> {
    this.cancel()
    if (!validPngPolicy(budget)) return Promise.resolve({ ok: false, error: 'invalidBudget' })
    if (!Number.isSafeInteger(file.size) || file.size <= 0 || file.size > budget.maxEncodedBytes) return Promise.resolve({ ok: false, error: 'imageBudget' })
    const known = preflightBudget(budget, file.size)
    if (file.size + known.fixedDecoderBytes + known.decoderScratchBytes > budget.maxWorkingBytes) return Promise.resolve({ ok: false, error: 'imageBudget' })
    if (typeof Worker !== 'function' || typeof crypto.subtle?.digest !== 'function') return Promise.resolve({ ok: false, error: 'unavailable' })
    return this.jobs.start(mode, budget.deadlineMs, async signal => {
      const [image, wasm] = await Promise.all([readFile(file, signal), readerAsset(signal)])
      return { image, wasm, budget }
    })
  }
}
