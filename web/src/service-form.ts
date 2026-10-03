import type { Network, Peer, ServiceConfigResult, ServicePayload, State } from './api'

export type ServiceMode = 'connect' | 'share'
export interface SavedServiceAction { id: string; intent: 'copy' | 'edit' }
export interface ServiceDraft {
  name: string; nameEdited: boolean; ports: string; exclusions: string; localPort: string
  protocol: 'tcp' | 'udp'; ttl: number; purpose: string; discovery: boolean; peers: string[]
  serviceId: string; backend: Network; legacyReviewed: boolean
  source?: { id: string; revision: string; backend: Network | ''; intent: 'copy' | 'edit' }
}
export const currentBackend = (state: State): Network => state.settings?.network === 'lan' ? 'lan' : 'tailnet'
export const savedRules = (state: State) => [...state.services, ...state.shares]
export function validServiceName(name: string) { return /^[\p{L}\p{N}][\p{L}\p{N}_-]{0,63}$/u.test(name) }
export function uniqueServiceName(base: string, state: State, omitId?: string) {
  const safe = base.replace(/[^\p{L}\p{N}_-]+/gu, '-').replace(/^[_-]+/, '').slice(0, 64) || 'service'
  const occupied = new Set(savedRules(state).filter(rule => rule.id !== omitId).map(rule => rule.name))
  if (!occupied.has(safe)) return safe
  for (let suffix = 2; ; suffix++) {
    const ending = `-${suffix}`
    const name = safe.slice(0, 64 - ending.length) + ending
    if (!occupied.has(name)) return name
  }
}
export function newServiceDraft(peer: Peer, mode: ServiceMode, state: State): ServiceDraft {
  return { name: uniqueServiceName(`${mode}-${peer.name}`, state), nameEdited: false, ports: '', exclusions: mode === 'share' ? '54543-54545' : '', localPort: '', protocol: 'tcp', ttl: 3600, purpose: 'generic', discovery: false, peers: [peer.id], serviceId: '', backend: currentBackend(state), legacyReviewed: false }
}
export function readServiceConfig(value: unknown, id: string, mode: ServiceMode): ServiceConfigResult {
  const record = value as ServiceConfigResult | undefined
  const config = record?.configuration
  if (!record || !config || config.id !== id || config.direction !== (mode === 'connect' ? 'forward' : 'share') ||
      !/^[0-9a-f]{64}$/.test(record.revision) || typeof record.active !== 'boolean' ||
      typeof config.name !== 'string' || !validServiceName(config.name) || !['tcp', 'udp'].includes(config.network) || typeof config.ports !== 'string' ||
      !Number.isInteger(config.ttlSeconds) || config.ttlSeconds < 1 || config.ttlSeconds > 86400 ||
      typeof config.purpose !== 'string' || typeof config.discoverable !== 'boolean' ||
      (config.backend !== undefined && !['', 'tailnet', 'lan'].includes(config.backend)) ||
      (config.excludePorts !== undefined && typeof config.excludePorts !== 'string') ||
      (config.serviceId !== undefined && typeof config.serviceId !== 'string') ||
      (config.localPort !== undefined && (!Number.isInteger(config.localPort) || config.localPort < 0 || config.localPort > 65535)) ||
      (mode === 'connect' ? typeof config.peerId !== 'string' || !config.peerId : !Array.isArray(config.peerIds) || !config.peerIds.length || config.peerIds.some(peer => typeof peer !== 'string' || !peer))) {
    throw new Error('invalid_response')
  }
  return record
}
export function draftFromConfig(record: ServiceConfigResult, action: SavedServiceAction, state: State): ServiceDraft {
  const c = record.configuration
  return {
    name: action.intent === 'copy' ? uniqueServiceName(c.name, state) : c.name, nameEdited: action.intent === 'edit',
    ports: c.ports, exclusions: c.excludePorts || '', localPort: c.localPort ? String(c.localPort) : '', protocol: c.network,
    ttl: c.ttlSeconds, purpose: c.purpose, discovery: c.discoverable, peers: c.direction === 'forward' ? [c.peerId!] : [...c.peerIds!],
    serviceId: c.serviceId || '', backend: c.backend || currentBackend(state), legacyReviewed: false,
    source: { id: c.id, revision: record.revision, backend: c.backend || '', intent: action.intent },
  }
}
export function serviceDraftIssue(draft: ServiceDraft, state: State, mode: ServiceMode, loaded?: ServiceConfigResult): string | null {
  if (!validServiceName(draft.name.trim())) return 'invalidName'
  const source = draft.source
  if (source && (!loaded || source.revision !== loaded.revision)) return 'service_revision_conflict'
  if (source?.intent === 'edit' && (loaded?.active || savedRules(state).some(rule => rule.id === source.id && ['active', 'reconnecting'].includes(rule.status)))) return 'service_active'
  if (draft.backend !== currentBackend(state) || (source?.backend && source.backend !== currentBackend(state))) return 'service_backend_mismatch'
  if (source && !source.backend && !draft.legacyReviewed) return 'legacyBackend'
  if (savedRules(state).some(rule => rule.name === draft.name.trim() && !(source?.intent === 'edit' && rule.id === source.id))) return 'service_name_conflict'
  if (!draft.peers.length || draft.peers.some(id => !state.peers.some(peer => peer.id === id && peer.networks.includes(draft.backend)))) return 'missingPeers'
  if (mode === 'connect' && draft.peers.length !== 1) return 'missingPeers'
  return null
}
export function servicePayload(draft: ServiceDraft, mode: ServiceMode): ServicePayload {
  return {
    backend: draft.backend, name: draft.name.trim(), network: draft.protocol, ports: draft.ports,
    excludePorts: draft.exclusions || undefined, ttlSeconds: draft.ttl, purpose: draft.purpose, discoverable: draft.discovery,
    ...(mode === 'connect' ? { peerId: draft.peers[0], serviceId: draft.serviceId || undefined, localPort: draft.localPort.trim() ? Number(draft.localPort) : undefined } : { peerIds: [...draft.peers] }),
    ...(draft.source?.intent === 'edit' ? { replaceId: draft.source.id, expectedRevision: draft.source.revision } : {}),
  }
}
