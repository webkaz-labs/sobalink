import type { FavoriteReference, FavoritesView } from './api'
import { validServiceName } from './service-form'

export function favoriteKey(reference: FavoriteReference) {
  return JSON.stringify(reference.kind === 'service' ? ['service', reference.serviceId] : ['group', reference.groupName])
}
export function favoriteReference(reference: FavoriteReference): FavoriteReference {
  return reference.kind === 'service' ? { kind: 'service', serviceId: reference.serviceId } : { kind: 'group', groupName: reference.groupName }
}
function record(value: unknown): value is Record<string, unknown> { return typeof value === 'object' && value !== null && !Array.isArray(value) }
function exactKeys(value: Record<string, unknown>, keys: string[]) { return Object.keys(value).length === keys.length && keys.every(key => Object.hasOwn(value, key)) }

// Preferences contain references only. Do not accept copied membership, scope or
// authority, coerce availability, normalize group names, or invent an empty store.
export function readFavorites(value: unknown): FavoritesView {
  if (!record(value) || !exactKeys(value, ['version', 'revision', 'entries', 'durabilityUncertain']) || value.version !== 1 || typeof value.revision !== 'string' || !/^[0-9a-f]{64}$/.test(value.revision) || !Array.isArray(value.entries) || typeof value.durabilityUncertain !== 'boolean') throw new Error('invalid_favorites')
  const seen = new Set<string>()
  const entries = value.entries.map(entry => {
    if (!record(entry) || typeof entry.available !== 'boolean') throw new Error('invalid_favorites')
    let reference: FavoriteReference
    if (entry.kind === 'service' && exactKeys(entry, ['kind', 'serviceId', 'available']) && typeof entry.serviceId === 'string' && /^[A-Za-z0-9][A-Za-z0-9:_-]{0,127}$/.test(entry.serviceId)) reference = { kind: 'service', serviceId: entry.serviceId }
    else if (entry.kind === 'group' && exactKeys(entry, ['kind', 'groupName', 'available']) && typeof entry.groupName === 'string' && validServiceName(entry.groupName)) reference = { kind: 'group', groupName: entry.groupName }
    else throw new Error('invalid_favorites')
    const key = favoriteKey(reference)
    if (seen.has(key)) throw new Error('invalid_favorites')
    seen.add(key)
    return { ...reference, available: entry.available }
  })
  return { version: 1, revision: value.revision, entries, durabilityUncertain: value.durabilityUncertain }
}
