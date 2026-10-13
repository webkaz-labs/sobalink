import { describe, expect, it } from 'vitest'
import { newTextRequestID, pendingTextLocators, readTextOutbox, readTextPage, resolveTextLocator, retainTextLocator, textDigest, textRequestTime, verifiedTextOutbox, type TextOutboxRecord } from './text-outbox'
import { textQueueReason, textQueueText } from './text-outbox-i18n'
const now = Date.parse('2026-10-13T09:00:00Z')
const id = `mt1_${now}_${'a'.repeat(32)}`
function fixture(): TextOutboxRecord { return { version: 1, kind: 'text_outbox', requestId: id, outboxId: id, messageId: id, peerId: 'peer-a', processId: 'a'.repeat(64), reviewRevision: 'b'.repeat(64), admission: 'prepared', status: 'prepared', deliveryOutcome: 'not_attempted', reasonCode: '', text: 'abc', textBytes: 3, sha256: 'ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad', createdAt: new Date(now).toISOString(), expiresAt: new Date(now + 86400000).toISOString(), updatedAt: new Date(now).toISOString() } }
function enqueued(): TextOutboxRecord { const row = fixture(); delete row.processId; return { ...row, admission: 'enqueued', status: 'waiting_peer' } }
function confirmed(): TextOutboxRecord {
  const row = enqueued(); return { ...row, status: 'completed', deliveryOutcome: 'confirmed', receipt: { messageId: id, textBytes: row.textBytes, textSHA256: row.sha256, savedAt: new Date(now + 1000).toISOString(), lease: { version: 1, deliveryId: id, digest: 'd'.repeat(64), receiverEpoch: 'e'.repeat(32), token: 'f'.repeat(32), recipientId: row.peerId, expiresAt: row.expiresAt, receiptUntil: new Date(now + 8 * 86400000 + 300000).toISOString() } } }
}
function page(items = [fixture()]) { return { version: 1, kind: 'text_outbox', status: 'ready', reasonCode: '', revision: 'd'.repeat(64), total: items.length, items: items.map(({ text: _text, ...r }) => ({ ...r, id: r.outboxId })) } }
describe('durable text DTO and original identity', () => {
  it('uses exact timestamp plus 16 random bytes and accepts only canonical IDs', () => {
    expect(newTextRequestID(now)).toMatch(new RegExp(`^mt1_${now}_[a-f0-9]{32}$`)); expect(textRequestTime(id)).toBe(now)
    for (const bad of [id.toUpperCase(), id + '0', id.replace('mt1_', 'ob1_'), 'mt1_0000000000000_' + 'a'.repeat(32), id.replace('a', 'G'), null]) expect(textRequestTime(bad)).toBeUndefined()
  })
  it('validates the immutable reviewed body and exact receipt; durable records have no process token', async () => {
    expect(await verifiedTextOutbox(fixture(), { text: 'abc', detail: true })).toEqual(fixture())
    expect(await textDigest('abc')).toBe(fixture().sha256)
    expect(readTextOutbox(confirmed(), { review: fixture(), requestId: id })).toEqual(confirmed())
    expect(readTextOutbox(confirmed(), { review: confirmed(), requestId: id })).toEqual(confirmed())
    expect(() => readTextOutbox({ ...enqueued(), processId: 'a'.repeat(64) })).toThrow()
    expect(() => readTextOutbox({ ...fixture(), processId: undefined })).toThrow()
  })
  it.each(['requestId', 'outboxId', 'messageId', 'peerId', 'reviewRevision', 'text', 'expiresAt', 'createdAt', 'textBytes', 'sha256'] as const)('rejects changed reviewed %s', field => {
    const changes = { requestId: id.replace(/a$/, 'b'), outboxId: id.replace(/a$/, 'b'), messageId: id.replace(/a$/, 'b'), peerId: 'peer-b', reviewRevision: 'd'.repeat(64), text: 'def', expiresAt: new Date(now + 3600000).toISOString(), createdAt: new Date(now + 1).toISOString(), textBytes: 4, sha256: 'd'.repeat(64) }
    expect(() => readTextOutbox({ ...fixture(), [field]: changes[field] }, { review: fixture(), text: 'abc' })).toThrow()
  })
  it('does not accept false receipt evidence, regressed confirmation or mismatched body hashes', async () => {
    for (const mutate of [
      (r: TextOutboxRecord) => { delete r.receipt },
      (r: TextOutboxRecord) => { r.receipt!.messageId += 'a' },
      (r: TextOutboxRecord) => { r.receipt!.lease.recipientId = 'peer-b' },
      (r: TextOutboxRecord) => { r.receipt!.lease.receiptUntil = r.expiresAt },
      (r: TextOutboxRecord) => { r.receipt!.lease.token = 'x' },
      (r: TextOutboxRecord) => { r.receipt!.textSHA256 = 'e'.repeat(64) },
      (r: TextOutboxRecord) => { r.deliveryOutcome = 'unknown' },
    ]) { const r = confirmed(); mutate(r); expect(() => readTextOutbox(r)).toThrow() }
    for (const status of ['completed', 'cancelled', 'expired', 'needs_review'] as const) expect(() => readTextOutbox({ ...enqueued(), status, deliveryOutcome: 'confirmed' })).toThrow()
    for (const field of ['digest', 'receiverEpoch', 'token'] as const) {
      const changed = confirmed(); changed.receipt!.lease[field] = 'a'.repeat(field === 'digest' ? 64 : 32)
      expect(() => readTextOutbox(changed, { review: confirmed() })).toThrow()
    }
    const changedSave = confirmed(); changedSave.receipt!.savedAt = new Date(now + 2000).toISOString()
    expect(() => readTextOutbox(changedSave, { review: confirmed() })).toThrow()
    expect(() => readTextOutbox(enqueued(), { review: confirmed() })).toThrow()
    await expect(verifiedTextOutbox({ ...fixture(), sha256: 'd'.repeat(64) })).rejects.toThrow()
    await expect(verifiedTextOutbox({ ...enqueued(), text: undefined, sha256: 'd'.repeat(64) }, { text: 'abc' })).rejects.toThrow()
    await expect(verifiedTextOutbox({ ...enqueued(), text: undefined }, { text: 'abc' })).resolves.toMatchObject({ admission: 'enqueued' })
    expect(() => readTextOutbox({ ...fixture(), text: undefined }, { detail: true })).toThrow()
  })
  it('checks pagination, omitted bodies, unknown fields and aggregate identity', () => {
    expect(readTextPage(page()).items[0].text).toBeUndefined()
    expect(readTextPage({ ...page(), total: 2048, nextCursor: id }).total).toBe(2048)
    expect(() => readTextPage({ ...page(), total: 2049, nextCursor: id })).toThrow()
    expect(readTextPage({ ...page([]), status: 'blocked', reasonCode: 'message_store_recovery_required' }).status).toBe('blocked')
    for (const bad of [page([fixture(), fixture()]), { ...page(), hidden: true }, { ...page(), total: 0 }, { ...page(), nextCursor: id }, { ...page(), revision: 'x' }, { ...page(), items: [{ ...fixture(), id }] }, { ...page(), status: 'ready', reasonCode: 'message_storage_uncertain' }]) expect(() => readTextPage(bad)).toThrow()
  })
  it('retains only body-free original locators through browser reload', () => {
    sessionStorage.clear(); retainTextLocator(id); retainTextLocator(id); retainTextLocator('invalid'); expect(pendingTextLocators()).toEqual([id])
    expect(sessionStorage.getItem('sobalink.text-outbox.pending.v1')).not.toContain('abc'); resolveTextLocator(id); expect(pendingTextLocators()).toEqual([])
  })
  it('has real aligned labels and dedicated busy recovery guidance', () => {
    expect(textQueueText('en', 'completed')).toBe('Receiver saved the text'); expect(textQueueText('ja', 'completed')).toBe('相手側で本文を保存済み')
    expect(textQueueText('en', 'consent')).toContain('keep this exact text locally'); expect(textQueueText('ja', 'consent')).toContain('正確な本文を保持')
    for (const code of ['storage_busy', 'message_store_busy', 'message_storage_busy']) { expect(textQueueReason('en', code)).toContain('retry the same operation'); expect(textQueueReason('ja', code)).toContain('同じ操作を再試行') }
    expect(textQueueReason('en', 'message_evidence_capacity')).toContain('Retained delivery evidence is full')
    expect(textQueueReason('ja', 'message_evidence_capacity')).toContain('到着記録の保持容量が上限')
    expect(textQueueReason('en', 'message_active_capacity')).toContain('Active text work')
    expect(textQueueReason('ja', 'message_peer_capacity')).toContain('この相手への動作中')
    expect(textQueueText('en', 'cleanup')).toContain('does not erase'); expect(textQueueText('ja', 'cleanup')).toContain('重複防止')
  })
})
