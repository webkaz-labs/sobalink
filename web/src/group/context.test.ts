import { describe, expect, it } from 'vitest'
import type { State } from '../api'
import type { ResourceSessionObservation } from '../resource/app-context'
import { groupContextFromSession } from './context'

const ownKey = 'f'.repeat(64), peerKey = '1'.repeat(64), boot = 'a'.repeat(64)
const state: State = {
  csrfToken: 'synthetic-group-context', processId: 123, resourceCatalogProcessId: boot,
  self: { name: 'Synthetic local device', status: 'online' }, peers: [], services: [], shares: [], messages: [], transfers: [],
  settings: { network: 'direct-lan' },
  directLAN: { configured: true, listenerReady: true, publicKey: ownKey, peers: [{ key: peerKey, name: 'Synthetic remote device', endpoint: '192.0.2.22:40000' }, { key: ownKey, name: 'Synthetic own device', endpoint: '192.0.2.23:40000' }] },
}
const observation: ResourceSessionObservation = { state, authenticated: true, stale: false, authEpoch: 1 }

describe('fixed-group local and remote context projection', () => {
  it('separates local history freshness from remote readiness and excludes the local peer', () => {
    const result = groupContextFromSession(observation)
    expect(result).toMatchObject({ localAvailable: true, remoteAvailable: true, localPeerKey: ownKey, peers: [{ key: peerKey, name: 'Synthetic remote device' }] })
    expect(Object.keys(result.peers[0])).toEqual(['key', 'name'])
    expect(JSON.stringify(result)).not.toContain('192.0.2.22')
    expect(Object.isFrozen(result)).toBe(true); expect(Object.isFrozen(result.peers)).toBe(true)
    const offline = groupContextFromSession({ ...observation, state: { ...state, directLAN: { ...state.directLAN!, listenerReady: false } } })
    expect(offline).toMatchObject({ localAvailable: true, remoteAvailable: false, peers: [] })
    const unselected = groupContextFromSession({ ...observation, state: { ...state, settings: { network: 'none' } } })
    expect(unselected).toMatchObject({ localAvailable: true, remoteAvailable: false, peers: [] })
  })
  it('fails closed for authentication stale boot process and malformed peer observations', () => {
    for (const update of [{ authenticated: false }, { stale: true }, { authEpoch: -1 }, { state: null }]) {
      expect(groupContextFromSession({ ...observation, ...update })).toMatchObject({ localAvailable: false, remoteAvailable: false, peers: [] })
    }
    for (const processId of [undefined, 0, -1, 1.5, NaN, Infinity, Number.MAX_SAFE_INTEGER + 1]) expect(groupContextFromSession({ ...observation, state: { ...state, processId } }).localAvailable).toBe(false)
    for (const resourceCatalogProcessId of [undefined, '', 'A'.repeat(64), '0'.repeat(63)]) expect(groupContextFromSession({ ...observation, state: { ...state, resourceCatalogProcessId } }).localAvailable).toBe(false)
    const duplicate = state.directLAN!.peers![0]
    expect(groupContextFromSession({ ...observation, state: { ...state, directLAN: { ...state.directLAN!, peers: [duplicate, duplicate] } } }).peers).toEqual([])
  })
  it('changes epoch for authority membership or boot changes but preserves name route and order-only polls', () => {
    const previous = groupContextFromSession(observation)
    const mutations: ResourceSessionObservation[] = [
      { ...observation, authEpoch: 2 }, { ...observation, state: { ...state, processId: 456 } },
      { ...observation, state: { ...state, resourceCatalogProcessId: 'b'.repeat(64) } },
      { ...observation, state: { ...state, directLAN: { ...state.directLAN!, publicKey: 'e'.repeat(64) } } },
      { ...observation, state: { ...state, directLAN: { ...state.directLAN!, peers: [] } } },
      { ...observation, state: { ...state, directLAN: { ...state.directLAN!, listenerReady: false } } },
    ]
    for (const next of mutations) expect(groupContextFromSession(next).revision).not.toBe(previous.revision)
    const renamed = groupContextFromSession({ ...observation, state: { ...state, directLAN: { ...state.directLAN!, peers: state.directLAN!.peers!.map(peer => ({ ...peer, name: 'Renamed synthetic device', endpoint: '192.0.2.24:40000' })).reverse() } } })
    expect(renamed.revision).toBe(previous.revision)
    expect(groupContextFromSession({ ...observation, state: { ...state } }).revision).toBe(previous.revision)
  })
})
