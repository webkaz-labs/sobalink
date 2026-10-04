import { describe, expect, it } from 'vitest'
import type { Peer } from './api'
import { peerCommunicationAllowed, peerPermissionKey, peerPresenceKey } from './peer-status'

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
})
