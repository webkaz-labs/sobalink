import { describe, expect, it } from 'vitest'
import { localRouteExpiry, readOwnRoutes, readPeerRoutes, readRouteReview, routeAddress, routeTimestamp, currentRouteObservation, readRouteObservation } from './lan-routes'
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
    expect(readRouteReview(offer, offer.issuer, offer.recipient)).toEqual(offer)
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
