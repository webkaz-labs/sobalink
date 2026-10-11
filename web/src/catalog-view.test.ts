import { describe, expect, it } from 'vitest'
import { catalogViewError, catalogViewSelector, readCatalogView } from './catalog-view'
const peer = 'a'.repeat(64), id = 'b'.repeat(32), grant = 'c'.repeat(32)
const selector = catalogViewSelector(id, grant, '2')!
const reply = () => ({ selector, peerKey: peer, checkedAt: new Date().toISOString(), reviewRevision: 'd'.repeat(32), services: [{ service: { id: 'e'.repeat(32), purpose: 'web', network: 'tcp', ports: '8080', lifetime: 'finite', expiresAt: new Date(Date.now() + 60_000).toISOString(), application: 'unverified' }, connectAllowed: false }] })
describe('catalog viewing contract', () => {
  it('keeps exact grant selector and view-only service permission', () => { const result = readCatalogView(reply(), peer, selector); expect(result.selector).toEqual(selector); expect(result.services[0].connectAllowed).toBe(false); expect(result.services[0].service.peerId).toBe(peer) })
  it('rejects different peer target grant revision and duplicate publications', () => {
    expect(() => readCatalogView(reply(), 'f'.repeat(64), selector)).toThrow()
    for (const change of [{ grantRevision: 3 }, { grantId: id }, { target: { schemaVersion: 1 as const, resourceId: grant } }]) expect(() => readCatalogView({ ...reply(), selector: { ...selector, ...change } }, peer, selector)).toThrow()
    const v = reply(); v.services.push(v.services[0]); expect(() => readCatalogView(v, peer, selector)).toThrow()
  })
  it('requires bounded exact human selector input', () => { expect(catalogViewSelector(id, grant, '0')).toBeNull(); expect(catalogViewSelector(id, grant, '9007199254740992')).toBeNull(); expect(catalogViewSelector(id, grant, '2.0')).toBeNull(); expect(catalogViewSelector('short', grant, '2')).toBeNull() })
  it('explains expired revoked stale unavailable and limited in both languages', () => { for (const state of ['expired', 'revoked', 'stale', 'unavailable', 'limited']) for (const ja of [false, true]) expect(catalogViewError({ code: `resource_catalog_view_${state}` }, ja)).toBeTruthy() })
})
