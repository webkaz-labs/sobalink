import { act, renderHook, waitFor } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { command, type State } from './api'
import { useServer } from './useServer'

it('never reapplies an authenticated snapshot or CSRF after a newer 401', async () => {
  const state: State = { csrfToken: 'old-token', self: { name: 'Test', status: 'online' }, peers: [], messages: [], transfers: [], services: [], shares: [] }
  let resolveLate!: (value: Response) => void
  const late = new Promise<Response>(resolve => { resolveLate = resolve })
  let reads = 0
  const fetch = vi.fn((path: string) => {
    if (path === '/api/state') return ++reads === 1 ? Promise.resolve(new Response(JSON.stringify(state))) : late
    return Promise.resolve(new Response('{"code":"unauthenticated"}', { status: 401 }))
  })
  vi.stubGlobal('fetch', fetch)
  const { result } = renderHook(() => useServer())
  await waitFor(() => expect(result.current.auth).toBe('ready'))
  let pending!: Promise<State | null>
  act(() => { pending = result.current.refresh() })
  await act(async () => { await result.current.run('peer.reconnect', { peerId: 'a' }) })
  expect(result.current.auth).toBe('locked')
  await act(async () => { resolveLate(new Response(JSON.stringify(state))); await pending })
  expect(result.current.auth).toBe('locked')
  expect(result.current.state).toBeNull()
  await expect(command('peer.reconnect', { peerId: 'a' })).rejects.toMatchObject({ code: 'unauthenticated' })
  const last = fetch.mock.calls.at(-1) as unknown as [string, RequestInit]
  expect(last[1].headers).not.toHaveProperty('X-CSRF-Token')
})
