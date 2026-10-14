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

export function liveRequestSetup(mode: 'request' | 'receive' | 'outbound' = 'request', verification: 'tofu' | 'full-sha256' = 'tofu'): SetupSnapshot {
  const live = liveSetup()
  delete live.contactText; delete live.contactQR
  live.version = 2; live.alias = live.contact!.alias; live.mode = mode; live.verification = verification
  live.state = mode === 'receive' ? 'receiving' : 'selecting'
  live.contact!.version = 2; live.candidates.forEach(item => { item.contact.version = 2 })
  if (mode === 'outbound') { delete live.contact; live.candidates = []; live.discovery = { state: 'unavailable', reasonCode: 'setup_outbound_only' } }
  return live
}
export function inspectedRequestSetup(): SetupSnapshot {
  const live = liveRequestSetup(), peer = live.candidates[0].contact
  return { ...live, target: { revision: 1, sessionId: peer.sessionId, peerKey: '4'.repeat(64), alias: peer.alias, address: peer.bootstrap, expiresAt: peer.expiresAt, verification: 'tofu', keyStatus: 'new', scopePrefix: setupScope.prefix, relayAddress: '10.44.0.3:48443', relayCertificateSHA256: '3'.repeat(64) } }
}
export function reviewRequestSetup(mode: 'request' | 'receive' | 'outbound' = 'receive', verification: 'tofu' | 'full-sha256' = 'tofu'): SetupSnapshot {
  const legacy = reviewSetup(), live = liveRequestSetup(mode, verification), peer = { ...legacy.selected!, version: 2 as const }, plan = legacy.plan!, requestId = '6'.repeat(64)
  plan.verification = verification
  plan.participants[0] = { ...plan.participants[0], protocol: 2, mode, verification, allow_host: mode === 'receive', relay: mode === 'receive' ? plan.participants[0].relay : null, bootstrap: mode === 'outbound' ? '10.44.0.2:0' : plan.participants[0].bootstrap }
  plan.participants[1] = { ...plan.participants[1], protocol: 2, mode: mode === 'receive' ? 'outbound' : 'receive', verification, allow_host: mode !== 'receive', relay: mode !== 'receive' ? { address: '10.44.0.3:48443', certificate_sha256: '3'.repeat(64) } : null }
  if (mode === 'receive') { peer.bootstrap = '10.44.0.3:0'; plan.participants[1].bootstrap = peer.bootstrap }
  else { plan.hostKey = '4'.repeat(64); plan.relay.address = '10.44.0.3:48443' }
  // One shared lifetime keeps the synthetic identity/plan binding exact.
  live.expiresAt = legacy.expiresAt; if (live.contact) live.contact.expiresAt = legacy.expiresAt!
  return { ...live, selected: peer, plan, state: 'awaiting_approval', requestId, request: { requestId, peerKey: '4'.repeat(64), alias: peer.alias, address: peer.bootstrap, expiresAt: peer.expiresAt, verification, keyStatus: 'new' }, ...(mode === 'receive' ? {} : { target: { revision: 1, sessionId: peer.sessionId, peerKey: '4'.repeat(64), alias: peer.alias, address: peer.bootstrap, expiresAt: peer.expiresAt, verification, keyStatus: 'new', relayAddress: plan.relay.address, relayCertificateSHA256: plan.relay.certificateSHA256, scopePrefix: setupScope.prefix } }) }
}
