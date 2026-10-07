import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { pngPolicy, READER_WASM_BYTES } from './png-policy'
import { DecodeJobController, type DecodeInput, type DecodeWorker } from './decode-job'

class WorkerStub implements DecodeWorker {
  onmessage: DecodeWorker['onmessage'] = null
  onerror: DecodeWorker['onerror'] = null
  terminate = vi.fn()
  postMessage = vi.fn()
}
const input = (): DecodeInput => ({ image: new ArrayBuffer(1), wasm: new ArrayBuffer(READER_WASM_BYTES), budget: { ...pngPolicy()!, deadlineMs: 100 } })
const flush = async () => { for (let i = 0; i < 5; i++) await Promise.resolve() }
const card = (mode = 'direct-lan') => `soba-card1.${btoa(JSON.stringify({ version: 1, mode, publicKey: 'a'.repeat(64), name: 'Fixture' })).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')}`
// A real Worker message is cloned into the receiving window's realm. Node's
// test TextEncoder returns a different realm's typed array in this jsdom setup.
const wireBytes = (text: string) => Uint8Array.from(text, char => char.charCodeAt(0))

describe('preparatory single decode job boundary', () => {
  let workers: WorkerStub[]
  let controller: DecodeJobController
  beforeEach(() => {
    vi.useFakeTimers(); vi.setSystemTime(1000)
    workers = []
    controller = new DecodeJobController(() => { const worker = new WorkerStub(); workers.push(worker); return worker }, () => Date.now())
  })
  afterEach(() => { controller.dispose(); vi.useRealTimers() })
  const reply = (worker: WorkerStub, data: Record<string, unknown>) => {
    const request = worker.postMessage.mock.calls[0][0] as { generation: number }
    worker.onmessage?.({ data: { schema: 1, generation: request.generation, ...data } })
  }
  it('returns only an exact card and terminates after success', async () => {
    const result = controller.start('direct-lan', 100, async () => input())
    await flush()
    const text = card()
    reply(workers[0], { status: 'decoded', bytes: wireBytes(text) })
    expect(await result).toEqual({ ok: true, text })
    expect(workers[0].terminate).toHaveBeenCalledTimes(1)
    expect(workers[0].onmessage).toBeNull()
    expect(vi.getTimerCount()).toBe(0)
  })
  it('cancels preparation and never spawns on late completion', async () => {
    let complete!: (value: DecodeInput) => void
    let signal!: AbortSignal
    const result = controller.start('direct-lan', 100, current => { signal = current; return new Promise(resolve => { complete = resolve }) })
    await flush(); controller.cancel()
    expect(signal.aborted).toBe(true)
    complete(input()); await flush()
    expect(await result).toEqual({ ok: false, error: 'cancelled' })
    expect(workers).toHaveLength(0)
  })
  it('reselections retire one worker and reject stale callbacks', async () => {
    const first = controller.start('direct-lan', 100, async () => input()); await flush()
    const stale = workers[0].onmessage!
    const oldGeneration = (workers[0].postMessage.mock.calls[0][0] as { generation: number }).generation
    const second = controller.start('direct-lan', 100, async () => input()); await flush()
    stale({ data: { schema: 1, generation: oldGeneration, status: 'decoded', bytes: wireBytes(card()) } })
    expect(await first).toEqual({ ok: false, error: 'cancelled' })
    expect(workers[0].terminate).toHaveBeenCalledTimes(1)
    expect(workers[1].terminate).not.toHaveBeenCalled()
    reply(workers[1], { status: 'error', error: 'noQRCode' })
    expect(await second).toEqual({ ok: false, error: 'noQRCode' })
  })
  it('terminates on timeout without relying on a worker cancellation message', async () => {
    const result = controller.start('direct-lan', 100, async () => input()); await flush()
    await vi.advanceTimersByTimeAsync(100)
    expect(await result).toEqual({ ok: false, error: 'deadlineExceeded' })
    expect(workers[0].terminate).toHaveBeenCalledTimes(1)
  })
  it('rejects late results even when the parent timer has not fired', async () => {
    const result = controller.start('direct-lan', 100, async () => input()); await flush()
    vi.setSystemTime(1100)
    reply(workers[0], { status: 'decoded', bytes: wireBytes(card()) })
    expect(await result).toEqual({ ok: false, error: 'deadlineExceeded' })
  })
  it.each([0, -1, NaN, Infinity, 0.5, 0x80000000])('rejects unrepresentable deadline %s before preparation', async deadline => {
    const prepare = vi.fn(async () => input())
    expect(await controller.start('direct-lan', deadline, prepare)).toEqual({ ok: false, error: 'invalidBudget' })
    expect(prepare).not.toHaveBeenCalled()
    expect(workers).toHaveLength(0)
  })
  it.each([
    { status: 'decoded', bytes: Uint8Array.of(255) },
    { status: 'decoded', bytes: new Uint8Array(1036) },
    { status: 'decoded', bytes: new Uint8Array(new ArrayBuffer(2000), 0, 1) },
    { status: 'decoded', bytes: wireBytes(card()), metadata: 'unexpected' },
    { status: 'error', error: 'arbitrary decoder exception' },
    { status: 'error', error: 'noQRCode', generation: 0 },
  ])('rejects malformed result without forwarding decoder data', async data => {
    const result = controller.start('direct-lan', 100, async () => input()); await flush()
    reply(workers[0], data)
    expect(await result).toEqual({ ok: false, error: 'invalidResult' })
    expect(workers[0].terminate).toHaveBeenCalledTimes(1)
  })
  it.each([` ${card()}`, card('lan')])('rejects normalization and wrong-mode card payloads', async text => {
    const result = controller.start('direct-lan', 100, async () => input()); await flush()
    reply(workers[0], { status: 'decoded', bytes: wireBytes(text) })
    expect(await result).toEqual({ ok: false, error: 'invalidCard' })
  })
  it('preserves bounded ambiguity and terminates after failure', async () => {
    const result = controller.start('direct-lan', 100, async () => input()); await flush()
    reply(workers[0], { status: 'error', error: 'ambiguousQRCode' })
    expect(await result).toEqual({ ok: false, error: 'ambiguousQRCode' })
    expect(workers[0].terminate).toHaveBeenCalledTimes(1)
  })
  it('disposal prevents new jobs', async () => {
    const result = controller.start('direct-lan', 100, async () => input()); await flush()
    controller.dispose()
    expect(await result).toEqual({ ok: false, error: 'cancelled' })
    expect(await controller.start('direct-lan', 100, async () => input())).toEqual({ ok: false, error: 'unavailable' })
    expect(workers).toHaveLength(1)
  })
  it('terminates a worker created during reentrant cancellation', async () => {
    const worker = new WorkerStub()
    controller = new DecodeJobController(() => { controller.cancel(); return worker }, () => Date.now())
    const result = controller.start('direct-lan', 100, async () => input()); await flush()
    expect(await result).toEqual({ ok: false, error: 'cancelled' })
    expect(worker.terminate).toHaveBeenCalledTimes(1)
    expect(worker.postMessage).not.toHaveBeenCalled()
  })
})
