// Local review inputs only. These fields never assert wire authentication.
export interface EndpointApproval { kind: 'exact'; proof_digest: string; endpoint: string; follow_revision: ''; granted: string; lifetime: string; expires: string }
export interface EndpointFollow { scope_digest: string; revision: string; granted: string; lifetime: string; expires: string; active: true }
export interface EndpointPayload {
  peerId?: string; action?: string; endpoint?: string; update?: string; operation?: string
  lifetime?: string; expires?: string; approval?: EndpointApproval; follow?: EndpointFollow
  deliveries?: { peerId: string; lifetime: string; expires?: string }[]
  expectedRevision?: string; transactionId?: string; cancel?: boolean
}
export const endpointOperations = ['move', 'export', 'reexport', 'delivery', 'import', 'reapprove', 'follow', 'disable-follow', 'revoke', 'recover'] as const
export type EndpointOperation = typeof endpointOperations[number]
export const endpointCommands = {
  move: ['direct-lan.endpoint.move.preview', 'direct-lan.endpoint.move.apply'],
  export: ['direct-lan.endpoint.export.preview', 'direct-lan.endpoint.export'],
  reexport: ['direct-lan.endpoint.reexport.preview', 'direct-lan.endpoint.reexport'],
  delivery: ['direct-lan.endpoint.delivery.preview', 'direct-lan.endpoint.delivery.apply'],
  import: ['direct-lan.endpoint.inspect', 'direct-lan.endpoint.accept'],
  reapprove: ['direct-lan.endpoint.inspect', 'direct-lan.endpoint.reapprove-current'],
  follow: ['direct-lan.endpoint.follow.preview', 'direct-lan.endpoint.follow.apply'],
  'disable-follow': ['direct-lan.endpoint.follow.preview', 'direct-lan.endpoint.follow.apply'],
  revoke: ['direct-lan.endpoint.inspect', 'direct-lan.endpoint.revoke'],
  recover: ['direct-lan.endpoint.recovery.inspect', 'direct-lan.endpoint.recovery.apply'],
} as const
export type EndpointCommand = typeof endpointCommands[EndpointOperation][number] | 'direct-lan.endpoint.status'
export type EndpointData = Record<string, unknown>
export function endpointObject(value: unknown): value is EndpointData { return Boolean(value && typeof value === 'object' && !Array.isArray(value)) }
export function endpointReviewMatches(value: unknown, operation: EndpointOperation, input: EndpointPayload): value is EndpointData & { revision: string } {
  if (!endpointObject(value) || typeof value.revision !== 'string' || !value.revision) return false
  if (input.peerId && value.peerId !== input.peerId) return false
  if (operation === 'move' && (value.endpoint !== input.endpoint || JSON.stringify(value.deliveries ?? []) !== JSON.stringify(input.deliveries ?? []) || !endpointObject(value.destinations))) return false
  if (input.action && value.action !== input.action) return false
  if (input.approval && !sameEndpointFields(value.approval, input.approval)) return false
  if (input.follow && !sameEndpointFields(value.follow, input.follow)) return false
  if (operation === 'export' && (value.operation !== input.operation || value.lifetime !== input.lifetime || value.expires !== input.expires)) return false
  if (operation === 'recover' && (value.transactionId !== input.transactionId || Boolean(value.cancel) !== Boolean(input.cancel))) return false
  return true
}
export function nextEndpointRevision(value: unknown): string | undefined {
  if (typeof value !== 'string' || !/^(0|[1-9][0-9]*)$/.test(value)) return undefined
  const n = BigInt(value)
  return n < 18446744073709551615n ? String(n + 1n) : undefined
}
export function endpointDeadlineValid(input: EndpointPayload | EndpointData, now: number): boolean {
  const bounds = [input.approval, input.follow, ...(Array.isArray(input.deliveries) ? input.deliveries : []), ...(input.lifetime ? [input] : [])]
  return bounds.every(bound => !endpointObject(bound) || bound.lifetime !== 'finite' || typeof bound.expires === 'string' && Date.parse(bound.expires) > now)
}

function sameEndpointFields(value: unknown, expected: object): boolean { return endpointObject(value) && Object.entries(expected).every(([key, field]) => value[key] === field) }

// Check only authority this action grants or uses. Retained independent proof
// history and follow consent must never prevent an authority reduction.
export function endpointReviewDeadlineValid(operation: EndpointOperation, input: EndpointPayload, review: EndpointData, now: number): boolean {
  if (operation === 'revoke' || operation === 'disable-follow' || operation === 'reexport') return true
  if (operation === 'follow') return endpointDeadlineValid({ follow: input.follow }, now)
  if (operation === 'move') return endpointDeadlineValid(input, now)
  if (operation === 'reapprove') return endpointDeadlineValid(input, now) && endpointDeadlineValid({ lifetime: review.lifetime, expires: review.expires }, now)
  if (operation === 'import') {
    // Exact duplicates are observations, not revived authority. The backend
    // classifies them before freshness and still owns the final admission.
    if (review.outcome === 'already_applied' || review.outcome === 'unchanged') return true
    const approval = endpointObject(review.approval) ? review.approval : undefined
    const follow = approval?.kind === 'follow' ? review.follow : undefined
    return endpointDeadlineValid(input, now) && endpointDeadlineValid({ lifetime: review.lifetime, expires: review.expires, approval, follow }, now)
  }
  return endpointDeadlineValid(input, now) && endpointDeadlineValid(review, now)
}

export function validEndpointStatus(value: unknown): value is EndpointData {
  if (!endpointObject(value) || typeof value.endpoint !== 'string' || !value.endpoint || typeof value.recoveryRequired !== 'boolean' || typeof value.endpointUpdatesEnabled !== 'boolean') return false
  if (value.peers !== undefined && (!Array.isArray(value.peers) || !value.peers.every(peer => endpointObject(peer) && typeof peer.peerId === 'string' && typeof peer.endpoint === 'string' && typeof peer.state === 'string'))) return false
  if (value.endpointUpdatesEnabled) return typeof value.state === 'string' && ['active', 'reconnecting', 'saved_unavailable', 'recovery_required'].includes(value.state)
  return typeof value.stateVersion === 'number' && Number.isInteger(value.stateVersion) && value.stateVersion >= 1 && value.stateVersion <= 4
}
const endpointDeliveryOutcomes = ['applied', 'withdrawn', 'accepted_inactive', 'already_applied', 'review_required', 'saved_pending_activation', 'unconfirmed']
function validEndpointDelivery(value: unknown): value is EndpointData {
  return endpointObject(value) && typeof value.peerId === 'string' && Boolean(value.peerId) && typeof value.sequence === 'string' && /^(0|[1-9][0-9]*)$/.test(value.sequence) && typeof value.outcome === 'string' && endpointDeliveryOutcomes.includes(value.outcome)
}
export function validEndpointResult(value: unknown, operation: EndpointOperation, input: EndpointPayload): value is EndpointData {
  if (!endpointObject(value)) return false
  if (operation === 'delivery') return validEndpointDelivery(value) && value.peerId === input.peerId
  if (typeof value.saved !== 'boolean' || typeof value.endpointUpdatesEnabled !== 'boolean') return false
  if (operation === 'move') return value.endpointUpdatesEnabled === true && typeof value.active === 'boolean' && (value.deliveries === null || Array.isArray(value.deliveries) && value.deliveries.every(validEndpointDelivery))
  if (operation === 'export' || operation === 'reexport') return value.saved === true && value.reexport === (operation === 'reexport') && ['storeRevision', 'sequence', 'proofDigest', 'update'].every(key => typeof value[key] === 'string' && Boolean(value[key]))
  if (value.saved === false && value.changed === false) return value.outcome === 'already_applied' || value.outcome === 'unchanged' || value.outcome === 'review_required'
  if (value.endpointUpdatesEnabled) return typeof value.active === 'boolean' && typeof value.outcome === 'string' && ['applied', 'saved_pending_activation', 'rejected'].includes(value.outcome)
  return value.saved === true && value.outcome === 'saved_only' && typeof value.changed === 'boolean' && typeof value.storeRevision === 'string' && typeof value.transactionId === 'string' && Array.isArray(value.peers) && (operation !== 'recover' || value.transactionId === input.transactionId)
}
