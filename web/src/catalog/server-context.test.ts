import { act, renderHook, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { ApiError, command } from '../api'
import { useServer } from '../useServer'
import { harness } from '../resource/fixtures.test-support'
import { ResourceCatalogController } from './controller'
import { appState, fixtureTransport } from './fixtures.test-support'
// Uncalled typecheck-only assertions preserve the closed command contract.
function checkCatalogCommandTypes() {
  void command('resource.catalog.snapshot', { schemaVersion: 1, sources: [{ kind: 'local_service' }] })
  // @ts-expect-error A catalog request cannot dispatch an arbitrary workflow.
  void command('resource.catalog.snapshot', { schemaVersion: 1, sources: [{ kind: 'local_service' }], action: 'apply' })
  // @ts-expect-error A remote source must supply the exact selector arm.
  void command('resource.catalog.snapshot', { schemaVersion: 1, sources: [{ kind: 'remote_settings_v2', peerKey: 'synthetic' }] })
}
describe('ordered catalog and settings session observation', () => {
  it('does not deliver older ready context to catalog after a W1 subscriber recursively clears authentication', async () => {
    const resource = harness().controller, fixture = fixtureTransport(), catalog = new ResourceCatalogController(fixture.transport, () => {})
    const fetch = vi.fn(async () => new Response(JSON.stringify(appState))); vi.stubGlobal('fetch', fetch)
    const hook = renderHook(() => useServer(resource, catalog)); await waitFor(() => expect(hook.result.current.auth).toBe('ready'))
    let invalidated = false
    const unsubscribe = resource.subscribe(() => { if (!invalidated && resource.getSnapshot().authenticated) { invalidated = true; hook.result.current.handleError(new ApiError('unauthenticated', '')) } })
    fetch.mockImplementation(async () => new Response(JSON.stringify({ ...appState, processId: 200 })))
    await act(async () => { expect(await hook.result.current.refresh()).toBeNull() }); unsubscribe()
    expect(hook.result.current.auth).toBe('locked'); expect(catalog.getSnapshot().context.available).toBe(false); expect(resource.getSnapshot().available).toBe(false); expect(fixture.calls).toEqual([])
  })
  it('does not publish success after a catalog subscriber recursively clears authentication', async () => {
    const resource = harness().controller, fixture = fixtureTransport(), catalog = new ResourceCatalogController(fixture.transport, () => {})
    const fetch = vi.fn(async () => new Response(JSON.stringify(appState))); vi.stubGlobal('fetch', fetch)
    const hook = renderHook(() => useServer(resource, catalog)); await waitFor(() => expect(hook.result.current.auth).toBe('ready'))
    let invalidated = false
    const unsubscribe = catalog.subscribe(() => { if (!invalidated && catalog.getSnapshot().context.available) { invalidated = true; hook.result.current.handleError(new ApiError('unauthenticated', '')) } })
    fetch.mockImplementation(async () => new Response(JSON.stringify({ ...appState, resourceCatalogProcessId: '9'.repeat(64) })))
    await act(async () => { expect(await hook.result.current.refresh()).toBeNull() }); unsubscribe()
    expect(hook.result.current.state).toBeNull(); expect(catalog.getSnapshot().context.available).toBe(false); expect(resource.getSnapshot().available).toBe(false)
  })
  it('invalidates both observers immediately on stale reads and unmount', async () => {
    const resource = harness().controller, fixture = fixtureTransport(), catalog = new ResourceCatalogController(fixture.transport, () => {})
    const fetch = vi.fn(async () => new Response(JSON.stringify(appState))); vi.stubGlobal('fetch', fetch)
    const hook = renderHook(() => useServer(resource, catalog)); await waitFor(() => expect(hook.result.current.auth).toBe('ready'))
    fetch.mockRejectedValue(new TypeError('Synthetic state failure')); await act(async () => { await hook.result.current.refresh() })
    expect(catalog.getSnapshot().context.available).toBe(false); expect(resource.getSnapshot().available).toBe(false); hook.unmount(); expect(catalog.getSnapshot().candidate).toBeNull()
  })
})
