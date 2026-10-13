import { SETUP_CONTACT_PREFIX, SETUP_VERIFY_PREFIX, type SetupContact, type SetupSnapshot } from './nearby-setup'
// Synthetic addresses and identities only. This is not a real contact or key.
export const setupScope = { scopeId: 'a'.repeat(64), interfaceIndex: 2, interface: 'fixture0', localAddress: '10.44.0.2', prefix: '10.44.0.0/24' }
export const setupScopes = { kind: 'sobalink-setup-scopes', version: 1, scopes: [setupScope] }
export const contactText = (contact: SetupContact) => SETUP_CONTACT_PREFIX + btoa(String.fromCharCode(...new TextEncoder().encode(JSON.stringify(contact)))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
export function idleSetup(): SetupSnapshot { return { kind: 'sobalink-setup', version: 1, state: 'idle', candidates: [], localApproved: false, remoteApproved: false, paired: false, connected: false, trusted: false, process: { state: 'ready', profilePreserved: true, approvalsSurviveRestart: false }, discovery: { state: 'stopped' } } }
export function liveSetup(): SetupSnapshot {
  const expiresAt = new Date(Date.now() + 100000).toISOString(), contact: SetupContact = { kind: 'sobalink-setup-contact', version: 1, sessionId: 'b'.repeat(64), alias: 'Peer A', bootstrap: '10.44.0.2:48445', expiresAt }
  const peer: SetupContact = { ...contact, sessionId: 'c'.repeat(64), alias: 'Peer B', bootstrap: '10.44.0.3:48445' }
  return { ...idleSetup(), state: 'discovering', sessionId: contact.sessionId, expiresAt, scope: { ...setupScope }, contact, contactText: contactText(contact), discovery: { state: 'active' }, candidates: [{ candidateId: 'd'.repeat(64), contact: peer, observedAt: new Date().toISOString(), expiresAt: new Date(Date.now() + 30000).toISOString(), verification: 'unverified' }] }
}
export function reviewSetup(): SetupSnapshot {
  const live = liveSetup(), peer = live.candidates[0].contact
  return { ...live, state: 'awaiting_approval', selected: peer, plan: { revision: 1, fingerprint: 'e'.repeat(64), verification: 'full-sha256', verificationText: SETUP_VERIFY_PREFIX + 'e'.repeat(64), backend: 'lan', participants: [
    { session_id: live.sessionId!, key: '1'.repeat(64), alias: 'Peer A', bootstrap: live.contact!.bootstrap, scope: { local_address: setupScope.localAddress, prefix: setupScope.prefix, consent_revision: '2'.repeat(64) }, allow_host: true, existing_host: false, relay: { address: '10.44.0.2:48443', certificate_sha256: '3'.repeat(64) }, expires: live.expiresAt! },
    { session_id: peer.sessionId, key: '4'.repeat(64), alias: peer.alias, bootstrap: peer.bootstrap, scope: { local_address: '10.44.0.3', prefix: setupScope.prefix, consent_revision: '5'.repeat(64) }, allow_host: false, existing_host: false, relay: null, expires: peer.expiresAt },
  ], hostKey: '1'.repeat(64), relay: { kind: 'local', address: '10.44.0.2:48443', certificateSHA256: '3'.repeat(64) }, expiresAt: live.expiresAt! } }
}
