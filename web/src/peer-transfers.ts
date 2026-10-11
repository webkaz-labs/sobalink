import * as api from './api'
export function transferKey(transfer: api.Transfer) { return `${transfer.direction}:${transfer.id}` }
export function peerTransferCounts(transfers: api.Transfer[]) {
  return { succeeded: transfers.filter(t => t.status === 'completed').length, failed: transfers.filter(t => t.status === 'failed').length,
    inProgress: transfers.filter(t => ['offered', 'awaiting-acceptance', 'queued', 'transferring', 'saving'].includes(t.status)).length,
    cancelled: transfers.filter(t => t.status === 'cancelled').length, declined: transfers.filter(t => t.status === 'declined').length }
}
export function retryExpected(state: api.State, peer: api.Peer, transfer: api.Transfer, stale: boolean, busy: Set<string>): api.TransferRetryExpected | undefined {
  if (!state.resourceCatalogProcessId || !/^[a-f0-9]{64}$/.test(state.resourceCatalogProcessId) || stale || !api.canExchange(peer) || transfer.peerId !== peer.id || transfer.status !== 'failed' || transfer.retryable !== true || busy.has(`transfer:${transfer.id}`) || (transfer.direction === 'incoming' && api.receivingBlocked(state))) return
  return { processId: state.resourceCatalogProcessId, peerId: peer.id, direction: transfer.direction }
}
