import { describe, expect, it } from 'vitest'
import { readCatalogRequest, readCatalogResponse } from './decode'
import { context, localRequest, processId, remoteSelection, responseFor, target } from './fixtures.test-support'
import type { CatalogRequest, SourceRequest } from './types'
function changed(path: (string | number)[], value: unknown) { const raw = structuredClone(responseFor()); let cursor: Record<string | number, unknown> = raw as unknown as Record<string | number, unknown>; for (const key of path.slice(0, -1)) cursor = cursor[key] as Record<string | number, unknown>; cursor[path[path.length - 1]] = value; return raw }
const read = (raw: unknown) => readCatalogResponse(raw, localRequest, processId, context.budget)
describe('closed local catalog response', () => {
  it('accepts the exact unified local snapshot and freezes decoded values', () => { const v = read(responseFor()); expect(v.snapshot.sources).toHaveLength(3); expect(Object.isFrozen(v.snapshot.sources[0].rows[0])).toBe(true) })
  it('accepts both explicit remote settings protocols without fallback and stale service observations', () => {
    const selectors: SourceRequest[] = [{ kind: 'remote_settings_v1', peerKey: remoteSelection.peerKey, target, grantId: remoteSelection.selector.grantId, grantRevision: 1 }, { kind: 'remote_settings_v2', peerKey: remoteSelection.peerKey, target, grantId: remoteSelection.selector.grantId, grantRevision: 1 }, { kind: 'remote_service', peerId: remoteSelection.peerKey }]
    for (const source of selectors) { const request: CatalogRequest = { schemaVersion: 1, sources: [source] }; expect(readCatalogResponse(responseFor(request), request, processId, context.budget).snapshot.sources[0].selection.kind).toBe(source.kind) }
  })
  it('rejects unknown envelope fields and null or missing required fields', () => { expect(() => read({ ...responseFor(), command: 'resource.apply' })).toThrow(); expect(() => read(changed(['snapshot', 'sources'], null))).toThrow(); const raw = responseFor(); expect(() => read({ snapshot: raw.snapshot, limits: raw.limits })).toThrow() })
  it('rejects unsafe negative fractional zero and nonfinite budgets', () => { for (const n of [0, -1, 1.5, NaN, Infinity, Number.MAX_SAFE_INTEGER + 1]) expect(() => read(changed(['limits', 'maxRows'], n))).toThrow() })
  it('rejects server budgets above the pinned browser view allowance', () => { expect(() => read(changed(['limits', 'maxBytes'], context.budget.bytes + 1))).toThrow(); expect(() => read(changed(['limits', 'maxRows'], 129))).toThrow() })
  it('matches source kind target peer grant epoch and process exactly', () => {
    for (const [path, value] of [[['snapshot', 'sources', 1, 'selection', 'target', 'resourceId'], 'b'.repeat(32)], [['snapshot', 'sources', 0, 'selection', 'epoch'], 'b'.repeat(64)], [['snapshot', 'sources', 2, 'selection', 'processId'], 'a'.repeat(64)], [['snapshot', 'sources', 0, 'selection', 'grantRevision'], 1]] as const) expect(() => read(changed([...path], value))).toThrow()
    expect(() => read(changed(['snapshot', 'sources', 0, 'selection', 'sourceId'], 'local_settings'))).toThrow()
  })
  it('rejects reordered sources duplicate identities and foreign row arms', () => { const raw = responseFor(); raw.snapshot.sources.reverse(); expect(() => read(raw)).toThrow(); expect(() => read(changed(['snapshot', 'sources', 0, 'rows'], [responseFor().snapshot.sources[0].rows[0], responseFor().snapshot.sources[0].rows[0]]))).toThrow(); expect(() => read(changed(['snapshot', 'sources', 0, 'rows', 0, 'remoteService'], {}))).toThrow() })
  it('preserves complete empty independently from failed and unconfirmed sources', () => {
    const raw = responseFor(); raw.snapshot.complete = false; raw.snapshot.sources[0] = { ...raw.snapshot.sources[0], rows: [], total: 0 }; const { total: _total, ...failedSource } = raw.snapshot.sources[1]; raw.snapshot.sources[1] = { ...failedSource, state: 'unavailable', revision: '', complete: false, rows: [] }
    const result = read(raw); expect(result.snapshot.sources[0].complete).toBe(true); expect(result.snapshot.sources[1].state).toBe('unavailable'); expect(result.snapshot.sources[2].rows).toHaveLength(1)
  })
  it('keeps a bounded limited prefix separate from independent complete sources', () => {
    const raw = responseFor(); raw.snapshot.complete = false; raw.snapshot.sources[0] = { ...raw.snapshot.sources[0], state: 'limited', complete: false }
    const result = read(raw); expect(result.snapshot.sources[0].rows).toHaveLength(1); expect(result.snapshot.sources[0].complete).toBe(false); expect(result.snapshot.sources[2].complete).toBe(true)
  })
  it('requires unconfirmed sources to have no invented observation timestamp revision or total', () => {
    const raw = responseFor(); raw.snapshot.complete = false; const { total: _total, ...source } = raw.snapshot.sources[0]
    raw.snapshot.sources[0] = { ...source, state: 'unconfirmed', checkedAt: 0, revision: '', complete: false, rows: [] }
    expect(read(raw).snapshot.sources[0].state).toBe('unconfirmed'); raw.snapshot.sources[0] = { ...raw.snapshot.sources[0], checkedAt: 1 }; expect(() => read(raw)).toThrow()
  })
  it('rejects inconsistent aggregate completion totals and failed-source rows', () => { expect(() => read(changed(['snapshot', 'complete'], false))).toThrow(); expect(() => read(changed(['snapshot', 'sources', 0, 'total'], 2))).toThrow(); expect(() => read(changed(['snapshot', 'sources', 1, 'state'], 'unavailable'))).toThrow(); expect(() => read(changed(['snapshot', 'sources', 1, 'rows'], []))).toThrow() })
  it('rejects current discovery even with a fresh timestamp and matching peer', () => { const request: CatalogRequest = { schemaVersion: 1, sources: [{ kind: 'remote_service', peerId: remoteSelection.peerKey }] }, raw = responseFor(request); raw.snapshot.sources[0] = { ...raw.snapshot.sources[0], state: 'current' }; expect(() => readCatalogResponse(raw, request, processId, context.budget)).toThrow() })
  it('allows only direction-matched non-expiring local service lifetimes', () => { expect(read(responseFor())).toBeDefined(); expect(() => read(changed(['snapshot', 'sources', 0, 'rows', 0, 'localService', 'direction'], 'share'))).toThrow(); expect(() => read(changed(['snapshot', 'sources', 0, 'rows', 0, 'localService', 'lifetime'], 'until-revoked'))).toThrow() })
  it('rejects invalid ports bytes identity lifetimes and malformed Unicode', () => { expect(() => read(changed(['snapshot', 'sources', 0, 'rows', 0, 'localService', 'ports'], '65536'))).toThrow(); expect(() => read(changed(['snapshot', 'sources', 2, 'rows', 0, 'transferActivity', 'completedBytes'], 101))).toThrow(); expect(() => read(changed(['snapshot', 'sources', 2, 'rows', 0, 'identity', 'lifetime'], 'persistent'))).toThrow(); expect(() => read(changed(['snapshot', 'sources', 0, 'rows', 0, 'localService', 'name'], '\ud800'))).toThrow() })
  it('bounds encoded UTF-8 strings and snapshot framing without claiming digest verification', () => { expect(() => read(changed(['limits', 'maxStringBytes'], 63))).toThrow(); const raw = responseFor(); raw.limits.maxBytes = raw.limits.maxPageBytes = raw.limits.maxStringBytes = 100; expect(() => read(raw)).toThrow(); const opaque = responseFor(); opaque.snapshot.revision = '9'.repeat(64); expect(read(opaque).snapshot.revision).toBe('9'.repeat(64)) })
  it('rejects duplicate mixed extra null and empty request selections', () => {
    for (const sources of [[], [{ kind: 'local_service' }, { kind: 'local_service' }], [{ kind: 'local_service', target }], [null], [{ kind: 'remote_service', peerId: 'peer-a' }, { kind: 'remote_settings_v1', peerKey: remoteSelection.peerKey, target, grantId: remoteSelection.selector.grantId, grantRevision: 1 }]]) expect(() => readCatalogRequest({ schemaVersion: 1, sources })).toThrow()
  })
})
