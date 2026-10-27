import { act, renderHook, waitFor } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { ApiError, type State } from './api'
import { useServer } from './useServer'

const state: State = { csrfToken: 'synthetic-csrf', self: { name: 'Example', status: 'online' }, peers: [], messages: [], transfers: [], services: [], shares: [] }

it('keeps the stop acknowledgement without requiring a snapshot, and never equates disconnection with completion', async () => {
  let stopped = false
  const fetch = vi.fn(async (path: string) => {
    if (path === '/api/state') {
      if (stopped) throw new Error('synthetic disconnected owner')
      return new Response(JSON.stringify(state))
    }
    stopped = true
    return new Response(JSON.stringify({ ok: true, result: { state: 'stopping' } }))
  })
  vi.stubGlobal('fetch', fetch)
  const { result } = renderHook(() => useServer())
  await waitFor(() => expect(result.current.auth).toBe('ready'))
  await act(async () => { expect(await result.current.run('application.stop', {})).toMatchObject({ result: { state: 'stopping' } }) })
  expect(fetch).toHaveBeenCalledTimes(2)
  expect(result.current.applicationStop).toBe('requested')
  expect(result.current.stale).toBe(true)
  expect(result.current.error).toBeNull()
  await act(async () => { await result.current.refresh(true) })
  expect(result.current.applicationStop).toBe('disconnected')
  await act(async () => { result.current.handleError(new ApiError('unauthenticated', '')) })
  expect(result.current.auth).toBe('ready')
  expect(result.current.state).toEqual(state)
  expect(result.current.error).toBeNull()
  await act(async () => {
    await result.current.run('application.stop', {}, 'different-key')
    await result.current.run('peer.reconnect', { peerId: 'synthetic-peer' })
  })
  expect(fetch).toHaveBeenCalledTimes(3)
})

it.each(['lost', 'malformed'])('preserves an unconfirmed %s stop reply without retrying or reviving from a late snapshot', async outcome => {
  let calls = 0
  vi.stubGlobal('fetch', vi.fn(async (path: string) => {
    if (path === '/api/state') return new Response(JSON.stringify(state))
    calls++
    if (outcome === 'lost') throw new Error('synthetic lost reply')
    return new Response(JSON.stringify({ ok: true, result: { state: 'other' } }))
  }))
  const { result } = renderHook(() => useServer())
  await waitFor(() => expect(result.current.auth).toBe('ready'))
  await act(async () => { await result.current.run('application.stop', {}) })
  expect(result.current.applicationStop).toBe('unconfirmed')
  expect(result.current.stale).toBe(true)
  expect(result.current.error).toBeNull()
  await act(async () => {
    await result.current.refresh(true)
    await result.current.run('application.stop', {})
  })
  expect(result.current.applicationStop).toBe('unconfirmed')
  expect(result.current.stale).toBe(true)
  expect(calls).toBe(1)
})

it('does not label an explicit rejected stop as accepted or completed', async () => {
  vi.stubGlobal('fetch', vi.fn(async (path: string) => path === '/api/state'
    ? new Response(JSON.stringify(state)) : new Response('{"code":"forbidden"}', { status: 403 })))
  const { result } = renderHook(() => useServer())
  await waitFor(() => expect(result.current.auth).toBe('ready'))
  await act(async () => { await result.current.run('application.stop', {}) })
  expect(result.current.applicationStop).toBeUndefined()
  expect(result.current.stale).toBe(false)
  expect(result.current.error).toMatchObject({ code: 'forbidden' })
})

it('blocks repeated stop clicks even with different keys while the first reply is pending', async () => {
  let finish!: (response: Response) => void
  const pending = new Promise<Response>(resolve => { finish = resolve })
  const fetch = vi.fn((path: string) => path === '/api/state' ? Promise.resolve(new Response(JSON.stringify(state))) : pending)
  vi.stubGlobal('fetch', fetch)
  const { result } = renderHook(() => useServer())
  await waitFor(() => expect(result.current.auth).toBe('ready'))
  let first!: Promise<unknown>
  await act(async () => {
    first = result.current.run('application.stop', {}, 'first')
    await result.current.run('application.stop', {}, 'second')
    await result.current.run('peer.reconnect', { peerId: 'example-peer' })
  })
  expect(fetch).toHaveBeenCalledTimes(2)
  await act(async () => { finish(new Response('{"ok":true,"result":{"state":"stopping"}}')); await first })
  expect(result.current.applicationStop).toBe('requested')
})

it.each(['success', 'unauthenticated'])('ignores a prior action finishing with %s after accepted stop', async outcome => {
  let finish!: (response: Response) => void
  const pending = new Promise<Response>(resolve => { finish = resolve })
  const fetch = vi.fn((path: string, init?: RequestInit) => {
    if (path === '/api/state') return Promise.resolve(new Response(JSON.stringify(state)))
    const command = JSON.parse(init!.body as string)
    return command.name === 'application.stop' ? Promise.resolve(new Response('{"ok":true,"result":{"state":"stopping"}}')) : pending
  })
  vi.stubGlobal('fetch', fetch)
  const { result } = renderHook(() => useServer())
  await waitFor(() => expect(result.current.auth).toBe('ready'))
  let prior!: Promise<unknown>
  await act(async () => {
    prior = result.current.run('peer.reconnect', { peerId: 'example-peer' })
    await result.current.run('application.stop', {})
  })
  await act(async () => {
    finish(outcome === 'success' ? new Response('{"ok":true}') : new Response('{"code":"unauthenticated"}', { status: 401 }))
    expect(await prior).toBeUndefined()
  })
  expect(result.current.applicationStop).toBe('requested')
  expect(result.current.auth).toBe('ready')
  expect(result.current.state).toEqual(state)
  expect(result.current.error).toBeNull()
  expect(result.current.stale).toBe(true)
  expect(fetch).toHaveBeenCalledTimes(3)
})

it('ignores an earlier action already awaiting its refresh when stop is accepted', async () => {
  let finish!: (response: Response) => void
  const pending = new Promise<Response>(resolve => { finish = resolve })
  let reads = 0
  vi.stubGlobal('fetch', vi.fn((path: string, init?: RequestInit) => {
    if (path === '/api/state') return ++reads === 1 ? Promise.resolve(new Response(JSON.stringify(state))) : pending
    const command = JSON.parse(init!.body as string)
    return Promise.resolve(new Response(JSON.stringify({ ok: true, result: command.name === 'application.stop' ? { state: 'stopping' } : {} })))
  }))
  const { result } = renderHook(() => useServer())
  await waitFor(() => expect(result.current.auth).toBe('ready'))
  let prior!: Promise<unknown>
  act(() => { prior = result.current.run('peer.reconnect', { peerId: 'example-peer' }) })
  await waitFor(() => expect(reads).toBe(2))
  await act(async () => { await result.current.run('application.stop', {}) })
  await act(async () => { finish(new Response(JSON.stringify(state))); expect(await prior).toBeUndefined() })
  expect(result.current.applicationStop).toBe('requested')
  expect(result.current.stale).toBe(true)
})
