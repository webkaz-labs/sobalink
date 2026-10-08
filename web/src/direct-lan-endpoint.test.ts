import { describe, expect, it } from 'vitest'
import { endpointCommands, endpointDeadlineValid, endpointReviewMatches, endpointReviewDeadlineValid, validEndpointStatus, validEndpointResult, nextEndpointRevision } from './direct-lan-endpoint'
describe('endpoint review data guards', () => {
  it('binds the complete local move and explicit recipients', () => {
    const input = { endpoint: '127.0.0.2:48444', deliveries: [{ peerId: 'synthetic-peer', lifetime: 'until-revoked' }] }
    const review = { ...input, revision: 'review', destinations: { 'synthetic-peer': '127.0.0.3:48444' } }
    expect(endpointReviewMatches(review, 'move', input)).toBe(true)
    expect(endpointReviewMatches({ ...review, endpoint: '127.0.0.4:48444' }, 'move', input)).toBe(false)
    expect(endpointReviewMatches({ ...review, deliveries: [] }, 'move', input)).toBe(false)
    expect(endpointReviewMatches({ ...review, revision: '' }, 'move', input)).toBe(false)
  })
  it('rejects a changed approval even when peer identity matches', () => {
    const approval = { kind: 'exact' as const, proof_digest: 'digest', endpoint: '127.0.0.2:48444', follow_revision: '' as const, granted: '2030-01-01T00:00:00Z', lifetime: 'finite', expires: '2030-01-01T01:00:00Z' }
    const input = { peerId: 'synthetic-peer', action: 'receive', approval }
    expect(endpointReviewMatches({ ...input, revision: 'r' }, 'import', input)).toBe(true)
    expect(endpointReviewMatches({ ...input, approval: { ...approval, expires: '2031-01-01T01:00:00Z' }, revision: 'r' }, 'import', input)).toBe(false)
  })
  it('keeps original deadlines and bounded decimal revisions', () => {
    expect(endpointDeadlineValid({ deliveries: [{ peerId: 'p', lifetime: 'finite', expires: '2030-01-01T00:00:00Z' }] }, Date.parse('2030-01-01T00:00:00Z'))).toBe(false)
    expect(endpointDeadlineValid({ lifetime: 'finite', expires: 'invalid' }, 0)).toBe(false)
    expect(nextEndpointRevision('18446744073709551615')).toBeUndefined()
    expect(nextEndpointRevision('01')).toBeUndefined()
    expect(nextEndpointRevision('0')).toBe('1')
  })
  it('allows reductions and new follow grants despite expired retained proof history', () => {
    const now = Date.parse('2030-01-01T00:00:00Z')
    const expired = { lifetime: 'finite', expires: '2029-01-01T00:00:00Z' }
    const review = { ...expired, approval: { ...expired, kind: 'exact' }, follow: expired }
    expect(endpointReviewDeadlineValid('revoke', { action: 'revoke' }, review, now)).toBe(true)
    expect(endpointReviewDeadlineValid('disable-follow', { action: 'disable-follow' }, review, now)).toBe(true)
    const follow = { scope_digest: 'scope', revision: '2', granted: '2030-01-01T00:00:00Z', lifetime: 'finite', expires: '2030-01-02T00:00:00Z', active: true as const }
    expect(endpointReviewDeadlineValid('follow', { follow }, review, now)).toBe(true)
    expect(endpointReviewDeadlineValid('follow', { follow: { ...follow, expires: expired.expires } }, review, now)).toBe(false)
    expect(endpointReviewDeadlineValid('delivery', { peerId: 'p' }, review, now)).toBe(false)
    expect(endpointReviewDeadlineValid('reexport', { peerId: 'p' }, review, now)).toBe(true)
  })
  it('does not apply an independent expired follow limit to exact reapproval', () => {
    const now = Date.parse('2030-01-01T00:00:00Z')
    const approval = { kind: 'exact' as const, proof_digest: 'digest', endpoint: '127.0.0.2:48444', follow_revision: '' as const, granted: '2030-01-01T00:00:00Z', lifetime: 'finite', expires: '2030-01-02T00:00:00Z' }
    const review = { ...approval, approval, follow: { lifetime: 'finite', expires: '2029-01-01T00:00:00Z' } }
    expect(endpointReviewDeadlineValid('reapprove', { approval }, review, now)).toBe(true)
    expect(endpointReviewDeadlineValid('reapprove', { approval: { ...approval, expires: '2029-01-01T00:00:00Z' } }, review, now)).toBe(false)
    expect(endpointReviewDeadlineValid('import', { approval }, { ...review, outcome: 'candidate' }, now)).toBe(true)
    expect(endpointReviewDeadlineValid('import', {}, { ...review, approval: { ...approval, kind: 'follow' }, outcome: 'candidate' }, now)).toBe(false)
  })
  it('requires actual status and operation result discriminants to resolve uncertainty', () => {
    expect(validEndpointStatus({})).toBe(false)
    expect(validEndpointStatus({ endpoint: '127.0.0.1:48444', recoveryRequired: false, endpointUpdatesEnabled: true, state: 'reconnecting' })).toBe(true)
    expect(validEndpointStatus({ endpoint: '127.0.0.1:48444', recoveryRequired: true, endpointUpdatesEnabled: false, stateVersion: 4 })).toBe(true)
    expect(validEndpointResult({}, 'move', { deliveries: [] })).toBe(false)
    expect(validEndpointResult({ saved: true }, 'move', { deliveries: [] })).toBe(false)
    expect(validEndpointResult({ saved: true, active: false, deliveries: null, endpointUpdatesEnabled: true }, 'move', { deliveries: [] })).toBe(true)
    expect(validEndpointResult({ peerId: 'other', sequence: '1', outcome: 'unconfirmed' }, 'delivery', { peerId: 'selected' })).toBe(false)
    expect(validEndpointResult({ peerId: 'selected', sequence: '1', outcome: 'unconfirmed' }, 'delivery', { peerId: 'selected' })).toBe(true)
    expect(validEndpointResult({ saved: false, changed: false, outcome: 'already_applied', endpointUpdatesEnabled: true }, 'import', {})).toBe(true)
  })
  it('does not map preview onto a mutating command', () => {
    for (const [preview, apply] of Object.values(endpointCommands)) expect(preview).not.toBe(apply)
    expect(endpointCommands.delivery).toEqual(['direct-lan.endpoint.delivery.preview', 'direct-lan.endpoint.delivery.apply'])
  })
})
