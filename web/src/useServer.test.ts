import { act, renderHook, waitFor } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { command, type State } from './api'
import { MAX_UNCERTAIN_MESSAGES, useServer } from './useServer'

const messageState: State = { csrfToken: 'synthetic-csrf', self: { name: 'Synthetic', status: 'online' }, peers: [], messages: [], transfers: [], services: [], shares: [] }

it.each(['message_history_unavailable', 'message_peer_storage_unavailable'])('blocks an identical %s draft despite refresh, unrelated commands and a successful different message', async code => {
  const requests: { name: string; payload: Record<string, unknown>; requestId: string }[] = []
  vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
    if (path === '/api/state') return new Response(JSON.stringify(messageState))
    const request = JSON.parse(init!.body as string)
    requests.push(request)
    return request.name === 'message.send' && request.payload.text === 'Uncertain original' && request.payload.peerId === 'a'
      ? new Response(JSON.stringify({ code }), { status: 507 }) : new Response('{"ok":true}')
  }))
  const { result } = renderHook(() => useServer())
  await waitFor(() => expect(result.current.auth).toBe('ready'))
  await act(async () => { await result.current.run('message.send', { peerId: 'a', text: 'Uncertain original' }, 'first') })
  expect(result.current.error).toMatchObject({ code })
  await act(async () => {
    result.current.setError(null)
    await result.current.refresh(true)
    // More successful commands than Core's 256-entry request cache can retain.
    for (let i = 0; i < 260; i++) await result.current.run('peer.reconnect', { peerId: 'a' }, 'first')
    await result.current.run('message.send', { peerId: 'a', text: 'Different intended message' }, 'first')
    await result.current.run('message.send', { peerId: 'b', text: 'Uncertain original' }, 'other-peer')
    await result.current.run('message.send', { peerId: 'a', text: 'Uncertain original' }, 'different-key')
  })
  expect(requests.filter(request => request.name === 'message.send')).toHaveLength(3)
  expect(result.current.error).toMatchObject({ code: 'message_resend_blocked' })
})

it('bounds uncertainty memory without evicting old guards and fails closed at capacity', async () => {
  let sends = 0
  vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
    if (path === '/api/state') return new Response(JSON.stringify(messageState))
    if (JSON.parse(init!.body as string).name === 'message.send') {
      sends++
      return new Response('{"code":"message_history_unavailable"}', { status: 507 })
    }
    return new Response('{"ok":true}')
  }))
  const { result } = renderHook(() => useServer())
  await waitFor(() => expect(result.current.auth).toBe('ready'))
  await act(async () => {
    for (let i = 0; i < MAX_UNCERTAIN_MESSAGES; i++) await result.current.run('message.send', { peerId: 'a', text: `Synthetic draft ${i}` })
    await result.current.run('message.send', { peerId: 'a', text: 'New draft after capacity' })
  })
  expect(sends).toBe(MAX_UNCERTAIN_MESSAGES)
  expect(result.current.error).toMatchObject({ code: 'message_safety_limit' })
  await act(async () => { await result.current.run('message.send', { peerId: 'a', text: 'Synthetic draft 0' }) })
  expect(sends).toBe(MAX_UNCERTAIN_MESSAGES)
  expect(result.current.error).toMatchObject({ code: 'message_resend_blocked' })
  expect(await result.current.messageBlock('a', 'Synthetic draft 0')).toBe('message_resend_blocked')
  expect(await result.current.messageBlock('b', 'New draft')).toBe('message_safety_limit')
  await act(async () => { expect(await result.current.run('peer.reconnect', { peerId: 'a' })).toEqual({ ok: true }) })
})

it('fails closed if fingerprinting is unavailable and releases the action lock', async () => {
  vi.stubGlobal('crypto', { randomUUID: () => 'synthetic-id' })
  const fetch = vi.fn(async () => new Response(JSON.stringify(messageState)))
  vi.stubGlobal('fetch', fetch)
  const { result } = renderHook(() => useServer())
  await waitFor(() => expect(result.current.auth).toBe('ready'))
  await act(async () => { await result.current.run('message.send', { peerId: 'a', text: 'Keep local' }) })
  expect(fetch).toHaveBeenCalledTimes(1)
  expect(result.current.error).toMatchObject({ code: 'message_safety_unavailable' })
  expect(result.current.busy.size).toBe(0)
})

it('does not allow simultaneous uncertain sends to bypass the guard with different action keys', async () => {
  let finish!: (response: Response) => void
  const pending = new Promise<Response>(resolve => { finish = resolve })
  const fetch = vi.fn((path: string) => path === '/api/state' ? Promise.resolve(new Response(JSON.stringify(messageState))) : pending)
  vi.stubGlobal('fetch', fetch)
  const { result } = renderHook(() => useServer())
  await waitFor(() => expect(result.current.auth).toBe('ready'))
  let first!: Promise<unknown>
  let second!: Promise<unknown>
  act(() => {
    first = result.current.run('message.send', { peerId: 'a', text: 'Same intent' }, 'one')
    second = result.current.run('message.send', { peerId: 'a', text: 'Same intent' }, 'two')
  })
  await waitFor(() => expect(fetch.mock.calls.filter(([path]) => path === '/api/command')).toHaveLength(1))
  await act(async () => { finish(new Response('{"code":"message_peer_storage_unavailable"}', { status: 507 })); await Promise.all([first, second]) })
  await act(async () => { await result.current.run('message.send', { peerId: 'a', text: 'Same intent' }, 'three') })
  expect(fetch.mock.calls.filter(([path]) => path === '/api/command')).toHaveLength(1)
})

it.each(['network_error', 'invalid_response'])('preserves %s retry identity for messages and other commands', async code => {
  const requests: { name: string; requestId: string }[] = []
  vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
    if (path === '/api/state') return new Response(JSON.stringify(messageState))
    requests.push(JSON.parse(init!.body as string))
    if (requests.length % 2) {
      if (code === 'network_error') throw new TypeError('Synthetic interruption')
      return new Response('invalid synthetic JSON')
    }
    return new Response('{"ok":true}')
  }))
  const { result } = renderHook(() => useServer())
  await waitFor(() => expect(result.current.auth).toBe('ready'))
  await act(async () => {
    await result.current.run('message.send', { peerId: 'a', text: 'Reviewable draft' }, 'message:a')
    await result.current.run('message.send', { peerId: 'a', text: 'Reviewable draft' }, 'message:a')
    await result.current.run('peer.reconnect', { peerId: 'a' })
    await result.current.run('peer.reconnect', { peerId: 'a' })
  })
  expect(requests).toHaveLength(4)
  expect(requests[0].requestId).toBe(requests[1].requestId)
  expect(requests[2].requestId).toBe(requests[3].requestId)
})

it.each(['network_error', 'invalid_response'])('never retains or replays failed favorites on %s, while preserving other retry identities', async code => {
  const requests: { name: string; requestId: string }[] = []
  vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
    if (path === '/api/state') return new Response(JSON.stringify(messageState))
    requests.push(JSON.parse(init!.body as string))
    if (code === 'network_error') throw new TypeError('Synthetic interruption')
    return new Response('invalid synthetic JSON')
  }))
  const { result, unmount } = renderHook(() => useServer())
  await waitFor(() => expect(result.current.auth).toBe('ready'))
  await act(async () => {
    // A preference request using the same UI key must neither overwrite nor
    // delete an existing non-favorite uncertainty guard.
    await result.current.run('peer.reconnect', { peerId: 'a' }, 'shared-key')
    for (let i = 0; i < 40; i++) {
      const key = i < 20 ? 'shared-key' : `favorites:one-shot-${i}`
      await result.current.run('favorites.list', {}, key)
      await result.current.run('favorites.add', { reference: { kind: 'service', serviceId: 'sample-service' }, expectedRevision: 'a'.repeat(64) }, key)
      await result.current.run('favorites.remove', { reference: { kind: 'group', groupName: 'Sample_Set' }, expectedRevision: 'b'.repeat(64) }, key)
    }
    await result.current.run('peer.reconnect', { peerId: 'a' }, 'shared-key')
  })
  const favorites = requests.filter(request => request.name.startsWith('favorites.'))
  expect(favorites).toHaveLength(120)
  expect(new Set(favorites.map(request => request.requestId)).size).toBe(120)
  expect(requests[0].requestId).toBe(requests.at(-1)!.requestId)
  expect(result.current.busy.size).toBe(0)
  expect(result.current.error).toMatchObject({ code })
  unmount()
})

it.each([true, false])('keeps another command guard when a favorite succeeds or fails definitively (success: %s)', async success => {
  const requests: { name: string; requestId: string }[] = []
  vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
    if (path === '/api/state') return new Response(JSON.stringify(messageState))
    const request = JSON.parse(init!.body as string)
    requests.push(request)
    if (request.name === 'peer.reconnect') throw new TypeError('Synthetic interruption')
    return success ? new Response('{"ok":true}') : new Response('{"code":"favorites_revision_conflict"}', { status: 409 })
  }))
  const { result } = renderHook(() => useServer())
  await waitFor(() => expect(result.current.auth).toBe('ready'))
  await act(async () => {
    await result.current.run('peer.reconnect', { peerId: 'a' }, 'shared-key')
    await result.current.run('favorites.list', {}, 'shared-key')
    await result.current.run('peer.reconnect', { peerId: 'a' }, 'shared-key')
  })
  expect(requests).toHaveLength(3)
  expect(requests[0].requestId).toBe(requests[2].requestId)
  expect(requests[1].requestId).not.toBe(requests[0].requestId)
})

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

it('does not return an invitation after its follow-up state refresh invalidates the session', async () => {
  let reads = 0
  const state = {csrfToken:'synthetic-csrf',self:{name:'Synthetic',status:'online'},peers:[],messages:[],transfers:[],services:[],shares:[]}
  vi.stubGlobal('fetch',vi.fn((path:string)=> {
    if(path==='/api/state') return Promise.resolve(++reads===1 ? new Response(JSON.stringify(state)) : new Response('{"code":"unauthenticated"}',{status:401}))
    return Promise.resolve(new Response(JSON.stringify({ok:true,result:{invitation:'synthetic-test-capability'}})))
  }))
  const {result}=renderHook(()=>useServer())
  await waitFor(()=>expect(result.current.auth).toBe('ready'))
  let response:unknown
  await act(async()=>{response=await result.current.run('lan.invite',{recipientPublicKey:'a'.repeat(64),name:'Synthetic peer',ttlSeconds:300})})
  expect(result.current.auth).toBe('locked')
  expect(response).toBeUndefined()
})

it.each(['network_error', 'invalid_response'])('never retains card reads on %s or disturbs another command using the same key', async code => {
  const requests: { name: string; requestId: string }[] = []
  vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
    if (path === '/api/state') return new Response(JSON.stringify(messageState))
    requests.push(JSON.parse(init!.body as string))
    if (code === 'network_error') throw new TypeError('Synthetic interruption')
    return new Response('invalid synthetic JSON')
  }))
  const { result } = renderHook(() => useServer())
  await waitFor(() => expect(result.current.auth).toBe('ready'))
  await act(async () => {
    await result.current.run('peer.reconnect', { peerId: 'a' }, 'shared-card-key')
    for (let i = 0; i < 40; i++) {
      // Exact card-read allowlist: no unreachable read signatures or request IDs
      // remain, even when a caller reuses an unrelated mutating action key.
      const key = i < 20 ? 'shared-card-key' : `synthetic-card-read-${i}`
      await result.current.run('device-card.export', { mode: 'lan', name: 'Synthetic alias' }, key)
      await result.current.run('device-card.inspect', { card: 'soba-card1.synthetic', expectedMode: 'lan' }, key)
    }
    await result.current.run('peer.reconnect', { peerId: 'a' }, 'shared-card-key')
  })
  const reads = requests.filter(request => request.name.startsWith('device-card.'))
  expect(reads).toHaveLength(80); expect(new Set(reads.map(request => request.requestId)).size).toBe(80)
  expect(requests[0].requestId).toBe(requests.at(-1)!.requestId); expect(result.current.busy.size).toBe(0); expect(result.current.error).toMatchObject({ code, message: '' })
})
it.each([true, false])('keeps other uncertainty guards when a device-card read succeeds or fails definitively (%s)', async success => {
  const requests: { name: string; requestId: string }[] = []
  vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
    if (path === '/api/state') return new Response(JSON.stringify(messageState))
    const request = JSON.parse(init!.body as string); requests.push(request)
    if (request.name === 'peer.reconnect') throw new TypeError('Synthetic interruption')
    return success ? new Response('{"ok":true}') : new Response('{"code":"device_card_invalid","message":"synthetic-private-payload"}', { status: 400 })
  }))
  const { result } = renderHook(() => useServer())
  await waitFor(() => expect(result.current.auth).toBe('ready'))
  await act(async () => {
    await result.current.run('peer.reconnect', { peerId: 'a' }, 'shared-card-key')
    await result.current.run('device-card.export', { mode: 'lan', name: 'Synthetic alias' }, 'shared-card-key')
    await result.current.run('device-card.inspect', { card: 'soba-card1.synthetic', expectedMode: 'lan' }, 'shared-card-key')
  })
  if (!success) expect(result.current.error).toMatchObject({ code: 'device_card_invalid', message: '' })
  await act(async () => { await result.current.run('peer.reconnect', { peerId: 'a' }, 'shared-card-key') })
  expect(requests).toHaveLength(4); expect(requests[0].requestId).toBe(requests[3].requestId); expect(new Set(requests.slice(0, 3).map(request => request.requestId)).size).toBe(3)
})
