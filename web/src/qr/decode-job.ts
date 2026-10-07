import { validPngPolicy, READER_WASM_BYTES, type PngPolicy } from './png-policy'
import { MAX_CARD_BYTES, readCardInput, type DeviceCardMode } from '../device-cards'

/** Preparatory parent-side boundary; not yet connected to a decoder or UI. */
export type DecodeError = 'cancelled' | 'deadlineExceeded' | 'invalidBudget' | 'invalidResult'
  | 'imageBudget' | 'animatedPNG' | 'unavailable' | 'invalidImage' | 'noQRCode' | 'ambiguousQRCode' | 'invalidCard' | 'decodeFailed'
export type DecodeResult = { ok: true; text: string } | { ok: false; error: DecodeError }
export interface DecodeWorker {
  onmessage: ((event: { data: unknown }) => void) | null
  onerror: (() => void) | null
  postMessage(message: unknown, transfer: ArrayBuffer[]): void
  terminate(): void
}
export interface DecodeInput { image: ArrayBuffer; wasm: ArrayBuffer; budget: PngPolicy }
interface Job {
  generation: number
  started: number
  deadline: number
  mode: DeviceCardMode
  abort: AbortController
  worker?: DecodeWorker
  timer?: ReturnType<typeof setTimeout>
  resolve: (result: DecodeResult) => void
}
const workerErrors = new Set(['deadlineExceeded', 'imageBudget', 'animatedPNG', 'unavailable', 'invalidImage', 'noQRCode', 'ambiguousQRCode', 'invalidCard', 'decodeFailed'])
const ownKeys = (value: object, expected: string[]) => {
  const keys = Object.keys(value)
  return keys.length === expected.length && keys.every(key => expected.includes(key))
}

/** One active job, no queue. The caller supplies finite policy and pinned assets.
 * cancel() must also be called on edits, auth/mode changes and panel closure.
 * Termination bounds lifecycle; it does not enforce a WASM heap or browser RSS cap.
 */
export class DecodeJobController {
  private generation = 0
  private active?: Job
  private disposed = false
  constructor(private readonly createWorker: () => DecodeWorker, private readonly now: () => number = () => performance.now()) {}

  cancel(): void {
    this.generation++
    if (this.active) this.finish(this.active, { ok: false, error: 'cancelled' })
  }
  dispose(): void { this.disposed = true; this.cancel() }

  start(mode: DeviceCardMode, deadlineMs: number, prepare: (signal: AbortSignal) => Promise<DecodeInput>): Promise<DecodeResult> {
    // Reserve ownership before cleanup: abort/terminate callbacks may start a
    // newer job synchronously. An older invocation must never overwrite it.
    const generation = ++this.generation
    if (this.active) this.finish(this.active, { ok: false, error: 'cancelled' })
    if (this.disposed) return Promise.resolve({ ok: false, error: 'unavailable' })
    if (generation !== this.generation) return Promise.resolve({ ok: false, error: 'cancelled' })
    // Native timers cannot represent a larger delay; reject instead of wrapping.
    if (!Number.isSafeInteger(deadlineMs) || deadlineMs <= 0 || deadlineMs > 0x7fffffff) return Promise.resolve({ ok: false, error: 'invalidBudget' })
    let started: number
    try { started = this.now() } catch { return Promise.resolve({ ok: false, error: 'deadlineExceeded' }) }
    if (generation !== this.generation) return Promise.resolve({ ok: false, error: 'cancelled' })
    if (!Number.isFinite(started) || started < 0) return Promise.resolve({ ok: false, error: 'deadlineExceeded' })
    return new Promise(resolve => {
      const job: Job = { generation, started, deadline: deadlineMs, mode, abort: new AbortController(), resolve }
      this.active = job
      job.timer = setTimeout(() => this.finish(job, { ok: false, error: 'deadlineExceeded' }), deadlineMs)
      // Include preparation in the deadline. Cancellation wins over late fetch/file
      // completion even when an underlying API ignores AbortSignal.
      void Promise.resolve().then(() => {
        if (!this.current(job)) return undefined
        return prepare(job.abort.signal)
      }).then(input => {
        if (!this.current(job) || !input) return
        if (this.expired(job)) return this.finish(job, { ok: false, error: 'deadlineExceeded' })
        if (!(input.image instanceof ArrayBuffer) || !(input.wasm instanceof ArrayBuffer) || input.image === input.wasm || !validPngPolicy(input.budget) || input.budget.deadlineMs !== job.deadline || input.image.byteLength === 0 || input.image.byteLength > input.budget.maxEncodedBytes || input.wasm.byteLength !== READER_WASM_BYTES) return this.finish(job, { ok: false, error: 'invalidImage' })
        const worker = this.createWorker()
        if (!this.current(job)) { worker.terminate(); return }
        job.worker = worker
        if (this.expired(job)) return this.finish(job, { ok: false, error: 'deadlineExceeded' })
        if (!this.current(job)) return
        worker.onmessage = event => this.receive(job, event.data)
        worker.onerror = () => this.finish(job, { ok: false, error: 'decodeFailed' })
        const remainingMs = job.deadline - (this.now() - job.started)
        if (!Number.isFinite(remainingMs) || remainingMs <= 0 || remainingMs > job.deadline) return this.finish(job, { ok: false, error: 'deadlineExceeded' })
        if (!this.current(job)) return
        worker.postMessage({ schema: 1, generation: job.generation, image: input.image, wasm: input.wasm, budget: input.budget, remainingMs }, [input.image, input.wasm])
      }).catch(() => this.finish(job, { ok: false, error: 'decodeFailed' }))
    })
  }
  private current(job: Job) { return this.active === job && this.generation === job.generation }
  private expired(job: Job) {
    try {
      const elapsed = this.now() - job.started
      return !Number.isFinite(elapsed) || elapsed < 0 || elapsed >= job.deadline
    } catch { return true }
  }
  private receive(job: Job, data: unknown) {
    if (!this.current(job)) return
    if (this.expired(job)) return this.finish(job, { ok: false, error: 'deadlineExceeded' })
    try {
      if (!data || typeof data !== 'object' || Array.isArray(data)) throw new Error()
      const value = data as Record<string, unknown>
      if (value.schema !== 1 || value.generation !== job.generation) throw new Error()
      if (value.status === 'error' && ownKeys(value, ['schema', 'generation', 'status', 'error']) && typeof value.error === 'string' && workerErrors.has(value.error)) {
        return this.finish(job, { ok: false, error: value.error as DecodeError })
      }
      if (value.status !== 'decoded' || !ownKeys(value, ['schema', 'generation', 'status', 'bytes']) || !(value.bytes instanceof Uint8Array) || !(value.bytes.buffer instanceof ArrayBuffer) || value.bytes.byteLength === 0 || value.bytes.byteLength > MAX_CARD_BYTES || value.bytes.buffer.byteLength > MAX_CARD_BYTES) throw new Error()
      // Require exact wire bytes: no Unicode replacement, whitespace trimming or
      // decoded-text substitution. Core inspection remains a separate user action.
      if (value.bytes.some(byte => byte > 127)) throw new Error()
      const text = new TextDecoder('utf-8', { fatal: true }).decode(value.bytes)
      const parsed = readCardInput(text, job.mode)
      if (typeof parsed === 'string' || parsed.text !== text) return this.finish(job, { ok: false, error: 'invalidCard' })
      this.finish(job, { ok: true, text })
    } catch { this.finish(job, { ok: false, error: 'invalidResult' }) }
  }
  private finish(job: Job, result: DecodeResult) {
    if (this.active !== job) return
    this.active = undefined
    if (job.timer !== undefined) clearTimeout(job.timer)
    // Invalidate and detach first, including synchronous callbacks from cleanup.
    const worker = job.worker
    if (worker) { worker.onmessage = null; worker.onerror = null }
    try { job.abort.abort() } catch { /* cleanup never exposes exception text */ }
    try { worker?.terminate() } catch { /* production Worker.terminate is void */ }
    job.resolve(result)
  }
}
