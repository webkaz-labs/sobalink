import type { CommandPayloads, Network, Peer, ServiceConfigResult, ServicePayload, ServiceLifetime, Service, State } from './api'

export type ServiceMode = 'connect' | 'share'
export interface SavedServiceAction { id: string; intent: 'copy' | 'edit' }
export interface ServiceDraft {
  name: string; nameEdited: boolean; ports: string; exclusions: string; localPort: string
  protocol: 'tcp' | 'udp'; ttl: number; lifetime: ServiceLifetime; loopbackHost: '127.0.0.1' | '::1'; purpose: string; discovery: boolean; peers: string[]
  serviceId: string; serviceRevision?: string; serviceCheckedAt?: string; serviceExpiresAt?: string | null; serviceLifetime?: ServiceLifetime; backend: Network; legacyReviewed: boolean
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
  return { name: uniqueServiceName(`${mode}-${peer.name}`, state), nameEdited: false, ports: '', exclusions: mode === 'share' ? '54543-54545' : '', localPort: '', protocol: 'tcp', ttl: 3600, lifetime: mode === 'connect' ? 'until-stopped' : 'finite', loopbackHost: '127.0.0.1', purpose: 'generic', discovery: false, peers: [peer.id], serviceId: '', backend: currentBackend(state), legacyReviewed: false }
}
export function readServiceConfig(value: unknown, id: string, mode: ServiceMode): ServiceConfigResult {
  const record = value as ServiceConfigResult | undefined
  const config = record?.configuration
  if (!record || !config || config.id !== id || config.direction !== (mode === 'connect' ? 'forward' : 'share') ||
      !/^[0-9a-f]{64}$/.test(record.revision) || typeof record.active !== 'boolean' ||
      typeof config.name !== 'string' || !validServiceName(config.name) || !['tcp', 'udp'].includes(config.network) || typeof config.ports !== 'string' ||
      !validLifetime(config.lifetime || 'finite', config.ttlSeconds, mode) ||
      typeof config.purpose !== 'string' || typeof config.discoverable !== 'boolean' ||
      (config.loopbackHost !== undefined && !['', '127.0.0.1', '::1'].includes(config.loopbackHost)) ||
      (config.backend !== undefined && !['', 'tailnet', 'lan'].includes(config.backend)) ||
      (config.excludePorts !== undefined && typeof config.excludePorts !== 'string') ||
      (config.serviceId !== undefined && typeof config.serviceId !== 'string') ||
      (config.serviceRevision !== undefined && typeof config.serviceRevision !== 'string') ||
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
    ttl: c.ttlSeconds || 3600, lifetime: c.lifetime || 'finite', loopbackHost: c.loopbackHost || '127.0.0.1', purpose: c.purpose, discovery: c.discoverable, peers: c.direction === 'forward' ? [c.peerId!] : [...c.peerIds!],
    serviceId: c.serviceId || '', serviceRevision: c.serviceRevision, backend: c.backend || currentBackend(state), legacyReviewed: false,
    source: { id: c.id, revision: record.revision, backend: c.backend || '', intent: action.intent },
  }
}
export function serviceDraftIssue(draft: ServiceDraft, state: State, mode: ServiceMode, loaded?: ServiceConfigResult): string | null {
  if (!validLifetime(draft.lifetime, draft.lifetime === 'finite' ? draft.ttl : 0, mode)) return 'invalidLifetime'
  if (!['127.0.0.1', '::1'].includes(draft.loopbackHost)) return 'invalidLoopback'
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
    excludePorts: draft.exclusions || undefined, lifetime: draft.lifetime, ttlSeconds: draft.lifetime === 'finite' ? draft.ttl : 0, loopbackHost: draft.loopbackHost, localPort: draft.localPort.trim() ? Number(draft.localPort) : undefined, purpose: draft.purpose, discoverable: draft.discovery,
    ...(mode === 'connect' ? { peerId: draft.peers[0], serviceId: draft.serviceId || undefined, serviceRevision: draft.serviceId ? draft.serviceRevision : undefined } : { peerIds: [...draft.peers] }),
    ...(draft.source?.intent === 'edit' ? { replaceId: draft.source.id, expectedRevision: draft.source.revision } : {}),
  }
}

export const MAX_SERVICE_TTL_SECONDS = 9_223_372_036
export function validLifetime(lifetime: string, ttl: number, mode: ServiceMode) {
  return lifetime === 'finite' ? Number.isSafeInteger(ttl) && ttl >= 1 && ttl <= MAX_SERVICE_TTL_SECONDS : ttl === 0 && lifetime === (mode === 'connect' ? 'until-stopped' : 'until-revoked')
}
export function remainingListeners(state: State): number | undefined {
  const limit = state.limits?.effective.resources.materializedListeners
  const usage = state.limits?.usage.materializedListeners
  return limit?.mode === 'limited' && Number.isSafeInteger(limit.value) && limit.value! >= 0 && Number.isSafeInteger(usage) && usage! >= 0 ? Math.max(0, limit.value! - usage!) : undefined
}
export function loopbackEndpoint(host: string, port: string) { return `${host === '::1' ? '[::1]' : host}:${port}` }

export function serviceDefinitionPayload(draft: ServiceDraft, mode: ServiceMode): CommandPayloads['service.save'] {
  const { replaceId, expectedRevision, ...settings } = servicePayload(draft, mode)
  return { configuration: { ...settings, direction: mode === 'connect' ? 'forward' : 'share', ...(replaceId ? { id: replaceId } : {}) }, ...(expectedRevision ? { expectedRevision } : {}) }
}

export function advertisedDraft(service: Service | undefined): Partial<ServiceDraft> {
  return service ? { serviceId: service.id, serviceRevision: service.revision, serviceCheckedAt: service.checkedAt, serviceExpiresAt: service.expiresAt, serviceLifetime: service.lifetime, purpose: service.purpose || 'generic', ports: service.ports || String(service.remotePort || ''), protocol: service.network, exclusions: '' } : { serviceId: '', serviceRevision: undefined, serviceCheckedAt: undefined, serviceExpiresAt: undefined, serviceLifetime: undefined }
}
export function matchesAdvertisedDraft(draft: ServiceDraft, service: Service | undefined) {
  if (!service || service.id !== draft.serviceId || service.peerId !== draft.peers[0] || service.network !== draft.protocol || (service.ports || String(service.remotePort || '')) !== draft.ports || (service.purpose || 'generic') !== draft.purpose) return false
  if (draft.serviceLifetime && service.lifetime !== draft.serviceLifetime) return false
  if (draft.serviceExpiresAt && (!service.expiresAt || Date.parse(service.expiresAt) < Date.parse(draft.serviceExpiresAt))) return false
  return true
}
export function freshAdvertisedDraft(draft: ServiceDraft, now = Date.now()) {
  const checked = Date.parse(draft.serviceCheckedAt || '')
  return Boolean(draft.serviceRevision && Number.isFinite(checked) && checked <= now + 5_000 && now - checked <= 15_000 && (!draft.serviceExpiresAt || Date.parse(draft.serviceExpiresAt) > now))
}
