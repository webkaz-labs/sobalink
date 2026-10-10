import { describe, expect, it } from 'vitest'
import { readChoice, readLocalCatalog, readLocalDescriptor, readLocalOperation, readLocalOperationID, readLocalPreview, readManagementReply, readOutcome, readRemoteInspection, readSelection, readSettings } from './decode'
import { applied, changedSettings, descriptor, effective, localOperation, localPreview, remotePreview, remoteReply, remoteSelection, settings, target, unknown } from './fixtures.test-support'
import type { ManagementRequest, RemoteSelector } from './types'

const selector = { ...remoteSelection.selector, protocolVersion: 2 as const }
describe('closed resource response readers', () => {
  it('accepts defaults and finite positive safe integers, never unlimited or implicit fields', () => {
    expect(readChoice({ mode: 'default' })).toEqual({ mode: 'default' })
    expect(readChoice({ mode: 'limited', value: Number.MAX_SAFE_INTEGER })).toEqual({ mode: 'limited', value: Number.MAX_SAFE_INTEGER })
    for (const value of [null, {}, { mode: 'unlimited' }, { mode: 'default', value: null }, { mode: 'default', value: 1 }, { mode: 'limited' }, { mode: 'limited', value: 1, extra: false }]) expect(() => readChoice(value)).toThrow()
    for (const value of [0, -1, 1.5, NaN, Infinity, Number.MAX_SAFE_INTEGER + 1, '2', null]) expect(() => readChoice({ mode: 'limited', value })).toThrow()
    expect(() => readSettings({ transferConcurrentFiles: { mode: 'default' } })).toThrow()
    expect(() => readSettings({ ...settings, extra: {} })).toThrow()
  })
  it('bounds the W1 local list and rejects unknown descriptors and schema', () => {
    expect(readLocalCatalog({ schemaVersion: 1, resources: [descriptor] }).resources).toHaveLength(1)
    expect(readLocalCatalog({ schemaVersion: 1, resources: [] }).resources).toHaveLength(0)
    for (const value of [{ schemaVersion: 2, resources: [] }, { schemaVersion: 1, resources: null }, { schemaVersion: 1, resources: [descriptor, descriptor] }]) expect(() => readLocalCatalog(value)).toThrow()
    for (const update of [{ provider: 'remote' }, { authority: 'trusted' }, { type: 'arbitrary' }, { operations: ['apply'] }, { extra: true }, { revision: 'unknown' }]) expect(() => readLocalDescriptor({ ...descriptor, ...update })).toThrow()
    expect(() => readLocalDescriptor(descriptor, { ...target, resourceId: '0'.repeat(32) })).toThrow()
  })
  it('separates local operation ID uint64 sequences from remote digest IDs', () => {
    expect(readLocalOperationID(`${target.resourceId}:${'e'.repeat(32)}:18446744073709551615`, target)).toContain('18446744073709551615')
    for (const sequence of ['0', '01', '-1', '18446744073709551616']) expect(() => readLocalOperationID(`${target.resourceId}:${'e'.repeat(32)}:${sequence}`, target)).toThrow()
    expect(() => readLocalOperationID(remotePreview.operationId, target)).toThrow()
    expect(() => readLocalPreview({ ...localPreview, operationId: remotePreview.operationId }, target, settings)).toThrow()
    expect(() => readLocalPreview(localPreview, target, changedSettings)).toThrow()
    expect(() => readLocalPreview({ ...localPreview, reviewRevision: localPreview.revision }, target, settings)).toThrow()
    expect(Object.isFrozen(readLocalPreview(localPreview, target, settings).requested.transferConcurrentFiles)).toBe(true)
  })
  it('requires complete boolean durability and preserves historical/current differences', () => {
    const result = readLocalOperation({ ...localOperation, current: { ...descriptor, requested: changedSettings } }, { ...target, operationId: localPreview.operationId })
    expect(result.outcome.status).toBe('applied')
    expect(result.current.requested).toEqual(changedSettings)
    const { evidenceDurable: _removed, ...missing } = localOperation
    for (const value of [missing, { ...localOperation, evidenceDurable: null }, { ...localOperation, evidenceDurable: 'true' }, { ...localOperation, journal: { ...localOperation.journal, records: -1 } }]) expect(() => readLocalOperation(value, { ...target, operationId: localPreview.operationId })).toThrow()
    expect(readLocalOperation({ ...localOperation, evidenceDurable: false, outcome: unknown }, { ...target, operationId: localPreview.operationId }).outcome.status).toBe('unknown')
  })
  it('enforces the provider outcome matrix instead of accepting arbitrary stage combinations', () => {
    const valid = [applied, unknown, { status: 'saved_not_applied', configuration: 'durable', accounting: 'failed', transfer: 'not_attempted' }, { status: 'failed', configuration: 'not_published', accounting: 'not_attempted', transfer: 'not_attempted' }, { status: 'canceled', configuration: 'not_attempted', accounting: 'not_attempted', transfer: 'not_attempted' }, { status: 'unknown', configuration: 'uncertain', accounting: 'succeeded', transfer: 'failed' }]
    for (const value of valid) expect(readOutcome(value)).toEqual(value)
    for (const value of [{ ...applied, configuration: 'uncertain' }, { ...unknown, transfer: 'succeeded' }, { ...applied, status: 'future' }, { ...applied, privatePath: '/synthetic' }]) expect(() => readOutcome(value)).toThrow()
  })
  it('keeps inspection v1 read-only, closed and target-matched', () => {
    const request: RemoteSelector<1> = { ...selector, protocolVersion: 1 }
    const value = { protocolVersion: 1, target, requested: settings, effective }
    expect(readRemoteInspection(value, request).requested).toEqual(settings)
    for (const update of [{ protocolVersion: 2 }, { target: { ...target, resourceId: '0'.repeat(32) } }, { grantId: selector.grantId }, { revision: descriptor.revision }, { provider: 'local' }, { effective: { ...effective, transferConcurrentFiles: 0 } }]) expect(() => readRemoteInspection({ ...value, ...update }, request)).toThrow()
  })
  it('requires exact v2 action, selector, target, and only the matching response arm', () => {
    const request: ManagementRequest = { ...selector, action: 'inspect' }
    expect(readManagementReply(remoteReply('inspect'), request).action).toBe('inspect')
    for (const update of [{ protocolVersion: 1 }, { action: 'apply' }, { grantRevision: 2 }, { grantId: '0'.repeat(32) }, { target: { ...target, resourceId: '0'.repeat(32) } }, { inspection: null }, { preview: remotePreview }, { authority: 'local' }]) expect(() => readManagementReply(remoteReply('inspect', update), request)).toThrow()
    const previewRequest: ManagementRequest = { ...selector, action: 'preview', preview: { settings } }
    expect(readManagementReply(remoteReply('preview'), previewRequest).action).toBe('preview')
    for (const update of [{ requested: changedSettings }, { operationId: localPreview.operationId }, { revision: localPreview.revision }, { reviewRevision: null }]) expect(() => readManagementReply(remoteReply('preview', { preview: { ...remotePreview, ...update } }), previewRequest)).toThrow()
  })
  it('accepts status-unavailable only for the exact operation and never on apply', () => {
    const status: ManagementRequest = { ...selector, action: 'operation.status', status: { operationId: remotePreview.operationId } }
    const unavailable = { ...selector, action: 'operation.status', unavailable: { operationId: remotePreview.operationId } }
    expect(readManagementReply(unavailable, status)).toEqual(unavailable)
    for (const update of [{ unavailable: null }, { unavailable: { operationId: 'f'.repeat(64) } }, { operation: { operationId: remotePreview.operationId, outcome: applied, evidenceDurable: true } }]) expect(() => readManagementReply({ ...unavailable, ...update }, status)).toThrow()
    const apply: ManagementRequest = { ...selector, action: 'apply', apply: { operationId: remotePreview.operationId, baseRevision: remotePreview.baseRevision, reviewRevision: remotePreview.reviewRevision, settings } }
    expect(() => readManagementReply({ ...unavailable, action: 'apply' }, apply)).toThrow()
    for (const operation of [{ operationId: remotePreview.operationId, outcome: applied }, { operationId: remotePreview.operationId, outcome: applied, evidenceDurable: null }, { operationId: remotePreview.operationId, outcome: applied, evidenceDurable: true, current: descriptor }, { operationId: localPreview.operationId, outcome: applied, evidenceDurable: true }]) expect(() => readManagementReply(remoteReply('apply', { operation }), apply)).toThrow()
  })
  it('does not treat syntactically valid selectors as grants and freezes copies', () => {
    const parsed = readSelection(remoteSelection)
    expect(parsed).toEqual(remoteSelection)
    expect(parsed).not.toBe(remoteSelection)
    expect(Object.isFrozen(parsed)).toBe(true)
    for (const update of [{ peerKey: 'name' }, { selector: { ...selector, grantRevision: Number.MAX_SAFE_INTEGER + 1 } }, { selector: { ...selector, grantRevision: 0 } }, { selector: { ...selector, permissions: ['apply'] } }, { trusted: true }]) expect(() => readSelection({ ...remoteSelection, ...update })).toThrow()
  })
})
