import type { ResourceSessionObservation } from '../resource/app-context'
import { resourceContextFromSession } from '../resource/app-context'
import type { SavedManagementPeer } from '../resource/types'
export type GroupContext = Readonly<{ localAvailable: boolean; remoteAvailable: boolean; revision: string; localPeerKey: string | null; peers: readonly SavedManagementPeer[] }>
export function groupContextFromSession(observation: ResourceSessionObservation): GroupContext {
  const resource = resourceContextFromSession(observation), state = observation.state
  const boot = typeof state?.resourceCatalogProcessId === 'string' && /^[0-9a-f]{64}$/.test(state.resourceCatalogProcessId) ? state.resourceCatalogProcessId : null
  const localPeerKey = typeof state?.directLAN?.publicKey === 'string' && /^[0-9a-f]{64}$/.test(state.directLAN.publicKey) ? state.directLAN.publicKey : null
  const localAvailable = resource.authenticated && !observation.stale && resource.processId !== null && boot !== null
  const remoteAvailable = localAvailable && resource.managedDirectLAN && localPeerKey !== null
  const peers = remoteAvailable ? Object.freeze(resource.peers.filter(peer => peer.key !== localPeerKey)) : Object.freeze([])
  return Object.freeze({ localAvailable, remoteAvailable, localPeerKey, peers,
    revision: JSON.stringify([localAvailable, remoteAvailable, observation.authEpoch, resource.processId, boot, resource.selectionRevision, peers.map(peer => peer.key).sort()]),
  })
}
