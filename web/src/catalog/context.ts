import type { ResourceSessionObservation } from '../resource/app-context'
import { resourceContextFromSession } from '../resource/app-context'
import { peerID } from './decode'
import type { DecodeBudget } from './types'
export type CatalogPeer = Readonly<{ id: string; name: string }>
export type CatalogContext = Readonly<{ available: boolean; revision: string; processId: string | null; managedDirectLAN: boolean; peers: readonly CatalogPeer[]; managedKeys: readonly string[]; budget: DecodeBudget }>
const bounded = (value: unknown, fallback: number, ceiling: number): number => typeof value === 'number' && Number.isSafeInteger(value) && value > 0 ? Math.min(value, ceiling) : fallback
export function catalogContextFromSession(observation: ResourceSessionObservation): CatalogContext {
  const resource = resourceContextFromSession(observation), state = observation.state
  const processId = typeof state?.resourceCatalogProcessId === 'string' && /^[0-9a-f]{64}$/.test(state.resourceCatalogProcessId) ? state.resourceCatalogProcessId : null
  let peers: readonly CatalogPeer[] = []
  try {
    if (!Array.isArray(state?.peers) || state.peers.length > 256) throw new Error('Unbounded peer selection')
    const ids = new Set<string>()
    peers = Object.freeze(state.peers.map(peer => { const id = peerID(peer.id); if (ids.has(id) || typeof peer.name !== 'string' || peer.name.length > 256) throw new Error('Invalid peer selection'); ids.add(id); return Object.freeze({ id, name: peer.name }) }))
  } catch { peers = Object.freeze([]) }
  const rows = state?.limits?.effective.resources.pageEntries, bytes = state?.limits?.effective.resources.pageBytes
  // Additional finite browser view ceilings do not change the provider policy.
  const budget = Object.freeze({ rows: bounded(rows?.mode === 'limited' ? rows.value : undefined, 128, 8192), bytes: bounded(bytes?.mode === 'limited' ? bytes.value : undefined, 1024 * 1024, 8 * 1024 * 1024) })
  const available = resource.authenticated && !observation.stale && resource.processId !== null && processId !== null
  const managedKeys = Object.freeze(resource.peers.map(peer => peer.key).sort())
  return Object.freeze({ available, processId: available ? processId : null, managedDirectLAN: available && resource.managedDirectLAN, peers: available ? peers : Object.freeze([]), managedKeys: available ? managedKeys : Object.freeze([]), budget,
    revision: JSON.stringify([available, observation.authEpoch, resource.processId, processId, resource.selectionRevision, managedKeys, peers.map(peer => peer.id).sort(), budget.rows, budget.bytes]),
  })
}
