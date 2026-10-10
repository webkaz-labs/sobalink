import { readDigest, readID, readOutcome } from './decode'
import type { OperationEvidence } from './types'

export type AttemptScope = Readonly<{ peerKey: string; resourceId: string }>
export type AttemptClaim = AttemptScope & Readonly<{ operationId: string }>
type Entry = { claims: readonly AttemptClaim[]; unresolved: Set<string> }
export const MAX_SHARED_ATTEMPTS = 80
const scopeKey = (scope: AttemptScope) => `${scope.peerKey}:${scope.resourceId}`
function claims(value: readonly AttemptClaim[], allowEmpty = false): readonly AttemptClaim[] {
  if (!Array.isArray(value) || value.length > 16 || !allowEmpty && !value.length) throw new Error('Invalid attempt claims')
  const seen = new Set<string>()
  return Object.freeze(value.map(item => {
    const claim = Object.freeze({ peerKey: readDigest(item.peerKey), resourceId: readID(item.resourceId), operationId: readDigest(item.operationId) })
    const key = scopeKey(claim); if (seen.has(key)) throw new Error('Duplicate attempt scope'); seen.add(key); return claim
  }).sort((a, b) => scopeKey(a).localeCompare(scopeKey(b))))
}
// App-memory inhibition only. No Core-wide admission, durable history, provider
// permission, browser persistence or cross-page/reload guarantee is claimed.
export class ResourceAttemptRegistry {
  private readonly entries = new Map<string, Entry>()
  private readonly listeners = new Set<() => void>()
  subscribe = (listener: () => void) => { this.listeners.add(listener); return () => { this.listeners.delete(listener) } }
  get full() { return this.entries.size >= MAX_SHARED_ATTEMPTS }
  blocked(scope: AttemptScope): boolean { const key = scopeKey(scope); return [...this.entries.values()].some(entry => entry.unresolved.has(key)) }
  private validOwner(owner: string) { return /^group:[0-9a-f]{32}$/.test(owner) || owner.startsWith('single:') && owner.length <= 1024 }
  // Reserve is deliberately notification-free. The caller must install its
  // consumed review, retained attempt and pending identity before emit().
  reserveNew(owner: string, input: readonly AttemptClaim[]): boolean {
    try {
      const selected = claims(input)
      if (!this.validOwner(owner) || this.entries.has(owner) || this.full || selected.some(scope => this.blocked(scope))) return false
      this.entries.set(owner, { claims: selected, unresolved: new Set(selected.map(scopeKey)) }); return true
    } catch { return false }
  }
  // Called only after a full owned RunView decoder proves immutable membership,
  // original requests, operation tokens and reducer. It cannot admit a command.
  retainHistory(owner: string, input: readonly AttemptClaim[]): boolean {
    try {
      const selected = claims(input, true), previous = this.entries.get(owner)
      if (!/^group:[0-9a-f]{32}$/.test(owner)) return false
      if (previous) return JSON.stringify(previous.claims) === JSON.stringify(selected)
      if (this.full) return false
      this.entries.set(owner, { claims: selected, unresolved: new Set(selected.map(scopeKey)) }); return true
    } catch { return false }
  }
  settleTarget(owner: string, scope: AttemptScope, evidence: OperationEvidence): boolean {
    const entry = this.entries.get(owner), key = scopeKey(scope), claim = entry?.claims.find(item => scopeKey(item) === key)
    try {
      if (!claim || readDigest(evidence.operationId) !== claim.operationId || evidence.evidenceDurable !== true || readOutcome(evidence.outcome).status === 'unknown') return false
      return entry!.unresolved.delete(key)
    } catch { return false }
  }
  settleGroupNonDispatch(owner: string, claim: AttemptClaim, proof: Readonly<{ runId: string; activity: string; admissionFinished: boolean; dispatch: string; admissionStop: string; localDurability: string; rowLocalDurability: string }>): boolean {
    const entry = this.entries.get(owner), key = scopeKey(claim)
    if (owner !== `group:${proof.runId}` || !entry?.claims.some(item => scopeKey(item) === key && item.operationId === claim.operationId)
      || proof.activity !== 'idle' || proof.admissionFinished !== true || proof.dispatch !== 'not_attempted'
      || !['user_canceled', 'budget_exhausted', 'context_changed', 'persistence_uncertain', 'restarted'].includes(proof.admissionStop)
      || proof.localDurability !== 'durable' || proof.rowLocalDurability !== 'durable') return false
    return entry.unresolved.delete(key)
  }
  // Notifications never fetch, retry, persist or clear claims on context change.
  emit() { this.listeners.forEach(listener => listener()) }
}
