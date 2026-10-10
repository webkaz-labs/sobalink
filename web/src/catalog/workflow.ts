import type { State } from '../api'
import type { ResourceSelection } from '../resource/types'
import { identityKey } from './decode'
import type { CatalogRow, CatalogSnapshot, SourceView, TransferFocus, Workflow, WorkflowKind } from './types'
export function workflows(source: SourceView, row: CatalogRow): readonly Workflow[] {
  if (source.state !== 'current' || !source.complete || !source.rows.includes(row)) return []
  let kinds: WorkflowKind[] = []
  if ('localSettings' in row || 'remoteSettingsV2' in row) kinds = ['inspect_settings', 'review_settings']
  else if ('remoteSettingsV1' in row) kinds = ['inspect_settings']
  else if ('localService' in row) {
    kinds = ['review_saved_service']
    if (['saved', 'stopped', 'expired'].includes(row.localService.state)) kinds.push('review_service_start')
    else if (['starting', 'active', 'reconnecting'].includes(row.localService.state)) kinds.push('review_service_stop')
  } else if ('transferActivity' in row) kinds = ['open_transfer']
  // Discovery remains stale/nonactionable until publication provenance is owned.
  return Object.freeze(kinds.map(kind => Object.freeze({ kind, sourceId: source.selection.sourceId, identity: row.identity })))
}
export type Navigation = Readonly<
  | { kind: 'settings'; selection: ResourceSelection }
  | { kind: 'saved_service'; id: string }
  | { kind: 'transfer'; focus: TransferFocus }
>
export function navigation(snapshot: CatalogSnapshot, workflow: Workflow): Navigation | null {
  const source = snapshot.sources.find(item => item.selection.sourceId === workflow.sourceId)
  const row = source?.rows.find(item => identityKey(item.identity) === identityKey(workflow.identity))
  if (!source || !row || !workflows(source, row).some(item => item.kind === workflow.kind)) return null
  const selected = source.selection
  if (workflow.kind === 'inspect_settings' || workflow.kind === 'review_settings') {
    if (selected.target.schemaVersion !== 1) return null
    return Object.freeze({ kind: 'settings', selection: selected.kind === 'local_settings'
      ? Object.freeze({ kind: 'local', target: selected.target })
      : Object.freeze({ kind: 'remote', peerKey: selected.peerKey, selector: Object.freeze({ protocolVersion: selected.kind === 'remote_settings_v1' ? 1 : 2, target: selected.target, grantId: selected.grantId, grantRevision: selected.grantRevision }) }) })
  }
  if ('localService' in row) return Object.freeze({ kind: 'saved_service', id: row.identity.id })
  if ('transferActivity' in row && (row.identity.direction === 'incoming' || row.identity.direction === 'outgoing')) return Object.freeze({ kind: 'transfer', focus: Object.freeze({ processId: selected.processId, peerId: row.transferActivity.peerId, id: row.identity.id, direction: row.identity.direction }) })
  return null
}
export function currentTransfer(state: State | null, stale: boolean, focus: TransferFocus): boolean {
  return !stale && state?.resourceCatalogProcessId === focus.processId && state.peers.some(peer => peer.id === focus.peerId)
    && state.transfers.some(transfer => transfer.id === focus.id && transfer.direction === focus.direction && transfer.peerId === focus.peerId)
}
