import { describe, expect, it } from 'vitest'
import type { Peer } from './api'
import { peerCommunicationAllowed, peerPermissionKey, peerPresenceKey, peerPath } from './peer-status'

const peer: Peer = { id: 'fixture', name: 'Example device', networks: ['lan'], online: true, bridge: true, trusted: true, verified: true, path: 'unknown' }

describe('independent device observations and permission', () => {
  it('does not infer LAN offline from an unanswered device', () => {
    expect(peerPresenceKey({ ...peer, online: false })).toBe('responseUnconfirmed')
    expect(peerPresenceKey({ ...peer, networks: ['tailnet'], online: false })).toBe('offline')
    expect(peerPresenceKey(peer)).toBe('online')
  })
  it('retains stored permission independently of discovery or verification', () => {
    expect(peerPermissionKey({ ...peer, bridge: false, verified: false })).toBe('trusted')
    expect(peerPermissionKey({ ...peer, trusted: false })).toBe('notTrusted')
    expect(peerPermissionKey({ ...peer, autosave: { enabled: true, paused: true } })).toBe('paused')
  })
  it.each(['online', 'bridge', 'trusted', 'verified'] as const)('keeps local exchange unavailable without %s', key => {
    expect(peerCommunicationAllowed({ ...peer, [key]: false })).toBe(false)
  })
  it('keeps paused exchange unavailable and allows only a fully ready peer', () => {
    expect(peerCommunicationAllowed({ ...peer, autosave: { enabled: false, paused: true } })).toBe(false)
    expect(peerCommunicationAllowed(peer)).toBe(true)
  })
  it('keeps stale observations unknown without changing stored permission', () => {
    const snapshot = { ...peer, path: 'direct' as const }
    expect(peerPath(snapshot, Date.now(), true)).toBe('unknown')
    expect(peerPresenceKey(snapshot, true)).toBe('responseUnconfirmed')
    expect(peerPermissionKey(snapshot)).toBe('trusted')
    expect(snapshot.path).toBe('direct')
    expect(snapshot.online).toBe(true)
  })
  const now = Date.parse('2026-10-07T02:00:00Z')
  const route = { state: 'ready' as const, path: 'direct' as const, observedAt: new Date(now).toISOString() }
  it.each([
    ['missing', undefined], ['aged', { ...route, observedAt: new Date(now - 30001).toISOString() }],
    ['future', { ...route, observedAt: new Date(now + 5001).toISOString() }],
    ['invalid', { ...route, observedAt: 'invalid' }], ['undated', { ...route, observedAt: undefined }],
    ['expired', { ...route, expires: new Date(now).toISOString() }],
    ['contradictory', { ...route, path: 'relay' as const }],
    ['not ready', { ...route, state: 'reconnecting' as const }],
  ])('keeps a %s LAN route unknown', (_, observation) => {
    expect(peerPath({ ...peer, path: 'direct', route: observation }, now)).toBe('unknown')
  })
  it('uses fresh matching observations and never infers a route from readiness', () => {
    expect(peerPath({ ...peer, path: 'direct', route }, now)).toBe('direct')
    expect(peerPath({ ...peer, path: 'unknown', route }, now)).toBe('unknown')
    expect(peerPath({ ...peer, online: false, path: 'direct', route }, now)).toBe('unknown')
    expect(peerPath({ ...peer, networks: ['tailnet'], path: 'relay' }, now)).toBe('relay')
  })
})
