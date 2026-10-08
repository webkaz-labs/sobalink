export const upgradeStates = ['idle', 'preparing', 'exchanging', 'confirming', 'connecting', 'connected', 'network-started', 'failed', 'cancelled', 'local-confirmed', 'restart-required'] as const
export type UpgradeState = typeof upgradeStates[number]
export interface UpgradeReview { revision: string; peerId: string; deadline: string; localEndpoint: string; peerEndpoint: string; scope: { family: string; prefixes: string[] }; restartRequired: boolean; resumePreparation: boolean; previousDeadline?: string }
export interface UpgradeStatus { state: UpgradeState; peerId?: string; deadline?: string; errorCode?: string; error?: string; restartRequired?: boolean }
export function upgradeDeadline(now = Date.now()) { return new Date(now + 300000).toISOString().replace(/(\.\d*?)0+Z$/, '$1Z').replace('.Z', 'Z') }
export function validUpgradeDeadline(value: unknown, now = Date.now()): value is string {
  return typeof value === 'string' && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/.test(value) && Date.parse(value) > now && Date.parse(value) <= now + 300000
}
export function validUpgradeReview(value: unknown, peerId: string, deadline: string): value is UpgradeReview {
  if (!value || typeof value !== 'object') return false
  const r = value as UpgradeReview
  return r.peerId === peerId && r.deadline === deadline && validUpgradeDeadline(r.deadline) &&
    typeof r.revision === 'string' && r.revision.length > 0 &&
    typeof r.localEndpoint === 'string' && r.localEndpoint.length > 0 &&
    typeof r.peerEndpoint === 'string' && r.peerEndpoint.length > 0 &&
    Boolean(r.scope) && ['ipv4', 'ipv6'].includes(r.scope.family) && Array.isArray(r.scope.prefixes) && r.scope.prefixes.length > 0 && r.scope.prefixes.every(prefix => typeof prefix === 'string' && prefix.length > 0) && typeof r.restartRequired === 'boolean' && typeof r.resumePreparation === 'boolean' && (!r.resumePreparation || (typeof r.previousDeadline === 'string' && Number.isFinite(Date.parse(r.previousDeadline))))
}
export function validUpgradeStatus(value: unknown): value is UpgradeStatus {
  if (!value || typeof value !== 'object') return false
  const s = value as UpgradeStatus
  return upgradeStates.includes(s.state) &&
    (s.state === 'idle' || (typeof s.peerId === 'string' && s.peerId.length > 0 && typeof s.deadline === 'string' && Number.isFinite(Date.parse(s.deadline)))) &&
    (s.errorCode === undefined || typeof s.errorCode === 'string') && (s.error === undefined || typeof s.error === 'string') &&
    (s.restartRequired === undefined || typeof s.restartRequired === 'boolean')
}
export function upgradeRunning(state?: UpgradeState) { return state !== undefined && ['preparing', 'exchanging', 'confirming', 'connecting'].includes(state) }
