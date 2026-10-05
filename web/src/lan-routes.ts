import type { LanOwnRoutes, LanPeerRoutes, LanRouteCandidate, LanRouteReview, Locale, LanRouteObservation, LanRouteLifetime } from './api'

export const routeKey = (value: unknown): value is string => typeof value === 'string' && /^[a-f0-9]{64}$/.test(value)
const date = (value: unknown): value is string => typeof value === 'string' && Number.isFinite(Date.parse(value))
export function routeAddress(value: string) {
  const match = /^(?:\[([a-f0-9:.]+)\]|([0-9.]+)):(\d{1,5})$/.exec(value)
  if (!match || Number(match[3]) < 1 || Number(match[3]) > 65535 || String(Number(match[3])) !== match[3]) return false
  if (match[2]) return match[2].split('.').length === 4 && match[2].split('.').every(part => /^(0|[1-9]\d{0,2})$/.test(part) && Number(part) <= 255)
  try { return new URL(`https://${value}`).hostname.startsWith('[') } catch { return false }
}
function candidates(value: unknown): value is LanRouteCandidate[] {
  return Array.isArray(value) && value.length <= 4 && value.every(item => item && routeKey(item.candidateId) && typeof item.address === 'string' && routeAddress(item.address) && routeKey(item.certificateSHA256) && ['local', 'external'].includes(item.scope)) && new Set(value.map(item => item.candidateId)).size === value.length
}
export function readOwnRoutes(value: unknown): LanOwnRoutes {
  const view = value as LanOwnRoutes
  if (!view || !candidates(view.candidates) || view.candidates.length && (!view.candidates.some(item => item.candidateId === view.primaryCandidateId) || typeof view.editable !== 'boolean')) throw new Error('invalid_response')
  return view
}
export function readRouteLifetime(value: { lifetime?: unknown; expires?: unknown }): { lifetime: LanRouteLifetime; expires: string | null } {
  // Old finite views may omit lifetime. A missing deadline never implies permission.
  if ((value.lifetime === 'finite' || value.lifetime === undefined) && date(value.expires) && Date.parse(value.expires) > 0) return { lifetime: 'finite', expires: value.expires }
  if (value.lifetime === 'until-revoked' && value.expires === null) return { lifetime: 'until-revoked', expires: null }
  throw new Error('invalid_response')
}
export function currentRoutePermission(value: { lifetime?: LanRouteLifetime; expires: string | null }, now: number) {
  return value.lifetime === 'until-revoked' ? value.expires === null : value.lifetime === 'finite' && value.expires !== null && Date.parse(value.expires) > now
}
export function readRouteReview(value: unknown, peerId: string, selfId?: string): LanRouteReview {
  const view = value as LanRouteReview
  if (!view || !routeKey(view.digest) || view.issuer !== peerId || !routeKey(view.recipient) || view.recipient !== selfId || !Number.isSafeInteger(view.sequence) || view.sequence < 1 || !date(view.issued) || !candidates(view.candidates)) throw new Error('invalid_response')
  const lifetime = readRouteLifetime(view)
  if (lifetime.expires !== null && Date.parse(lifetime.expires) <= Date.parse(view.issued)) throw new Error('invalid_response')
  return { ...view, ...lifetime }
}
export function readPeerRoutes(value: unknown): LanPeerRoutes {
  const view = value as LanPeerRoutes
  if (!view || typeof view.legacy !== 'boolean' || typeof view.recoveryRequired !== 'boolean' || !Number.isSafeInteger(view.issuedSequence) || view.issuedSequence < 0 || !Number.isSafeInteger(view.receivedSequence) || view.receivedSequence < 0 || !candidates(view.candidates) || view.expires !== null && !date(view.expires) || view.nextExpiry !== null && !date(view.nextExpiry) || !Array.isArray(view.approvals) || view.approvals.length > 4 || !view.approvals.every(item => item && view.candidates.some(candidate => candidate.candidateId === item.candidateId)) || !Array.isArray(view.permittedIds) || !view.permittedIds.every(id => view.candidates.some(item => item.candidateId === id))) throw new Error('invalid_response')
  if (view.observation !== undefined) readRouteObservation(view.observation)
  if (view.legacy) {
    if (view.approvals.length || view.permittedIds.length) throw new Error('invalid_response')
    return view
  }
  const lifetime = readRouteLifetime(view)
  const approvals = view.approvals.map(item => {
    const approval = { ...item, ...readRouteLifetime(item) }
    if (lifetime.lifetime === 'finite' && (approval.expires === null || Date.parse(approval.expires) > Date.parse(lifetime.expires!))) throw new Error('invalid_response')
    return approval
  })
  return { ...view, ...lifetime, approvals }
}
export function localRouteExpiry(value: string) {
  const time = new Date(value)
  return new Date(time.getTime() - time.getTimezoneOffset() * 60000).toISOString().slice(0, 16)
}

export function routeTimestamp(value: string, locale: Locale) {
  const time = new Date(value)
  return Number.isNaN(time.getTime()) ? '' : new Intl.DateTimeFormat(locale, { year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', timeZoneName: 'short' }).format(time)
}

export function readRouteObservation(value: unknown): LanRouteObservation | undefined {
  if (value === undefined) return undefined
  const view = value as LanRouteObservation
  if (!view || !['idle', 'connecting', 'reconnecting', 'ready', 'unavailable', 'closed', 'expired', 'unknown'].includes(view.state) || !['unknown', 'direct', 'relay'].includes(view.path) || view.candidateId !== undefined && !routeKey(view.candidateId) || view.scope !== undefined && !['local', 'external'].includes(view.scope) || view.observedAt !== undefined && !date(view.observedAt) || view.expires !== undefined && !date(view.expires)) throw new Error('invalid_response')
  return view
}
export function currentRouteObservation(value: unknown, now: number, stale = false): LanRouteObservation {
  const unknown: LanRouteObservation = { state: 'unknown', path: 'unknown' }
  if (stale) return unknown
  let observation: LanRouteObservation | undefined
  try { observation = readRouteObservation(value) } catch { return unknown }
  if (!observation) return unknown
  if (observation.expires && Date.parse(observation.expires) <= now) return { state: 'expired', path: 'unknown' }
  if (observation.state !== 'ready') return { ...observation, path: 'unknown' }
  const time = observation.observedAt ? Date.parse(observation.observedAt) : NaN
  return !Number.isFinite(time) || now - time > 30000 || time - now > 5000 ? unknown : observation
}
