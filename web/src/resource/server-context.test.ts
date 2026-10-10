import { act, renderHook, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { ApiError, command, type State } from '../api'
import { useServer } from '../useServer'
import { deferred, harness, localPreview, remotePreview, remoteSelection, selectRemote, settings, target } from './fixtures.test-support'

const state: State = { csrfToken: 'synthetic-server-csrf', processId: 123, self: { name: 'Synthetic local', status: 'online' }, peers: [], services: [], shares: [], messages: [], transfers: [], settings: { network: 'direct-lan' }, directLAN: { configured: true, listenerReady: true, publicKey: 'a'.repeat(64), peers: [{ key: remoteSelection.peerKey, name: 'Synthetic remote', endpoint: '192.0.2.20:40000' }] } }
// Typecheck-only contract. This function is deliberately never called or
// exported; invalid examples cause no request or runtime test side effect.
function checkResourceCommandTypes() {
  void command('resource.inspect', target)
  void command('resource.apply', { ...target, operationId: localPreview.operationId, baseRevision: localPreview.baseRevision, revision: localPreview.revision, settings })
  const request = { ...remoteSelection.selector, protocolVersion: 2 as const, action: 'apply' as const, apply: { operationId: remotePreview.operationId, baseRevision: remotePreview.baseRevision, reviewRevision: remotePreview.reviewRevision, settings } }
  void command('resource.remote.management.apply', { peerKey: remoteSelection.peerKey, request, confirm: true })
  // @ts-expect-error Local inspect cannot carry settings or become preview.
  void command('resource.inspect', { ...target, settings })
  // @ts-expect-error Local apply cannot use the remote reviewRevision shape.
  void command('resource.apply', { ...target, operationId: localPreview.operationId, baseRevision: localPreview.baseRevision, reviewRevision: remotePreview.reviewRevision, settings })
  // @ts-expect-error A status-shaped payload cannot be sent as local apply.
  void command('resource.apply', { ...target, operationId: localPreview.operationId })
  // @ts-expect-error Remote apply requires explicit confirmation true.
  void command('resource.remote.management.apply', { peerKey: remoteSelection.peerKey, request, confirm: false })
  // @ts-expect-error The command name must match the request action.
  void command('resource.remote.management.apply', { peerKey: remoteSelection.peerKey, request: { ...remoteSelection.selector, protocolVersion: 2, action: 'inspect' }, confirm: true })
}
describe('fixed resource context observation in useServer', () => {
  it('invalidates synchronously at auth loss before React state catches up without changing the Server shape', async () => {
    const { controller, calls } = harness()
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(state))))
    const hook = renderHook(() => useServer(controller))
    await waitFor(() => expect(hook.result.current.auth).toBe('ready'))
    await act(async () => { await selectRemote(controller); await controller.preview() })
    act(() => {
      hook.result.current.handleError(new ApiError('unauthenticated', ''))
      expect(controller.getSnapshot()).toMatchObject({ authenticated: false, available: false, selection: null, review: null })
    })
    await controller.confirmApply()
    expect(calls.some(call => call.name.endsWith('.apply'))).toBe(false)
    expect(Object.keys(hook.result.current).sort()).toEqual(['state', 'auth', 'stale', 'error', 'setError', 'busy', 'refresh', 'run', 'updatedAt', 'handleError', 'messageBlock', 'messageGuardRevision'].sort())
  })
  it('mirrors stale-read failure immediately and hides context on hook unmount', async () => {
    const { controller } = harness()
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify(state)))
    vi.stubGlobal('fetch', fetch)
    const hook = renderHook(() => useServer(controller))
    await waitFor(() => expect(hook.result.current.auth).toBe('ready'))
    fetch.mockRejectedValue(new TypeError('Synthetic state failure'))
    await act(async () => { await hook.result.current.refresh() })
    expect(controller.getSnapshot()).toMatchObject({ authenticated: true, available: false, selection: null })
    hook.unmount()
    expect(controller.getSnapshot()).toMatchObject({ authenticated: false, available: false, peers: [] })
  })
  it('ignores late successful state after auth loss without resurrecting resource context', async () => {
    const { controller } = harness(), pendingState = deferred<Response>()
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify(state)))
    vi.stubGlobal('fetch', fetch)
    const hook = renderHook(() => useServer(controller))
    await waitFor(() => expect(hook.result.current.auth).toBe('ready'))
    fetch.mockImplementation(() => pendingState.promise)
    let refresh!: Promise<State | null>
    act(() => { refresh = hook.result.current.refresh(); hook.result.current.handleError(new ApiError('unauthenticated', '')) })
    await act(async () => { pendingState.resolve(new Response(JSON.stringify(state))); await refresh })
    expect(hook.result.current.auth).toBe('locked')
    expect(controller.getSnapshot()).toMatchObject({ authenticated: false, available: false, selection: null })
  })
  it('does not publish or return accepted state after reentrant auth loss during a successful observation', async () => {
    const { controller } = harness()
    const fetch = vi.fn().mockImplementation(async () => new Response(JSON.stringify(state)))
    vi.stubGlobal('fetch', fetch)
    const hook = renderHook(() => useServer(controller))
    await waitFor(() => expect(hook.result.current.auth).toBe('ready'))
    let invalidated = false
    const unsubscribe = controller.subscribe(() => {
      if (invalidated || !controller.getSnapshot().authenticated) return
      invalidated = true
      hook.result.current.handleError(new ApiError('unauthenticated', ''))
    })
    fetch.mockImplementation(async () => new Response(JSON.stringify({ ...state, processId: 456 })))
    await act(async () => { expect(await hook.result.current.refresh(true)).toBeNull() })
    unsubscribe()
    expect(invalidated).toBe(true)
    expect(hook.result.current).toMatchObject({ auth: 'locked', state: null, stale: false, error: null })
    expect(controller.getSnapshot()).toMatchObject({ authenticated: false, available: false, selection: null })
  })
  it('does not overwrite locked state or publish a stale error after reentrant auth loss during failed observation', async () => {
    const { controller } = harness()
    const fetch = vi.fn().mockImplementation(async () => new Response(JSON.stringify(state)))
    vi.stubGlobal('fetch', fetch)
    const hook = renderHook(() => useServer(controller))
    await waitFor(() => expect(hook.result.current.auth).toBe('ready'))
    let invalidated = false
    const unsubscribe = controller.subscribe(() => {
      if (invalidated || !controller.getSnapshot().authenticated || controller.getSnapshot().available) return
      invalidated = true
      hook.result.current.handleError(new ApiError('unauthenticated', ''))
    })
    fetch.mockRejectedValue(new TypeError('Synthetic stale failure'))
    await act(async () => { expect(await hook.result.current.refresh(true)).toBeNull() })
    unsubscribe()
    expect(invalidated).toBe(true)
    expect(hook.result.current).toMatchObject({ auth: 'locked', state: null, stale: false, error: null })
    expect(controller.getSnapshot()).toMatchObject({ authenticated: false, available: false, selection: null })
  })
})
