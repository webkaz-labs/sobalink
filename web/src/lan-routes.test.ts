import { describe, expect, it } from 'vitest'
import { localRouteExpiry, readOwnRoutes, readPeerRoutes, readRouteReview, routeAddress, routeTimestamp, currentRouteObservation, readRouteObservation, readRouteLifetime, currentRoutePermission } from './lan-routes'
import { routeEnglish, routeJapanese } from './route-i18n'

const candidate = { candidateId: '1'.repeat(64), address: '192.0.2.20:443', certificateSHA256: '2'.repeat(64), scope: 'external' }
const offer = { digest: '3'.repeat(64), issuer: '4'.repeat(64), recipient: '5'.repeat(64), sequence: 1, issued: '2026-10-04T12:00:00Z', expires: '2026-10-05T12:00:00Z', candidates: [candidate] }
describe('paired route contracts', () => {
  it('keeps Japanese and English labels aligned', () => {
    expect(Object.keys(routeJapanese).sort()).toEqual(Object.keys(routeEnglish).sort())
    expect(Object.values(routeJapanese).every(value => value.trim().length > 0)).toBe(true)
  })
  it.each(['192.0.2.20:443', '[fd00::5]:54446', '[::ffff:192.0.2.20]:443'])('preserves numeric endpoint %s', value => {
    expect(routeAddress(value)).toBe(true)
  })
  it.each(['example.com:443', '192.0.2.20:0', '192.0.2.20:65536', '192.0.2.20:0443', '192.0.2.999:443', '192.00.2.20:443', '[not-ip]:443', 'https://192.0.2.20:443', '192.0.2.20:443/path'])('rejects non-endpoint %s', value => {
    expect(routeAddress(value)).toBe(false)
  })
  it('accepts empty unconfigured list and strict own-candidate primary', () => {
    expect(readOwnRoutes({ candidates: [] }).candidates).toHaveLength(0)
    expect(readOwnRoutes({ candidates: [candidate], primaryCandidateId: candidate.candidateId, editable: true }).editable).toBe(true)
    for (const value of [{ candidates: [candidate] }, { candidates: [candidate], primaryCandidateId: 'other', editable: true }, { candidates: [candidate, candidate], primaryCandidateId: candidate.candidateId, editable: true }]) expect(() => readOwnRoutes(value)).toThrow()
  })
  it('binds review to exact pair and validates bounded candidate identity', () => {
    expect(readRouteReview(offer, offer.issuer, offer.recipient)).toEqual({ ...offer, lifetime: 'finite' })
    expect(readRouteReview({ ...offer, candidates: [] }, offer.issuer, offer.recipient).candidates).toEqual([])
    for (const value of [{ ...offer, sequence: -1 }, { ...offer, sequence: Number.MAX_SAFE_INTEGER + 1 }, { ...offer, candidates: [candidate, candidate] }, { ...offer, candidates: [{ ...candidate, scope: 'strict' }] }, { ...offer, expires: 'invalid' }, { ...offer, digest: 'untrusted' }]) expect(() => readRouteReview(value, offer.issuer, offer.recipient)).toThrow()
    expect(() => readRouteReview(offer, '6'.repeat(64), offer.recipient)).toThrow()
    expect(() => readRouteReview(offer, offer.issuer, undefined)).toThrow()
  })
  it('validates snapshots without confusing the legacy zero date with infinite approval', () => {
    const snapshot = { legacy: true, issuedSequence: 0, receivedSequence: 0, candidates: [], approvals: [], permittedIds: [], expires: '0001-01-01T00:00:00Z', nextExpiry: '0001-01-01T00:00:00Z', recoveryRequired: false }
    expect(readPeerRoutes(snapshot)).toEqual(snapshot)
    expect(() => readPeerRoutes({ ...snapshot, permittedIds: [candidate.candidateId] })).toThrow()
    expect(() => readPeerRoutes({ ...snapshot, approvals: [{ candidateId: candidate.candidateId, expires: offer.expires }] })).toThrow()
    expect(() => readPeerRoutes({ ...snapshot, recoveryRequired: undefined })).toThrow()
  })
  it('uses only bounded fresh observed paths, separately from relay scope', () => {
    const now = Date.now()
    const ready = { state: 'ready', path: 'direct', candidateId: candidate.candidateId, scope: 'external', observedAt: new Date(now - 1000).toISOString(), expires: new Date(now + 60000).toISOString() }
    expect(currentRouteObservation(ready, now)).toMatchObject({ path: 'direct', scope: 'external' })
    expect(currentRouteObservation(ready, now, true)).toEqual({ state: 'unknown', path: 'unknown' })
    expect(currentRouteObservation(ready, now + 31001)).toEqual({ state: 'unknown', path: 'unknown' })
    expect(currentRouteObservation(ready, now + 60001)).toEqual({ state: 'expired', path: 'unknown' })
    expect(currentRouteObservation({ ...ready, observedAt: undefined }, now).path).toBe('unknown')
    expect(currentRouteObservation({ ...ready, observedAt: new Date(now + 60000).toISOString() }, now).path).toBe('unknown')
    expect(currentRouteObservation({ ...ready, state: 'reconnecting' }, now).path).toBe('unknown')
    expect(currentRouteObservation(undefined, now)).toEqual({ state: 'unknown', path: 'unknown' })
    expect(currentRouteObservation({ ...ready, path: 'LAN-only' }, now).path).toBe('unknown')
    expect(() => readRouteObservation({ ...ready, state: 'unrecognized' })).toThrow()
  })
  it('requires an explicit permanent mode and preserves omitted legacy finite deadlines', () => {
    expect(readRouteLifetime({ expires: offer.expires })).toEqual({ lifetime: 'finite', expires: offer.expires })
    expect(readRouteReview({ ...offer, lifetime: 'until-revoked', expires: null }, offer.issuer, offer.recipient)).toMatchObject({ lifetime: 'until-revoked', expires: null })
    for (const lifetime of [{ expires: null }, { lifetime: 'finite', expires: null }, { lifetime: 'until-revoked', expires: offer.expires }, { lifetime: 'unknown', expires: null }, { lifetime: 'until-revoked' }]) expect(() => readRouteLifetime(lifetime)).toThrow()
    const now = Date.now() + 365 * 86400000
    expect(currentRoutePermission({ lifetime: 'until-revoked', expires: null }, now)).toBe(true)
    expect(currentRoutePermission({ lifetime: 'finite', expires: offer.expires }, now)).toBe(false)
    expect(currentRoutePermission({ expires: null }, now)).toBe(false)
  })
  it('supports mixed permanent and finite approvals while rejecting permanent approval beyond a finite offer', () => {
    const second = { ...candidate, candidateId: '6'.repeat(64), address: '192.0.2.21:443' }
    const view = { legacy: false, issuedSequence: 0, receivedSequence: 1, lifetime: 'until-revoked', expires: null, nextExpiry: offer.expires, candidates: [candidate, second], approvals: [{ candidateId: candidate.candidateId, lifetime: 'until-revoked', expires: null }, { candidateId: second.candidateId, lifetime: 'finite', expires: offer.expires }], permittedIds: [candidate.candidateId, second.candidateId], recoveryRequired: false }
    expect(readPeerRoutes(view)).toEqual(view)
    expect(() => readPeerRoutes({ ...view, lifetime: 'finite', expires: offer.expires })).toThrow()
    expect(() => readPeerRoutes({ ...view, lifetime: undefined })).toThrow()
    expect(() => readPeerRoutes({ ...view, approvals: [{ candidateId: candidate.candidateId, expires: null }] })).toThrow()
  })
  it('shows the expiry date and time zone in both interface languages', () => {
    for (const locale of ['en', 'ja'] as const) {
      expect(routeTimestamp(offer.expires, locale)).toContain('2026')
      expect(routeTimestamp(offer.expires, locale).length).toBeGreaterThan(15)
    }
  })
  it('round-trips the displayed finite local expiry to within its minute precision', () => {
    const expiry = '2026-10-05T12:00:30Z'
    const converted = new Date(localRouteExpiry(expiry)).getTime()
    expect(Date.parse(expiry) - converted).toBe(30000)
  })
})
