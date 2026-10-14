import { act, renderHook, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { ApiError, type State } from './api'
import { en, ja, errorText, translator } from './i18n'
import { useServer } from './useServer'

const state: State = { csrfToken: 'synthetic-csrf', self: { name: 'Synthetic device', status: 'online' }, peers: [], messages: [], transfers: [], services: [], shares: [] }
const payload = { transferId: 'original-delivery', destination: '/synthetic-receiving' }
type Request = { requestId: string; name: string; payload: typeof payload }

describe('acceptance command feedback', () => {
  it('uses a fresh invocation only after an explicit repeat of proven busy, keeping the exact payload', async () => {
    const requests: Request[] = []
    vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
      if (path === '/api/state') return new Response(JSON.stringify(state))
      requests.push(JSON.parse(init!.body as string))
      return requests.length === 1 ? new Response('{"code":"transfer_accept_busy","error":"Synthetic prepublication refusal"}', { status: 400 }) : new Response('{"ok":true}')
    }))
    const { result } = renderHook(() => useServer())
    await waitFor(() => expect(result.current.auth).toBe('ready'))
    await act(async () => { await result.current.run('transfer.accept', payload, 'transfer:original-delivery') })
    expect(result.current.error).toBeInstanceOf(ApiError)
    expect(result.current.error).toMatchObject({ code: 'transfer_accept_busy', status: 400, message: 'Synthetic prepublication refusal', displayCode: 'transfer_accept_busy' })
    await act(async () => { await result.current.refresh(true) })
    expect(requests).toHaveLength(1)
    await act(async () => { await result.current.run('transfer.accept', payload, 'transfer:original-delivery') })
    expect(requests).toHaveLength(2)
    expect(requests.map(request => ({ name: request.name, payload: request.payload }))).toEqual(Array(2).fill({ name: 'transfer.accept', payload }))
    expect(requests[1].requestId).not.toBe(requests[0].requestId)
  })

  it.each(['transfer_accept_unconfirmed', 'command_failed', 'receive_recovery_required', 'outbox_storage_uncertain', 'busy', 'outbox_storage_busy', 'transfer_accept_busy extra', 'disk_space_low extra', 'disk_space_unknown extra', 'unknown_acceptance_failure', undefined])('keeps %s conservative without changing the original error code/status or issuing another request', async code => {
    const requests: Request[] = []
    vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
      if (path === '/api/state') return new Response(JSON.stringify(state))
      requests.push(JSON.parse(init!.body as string))
      // Text resembling a busy code must never authorize the busy guidance.
      return new Response(JSON.stringify({ code, error: 'Synthetic detail: transfer_accept_busy' }), { status: 400 })
    }))
    const { result } = renderHook(() => useServer())
    await waitFor(() => expect(result.current.auth).toBe('ready'))
    await act(async () => { await result.current.run('transfer.accept', payload) })
    expect(result.current.error).toBeInstanceOf(ApiError)
    expect(result.current.error).toMatchObject({ code: code || 'request_failed', status: 400, message: 'Synthetic detail: transfer_accept_busy', displayCode: 'transfer_accept_unconfirmed' })
    for (const locale of ['en', 'ja'] as const) {
      const copy = locale === 'ja' ? ja : en
      expect(errorText(result.current.error, translator(locale))).toBe(copy.transfer_accept_unconfirmed)
      expect(errorText(result.current.error, translator(locale))).not.toBe(copy.transfer_accept_busy)
    }
    await act(async () => { await result.current.refresh(true) })
    expect(requests).toHaveLength(1)
  })

  it.each(['disk_space_low', 'disk_space_unknown'] as const)('preserves exact typed %s guidance separately from busy acceptance', async code => {
    const requests: Request[] = []
    vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
      if (path === '/api/state') return new Response(JSON.stringify(state))
      requests.push(JSON.parse(init!.body as string))
      return new Response(JSON.stringify({ code, error: 'Synthetic disk-space diagnostic' }), { status: 400 })
    }))
    const { result } = renderHook(() => useServer())
    await waitFor(() => expect(result.current.auth).toBe('ready'))
    await act(async () => { await result.current.run('transfer.accept', payload) })
    expect(result.current.error).toMatchObject({ code, status: 400, displayCode: code })
    for (const locale of ['en', 'ja'] as const) {
      const copy = locale === 'ja' ? ja : en
      expect(errorText(result.current.error, translator(locale))).toBe(copy[code])
      expect(errorText(result.current.error, translator(locale))).not.toBe(copy.transfer_accept_busy)
    }
    await act(async () => { await result.current.refresh(true) })
    expect(requests).toHaveLength(1)
  })

  it.each(['network_error', 'invalid_response'])('preserves the original invocation identity after %s without retry guidance or automatic requests', async code => {
    const requests: Request[] = []
    vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
      if (path === '/api/state') return new Response(JSON.stringify(state))
      requests.push(JSON.parse(init!.body as string))
      if (requests.length === 1) {
        if (code === 'network_error') throw new TypeError('Synthetic lost response')
        return new Response('invalid synthetic JSON')
      }
      return new Response('{"ok":true}')
    }))
    const { result } = renderHook(() => useServer())
    await waitFor(() => expect(result.current.auth).toBe('ready'))
    await act(async () => { await result.current.run('transfer.accept', payload) })
    expect(result.current.error).toMatchObject({ code, displayCode: 'transfer_accept_unconfirmed' })
    await act(async () => { await result.current.refresh(true) })
    expect(requests).toHaveLength(1)
    // A later explicit invocation retains existing lost-response recovery;
    // presentation must not turn an unknown result into a fresh busy retry.
    await act(async () => { await result.current.run('transfer.accept', payload) })
    expect(requests).toHaveLength(2)
    expect(requests[1]).toEqual(requests[0])
  })

  it('keeps unrelated command failures on their existing presentation path', async () => {
    vi.stubGlobal('fetch', vi.fn(async (path: string) => path === '/api/state' ? new Response(JSON.stringify(state)) : new Response('{"code":"command_failed"}', { status: 400 })))
    const { result } = renderHook(() => useServer())
    await waitFor(() => expect(result.current.auth).toBe('ready'))
    await act(async () => { await result.current.run('peer.reconnect', { peerId: 'synthetic-peer' }) })
    expect(result.current.error).not.toHaveProperty('displayCode')
    expect(errorText(result.current.error, translator('en'))).toBe(en.command_failed)
  })
})
