import { canExchange, type Peer } from './api'
import { currentRouteObservation } from './lan-routes'

// Tailnet reports presence; a LAN pairing without a recent response does not.
export function peerPresenceKey(peer: Peer, stale = false): 'online' | 'offline' | 'responseUnconfirmed' {
  if (stale) return 'responseUnconfirmed'
  return peer.online ? 'online' : peer.networks.includes('tailnet') ? 'offline' : 'responseUnconfirmed'
}

// Derive presentation only. A retained snapshot must not rewrite permission or
// transport state, and an old route must not look like a current observation.
export function peerPath(peer: Peer, now = Date.now(), stale = false): Peer['path'] {
  if (stale || !peer.online) return 'unknown'
  if (peer.route !== undefined || peer.networks.includes('lan') && !peer.networks.includes('mixed')) {
    const route = currentRouteObservation(peer.route, now)
    return route.state === 'ready' && route.path === peer.path ? route.path : 'unknown'
  }
  return peer.path === 'direct' || peer.path === 'relay' ? peer.path : 'unknown'
}

// Stored local permission is independent of discovery and identity verification.
export function peerPermissionKey(peer: Peer): 'paused' | 'trusted' | 'notTrusted' {
  return peer.autosave?.paused ? 'paused' : peer.trusted ? 'trusted' : 'notTrusted'
}

// This is local readiness for files/messages, not confirmation of remote acceptance.
export function peerCommunicationAllowed(peer: Peer): boolean {
  return canExchange(peer)
}
