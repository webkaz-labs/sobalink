import type { SavedProxyList, SavedProxyView, StartupList, StartupReview } from './api'
import { validProxyScope } from './advanced-connections'
export const reviewRevision = (value: unknown) => typeof value === 'string' && /^[a-f0-9]{64}$/.test(value)
export function readSavedProxy(value: unknown): SavedProxyView {
  const entry = value as SavedProxyView
  if (!entry || typeof entry.name !== 'string' || !entry.scope || entry.scope.name !== entry.name || !Array.isArray(entry.scope.targets) || !validProxyScope(entry.scope, entry.scope.targets.map(target => target.peerId)) || typeof entry.revision !== 'string' || !entry.revision || entry.credentialsSaved !== true || typeof entry.startOnLaunch !== 'boolean' || typeof entry.valid !== 'boolean' || !['saved', 'stale', 'started', 'failed', 'active', 'reconnecting', 'stopped', 'expired', 'disabled'].includes(entry.state)) throw new Error('invalid_response')
  // Return only the allowlisted public view. Secret-bearing results never enter state.
  return { name: entry.name, scope: { name: entry.scope.name, backend: entry.scope.backend, loopbackHost: entry.scope.loopbackHost, localPort: entry.scope.localPort, lifetime: entry.scope.lifetime, ttlSeconds: entry.scope.ttlSeconds, targets: entry.scope.targets.map(target => ({ peerId: target.peerId, port: target.port })) }, revision: entry.revision, credentialsSaved: true, startOnLaunch: entry.startOnLaunch, valid: entry.valid, state: entry.state }
}
export function readSavedProxies(value: unknown): SavedProxyList {
  const raw = value as SavedProxyList
  if (!raw || !reviewRevision(raw.revision) || typeof raw.suppressed !== 'boolean' || !Array.isArray(raw.entries)) throw new Error('invalid_response')
  return { entries: raw.entries.map(readSavedProxy), revision: raw.revision, suppressed: raw.suppressed }
}
export function readStartupList(value: unknown): StartupList {
  const raw = value as StartupList
  if (!raw || !reviewRevision(raw.revision) || typeof raw.suppressed !== 'boolean' || !Array.isArray(raw.entries) || raw.entries.some(entry => !entry || typeof entry.name !== 'string' || typeof entry.enabled !== 'boolean' || typeof entry.valid !== 'boolean' || typeof entry.state !== 'string' || !reviewRevision(entry.revision) || !Array.isArray(entry.services) || entry.services.some(service => service.direction !== 'forward' || typeof service.id !== 'string' || typeof service.name !== 'string' || typeof service.ports !== 'string'))) throw new Error('invalid_response')
  return raw
}
export function readStartupReview(value: unknown, target: { name: string; ids?: string[]; group?: string }): StartupReview {
  const raw = value as StartupReview
  if (!raw || raw.name !== target.name || raw.enabled !== false || !reviewRevision(raw.revision) || !reviewRevision(raw.storeRevision) || !reviewRevision(raw.selectionRevision) || typeof raw.network !== 'string' || typeof raw.hostname !== 'string' || !Array.isArray(raw.services) || raw.services.length === 0 || raw.services.some(service => service.direction !== 'forward' || typeof service.id !== 'string' || typeof service.ports !== 'string') || target.group && (raw.group !== target.group || Boolean(raw.ids?.length)) || target.ids && (Boolean(raw.group) || raw.services.length !== target.ids.length || raw.services.some(service => !target.ids!.includes(service.id)))) throw new Error('invalid_response')
  return raw
}
