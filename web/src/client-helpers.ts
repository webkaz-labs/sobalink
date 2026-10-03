import type { ClientNotice, ClientSettingsView, RustDeskClientSettings, RustDeskSetup, RustDeskSetupReview, ServiceLifetime } from './api'
import { loopbackEndpoint, validLifetime, validServiceName } from './service-form'
const peerID = /^[a-zA-Z0-9][a-zA-Z0-9:_-]{0,127}$/
const integerPort = (value: number, min: number) => Number.isInteger(value) && value >= min && value <= 65535
export function validRustDeskSetup(value: RustDeskSetup) {
  let publicKey = false
  try { publicKey = /^[A-Za-z0-9+/]{43}=$/.test(value.publicKey) && atob(value.publicKey).length === 32 } catch { /* invalid public key */ }
  return validServiceName(`${value.name}-heartbeat`) && Boolean(value.name) && ['tailnet', 'lan'].includes(value.backend) && peerID.test(value.idPeerId) && peerID.test(value.relayPeerId) && publicKey && integerPort(value.idPort, 1025) && integerPort(value.localIdPort, 1025) && integerPort(value.relayPort, 1024) && integerPort(value.localRelayPort, 1024) && ![value.localIdPort, value.localIdPort - 1].includes(value.localRelayPort) && (value.idPeerId !== value.relayPeerId || ![value.idPort, value.idPort - 1].includes(value.relayPort)) && ['127.0.0.1', '::1'].includes(value.loopbackHost) && validLifetime(value.lifetime, value.ttlSeconds, 'connect')
}
function validNotices(value: ClientNotice[]) { return Array.isArray(value) && value.every(notice => typeof notice.code === 'string' && typeof notice.message === 'string' && typeof notice.messageJa === 'string') }
export function readRustDeskSettings(value: unknown): RustDeskClientSettings {
  const raw = value as RustDeskClientSettings
  if (!raw || typeof raw.group !== 'string' || typeof raw.publicKey !== 'string' || typeof raw.idServer !== 'string' || typeof raw.relayServer !== 'string' || raw.proxy !== '' || raw.udpEnabled !== true || raw.remoteIdSuffix !== '/r' || raw.application !== 'unverified' || !validNotices(raw.notices) || !Array.isArray(raw.roles) || raw.roles.length !== 4 || ['nat', 'id', 'heartbeat', 'relay'].some(role => raw.roles.filter(item => item.role === role).length !== 1) || raw.roles.some(role => typeof role.serviceId !== 'string' || typeof role.localEndpoint !== 'string' || typeof role.peerId !== 'string' || !['tcp', 'udp'].includes(role.network) || !integerPort(role.remotePort, 1) || typeof role.listenerReady !== 'boolean' || typeof role.status !== 'string')) throw new Error('invalid_response')
  return raw
}
export function readRustDeskReview(value: unknown, expected: RustDeskSetup): RustDeskSetupReview {
  const raw = value as RustDeskSetupReview
  if (!raw || !/^[a-f0-9]{64}$/.test(raw.revision) || typeof raw.saved !== 'boolean' || typeof raw.applied !== 'boolean' || !raw.configuration || Object.keys(expected).some(key => raw.configuration[key as keyof RustDeskSetup] !== expected[key as keyof RustDeskSetup]) || raw.group?.name !== expected.name || !Array.isArray(raw.group.serviceIds) || raw.group.serviceIds.length !== 4 || new Set(raw.group.serviceIds).size !== 4 || !Array.isArray(raw.services) || raw.services.length !== 4) throw new Error('invalid_response')
  const settings = readRustDeskSettings(raw.clientSettings)
  if (settings.group !== expected.name || settings.publicKey !== expected.publicKey || settings.idServer !== loopbackEndpoint(expected.loopbackHost, String(expected.localIdPort)) || settings.relayServer !== loopbackEndpoint(expected.loopbackHost, String(expected.localRelayPort))) throw new Error('invalid_response')
  const roles = [ ['nat', 'tcp', expected.idPeerId, expected.idPort - 1, expected.localIdPort - 1], ['id', 'tcp', expected.idPeerId, expected.idPort, expected.localIdPort], ['heartbeat', 'udp', expected.idPeerId, expected.idPort, expected.localIdPort], ['relay', 'tcp', expected.relayPeerId, expected.relayPort, expected.localRelayPort] ] as const
  for (const [role, network, peerId, remotePort, localPort] of roles) {
    const setting = settings.roles.find(item => item.role === role)!
    const service = raw.services.find(item => item.id === setting.serviceId)
    const metadataKey = `${role}ServiceId` as 'natServiceId' | 'idServiceId' | 'heartbeatServiceId' | 'relayServiceId'
    if (!service || !raw.group.serviceIds.includes(service.id) || raw.group.rustdesk?.[metadataKey] !== service.id || raw.group.rustdesk.publicKey !== expected.publicKey || service.name !== `${expected.name}-${role}` || service.direction !== 'forward' || service.purpose !== 'rustdesk' || Boolean(service.discoverable) || Boolean(service.excludePorts) || Boolean(service.serviceId) || Boolean(service.peerIds?.length) || service.backend !== expected.backend || service.network !== network || service.peerId !== peerId || service.ports !== String(remotePort) || service.localPort !== localPort || service.loopbackHost !== expected.loopbackHost || service.lifetime !== expected.lifetime || service.ttlSeconds !== expected.ttlSeconds || setting.network !== network || setting.peerId !== peerId || setting.remotePort !== remotePort || setting.localEndpoint !== loopbackEndpoint(expected.loopbackHost, String(localPort)) || !validLifetime(setting.lifetime, setting.ttlSeconds, 'connect')) throw new Error('invalid_response')
  }
  return raw
}
export function readClientSettings(value: unknown): ClientSettingsView {
  const raw = value as ClientSettingsView
  if (!raw || raw.application !== 'unverified' || !validNotices(raw.notices) || !Array.isArray(raw.services) || !Array.isArray(raw.rustdesk)) throw new Error('invalid_response')
  raw.rustdesk.forEach(readRustDeskSettings)
  const services = raw.services.map(service => ({ ...service, lifetime: (service.lifetime || (service.ttlSeconds > 0 ? 'finite' : '')) as ServiceLifetime }))
  for (const service of services) {
    if (!validLifetime(service.lifetime, service.ttlSeconds, service.direction === 'forward' ? 'connect' : 'share') || typeof service.id !== 'string' || typeof service.name !== 'string' || typeof service.localHost !== 'string' || !['forward', 'share'].includes(service.direction) || !['tcp', 'udp'].includes(service.network) || service.application !== 'unverified' || typeof service.listenerReady !== 'boolean' || service.remoteHosts !== undefined && (!Array.isArray(service.remoteHosts) || !service.remoteHosts.every(host => typeof host === 'string')) || !Array.isArray(service.remoteEndpoints) || !service.remoteEndpoints.every(endpoint => typeof endpoint === 'string') || !Array.isArray(service.mappings) || service.mappings.some(mapping => ![mapping.localFirst, mapping.localLast, mapping.remoteFirst, mapping.remoteLast].every(port => integerPort(port, 1)) || mapping.localFirst > mapping.localLast || mapping.remoteFirst > mapping.remoteLast) || !validNotices(service.notices) || service.ssh && (typeof service.ssh.command !== 'string' || typeof service.ssh.hostKeyAlias !== 'string') || service.httpCandidate !== undefined && typeof service.httpCandidate !== 'string') throw new Error('invalid_response')
  }
  return { ...raw, services }
}
