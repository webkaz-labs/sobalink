import { describe, expect, it } from 'vitest'
import type { State } from '../api'
import { resourceContextFromSession, type ResourceSessionObservation } from './app-context'
import { harness, remoteSelection, selectRemote } from './fixtures.test-support'

const state: State = {
  csrfToken: 'synthetic-context-csrf', processId: 123,
  self: { name: 'Synthetic local device', status: 'online' }, peers: [], services: [], shares: [], messages: [], transfers: [],
  settings: { network: 'direct-lan' },
  directLAN: { configured: true, listenerReady: true, publicKey: 'a'.repeat(64), peers: [{ key: remoteSelection.peerKey, name: 'Synthetic remote', endpoint: '192.0.2.20:40000' }] },
}
const observation: ResourceSessionObservation = { state, authenticated: true, stale: false, authEpoch: 1 }
describe('resource App session projection', () => {
  it('projects only exact saved keys and display names without endpoints or incoming permissions', () => {
    const value = resourceContextFromSession(observation)
    expect(value).toMatchObject({ authenticated: true, processId: 123, managedDirectLAN: true, peers: [{ key: remoteSelection.peerKey, name: 'Synthetic remote' }] })
    expect(Object.keys(value.peers[0])).toEqual(['key', 'name'])
    expect(JSON.stringify(value)).not.toContain('192.0.2.20')
    expect(Object.isFrozen(value)).toBe(true)
    expect(Object.isFrozen(value.peers[0])).toBe(true)
  })
  it.each([undefined, 0, -1, 1.5, NaN, Infinity, Number.MAX_SAFE_INTEGER + 1])('fails closed for missing or unsafe process identity %s', processId => {
    expect(resourceContextFromSession({ ...observation, state: { ...state, processId } })).toMatchObject({ processId: null, managedDirectLAN: false, peers: [] })
  })
  it('disables controls for stale or unauthenticated observations and invalid auth epochs', () => {
    expect(resourceContextFromSession({ ...observation, stale: true })).toMatchObject({ authenticated: true, processId: null, managedDirectLAN: false, peers: [] })
    expect(resourceContextFromSession({ ...observation, authenticated: false })).toMatchObject({ authenticated: false, processId: null, managedDirectLAN: false, peers: [] })
    expect(resourceContextFromSession({ ...observation, authEpoch: -1 })).toMatchObject({ authenticated: false, processId: null })
    expect(resourceContextFromSession({ ...observation, state: null })).toMatchObject({ authenticated: false, processId: null })
  })
  it.each(['lan', 'tailnet', 'mixed', 'none'] as const)('never treats %s or peer trust as selected DirectLAN authority', network => {
    const value = resourceContextFromSession({ ...observation, state: { ...state, settings: { network }, peers: [{ id: remoteSelection.peerKey, name: 'Trusted synthetic device', trusted: true, verified: true, bridge: true, online: true, networks: ['direct-lan'], path: 'direct' }] } })
    expect(value.managedDirectLAN).toBe(false)
    expect(value.peers).toEqual([])
    expect(value.processId).toBe(123)
  })
  it('rejects malformed, duplicate and oversized saved-key inputs rather than partially selecting them', () => {
    const peer = state.directLAN!.peers![0]
    for (const peers of [[{ ...peer, key: 'display-name' }], [peer, peer], [{ ...peer, name: 'x'.repeat(257) }], Array.from({ length: 257 }, () => peer)]) {
      expect(resourceContextFromSession({ ...observation, state: { ...state, directLAN: { ...state.directLAN!, peers } } }).peers).toEqual([])
    }
    expect(resourceContextFromSession({ ...observation, state: { ...state, directLAN: { ...state.directLAN!, publicKey: 'invalid' } } }).managedDirectLAN).toBe(false)
  })
  it('changes identity hints for auth epoch, selected mode and local key, never for display names or routes', () => {
    const original = resourceContextFromSession(observation)
    for (const next of [
      { ...observation, authEpoch: 2 },
      { ...observation, state: { ...state, settings: { network: 'mixed' as const } } },
      { ...observation, state: { ...state, directLAN: { ...state.directLAN!, publicKey: 'c'.repeat(64) } } },
    ]) expect(resourceContextFromSession(next).selectionRevision).not.toBe(original.selectionRevision)
    expect(resourceContextFromSession({ ...observation, state: { ...state, directLAN: { ...state.directLAN!, peers: [{ ...state.directLAN!.peers![0], name: 'Renamed synthetic device', endpoint: '192.0.2.21:40000' }] } } }).selectionRevision).toBe(original.selectionRevision)
  })
  it('preserves a review across unchanged polling and display-only changes but invalidates on peer-key change', async () => {
    const { controller, calls } = harness()
    controller.updateContext(resourceContextFromSession(observation))
    await selectRemote(controller); await controller.preview()
    const review = controller.getSnapshot().review
    controller.updateContext(resourceContextFromSession({ ...observation, state: { ...state } }))
    controller.updateContext(resourceContextFromSession({ ...observation, state: { ...state, directLAN: { ...state.directLAN!, peers: [{ ...state.directLAN!.peers![0], name: 'Renamed synthetic device' }] } } }))
    expect(controller.getSnapshot().review).toBe(review)
    expect(calls).toHaveLength(2)
    controller.updateContext(resourceContextFromSession({ ...observation, state: { ...state, directLAN: { ...state.directLAN!, peers: [{ ...state.directLAN!.peers![0], key: 'd'.repeat(64) }] } } }))
    expect(controller.getSnapshot().review).toBeNull()
    expect(controller.getSnapshot().selection).toBeNull()
    expect(calls).toHaveLength(2)
  })
})
