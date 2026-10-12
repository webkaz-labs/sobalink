import { beforeEach, expect, it } from 'vitest'
import { pendingOutboxLocators, readOutbox, readOutboxList, readOutboxSnapshot, resolveOutboxLocator, retainOutboxLocator } from './outbox'
import { fixtureOutbox, fixtureOutboxID } from './outbox-fixture'
beforeEach(() => sessionStorage.clear())
it('binds exact review while durable status survives process change', () => {
 const review = readOutbox(fixtureOutbox(), { processId: 'a'.repeat(64) })
 expect(readOutbox({ ...fixtureOutbox(), processId: undefined, admission: 'enqueued', status: 'queued', children: [{ ...fixtureOutbox().children[0], status: 'queued' }] }, { requestId: fixtureOutboxID, processId: 'd'.repeat(64), review }).admission).toBe('enqueued')
 expect(() => readOutbox(fixtureOutbox(), { processId: 'd'.repeat(64) })).toThrow()
})
it.each(['requestId', 'outboxId', 'reviewRevision', 'expiresAt', 'peerIds', 'entries', 'children'])('rejects changed reviewed %s', field => {
 const review = readOutbox(fixtureOutbox()), changed = fixtureOutbox() as unknown as Record<string, unknown>
 changed[field] = { requestId: 'other', outboxId: 'other', reviewRevision: 'd'.repeat(64), expiresAt: '2026-10-14T10:00:00Z', peerIds: ['peer-b'], entries: [{ ...fixtureOutbox().entries[0], path: 'other.txt' }], children: [{ ...fixtureOutbox().children[0], deliveryId: 'other' }] }[field]
 expect(() => readOutbox(changed, { requestId: review.requestId, review })).toThrow()
})
it.each(['cancelled', 'expired'] as const)('keeps receiver evidence when work is %s', status => {
 const r = fixtureOutbox(); r.admission = 'enqueued'; delete r.processId; r.status = status; r.deliveryOutcome = 'confirmed'; r.children[0] = { ...r.children[0], status, deliveryOutcome: 'confirmed', files: [{ batchId: 'delivery-a', fileId: 'file-a', size: 3, sha256: 'c'.repeat(64) }] }
 expect(readOutbox(r).deliveryOutcome).toBe('confirmed')
})
it('rejects malformed receipts, completed without proof and localized wire labels', () => {
 const r = fixtureOutbox(); r.children[0].files = [{ batchId: 'wrong', fileId: 'file-a', size: 3, sha256: 'c'.repeat(64) }]
 expect(() => readOutbox(r)).toThrow(); expect(() => readOutbox({ ...fixtureOutbox(), admission: 'enqueued', status: 'completed' })).toThrow()
 expect(() => readOutbox({ ...fixtureOutbox(), status: '送信待ち' })).toThrow()
 expect(() => readOutboxList({ version: 1, kind: 'outbox', items: [fixtureOutbox(), fixtureOutbox()] })).toThrow()
 expect(readOutboxSnapshot({ version: 1, kind: 'outbox', status: 'blocked_recovery', reasonCode: 'outbox_recovery_required', items: [] }).status).toBe('blocked_recovery')
})
it('retains only original request locators until authoritative recovery', () => {
 retainOutboxLocator(fixtureOutboxID); retainOutboxLocator(fixtureOutboxID); expect(pendingOutboxLocators()).toEqual([fixtureOutboxID])
 resolveOutboxLocator(fixtureOutboxID); expect(pendingOutboxLocators()).toEqual([])
})
it('requires complete exact durable receipts including empty directories', () => {
 const r = fixtureOutbox(); r.admission = 'enqueued'; delete r.processId; r.status = 'completed'; r.deliveryOutcome = 'confirmed'; r.entries = [{ id: 'directory-a', path: 'empty', kind: 'directory', size: 0 }]; r.totalBytes = 0; r.children[0].status = 'completed'; r.children[0].deliveryOutcome = 'confirmed'
 expect(() => readOutbox(r)).toThrow()
 r.children[0].files = [{ batchId: 'delivery-a', fileId: 'directory-a', size: 0, sha256: '' }]
 expect(readOutbox(r).children[0].files).toHaveLength(1)
})
