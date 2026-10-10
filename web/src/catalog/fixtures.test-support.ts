import type { State } from '../api'
import { descriptor, target, settings, effective, remoteSelection } from '../resource/fixtures.test-support'
import type { CatalogContext } from './context'
import type { CatalogRequest, CatalogRow, CatalogTransport, SourceRequest, SourceView } from './types'
export { descriptor, target, settings, effective, remoteSelection }
export const processId = 'e'.repeat(64), epoch = 'f'.repeat(64), checkedAt = 1700000000000
export const context: CatalogContext = { available: true, revision: 'synthetic-context-1', processId, managedDirectLAN: true, peers: [{ id: remoteSelection.peerKey, name: 'Synthetic peer' }], managedKeys: [remoteSelection.peerKey], budget: { rows: 128, bytes: 1024 * 1024 } }
export const localRequest: CatalogRequest = { schemaVersion: 1, sources: [{ kind: 'local_service' }, { kind: 'local_settings', target }, { kind: 'transfer_activity' }] }
export const savedRow: CatalogRow = { identity: { id: 'saved-synthetic', lifetime: 'persistent', direction: '' }, localService: { name: 'Synthetic service', direction: 'forward', network: 'tcp', ports: '8080', lifetime: 'until-stopped', state: 'saved', application: 'unverified' } }
export const transferRow: CatalogRow = { identity: { id: 'batch-synthetic', lifetime: 'process', direction: 'incoming' }, transferActivity: { peerId: remoteSelection.peerKey, totalBytes: 100, completedBytes: 50, state: 'transferring' } }
export const sharedRow: CatalogRow = { identity: { id: 'activation-synthetic', lifetime: 'activation', direction: '' }, remoteService: { purpose: 'web', network: 'tcp', ports: '8080', lifetime: 'until-revoked', expiresAt: 0, application: 'unverified', reviewRevision: 'synthetic-existing-discovery-review' } }
export function sourceFor(source: SourceRequest): SourceView {
  const remote = source.kind === 'remote_settings_v1' || source.kind === 'remote_settings_v2'
  const selection = { sourceId: source.kind, kind: source.kind, epoch, peerKey: remote ? source.peerKey : source.kind === 'remote_service' ? source.peerId : '', target: 'target' in source ? source.target : { schemaVersion: 0 as const, resourceId: '' as const }, grantId: remote ? source.grantId : '', grantRevision: remote ? source.grantRevision : 0, processId: source.kind === 'transfer_activity' ? processId : '' }
  const row: CatalogRow = source.kind === 'local_settings' ? { identity: { id: target.resourceId, lifetime: 'persistent', direction: '' }, localSettings: descriptor }
    : source.kind === 'remote_settings_v1' ? { identity: { id: target.resourceId, lifetime: 'persistent', direction: '' }, remoteSettingsV1: { requested: settings, effective } }
    : source.kind === 'remote_settings_v2' ? { identity: { id: target.resourceId, lifetime: 'persistent', direction: '' }, remoteSettingsV2: { requested: settings, effective } }
    : source.kind === 'local_service' ? savedRow : source.kind === 'remote_service' ? sharedRow : transferRow
  return { selection, state: source.kind === 'remote_service' ? 'stale' : 'current', checkedAt, revision: 'd'.repeat(64), complete: true, total: 1, rows: [row] }
}
export function responseFor(request: CatalogRequest = localRequest) {
  return { schemaVersion: 1 as const, limits: { maxSources: 5, maxRemoteTargets: 1, maxRows: 128, maxPageRows: 128, maxPages: 1, maxBytes: 1024 * 1024 - 300, maxPageBytes: 1024 * 1024 - 300, maxStringBytes: 1024 * 1024 - 300 }, snapshot: { schemaVersion: 1 as const, id: epoch, scopeId: 'a'.repeat(64), revision: 'b'.repeat(64), complete: true, sources: [...request.sources].sort((a, b) => a.kind < b.kind ? -1 : 1).map(sourceFor) } }
}
export function fixtureTransport() {
  const calls: { name: string; request?: CatalogRequest; signal: AbortSignal }[] = []
  const transport: CatalogTransport = { list: async signal => { calls.push({ name: 'resource.list', signal }); return { schemaVersion: 1, resources: [descriptor] } }, snapshot: async (request, signal) => { calls.push({ name: 'resource.catalog.snapshot', request, signal }); return responseFor(request) } }
  return { calls, transport }
}
export const appState: State = { csrfToken: 'synthetic-catalog-csrf', processId: 100, resourceCatalogProcessId: processId, self: { name: 'Synthetic local', status: 'online' }, peers: [{ id: remoteSelection.peerKey, name: 'Synthetic peer', networks: ['direct-lan'], online: true, verified: true, trusted: true, bridge: true, path: 'direct' }], services: [], shares: [], messages: [], transfers: [{ id: 'batch-synthetic', peerId: remoteSelection.peerKey, name: 'Synthetic batch', direction: 'incoming', status: 'transferring', totalBytes: 100, completedBytes: 50, createdAt: '2023-11-14T22:13:20Z', entries: [] }], settings: { network: 'direct-lan' }, directLAN: { configured: true, listenerReady: true, publicKey: 'c'.repeat(64), peers: [{ key: remoteSelection.peerKey, name: 'Synthetic peer', endpoint: '192.0.2.25:40000' }] } }
