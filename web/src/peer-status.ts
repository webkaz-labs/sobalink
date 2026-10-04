import { canExchange, type Peer } from './api'

// Tailnet reports presence; a LAN pairing without a recent response does not.
export function peerPresenceKey(peer: Peer): 'online' | 'offline' | 'responseUnconfirmed' {
  return peer.online ? 'online' : peer.networks.includes('tailnet') ? 'offline' : 'responseUnconfirmed'
}

// Stored local permission is independent of discovery and identity verification.
export function peerPermissionKey(peer: Peer): 'paused' | 'trusted' | 'notTrusted' {
  return peer.autosave?.paused ? 'paused' : peer.trusted ? 'trusted' : 'notTrusted'
}

// This is local readiness for files/messages, not confirmation of remote acceptance.
export function peerCommunicationAllowed(peer: Peer): boolean {
  return canExchange(peer)
}
