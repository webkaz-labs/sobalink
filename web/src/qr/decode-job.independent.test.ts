import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { pngPolicy, READER_WASM_BYTES } from './png-policy'
import { DecodeJobController, type DecodeInput, type DecodeResult, type DecodeWorker } from './decode-job'

class WorkerStub implements DecodeWorker {
  onmessage: DecodeWorker['onmessage'] = null
  onerror: DecodeWorker['onerror'] = null
  terminate = vi.fn()
  postMessage = vi.fn()
}
const input = (): DecodeInput => ({ image: new ArrayBuffer(1), wasm: new ArrayBuffer(READER_WASM_BYTES), budget: { ...pngPolicy()!, deadlineMs: 100 } })
const flush = async () => { for (let i = 0; i < 10; i++) await Promise.resolve() }
const text = `soba-card1.${btoa(JSON.stringify({ version: 1, mode: 'direct-lan', publicKey: 'a'.repeat(64), name: 'Fixture' })).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')}`
const bytes = () => Uint8Array.from(text, char => char.charCodeAt(0))

describe('independent decode lifecycle counterexamples', () => {
  let controller: DecodeJobController
  let workers: WorkerStub[]
  beforeEach(() => {
    vi.useFakeTimers(); vi.setSystemTime(1000)
    workers = []
    controller = new DecodeJobController(() => { const worker = new WorkerStub(); workers.push(worker); return worker }, () => Date.now())
  })
  afterEach(() => { controller.dispose(); vi.clearAllTimers(); vi.useRealTimers() })
  const reply = (worker: WorkerStub, data: unknown) => worker.onmessage?.({ data })
  const success = (worker: WorkerStub) => {
    const { generation } = worker.postMessage.mock.calls[0][0] as { generation: number }
    reply(worker, { schema: 1, generation, status: 'decoded', bytes: bytes() })
  }

  it.each(['abort', 'terminate'] as const)('settles every request when old %s cleanup reenters start', async cleanup => {
    let nested: Promise<DecodeResult> | undefined
    let nestedSettled = false
    const reenter = () => {
      nested = controller.start('direct-lan', 100, async () => input())
      void nested.then(() => { nestedSettled = true })
    }
    const first = controller.start('direct-lan', 100, async signal => {
      if (cleanup === 'abort') signal.addEventListener('abort', reenter, { once: true })
      return input()
    })
    await flush()
    if (cleanup === 'terminate') workers[0].terminate.mockImplementationOnce(reenter)
    const outer = controller.start('direct-lan', 100, async () => input())
    await flush()
    expect(await first).toEqual({ ok: false, error: 'cancelled' })
    expect(nested).toBeDefined()
    controller.cancel()
    await vi.advanceTimersByTimeAsync(200)
    await outer
    expect(vi.getTimerCount()).toBe(0)
    expect(nestedSettled).toBe(true)
  })

  it('does not dispatch bytes after synchronous worker creation exhausts the deadline', async () => {
    const worker = new WorkerStub()
    controller = new DecodeJobController(() => { vi.setSystemTime(1100); return worker }, () => Date.now())
    const result = controller.start('direct-lan', 100, async () => input())
    await flush()
    expect(worker.postMessage).not.toHaveBeenCalled()
    expect(worker.terminate).toHaveBeenCalledTimes(1)
    expect(await result).toEqual({ ok: false, error: 'deadlineExceeded' })
  })

  it('converts a throwing admission clock into a settled bounded result', async () => {
    controller = new DecodeJobController(() => new WorkerStub(), () => { throw new Error('clock implementation detail') })
    let result: Promise<DecodeResult> | undefined
    expect(() => { result = controller.start('direct-lan', 100, async () => input()) }).not.toThrow()
    expect(await result).toMatchObject({ ok: false })
    expect(vi.getTimerCount()).toBe(0)
  })

  it('converts a throwing result-time clock into cleanup and a settled bounded result', async () => {
    let fails = false
    controller = new DecodeJobController(() => { const worker = new WorkerStub(); workers.push(worker); return worker }, () => {
      if (fails) throw new Error('clock implementation detail')
      return 1000
    })
    const result = controller.start('direct-lan', 100, async () => input())
    await flush(); fails = true
    expect(() => success(workers[0])).not.toThrow()
    expect(await result).toMatchObject({ ok: false })
    expect(workers[0].terminate).toHaveBeenCalledTimes(1)
    expect(vi.getTimerCount()).toBe(0)
  })

  it.each(['prepare throw', 'prepare reject', 'factory throw', 'post throw', 'error event'] as const)('cleans and settles %s', async fault => {
    const worker = new WorkerStub()
    controller = new DecodeJobController(() => {
      if (fault === 'factory throw') throw new Error('private factory context')
      return worker
    }, () => Date.now())
    if (fault === 'post throw') worker.postMessage.mockImplementation(() => { throw new Error('private post context') })
    const result = controller.start('direct-lan', 100, () => {
      if (fault === 'prepare throw') throw new Error('private prepare context')
      if (fault === 'prepare reject') return Promise.reject(new Error('private prepare context'))
      return Promise.resolve(input())
    })
    await flush()
    if (fault === 'error event') worker.onerror?.()
    expect(await result).toEqual({ ok: false, error: 'decodeFailed' })
    expect(vi.getTimerCount()).toBe(0)
    if (fault === 'post throw' || fault === 'error event') {
      expect(worker.terminate).toHaveBeenCalledTimes(1)
      expect(worker.onmessage).toBeNull()
      expect(worker.onerror).toBeNull()
    }
  })

  it('settles a synchronous post callback even when post then throws', async () => {
    const worker = new WorkerStub()
    controller = new DecodeJobController(() => worker, () => Date.now())
    worker.postMessage.mockImplementation(message => {
      const { generation } = message as { generation: number }
      reply(worker, { schema: 1, generation, status: 'decoded', bytes: bytes() })
      throw new Error('post callback implementation detail')
    })
    expect(await controller.start('direct-lan', 100, async () => input())).toEqual({ ok: true, text })
    expect(worker.terminate).toHaveBeenCalledTimes(1)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('ignores callbacks fired synchronously during termination and tolerates termination exceptions', async () => {
    const result = controller.start('direct-lan', 100, async () => input())
    await flush()
    const callback = workers[0].onmessage!
    workers[0].terminate.mockImplementation(() => {
      callback({ data: { schema: 1, generation: 1, status: 'decoded', bytes: bytes() } })
      throw new Error('termination implementation detail')
    })
    controller.cancel()
    expect(await result).toEqual({ ok: false, error: 'cancelled' })
    expect(vi.getTimerCount()).toBe(0)
  })

  it('skips preparation when cancelled before its first microtask', async () => {
    const prepare = vi.fn(async () => input())
    const result = controller.start('direct-lan', 100, prepare)
    controller.cancel(); await flush()
    expect(await result).toEqual({ ok: false, error: 'cancelled' })
    expect(prepare).not.toHaveBeenCalled()
    expect(workers).toHaveLength(0)
  })

  it('handles disposal reentered by an abort listener', async () => {
    const first = controller.start('direct-lan', 100, async signal => {
      signal.addEventListener('abort', () => controller.dispose(), { once: true })
      return input()
    })
    await flush()
    expect(await controller.start('direct-lan', 100, async () => input())).toEqual({ ok: false, error: 'unavailable' })
    expect(await first).toEqual({ ok: false, error: 'cancelled' })
    expect(workers).toHaveLength(1)
    expect(vi.getTimerCount()).toBe(0)
  })

  it.each([
    null, [], 'arbitrary text',
    { schema: 2, generation: 1, status: 'decoded', bytes: bytes() },
    { schema: 1, generation: 1, status: 'decoded', bytes: new Uint8Array() },
    { schema: 1, generation: 1, status: 'decoded', bytes: new Uint8ClampedArray(1) },
    { schema: 1, generation: 1, status: 'decoded', bytes: new Uint8Array(new SharedArrayBuffer(1)) },
    { schema: 1, generation: 1, status: 'decoded', bytes: bytes(), text },
    { schema: 1, generation: 1, status: 'error', error: 'decodeFailed', details: 'arbitrary private context' },
  ])('rejects nonconforming result %#', async data => {
    const result = controller.start('direct-lan', 100, async () => input())
    await flush(); reply(workers[0], data)
    expect(await result).toEqual({ ok: false, error: 'invalidResult' })
    expect(workers[0].terminate).toHaveBeenCalledTimes(1)
    expect(vi.getTimerCount()).toBe(0)
  })
})
