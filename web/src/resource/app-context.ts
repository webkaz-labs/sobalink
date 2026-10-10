import type { State } from '../api'
import type { ResourceContext } from './controller'
import type { SavedManagementPeer } from './types'

// Read-only observations from the existing useServer owner. Neither this
// projection nor these hints establish provider admission or grant authority.
export type ResourceSessionObservation = Readonly<{
  state: State | null
  authenticated: boolean
  stale: boolean
  authEpoch: number
}>
const keyPattern = /^[0-9a-f]{64}$/
export function resourceContextFromSession(observation: ResourceSessionObservation): ResourceContext {
  const epochValid = Number.isSafeInteger(observation.authEpoch) && observation.authEpoch >= 0
  const authenticated = observation.authenticated && epochValid && observation.state !== null
  const state = authenticated ? observation.state : null
  const processId = !observation.stale && typeof state?.processId === 'number' && Number.isSafeInteger(state.processId) && state.processId > 0 ? state.processId : null
  const catalogProcess = typeof state?.resourceCatalogProcessId === 'string' && keyPattern.test(state.resourceCatalogProcessId) ? state.resourceCatalogProcessId : null
  const network = state?.settings?.network
  const mode = network === 'direct-lan' || network === 'lan' || network === 'tailnet' || network === 'mixed' || network === 'none' ? network : 'unknown'
  const direct = state?.directLAN
  const localKey = typeof direct?.publicKey === 'string' && keyPattern.test(direct.publicKey) ? direct.publicKey : null
  const managedDirectLAN = processId !== null && mode === 'direct-lan' && localKey !== null
    && direct?.configured === true && direct.listenerReady === true
    && direct.recoveryRequired !== true && direct.resourceRestartRequired !== true
  let peers: readonly SavedManagementPeer[] = Object.freeze([])
  if (managedDirectLAN && Array.isArray(direct?.peers) && direct.peers.length <= 256) {
    const keys = new Set<string>()
    const candidate: SavedManagementPeer[] = []
    let valid = true
    for (const peer of direct.peers) {
      if (!peer || typeof peer.key !== 'string' || !keyPattern.test(peer.key) || keys.has(peer.key)
        || peer.name !== undefined && (typeof peer.name !== 'string' || peer.name.length > 256)) { valid = false; break }
      keys.add(peer.key)
      candidate.push(Object.freeze({ key: peer.key, name: peer.name || '' }))
    }
    if (valid) peers = Object.freeze(candidate)
  }
  return Object.freeze({
    authenticated, processId, managedDirectLAN, peers,
    // Auth epoch, backend and local identity are invalidation hints only.
    // Saved peer-key membership is independently tracked by the controller.
    // Do not derive identity from names, routes, permissions or polling time.
    selectionRevision: JSON.stringify([epochValid ? observation.authEpoch : null, mode, localKey, managedDirectLAN, catalogProcess]),
  })
}
